// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ── createNamespace ────────────────────────────────────────────────────────────

func TestCreateNamespace_userTier_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("acme-corp", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	require.NotNil(t, ns)
	assert.Equal(t, "acme-corp", ns.Name)
	assert.Equal(t, "alice", ns.CreationActor)
	assert.NotEmpty(t, ns.ID)
}

func TestCreateNamespace_orgTier_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("acme-engineering", model.NamespaceTierOrganization)
	ns, err := svc.CreateNamespace(context.Background(), input, "bob")
	require.NoError(t, err)
	require.NotNil(t, ns)
	assert.Equal(t, "acme-engineering", ns.Name)
}

func TestCreateNamespace_duplicateIdentifier_conflict(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("duplicate-ns", model.NamespaceTierUser)
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)

	// second call with same identifier
	_, err = svc.CreateNamespace(context.Background(), input, "bob")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateNamespace_invalidIdentifier_spaces(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("invalid identifier", model.NamespaceTierUser)
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
	assert.Error(t, err)
}

func TestCreateNamespace_uppercaseIdentifier_normalizedToLowercase(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("InvalidNS", model.NamespaceTierUser)
	// uppercase is folded to lowercase before validation; "invalidns" is a valid identifier
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	assert.Equal(t, "invalidns", ns.Name)
}

func TestCreateNamespace_invalidIdentifier_leadingHyphen(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("-leading-hyphen", model.NamespaceTierUser)
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
	assert.Error(t, err)
}

