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
	"github.com/vektah/gqlparser/v2/gqlerror"
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

func TestRepositoryCommittedMutationPreservesAdmissionEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		code    admission.Code
		reason  string
	}{
		{"version-conflict", admission.NewError(admission.CodeConflict, "RESOURCE_VERSION_CONFLICT", "snapshot changed"), admission.CodeConflict, "RESOURCE_VERSION_CONFLICT"},
		{"superseded", admission.ErrCommittedManifestSuperseded, admission.CodeConflict, "SUPERSEDED"},
		{"rejection", admission.NewError(admission.CodeFailedPrecondition, "REPOSITORY_TERMINATING", "deletion pending"), admission.CodeFailedPrecondition, "REPOSITORY_TERMINATING"},
		{"unexpected-admission-failure", errors.New("admission unavailable"), admission.CodeAdmissionRejected, "ADMISSION_FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCategoryLifecycleEnv(t)
			e.service.committedAdmitter = infrastructureAdmitFunc(func(context.Context, admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
				return nil, tc.failure
			})
			_, err := e.service.CommitRepositoryManifest(t.Context(), repositoryAPIVersion, repositoryKind,
				&model.ObjectMetaInput{Name: "envelope", Namespace: lifecycleNamespace}, &model.RepositorySpecInput{}, "alice", true)
			var graphErr *gqlerror.Error
			require.ErrorAs(t, err, &graphErr)
			require.Equal(t, string(tc.code), graphErr.Extensions["code"])
			diagnostics, ok := graphErr.Extensions["diagnostics"].([]map[string]any)
			require.True(t, ok)
			require.Len(t, diagnostics, 1)
			require.Equal(t, tc.reason, diagnostics[0]["reason"])
			if tc.code == admission.CodeAdmissionRejected {
				require.Equal(t, string(admission.PhasePostReceive), graphErr.Extensions["phase"])
				require.NotEmpty(t, graphErr.Extensions["commit"])
			} else {
				require.NotContains(t, graphErr.Extensions, "phase")
				require.NotContains(t, graphErr.Extensions, "commit")
			}
		})
	}
}

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

func createInfrastructureNamespace(t *testing.T, e *categoryLifecycleEnv, name string) *datastore.Namespace {
	t.Helper()
	ctx := t.Context()
	const bootstrapUID = "00000000-0000-0000-0000-00000000b000"
	const bootstrapRepo = "00000000-0000-0000-0000-00000000b001"
	require.NoError(t, e.store.CreateNamespace(ctx, &datastore.Namespace{UID: bootstrapUID, Name: SystemRepositoryName}))
	require.NoError(t, e.store.CreateRepository(ctx, &datastore.Repository{UID: bootstrapRepo, Namespace: SystemRepositoryName, Name: SystemRepositoryName}))
	require.NoError(t, e.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: SystemRepositoryName, Name: SystemRepositoryName, RepositoryID: bootstrapRepo}))
	ns, err := e.service.CreateNamespace(ctx, model.CreateNamespaceInput{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace",
		Metadata: &model.NamespaceMetadataInput{Name: name},
		Spec:     &model.NamespaceSpecInput{Tier: model.NamespaceTierOrganization},
	}, "alice")
	require.NoError(t, err)
	return ns
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
	removal, err := e.git.ResolveRefForRepo(ctx, intent.RepositoryID, intent.Ref)
	require.NoError(t, err)
	head := e.git.write(intent.RepositoryID, "unrelated.md", []byte("unrelated authoring work"))
	peer, err := NewService(ServiceDeps{Store: e.store, GitWriter: e.git, Logger: zap.NewNop(), CommittedManifestAdmitter: e.catalog})
	require.NoError(t, err)
	_, err = peer.CompleteRepositoryDeletion(ctx, pending.Namespace, pending.Name, pending.ResourceVersion, pending.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	current, err := e.store.GetRepository(ctx, before.UID)
	require.NoError(t, err)
	require.NotNil(t, current.DeletionTimestamp)
	require.Equal(t, before.UID, current.UID)
	require.Equal(t, before.Generation, current.Generation)
	intent, err = admission.ReadDeletionIntent(current.Status)
	require.NoError(t, err)
	require.Equal(t, removal, intent.RemovalCommit)
	actualHead, err := e.git.ResolveRefForRepo(ctx, intent.RepositoryID, intent.Ref)
	require.NoError(t, err)
	require.Equal(t, head, actualHead)
	require.Len(t, e.git.deletes, 1)
}

