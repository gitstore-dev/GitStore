// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	err = svc.DeleteNamespace(context.Background(), ns)
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
	err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)
}

func TestDeleteNamespace_withoutAuthorizationCheck_serviceAllowsDelete(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("alices-ns", model.NamespaceTierUser)
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)

	err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)
}

func TestDeleteNamespace_unknownIdentifier_notFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	err := svc.DeleteNamespace(context.Background(), &datastore.Namespace{ID: "does-not-exist", Name: "does-not-exist"})
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

	err = svc.DeleteNamespace(ctx, ns)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains repositories and cannot be deleted")

	_, err = svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)

	result, err := svc.Store().ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{})
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

	err = svc.DeleteNamespace(ctx, ns)
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

	require.NoError(t, svc.DeleteNamespace(ctx, ns))
	first, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteNamespace(ctx, first))
	second, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion)
	assert.Equal(t, first.DeletionTimestamp, second.DeletionTimestamp)
}

func TestDeleteNamespace_bootstrapRejected(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ns, err := svc.GetNamespaceByName(context.Background(), "gitstore-system")
	require.NoError(t, err)
	err = svc.DeleteNamespace(context.Background(), ns)
	require.ErrorContains(t, err, "system-managed")
}

func TestCompleteNamespaceDeletion_removesTerminatingNamespace(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("complete-delete", model.NamespaceTierUser), "alice")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteNamespace(ctx, ns))
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
	require.NoError(t, svc.DeleteNamespace(ctx, ns))

	current, err := svc.CompleteNamespaceDeletion(ctx, ns.Name, ns.ResourceVersion)
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.NotNil(t, current)
	assert.Equal(t, "2", current.ResourceVersion)
}

func TestCompleteNamespaceDeletion_rechecksRepositories(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("repo-race-delete", model.NamespaceTierUser), "alice")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteNamespace(ctx, ns))
	terminating, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	_, err = svc.CreateRepository(ctx, ns.ID, "late-repo", "main", "default", "alice")
	require.NoError(t, err)

	_, err = svc.CompleteNamespaceDeletion(ctx, ns.Name, terminating.ResourceVersion)
	require.ErrorContains(t, err, "still contains repositories")
}

func TestDeleteNamespace_recreatedIdentifierDoesNotDeleteReplacement(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := createNamespaceInput("auth-test-ns", model.NamespaceTierUser)
	authorized, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	require.NoError(t, svc.Store().DeleteNamespace(context.Background(), authorized.ID))
	replacement, err := svc.CreateNamespace(context.Background(), input, "mallory")
	require.NoError(t, err)

	err = svc.DeleteNamespace(context.Background(), authorized)
	require.Error(t, err)

	got, err := svc.GetNamespaceByName(context.Background(), input.Metadata.Name)
	require.NoError(t, err)
	assert.Equal(t, replacement.ID, got.ID)
}