func TestCreateNamespace_reservedIdentifier_admin(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("admin", model.NamespaceTierUser)
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestCreateNamespace_enterpriseTier_rejected(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	// ENTERPRISE is no longer a valid NamespaceTier value; the service must reject it
	// regardless of caller permissions. The raw string bypasses schema validation so the
	// service-layer guard is exercised directly.
	input := createNamespaceInput("acme-enterprise", model.NamespaceTier("ENTERPRISE"))
	_, err := svc.CreateNamespace(context.Background(), input, "admin-user")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ENTERPRISE")
}

func TestCreateNamespace_commitsManifestToBootstrapRepository(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	input := createNamespaceInput("committed-namespace", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	require.NotNil(t, ns)
	require.Len(t, writer.commitCalls, 1)
	assert.Equal(t, "namespaces/committed-namespace.md", writer.commitCalls[0].Path)
	assert.Contains(t, string(writer.commitCalls[0].Content), "kind: Namespace")
	assert.NotEmpty(t, writer.commitRepoIDs[0])
}

func TestCreateNamespace_bootstrapNameRejected(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	_, err := svc.CreateNamespace(context.Background(), createNamespaceInput("default", model.NamespaceTierUser), "alice")
	require.ErrorContains(t, err, "system-managed")
	assert.Empty(t, writer.commitCalls)
}

func TestCreateNamespace_commitFailureDoesNotCreateDatastoreRow(t *testing.T) {
	writer := &mockGitWriter{commitErr: errors.New("commit failed")}
	svc := newTestSvc(t, writer)
	_, err := svc.CreateNamespace(context.Background(), createNamespaceInput("commit-failure", model.NamespaceTierUser), "alice")
	require.ErrorContains(t, err, "failed to commit")
	_, err = svc.GetNamespaceByName(context.Background(), "commit-failure")
	require.Error(t, err)
}

func TestCreateNamespace_setsAdmissionStatusForCurrentGeneration(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ns, err := svc.CreateNamespace(
		context.Background(),
		createNamespaceInput("admission-status", model.NamespaceTierUser),
		"alice",
	)
	require.NoError(t, err)

	var status catalog.NamespaceStatus
	require.NoError(t, json.Unmarshal(ns.Status, &status))
	assert.Equal(t, ns.Generation, status.ObservedGeneration)
	assert.Equal(t, "main@sha1:deadbeef", status.LastAppliedRevision)
	require.Len(t, status.Conditions, 1)
	assert.Equal(t, catalog.ConditionAdmissionAccepted, status.Conditions[0].Type)
	assert.Equal(t, catalog.ConditionTrue, status.Conditions[0].Status)
	assert.Equal(t, ns.Generation, status.Conditions[0].ObservedGeneration)
}

func TestUpdateNamespace_advancesGenerationAndAdmissionRevision(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	created, err := svc.CreateNamespace(ctx, createNamespaceInput("status-update", model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	input := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
	title := "Updated namespace"
	input.Spec.Title = &title
	updated, err := svc.UpdateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	assert.Equal(t, created.Generation+1, updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	var status catalog.NamespaceStatus
	require.NoError(t, json.Unmarshal(updated.Status, &status))
	assert.Equal(t, updated.Generation, status.ObservedGeneration)
	assert.Equal(t, "main@sha1:deadbeef", status.LastAppliedRevision)
	require.Len(t, status.Conditions, 1)
	assert.Equal(t, updated.Generation, status.Conditions[0].ObservedGeneration)
}

func TestUpdateNamespacePreservesCurrentGitMarkdownBody(t *testing.T) {
	ctx := context.Background()
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	created, err := svc.CreateNamespace(ctx, createNamespaceInput("preserved-body", model.NamespaceTierUser), "alice")
	require.NoError(t, err)
	require.Len(t, writer.commitCalls, 1)

	const body = "# Existing documentation\n\nKeep this Markdown exactly.\n"
	path := "namespaces/preserved-body.md"
	currentManifest := append([]byte(nil), writer.commitCalls[0].Content...)
	currentManifest = append(currentManifest, []byte(body)...)
	writer.setFile(path, currentManifest)

	input := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
	title := "Updated frontmatter"
	input.Spec.Title = &title
	updated, err := svc.UpdateNamespace(ctx, input, "bob")
	require.NoError(t, err)
	require.Len(t, writer.commitCalls, 2)

	assert.Equal(t, body, updated.Body)
	assert.Equal(t, body, string(writer.commitCalls[1].Content[len(writer.commitCalls[1].Content)-len(body):]))
	assert.Contains(t, string(writer.commitCalls[1].Content), "title: Updated frontmatter")
}

func TestNamespaceGraphQLPersistsCompleteAuthoredStateAndProvenance(t *testing.T) {
	ctx := context.Background()
	createSHA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	updateSHA := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	writer := newCommitOrderGitWriter("deadbeef", createSHA, updateSHA)
	svc := newTestSvc(t, &mockGitWriter{})
	svc.SetGitWriter(writer)

	title := "Complete authored state"
	visibility := model.RepositoryVisibilityPrivate
	defaultBranch := "trunk"
	maxPack := int64(4096)
	maxFile := int64(1024)
	input := createNamespaceInput("complete-authored", model.NamespaceTierUser)
	input.Metadata.Labels = map[string]any{"team": "catalog"}
	input.Metadata.Annotations = map[string]any{"owner": "alice"}
	input.Spec.Title = &title
	input.Spec.RepositoryDefaults = &model.NamespaceRepositoryDefaultsInput{
		Visibility:    &visibility,
		DefaultBranch: &defaultBranch,
	}
	input.Spec.PushPolicyDefaults = &model.NamespacePushPolicyDefaultsInput{
		MaxPackSizeBytes: &maxPack,
		MaxFileSizeBytes: &maxFile,
	}

	created, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)
	assert.Equal(t, "gitstore.dev/v1beta1", created.APIVersion)
	assert.Equal(t, "Namespace", created.Kind)
	assert.Equal(t, map[string]string{"team": "catalog"}, created.Labels)
	assert.Equal(t, map[string]string{"owner": "alice"}, created.Annotations)
	assert.Equal(t, "namespaces/complete-authored.md", created.SourcePath)
	assert.Equal(t, createSHA, created.GitCommitSHA)
	assert.Equal(t, "refs/heads/main", created.GitRef)
	var createdSpec catalog.NamespaceSpec
	require.NoError(t, json.Unmarshal(created.Spec, &createdSpec))
	assert.Equal(t, "trunk", createdSpec.RepositoryDefaults.DefaultBranch)
	assert.Equal(t, "PRIVATE", createdSpec.RepositoryDefaults.Visibility)
	assert.Equal(t, maxPack, createdSpec.PushPolicyDefaults.MaxPackSizeBytes)
	assert.Equal(t, maxFile, createdSpec.PushPolicyDefaults.MaxFileSizeBytes)

	updatedInput := updateNamespaceInput(created.Name, model.NamespaceTierUser)
	updatedInput.Metadata.Labels = map[string]any{"team": "platform"}
	updatedInput.Metadata.Annotations = map[string]any{"owner": "bob"}
	updatedInput.Spec.Title = &title
	updatedVisibility := model.RepositoryVisibilityInternal
	updatedBranch := "main"
	updatedPack := int64(8192)
	updatedFile := int64(2048)
	updatedInput.Spec.RepositoryDefaults = &model.NamespaceRepositoryDefaultsInput{
		Visibility:    &updatedVisibility,
		DefaultBranch: &updatedBranch,
	}
	updatedInput.Spec.PushPolicyDefaults = &model.NamespacePushPolicyDefaultsInput{
		MaxPackSizeBytes: &updatedPack,
		MaxFileSizeBytes: &updatedFile,
	}

	updated, err := svc.UpdateNamespace(ctx, updatedInput, "bob")
	require.NoError(t, err)
	assert.Equal(t, created.Generation+1, updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, map[string]string{"team": "platform"}, updated.Labels)
	assert.Equal(t, map[string]string{"owner": "bob"}, updated.Annotations)
	assert.Equal(t, updateSHA, updated.GitCommitSHA)
	assert.Equal(t, "refs/heads/main", updated.GitRef)
	var updatedSpec catalog.NamespaceSpec
	require.NoError(t, json.Unmarshal(updated.Spec, &updatedSpec))
	assert.Equal(t, "main", updatedSpec.RepositoryDefaults.DefaultBranch)
	assert.Equal(t, "INTERNAL", updatedSpec.RepositoryDefaults.Visibility)
	assert.Equal(t, updatedPack, updatedSpec.PushPolicyDefaults.MaxPackSizeBytes)
	assert.Equal(t, updatedFile, updatedSpec.PushPolicyDefaults.MaxFileSizeBytes)
}

func TestNamespaceGraphQLSystemOnlyProvenanceAdvancesOnlyResourceVersion(t *testing.T) {
	ctx := context.Background()
	createSHA := "cccccccccccccccccccccccccccccccccccccccc"
	updateSHA := "dddddddddddddddddddddddddddddddddddddddd"
	writer := newCommitOrderGitWriter("deadbeef", createSHA, updateSHA)
	svc := newTestSvc(t, &mockGitWriter{})
	svc.SetGitWriter(writer)
	input := createNamespaceInput("provenance-only", model.NamespaceTierUser)

	created, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)
	updated, err := svc.UpdateNamespace(ctx, updateNamespaceInput(created.Name, model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	assert.Equal(t, created.Generation, updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, updateSHA, updated.GitCommitSHA)
	assert.Equal(t, "refs/heads/main", updated.GitRef)
	assert.Equal(t, "namespaces/provenance-only.md", updated.SourcePath)
}

// ── namespaces query ───────────────────────────────────────────────────────────

func TestListNamespaces_returnsAll(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	for _, id := range []string{"ns-alpha", "ns-beta", "ns-gamma"} {
		input := createNamespaceInput(id, model.NamespaceTierUser)
		_, err := svc.CreateNamespace(context.Background(), input, "alice")
		require.NoError(t, err)
	}

	result, err := svc.ListNamespaces(context.Background(), datastore.PageParams{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.Items), 3)
}

// ── namespace query ────────────────────────────────────────────────────────────

func TestGetNamespaceByName_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("lookup-me", model.NamespaceTierUser)
	created, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)

	got, err := svc.GetNamespaceByName(context.Background(), "lookup-me")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestGetNamespaceByName_notFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	_, err := svc.GetNamespaceByName(context.Background(), "does-not-exist")
	assert.Error(t, err)
}

// ── deleteNamespace ────────────────────────────────────────────────────────────

func TestDeleteNamespace_owner_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("to-delete", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)

	_, err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)

	terminating, err := svc.GetNamespaceByName(context.Background(), "to-delete")
	require.NoError(t, err)
	require.NotNil(t, terminating.DeletionTimestamp)
	assert.Contains(t, terminating.Finalizers, datastore.NamespaceForegroundDeletionFinalizer)
	assert.Equal(t, "2", terminating.ResourceVersion)
}

func TestDeleteNamespace_admin_canDeleteAny(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("owned-by-alice", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)

	// admin deletes alice's namespace
	_, err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)
}

