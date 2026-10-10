// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRepositoryDeletionRemovesAuthoringManifest(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	_, err := e.service.CommitRepositoryManifest(ctx, "gitstore.dev/v1beta1", "Repository",
		&model.ObjectMetaInput{Name: "removable", Namespace: lifecycleNamespace}, &model.RepositorySpecInput{}, "alice", true)
	require.NoError(t, err)
	mapping, err := e.store.LookupRepository(ctx, lifecycleNamespace, "removable")
	require.NoError(t, err)
	before, err := e.store.GetRepository(ctx, mapping.RepositoryID)
	require.NoError(t, err)
	deleted, started, err := e.service.deleteRepositoryWithOutcome(ctx, before.UID, "alice")
	require.NoError(t, err)
	require.True(t, started)
	require.NotNil(t, deleted.DeletionTimestamp)
	require.Equal(t, before.Generation, deleted.Generation)
	_, err = e.git.ReadFile(ctx, lifecycleSystemRepoID, "repositories/removable.md", "refs/heads/main")
	require.Equal(t, codes.NotFound, status.Code(err))
	_, started, err = e.service.deleteRepositoryWithOutcome(ctx, before.UID, "alice")
	require.NoError(t, err)
	require.False(t, started)
	require.Len(t, e.git.deletes, 1)
	assertInfrastructureReadFailuresBlockCompletion(t, e, func() error {
		_, err := e.service.CompleteRepositoryDeletion(ctx, lifecycleNamespace, before.Name, deleted.ResourceVersion, before.UID)
		return err
	})
	_, err = e.service.CompleteRepositoryDeletion(ctx, lifecycleNamespace, before.Name, deleted.ResourceVersion, before.UID)
	require.NoError(t, err)
	_, err = e.store.GetRepository(ctx, before.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

type infrastructureAdmitFunc func(context.Context, admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error)

func (f infrastructureAdmitFunc) AdmitCommittedManifest(ctx context.Context, req admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
	return f(ctx, req)
}

func createInfrastructureRepository(t *testing.T, e *categoryLifecycleEnv, name string) *datastore.Repository {
	t.Helper()
	_, err := e.service.CommitRepositoryManifest(context.Background(), "gitstore.dev/v1beta1", "Repository",
		&model.ObjectMetaInput{Name: name, Namespace: lifecycleNamespace}, &model.RepositorySpecInput{}, "alice", true)
	require.NoError(t, err)
	mapping, err := e.store.LookupRepository(context.Background(), lifecycleNamespace, name)
	require.NoError(t, err)
	repo, err := e.store.GetRepository(context.Background(), mapping.RepositoryID)
	require.NoError(t, err)
	return repo
}

func TestInfrastructureDeletionRecoversAfterAdmissionProcessLoss(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	before := createInfrastructureRepository(t, e, "recover")
	e.service.committedAdmitter = infrastructureAdmitFunc(func(context.Context, admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
		return nil, errors.New("admission process lost")
	})
	_, _, err := e.service.deleteRepositoryWithOutcome(ctx, before.UID, "alice")
	require.Error(t, err)
	pending, err := e.store.GetRepository(ctx, before.UID)
	require.NoError(t, err)
	require.Nil(t, pending.DeletionTimestamp)
	intent, err := admission.ReadDeletionIntent(pending.Status)
	require.NoError(t, err)
	require.Equal(t, "alice", intent.Actor)
	require.NotEmpty(t, intent.RemovalCommit)

	replacement, err := NewService(ServiceDeps{Store: e.store, GitWriter: e.git, Logger: zap.NewNop(), CommittedManifestAdmitter: e.catalog})
	require.NoError(t, err)
	_, err = replacement.CompleteRepositoryDeletion(ctx, pending.Namespace, pending.Name, pending.ResourceVersion, pending.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	terminating, err := e.store.GetRepository(ctx, before.UID)
	require.NoError(t, err)
	require.NotNil(t, terminating.DeletionTimestamp)
	require.Equal(t, before.Generation, terminating.Generation)
	require.Len(t, e.git.deletes, 1, "recovery must not create another removal commit")
	_, err = replacement.CompleteRepositoryDeletion(ctx, terminating.Namespace, terminating.Name, terminating.ResourceVersion, terminating.UID)
	require.NoError(t, err)
}

type lostDeletionResponse struct{ GitWriter }

type unavailableDeletionRead struct {
	GitWriter
	readErr        error
	storageDeletes int
}

func (w *unavailableDeletionRead) ReadFileForRepo(context.Context, string, string, string) ([]byte, error) {
	return nil, w.readErr
}

func (w *unavailableDeletionRead) DeleteRepository(context.Context, string) error {
	w.storageDeletes++
	return errors.New("storage deletion must not be attempted")
}

func assertInfrastructureReadFailuresBlockCompletion(t *testing.T, e *categoryLifecycleEnv, complete func() error) {
	t.Helper()
	writer := e.service.gitWriter
	defer func() { e.service.gitWriter = writer }()
	for _, fault := range []struct {
		name string
		code codes.Code
	}{
		{"authoring-repository-missing", codes.FailedPrecondition},
		{"authoring-ref-missing", codes.FailedPrecondition},
		{"path-is-directory", codes.FailedPrecondition},
		{"transport-unavailable", codes.Unavailable},
		{"unreadable-tree", codes.Internal},
		{"read-denied", codes.PermissionDenied},
	} {
		t.Run(fault.name, func(t *testing.T) {
			failed := &unavailableDeletionRead{GitWriter: writer, readErr: status.Error(fault.code, fault.name)}
			e.service.gitWriter = failed
			err := complete()
			require.Error(t, err)
			require.Equal(t, fault.code, status.Code(err))
			require.Zero(t, failed.storageDeletes)
		})
	}
}

func (w lostDeletionResponse) DeleteFileForRepo(ctx context.Context, repo string, p gitclient.DeleteFileParams) (string, error) {
	_, err := w.GitWriter.DeleteFileForRepo(ctx, repo, p)
	if err != nil {
		return "", err
	}
	return "", status.Error(codes.Unavailable, "response lost after commit")
}

func TestInfrastructureDeletionRecoversLostGitResponse(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	before := createInfrastructureRepository(t, e, "lost-response")
	e.service.gitWriter = lostDeletionResponse{e.git}
	_, _, err := e.service.deleteRepositoryWithOutcome(ctx, before.UID, "alice")
	require.Error(t, err)
	pending, err := e.store.GetRepository(ctx, before.UID)
	require.NoError(t, err)
	intent, err := admission.ReadDeletionIntent(pending.Status)
	require.NoError(t, err)
	require.Empty(t, intent.RemovalCommit, "the API never received the commit")
	e.service.gitWriter = e.git
	_, err = e.service.CompleteRepositoryDeletion(ctx, pending.Namespace, pending.Name, pending.ResourceVersion, pending.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	current, err := e.store.GetRepository(ctx, before.UID)
	require.NoError(t, err)
	require.NotNil(t, current.DeletionTimestamp)
	require.Len(t, e.git.deletes, 1)
}

func TestInfrastructureDeletionDoesNotTreatArbitraryAbsenceAsSuccess(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	before := createInfrastructureRepository(t, e, "missing")
	head, err := e.git.ResolveRef(ctx, lifecycleSystemRepoID, before.GitRef)
	require.NoError(t, err)
	intent := &admission.InfrastructureDeletionIntent{
		UID: before.UID, RepositoryID: lifecycleSystemRepoID, Path: before.SourcePath,
		Ref: before.GitRef, ExpectedCommit: head, Actor: "alice",
	}
	require.NoError(t, e.service.saveDeletionIntent(ctx, before, intent))
	e.git.write(lifecycleSystemRepoID, before.SourcePath, nil)
	_, err = e.service.CompleteRepositoryDeletion(ctx, before.Namespace, before.Name, before.ResourceVersion, before.UID)
	require.Error(t, err)
	current, err := e.store.GetRepository(ctx, before.UID)
	require.NoError(t, err)
	require.Nil(t, current.DeletionTimestamp)
	require.Empty(t, e.git.deletes)
}

func TestInfrastructureDeletionRejectsSupersededManifest(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	before := createInfrastructureRepository(t, e, "changed")
	e.git.write(lifecycleSystemRepoID, before.SourcePath, []byte("replacement content"))
	_, _, err := e.service.deleteRepositoryWithOutcome(context.Background(), before.UID, "alice")
	require.Error(t, err)
	require.Empty(t, e.git.deletes)
	current, err := e.store.GetRepository(context.Background(), before.UID)
	require.NoError(t, err)
	require.Nil(t, current.DeletionTimestamp)
	intent, err := admission.ReadDeletionIntent(current.Status)
	require.NoError(t, err)
	require.Nil(t, intent)
}

func TestInfrastructureAuthorizedPushRepairsIncompleteDeletion(t *testing.T) {
	for _, phase := range []string{"pending", "legacy-terminating"} {
		t.Run(phase, func(t *testing.T) {
			e := newCategoryLifecycleEnv(t)
			ctx := context.Background()
			before := createInfrastructureRepository(t, e, "repair")
			if phase == "pending" {
				require.NoError(t, e.service.saveDeletionIntent(ctx, before, &admission.InfrastructureDeletionIntent{
					UID: before.UID, RepositoryID: lifecycleSystemRepoID, Path: before.SourcePath,
					Ref: before.GitRef, ExpectedCommit: before.GitCommitSHA, Actor: "alice",
				}))
			} else {
				require.NoError(t, admission.MarkInfrastructureDeletion(ctx, e.store, before, e.service.clock.Now().UTC(), "alice"))
			}
			commit := e.git.write(lifecycleSystemRepoID, before.SourcePath, nil)
			_, err := e.catalog.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
				RepositoryId: lifecycleSystemRepoID, OldCommitSha: before.GitCommitSHA, NewCommitSha: commit,
				RefName: before.GitRef, ChangedPaths: []string{before.SourcePath}, ActorSubject: "operator",
			})
			require.NoError(t, err)
			current, err := e.store.GetRepository(ctx, before.UID)
			require.NoError(t, err)
			require.NotNil(t, current.DeletionTimestamp)
			intent, err := admission.ReadDeletionIntent(current.Status)
			require.NoError(t, err)
			require.NotNil(t, intent)
			require.Equal(t, commit, intent.RemovalCommit)
			_, err = e.service.CompleteRepositoryDeletion(ctx, current.Namespace, current.Name, current.ResourceVersion, current.UID)
			require.NoError(t, err)
		})
	}
}

func TestInfrastructureCompletionResumesWithoutNameMapping(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	before := createInfrastructureRepository(t, e, "partial-cleanup")
	terminating, _, err := e.service.deleteRepositoryWithOutcome(ctx, before.UID, "alice")
	require.NoError(t, err)
	require.NoError(t, e.store.DeleteNamespaceMapping(ctx, before.Namespace, before.Name))

	_, err = e.service.CompleteRepositoryDeletion(ctx, before.Namespace, "wrong-name", terminating.ResourceVersion, before.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	_, err = e.service.CompleteRepositoryDeletion(ctx, before.Namespace, before.Name, "stale", before.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	completed, err := e.service.CompleteRepositoryDeletion(ctx, before.Namespace, before.Name, terminating.ResourceVersion, before.UID)
	require.NoError(t, err)
	require.NotNil(t, completed)
	_, err = e.store.GetRepository(ctx, before.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	completed, err = e.service.CompleteRepositoryDeletion(ctx, before.Namespace, before.Name, terminating.ResourceVersion, before.UID)
	require.NoError(t, err)
	require.Nil(t, completed)
}

func TestInfrastructureCompletionRejectsStaleIncarnation(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	before := createInfrastructureRepository(t, e, "reuse")
	terminating, _, err := e.service.deleteRepositoryWithOutcome(ctx, before.UID, "alice")
	require.NoError(t, err)
	_, err = e.service.CompleteRepositoryDeletion(ctx, before.Namespace, before.Name, terminating.ResourceVersion, before.UID)
	require.NoError(t, err)
	replacement := createInfrastructureRepository(t, e, "reuse")
	require.NotEqual(t, before.UID, replacement.UID)
	_, err = e.service.CompleteRepositoryDeletion(ctx, replacement.Namespace, replacement.Name, replacement.ResourceVersion, before.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	current, err := e.store.GetRepository(ctx, replacement.UID)
	require.NoError(t, err)
	require.Nil(t, current.DeletionTimestamp)
}

func TestInfrastructureNamespacePushRemovalStartsTermination(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	const author = "00000000-0000-0000-0000-00000000b001"
	require.NoError(t, e.store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-00000000b000", Name: "gitstore-system"}))
	require.NoError(t, e.store.CreateRepository(ctx, &datastore.Repository{UID: author, Namespace: "gitstore-system", Name: "gitstore-system"}))
	require.NoError(t, e.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "gitstore-system", Name: "gitstore-system", RepositoryID: author}))
	ns, err := e.service.CreateNamespace(ctx, model.CreateNamespaceInput{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace",
		Metadata: &model.NamespaceMetadataInput{Name: "pushed"},
		Spec:     &model.NamespaceSpecInput{Tier: model.NamespaceTierUser},
	}, "alice")
	require.NoError(t, err)
	commit := e.git.write(author, ns.SourcePath, nil)
	_, err = e.catalog.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: author, OldCommitSha: ns.GitCommitSHA, NewCommitSha: commit,
		RefName: ns.GitRef, ChangedPaths: []string{ns.SourcePath}, ActorSubject: "alice",
	})
	require.NoError(t, err)
	current, err := e.store.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	require.NotNil(t, current.DeletionTimestamp)
	require.Equal(t, ns.Generation, current.Generation)
	assertInfrastructureReadFailuresBlockCompletion(t, e, func() error {
		_, err := e.service.CompleteNamespaceDeletion(ctx, current.Name, current.ResourceVersion, current.UID)
		return err
	})
	_, err = e.service.CompleteNamespaceDeletion(ctx, current.Name, current.ResourceVersion, current.UID)
	require.NoError(t, err)
}

func TestSystemRepositoryCannotBeDeletedByUser(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	_, _, err := e.service.deleteRepositoryWithOutcome(context.Background(), lifecycleSystemRepoID, "alice")
	require.Error(t, err)
	require.Empty(t, e.git.deletes)
}

func TestNamespaceDeletionRemovesManifestAndFinalizesEmptySystemRepository(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	const bootstrapUID = "00000000-0000-0000-0000-00000000b000"
	const bootstrapRepo = "00000000-0000-0000-0000-00000000b001"
	require.NoError(t, e.store.CreateNamespace(ctx, &datastore.Namespace{UID: bootstrapUID, Name: "gitstore-system"}))
	require.NoError(t, e.store.CreateRepository(ctx, &datastore.Repository{UID: bootstrapRepo, Namespace: "gitstore-system", Name: "gitstore-system"}))
	require.NoError(t, e.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "gitstore-system", Name: "gitstore-system", RepositoryID: bootstrapRepo}))
	ns, err := e.service.CreateNamespace(ctx, model.CreateNamespaceInput{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace",
		Metadata: &model.NamespaceMetadataInput{Name: "removable"},
		Spec:     &model.NamespaceSpecInput{Tier: model.NamespaceTierUser},
	}, "alice")
	require.NoError(t, err)
	require.NoError(t, e.service.ProvisionSystemRepository(ctx, ns.Name, "controller"))
	_, err = e.service.DeleteNamespace(ctx, ns)
	require.NoError(t, err)
	_, err = e.git.ReadFile(ctx, bootstrapRepo, "namespaces/removable.md", "refs/heads/main")
	require.Equal(t, codes.NotFound, status.Code(err))
	current, err := e.store.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	require.NotNil(t, current.DeletionTimestamp)
	require.Equal(t, ns.Generation, current.Generation)
	assertInfrastructureReadFailuresBlockCompletion(t, e, func() error {
		_, err := e.service.CompleteNamespaceDeletion(ctx, current.Name, current.ResourceVersion, current.UID)
		return err
	})
	_, err = e.service.CompleteNamespaceDeletion(ctx, current.Name, current.ResourceVersion, current.UID)
	require.NoError(t, err)
	_, err = e.store.GetNamespaceByName(ctx, current.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = e.store.LookupRepository(ctx, current.Name, "gitstore-system")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}