func TestNamespaceDeletionPeerRecoversLostGitResponseAfterUnrelatedCommit(t *testing.T) {
	e := newCategoryLifecycleEnv(t)
	ctx := t.Context()
	before := createInfrastructureNamespace(t, e, "lost-response")
	require.NoError(t, e.service.ProvisionSystemRepository(ctx, before.Name, "controller"))
	e.service.gitWriter = lostDeletionResponse{e.git}
	_, err := e.service.DeleteNamespace(ctx, before)
	require.Error(t, err)
	pending, err := e.store.GetNamespaceByName(ctx, before.Name)
	require.NoError(t, err)
	require.Nil(t, pending.DeletionTimestamp)
	intent, err := admission.ReadDeletionIntent(pending.Status)
	require.NoError(t, err)
	require.NotNil(t, intent)
	require.Empty(t, intent.RemovalCommit, "the response containing the deletion receipt was lost")
	removal, err := e.git.ResolveRefForRepo(ctx, intent.RepositoryID, intent.Ref)
	require.NoError(t, err)
	head := e.git.write(intent.RepositoryID, "unrelated.md", []byte("unrelated authoring work"))
	require.NotEqual(t, removal, head)

	peer, err := NewService(ServiceDeps{Store: e.store, GitWriter: e.git, Logger: zap.NewNop(), CommittedManifestAdmitter: e.catalog})
	require.NoError(t, err)
	_, err = peer.CompleteNamespaceDeletion(ctx, pending.Name, pending.ResourceVersion, pending.UID)
	require.ErrorIs(t, err, datastore.ErrConflict, "recovery publishes a newer terminating version before completion")
	current, err := e.store.GetNamespaceByName(ctx, before.Name)
	require.NoError(t, err)
	require.Equal(t, before.UID, current.UID)
	require.Equal(t, before.Generation, current.Generation)
	require.NotNil(t, current.DeletionTimestamp)
	intent, err = admission.ReadDeletionIntent(current.Status)
	require.NoError(t, err)
	require.Equal(t, removal, intent.RemovalCommit, "retain the original receipt, not the unrelated current tip")
	require.Len(t, e.git.deletes, 1, "peer recovery must not create a second removal commit")
	actualHead, err := e.git.ResolveRefForRepo(ctx, intent.RepositoryID, intent.Ref)
	require.NoError(t, err)
	require.Equal(t, head, actualHead)
	_, err = peer.CompleteNamespaceDeletion(ctx, current.Name, current.ResourceVersion, current.UID)
	require.NoError(t, err)
	_, err = e.store.GetNamespaceByName(ctx, current.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = e.store.LookupRepository(ctx, current.Name, SystemRepositoryName)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

type lostStorageDeletionResponse struct {
	GitWriter
	deleted  map[string]bool
	calls    []string
	retryErr error
}

func (w *lostStorageDeletionResponse) DeleteRepository(_ context.Context, uid string) error {
	w.calls = append(w.calls, uid)
	if w.deleted[uid] {
		return w.retryErr
	}
	w.deleted[uid] = true
	return status.Error(codes.Unavailable, "response lost after storage removal")
}

func TestInfrastructureCompletionPeerRecoversLostStorageResponse(t *testing.T) {
	for _, tc := range []struct {
		name           string
		retryErr       error
		missingMapping bool
	}{
		{name: "idempotent-native-retry"},
		{name: "not-found-retry", retryErr: status.Error(codes.NotFound, "storage was already removed")},
		{name: "partial-name-cleanup", missingMapping: true},
	} {
		for _, kind := range []string{"Repository", "Namespace"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				e := newCategoryLifecycleEnv(t)
				ctx := t.Context()
				var parent *datastore.Namespace
				var repository *datastore.Repository
				if kind == "Namespace" {
					parent = createInfrastructureNamespace(t, e, "storage-response")
					require.NoError(t, e.service.ProvisionSystemRepository(ctx, parent.Name, "controller"))
					_, err := e.service.DeleteNamespace(ctx, parent)
					require.NoError(t, err)
					parent, err = e.store.GetNamespaceByName(ctx, parent.Name)
					require.NoError(t, err)
					mapping, err := e.store.LookupRepository(ctx, parent.Name, SystemRepositoryName)
					require.NoError(t, err)
					repository, err = e.store.GetRepository(ctx, mapping.RepositoryID)
					require.NoError(t, err)
				} else {
					repository = createInfrastructureRepository(t, e, "storage-response")
					var err error
					repository, _, err = e.service.deleteRepositoryWithOutcome(ctx, repository.UID, "alice")
					require.NoError(t, err)
				}
				writer := &lostStorageDeletionResponse{GitWriter: e.git, deleted: map[string]bool{}, retryErr: tc.retryErr}
				e.service.gitWriter = writer
				complete := func(service *Service, resourceVersion string) error {
					if parent != nil {
						_, err := service.CompleteNamespaceDeletion(ctx, parent.Name, resourceVersion, parent.UID)
						return err
					}
					_, err := service.CompleteRepositoryDeletion(ctx, repository.Namespace, repository.Name, resourceVersion, repository.UID)
					return err
				}
				version := repository.ResourceVersion
				if parent != nil {
					version = parent.ResourceVersion
				}
				require.Error(t, complete(e.service, version))
				require.True(t, writer.deleted[repository.UID])
				require.Equal(t, []string{repository.UID}, writer.calls)
				retained, err := e.store.GetRepository(ctx, repository.UID)
				require.NoError(t, err)
				require.NotNil(t, retained.DeletionTimestamp)
				require.Contains(t, retained.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
				mapping, err := e.store.LookupRepository(ctx, repository.Namespace, repository.Name)
				require.NoError(t, err)
				require.Equal(t, repository.UID, mapping.RepositoryID)
				if parent != nil {
					current, err := e.store.GetNamespaceByName(ctx, parent.Name)
					require.NoError(t, err)
					require.Equal(t, parent.UID, current.UID)
					require.Equal(t, parent.ResourceVersion, current.ResourceVersion)
					require.Equal(t, parent.Finalizers, current.Finalizers)
				}
				peer, err := NewService(ServiceDeps{Store: e.store, GitWriter: writer, Logger: zap.NewNop(), CommittedManifestAdmitter: e.catalog})
				require.NoError(t, err)
				require.ErrorIs(t, complete(peer, "stale"), datastore.ErrConflict)
				require.Len(t, writer.calls, 1, "stale completion cannot retry storage removal")
				if tc.missingMapping {
					require.NoError(t, e.store.DeleteNamespaceMapping(ctx, repository.Namespace, repository.Name))
					if parent != nil {
						require.ErrorContains(t, complete(peer, version), "still contains repositories")
						require.Len(t, writer.calls, 1, "parent cleanup must wait for the UID-bound child cleanup")
						current, err := e.store.GetNamespaceByName(ctx, parent.Name)
						require.NoError(t, err)
						require.Equal(t, parent.UID, current.UID)
						require.Equal(t, parent.ResourceVersion, current.ResourceVersion)
						_, err = peer.CompleteRepositoryDeletion(ctx, retained.Namespace, retained.Name, retained.ResourceVersion, retained.UID)
						require.NoError(t, err, "the Repository controller can resume its retained UID without a name mapping")
					}
				}
				require.NoError(t, complete(peer, version))
				require.Equal(t, []string{repository.UID, repository.UID}, writer.calls)
				_, err = e.store.GetRepository(ctx, repository.UID)
				require.ErrorIs(t, err, datastore.ErrNotFound)
				_, err = e.store.LookupRepository(ctx, repository.Namespace, repository.Name)
				require.ErrorIs(t, err, datastore.ErrNotFound)
				if parent != nil {
					_, err = e.store.GetNamespaceByName(ctx, parent.Name)
					require.ErrorIs(t, err, datastore.ErrNotFound)
				}
				require.NoError(t, complete(peer, version), "completed cleanup remains idempotent")
				require.Len(t, writer.calls, 2, "completed cleanup must not touch storage again")
			})
		}
	}
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
				RefName: before.GitRef, ChangedPaths: []string{before.SourcePath}, Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "operator", AuthMethod: "static-users"}},
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
		Spec:     &model.NamespaceSpecInput{Tier: model.NamespaceTierOrganization},
	}, "alice")
	require.NoError(t, err)
	commit := e.git.write(author, ns.SourcePath, nil)
	_, err = e.catalog.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: author, OldCommitSha: ns.GitCommitSHA, NewCommitSha: commit,
		RefName: ns.GitRef, ChangedPaths: []string{ns.SourcePath}, Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
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
	const bootstrapRepo = "00000000-0000-0000-0000-00000000b001"
	ns := createInfrastructureNamespace(t, e, "removable")
	require.NoError(t, e.service.ProvisionSystemRepository(ctx, ns.Name, "controller"))
	_, err := e.service.DeleteNamespace(ctx, ns)
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
	mapping, err := e.store.LookupRepository(ctx, current.Name, SystemRepositoryName)
	require.NoError(t, err)
	system, err := e.store.GetRepository(ctx, mapping.RepositoryID)
	require.NoError(t, err)
	expected := current.ResourceVersion
	current.Finalizers = append(current.Finalizers, "example.test/retain")
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, e.store.UpdateNamespace(ctx, current, expected))
	_, err = e.service.CompleteNamespaceDeletion(ctx, current.Name, current.ResourceVersion, current.UID)
	require.ErrorContains(t, err, "other finalizers")
	_, err = e.service.CompleteRepositoryDeletion(ctx, system.Namespace, system.Name, system.ResourceVersion, system.UID)
	require.ErrorContains(t, err, "other finalizers", "direct System Repository completion must honor the Namespace blocker")
	preserved, err := e.store.GetRepository(ctx, system.UID)
	require.NoError(t, err)
	require.Equal(t, system.ResourceVersion, preserved.ResourceVersion)
	require.Nil(t, preserved.DeletionTimestamp, "an unrelated Namespace finalizer must block System Repository cleanup")
	require.Equal(t, system.Finalizers, preserved.Finalizers)
	preservedNamespace, err := e.store.GetNamespace(ctx, current.UID)
	require.NoError(t, err)
	require.Equal(t, current.ResourceVersion, preservedNamespace.ResourceVersion)
	require.Equal(t, current.Finalizers, preservedNamespace.Finalizers)
	expected = current.ResourceVersion
	current.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, e.store.UpdateNamespace(ctx, current, expected))
	_, err = e.service.CompleteNamespaceDeletion(ctx, current.Name, current.ResourceVersion, current.UID)
	require.NoError(t, err)
	_, err = e.store.GetNamespaceByName(ctx, current.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = e.store.LookupRepository(ctx, current.Name, "gitstore-system")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

type infrastructureCompletionReadHook struct {
	GitWriter
	afterRemovalRead func()
}

func (w *infrastructureCompletionReadHook) ReadFileForRepo(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
	content, err := w.GitWriter.ReadFileForRepo(ctx, repositoryID, path, ref)
	if status.Code(err) == codes.NotFound && w.afterRemovalRead != nil {
		hook := w.afterRemovalRead
		w.afterRemovalRead = nil
		hook()
	}
	return content, err
}

type infrastructureCompletionNamespaceReadHook struct {
	datastore.Datastore
	name            string
	reads           int
	afterParentRead func()
}

func (s *infrastructureCompletionNamespaceReadHook) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	current, err := s.Datastore.GetNamespaceByName(ctx, name)
	if err == nil && name == s.name {
		s.reads++
		if s.reads == 2 && s.afterParentRead != nil {
			hook := s.afterParentRead
			s.afterParentRead = nil
			hook()
		}
	}
	return current, err
}