func TestDeleteNamespace_withoutAuthorizationCheck_serviceAllowsDelete(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("alices-ns", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)

	_, err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)
}

func TestDeleteNamespace_unknownIdentifier_notFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	_, err := svc.DeleteNamespace(context.Background(), &datastore.Namespace{ID: "does-not-exist", Name: "does-not-exist"})
	assert.Error(t, err)
}

func TestDeleteNamespace_withRepository_rejected(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	input := createNamespaceInput("ns-with-repo", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	_, err = svc.CreateRepository(ctx, ns.ID, "some-repo", "main", "default", "alice")
	require.NoError(t, err)

	_, err = svc.DeleteNamespace(ctx, ns)
	require.Error(t, err)
	requireDeletionReasons(t, err, namespaceadmission.ReasonNamespaceNotEmpty)

	_, err = svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)

	result, err := svc.Store().ListRepositoriesByNamespace(ctx, ns.Name, datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 1)
}

func TestDeleteNamespace_afterRepositoriesRemoved_succeeds(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	input := createNamespaceInput("ns-repo-removed", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	repo, err := svc.CreateRepository(ctx, ns.ID, "temp-repo", "main", "default", "alice")
	require.NoError(t, err)

	require.NoError(t, svc.DeleteRepository(ctx, repo.ID, "alice"))

	_, err = svc.DeleteNamespace(ctx, ns)
	require.NoError(t, err)
	terminating, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	require.NotNil(t, terminating.DeletionTimestamp)
}

func TestDeleteNamespace_redundantDeleteDoesNotAdvanceVersion(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("redundant-delete", model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	_, err = svc.DeleteNamespace(ctx, ns)
	require.NoError(t, err)
	first, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	_, err = svc.DeleteNamespace(ctx, first)
	require.NoError(t, err)
	second, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion)
	assert.Equal(t, first.DeletionTimestamp, second.DeletionTimestamp)
}

func TestDeleteNamespace_bootstrapRejected(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ns, err := svc.GetNamespaceByName(context.Background(), "gitstore-system")
	require.NoError(t, err)
	_, err = svc.DeleteNamespace(context.Background(), ns)
	requireDeletionReasons(t, err,
		namespaceadmission.ReasonBootstrapNamespace,
		namespaceadmission.ReasonNamespaceNotEmpty,
	)
}

func TestCompleteNamespaceDeletion_removesTerminatingNamespace(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("complete-delete", model.NamespaceTierUser), "alice")
	require.NoError(t, err)
	_, err = svc.DeleteNamespace(ctx, ns)
	require.NoError(t, err)
	terminating, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)

	deleted, err := svc.CompleteNamespaceDeletion(ctx, ns.Name, terminating.ResourceVersion)
	require.NoError(t, err)
	require.NotNil(t, deleted)
	assert.Equal(t, terminating.ResourceVersion, deleted.ResourceVersion)
	_, err = svc.GetNamespaceByName(ctx, ns.Name)
	require.Error(t, err)
}