func TestNamespaceCompletionPreservesReplacementAcrossSystemRepositorySelection(t *testing.T) {
	for _, stage := range []string{"after_manifest_read", "after_parent_read"} {
		t.Run(stage, func(t *testing.T) {
			e := newCategoryLifecycleEnv(t)
			ctx := t.Context()
			const bootstrapUID = "00000000-0000-0000-0000-00000000b000"
			const bootstrapRepo = "00000000-0000-0000-0000-00000000b001"
			require.NoError(t, e.store.CreateNamespace(ctx, &datastore.Namespace{UID: bootstrapUID, Name: "gitstore-system"}))
			require.NoError(t, e.store.CreateRepository(ctx, &datastore.Repository{UID: bootstrapRepo, Namespace: "gitstore-system", Name: "gitstore-system"}))
			require.NoError(t, e.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "gitstore-system", Name: "gitstore-system", RepositoryID: bootstrapRepo}))
			input := model.CreateNamespaceInput{
				APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace",
				Metadata: &model.NamespaceMetadataInput{Name: "completion-replacement"},
				Spec:     &model.NamespaceSpecInput{Tier: model.NamespaceTierOrganization},
			}
			original, err := e.service.CreateNamespace(ctx, input, "alice")
			require.NoError(t, err)
			require.NoError(t, e.service.ProvisionSystemRepository(ctx, original.Name, "controller"))
			_, err = e.service.DeleteNamespace(ctx, original)
			require.NoError(t, err)
			terminating, err := e.store.GetNamespaceByName(ctx, original.Name)
			require.NoError(t, err)
			peer, err := NewService(ServiceDeps{Store: e.store, GitWriter: e.git, Logger: zap.NewNop(), CommittedManifestAdmitter: e.catalog})
			require.NoError(t, err)
			var replacement *datastore.Namespace
			var replacementRepository *datastore.Repository
			replaced := false
			replace := func() {
				_, err := peer.CompleteNamespaceDeletion(ctx, terminating.Name, terminating.ResourceVersion, terminating.UID)
				require.NoError(t, err)
				replacement, err = peer.CreateNamespace(ctx, input, "mallory")
				require.NoError(t, err)
				require.NotEqual(t, original.UID, replacement.UID)
				require.NoError(t, peer.ProvisionSystemRepository(ctx, replacement.Name, "controller"))
				mapping, err := e.store.LookupRepository(ctx, replacement.Name, SystemRepositoryName)
				require.NoError(t, err)
				replacementRepository, err = e.store.GetRepository(ctx, mapping.RepositoryID)
				require.NoError(t, err)
				require.Nil(t, replacementRepository.DeletionTimestamp)
				replaced = true
			}
			if stage == "after_manifest_read" {
				e.service.gitWriter = &infrastructureCompletionReadHook{GitWriter: e.git, afterRemovalRead: replace}
			} else {
				e.service.store = &infrastructureCompletionNamespaceReadHook{
					Datastore: e.store, name: terminating.Name, afterParentRead: replace,
				}
			}
			observed, err := e.service.CompleteNamespaceDeletion(ctx, terminating.Name, terminating.ResourceVersion, terminating.UID)
			require.True(t, replaced, "peer replacement must occur at the selected interleaving")
			if stage == "after_manifest_read" {
				require.ErrorIs(t, err, datastore.ErrConflict)
				require.NotNil(t, observed)
				require.Equal(t, replacement.UID, observed.UID)
				require.Equal(t, replacement.ResourceVersion, observed.ResourceVersion)
			} else {
				require.ErrorIs(t, err, datastore.ErrNotFound, "the captured old System Repository must fail its UID-bound CAS")
			}
			require.NotNil(t, replacementRepository)
			current, getErr := e.store.GetRepository(ctx, replacementRepository.UID)
			require.NoError(t, getErr)
			require.Equal(t, replacementRepository.ResourceVersion, current.ResourceVersion)
			require.Equal(t, replacementRepository.Finalizers, current.Finalizers)
			require.Nil(t, current.DeletionTimestamp, "stale Namespace completion must not terminate the replacement's System Repository")
			namespace, getErr := e.store.GetNamespaceByName(ctx, replacement.Name)
			require.NoError(t, getErr)
			require.Equal(t, replacement.UID, namespace.UID)
			require.Nil(t, namespace.DeletionTimestamp)
		})
	}
}