func TestCompleteNamespaceDeletion_staleVersionConflicts(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("stale-complete", model.NamespaceTierUser), "alice")
	require.NoError(t, err)
	_, err = svc.DeleteNamespace(ctx, ns)
	require.NoError(t, err)

	current, err := svc.CompleteNamespaceDeletion(ctx, ns.Name, ns.ResourceVersion)
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.NotNil(t, current)
	assert.Equal(t, "2", current.ResourceVersion)
}

func TestCreateRepository_terminatingNamespaceRejected(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("repo-race-delete", model.NamespaceTierUser), "alice")
	require.NoError(t, err)
	_, err = svc.DeleteNamespace(ctx, ns)
	require.NoError(t, err)
	terminating, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	_, err = svc.CreateRepository(ctx, ns.ID, "late-repo", "main", "default", "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminating")
	assert.Equal(t, "2", terminating.ResourceVersion)
}

type namespaceDeleteCreateRaceStore struct {
	datastore.Datastore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *namespaceDeleteCreateRaceStore) blockDeletionMark() {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
}

func (s *namespaceDeleteCreateRaceStore) UpdateNamespace(
	ctx context.Context,
	namespace *datastore.Namespace,
	expectedResourceVersion string,
) error {
	if namespace.DeletionTimestamp != nil {
		s.blockDeletionMark()
	}
	return s.Datastore.UpdateNamespace(ctx, namespace, expectedResourceVersion)
}

func (s *namespaceDeleteCreateRaceStore) MarkNamespaceDeletion(
	ctx context.Context,
	namespace *datastore.Namespace,
	expectedResourceVersion string,
) error {
	s.blockDeletionMark()
	type deletionMarker interface {
		MarkNamespaceDeletion(context.Context, *datastore.Namespace, string) error
	}
	return s.Datastore.(deletionMarker).MarkNamespaceDeletion(ctx, namespace, expectedResourceVersion)
}

func TestNamespaceReplicaRepositoryCreateCannotCommitAcrossDeletionMark(t *testing.T) {
	ctx := context.Background()
	seed := newTestSvc(t, &mockGitWriter{})
	namespace, err := seed.CreateNamespace(ctx, createNamespaceInput("delete-create-race", model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	store := &namespaceDeleteCreateRaceStore{
		Datastore: seed.Store(),
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	deleteReplica, err := resolver.NewService(resolver.ServiceDeps{
		Store:     store,
		GitWriter: &mockGitWriter{},
		Logger:    zap.NewNop(),
	})
	require.NoError(t, err)
	createReplica, err := resolver.NewService(resolver.ServiceDeps{
		Store:     store,
		GitWriter: &mockGitWriter{},
		Logger:    zap.NewNop(),
	})
	require.NoError(t, err)

	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := deleteReplica.DeleteNamespace(ctx, namespace)
		deleteDone <- deleteErr
	}()

	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("namespace deletion did not reach its conditional mark")
	}
	repository, err := createReplica.CreateRepository(ctx, namespace.ID, "winner", "main", "default", "bob")
	require.NoError(t, err)
	close(store.release)
	select {
	case deleteErr := <-deleteDone:
		requireDeletionReasons(t, deleteErr, namespaceadmission.ReasonNamespaceNotEmpty)
	case <-time.After(2 * time.Second):
		t.Fatal("namespace deletion did not finish")
	}

	current, err := seed.GetNamespaceByName(ctx, namespace.Name)
	require.NoError(t, err)
	assert.Nil(t, current.DeletionTimestamp)
	persistedRepository, err := seed.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, namespace.Name, persistedRepository.Namespace)
}

func TestDeleteNamespace_recreatedIdentifierDoesNotDeleteReplacement(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("auth-test-ns", model.NamespaceTierUser)
	authorized, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	require.NoError(t, svc.Store().DeleteNamespace(context.Background(), authorized.ID))
	replacement, err := svc.CreateNamespace(context.Background(), input, "mallory")
	require.NoError(t, err)

	_, err = svc.DeleteNamespace(context.Background(), authorized)
	require.Error(t, err)

	got, err := svc.GetNamespaceByName(context.Background(), input.Metadata.Name)
	require.NoError(t, err)
	assert.Equal(t, replacement.ID, got.ID)
}
