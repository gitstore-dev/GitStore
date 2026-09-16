// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const repositoryManifest = `---
apiVersion: gitstore.dev/v1beta1
kind: Repository
metadata:
  name: catalog
  namespace: acme
spec:
  defaultBranch: main
  visibility: PRIVATE
  storageClass: standard
---
`

type repositoryConflictOnceStore struct {
	datastore.Datastore
	remaining atomic.Int32
}

func (s *repositoryConflictOnceStore) UpdateRepository(ctx context.Context, repository *datastore.Repository, expectedResourceVersion string) error {
	if s.remaining.CompareAndSwap(1, 0) {
		return datastore.ErrConflict
	}
	return s.Datastore.UpdateRepository(ctx, repository, expectedResourceVersion)
}

func TestValidateResources_RepositoryAuthoringTarget(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierUser}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	srv := newCatalogServer(t, store, nil)

	valid, err := srv.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID, Blobs: []*catalogv1.ResourceBlob{{Path: "repositories/catalog.md", Content: []byte(repositoryManifest)}}})
	require.NoError(t, err)
	assert.True(t, valid.Accepted)

	wrongPath, err := srv.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID, Blobs: []*catalogv1.ResourceBlob{{Path: "catalog/catalog.md", Content: []byte(repositoryManifest)}}})
	require.NoError(t, err)
	assert.False(t, wrongPath.Accepted)
	assert.Contains(t, wrongPath.Errors[0].Message, "repositories/catalog.md")
}

func TestValidateResources_RepositoryDeletionRejectsDependentCatalogResources(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierUser}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	const repositoryID = "00000000-0000-0000-0000-000000000102"
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID: repositoryID, ID: repositoryID, RepositoryID: repositoryID, Namespace: "acme", NamespaceID: "acme", Name: "catalog",
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository", ResourceVersion: "1", Generation: 1,
		SourcePath: "repositories/catalog.md", GitRef: "refs/heads/main",
	}))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", Name: "catalog", RepositoryID: repositoryID}))
	require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
		UID: "00000000-0000-0000-0000-000000000103", Namespace: "acme", Name: "dependent-product", RepositoryID: repositoryID,
	}))

	response, err := newCatalogServer(t, store, nil).ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{
		RepositoryId: testRepoID,
		Trees: []*catalogv1.ResourceValidationTree{{
			OldBlobs: []*catalogv1.ResourceBlob{{Path: "repositories/catalog.md", Content: repositoryManifestFor("catalog", "acme", "main", "standard")}},
		}},
	})
	require.NoError(t, err)
	require.False(t, response.Accepted)
	require.Len(t, response.Errors, 1)
	assert.Equal(t, "dependent_resources", response.Errors[0].Constraint)
	assert.Contains(t, response.Errors[0].Message, "cannot be deleted")
}

func repositoryManifestFor(name, namespace, branch, storageClass string) []byte {
	return []byte("---\napiVersion: gitstore.dev/v1beta1\nkind: Repository\nmetadata:\n  name: " + name + "\n  namespace: " + namespace + "\nspec:\n  defaultBranch: " + branch + "\n  visibility: PRIVATE\n  storageClass: " + storageClass + "\n---\n")
}

func TestAdmitResources_RepositoryLifecycleWritesCanonicalNamespaceOwnerReference(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	namespace := &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierUser}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))

	zero := strings.Repeat("0", 40)
	first := strings.Repeat("a", 40)
	second := strings.Repeat("b", 40)
	current := first
	path := "repositories/catalog.md"
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		first:  {path: repositoryManifestFor("catalog", "acme", "main", "standard")},
		second: {path: repositoryManifestFor("catalog", "acme", "trunk", "premium")},
	}))

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, OldCommitSha: zero, NewCommitSha: first, CommitSha: first, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.NoError(t, err)
	createdMapping, err := store.LookupRepository(ctx, "acme", "catalog")
	require.NoError(t, err)
	created, err := store.GetRepository(ctx, createdMapping.RepositoryID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), created.Generation)
	assert.Equal(t, "1", created.ResourceVersion)
	assert.Equal(t, "main", created.DefaultBranch)
	assert.Equal(t, "standard", created.StorageClass)
	var refs []catalog.OwnerReference
	require.NoError(t, json.Unmarshal(created.OwnerReferences, &refs))
	require.Equal(t, []catalog.OwnerReference{{APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", Name: "acme", UID: namespace.UID, BlockOwnerDeletion: true}}, refs)

	current = second
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, OldCommitSha: first, NewCommitSha: second, CommitSha: second, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.NoError(t, err)
	updated, err := store.GetRepository(ctx, created.UID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, "trunk", updated.DefaultBranch)
	assert.Equal(t, "premium", updated.StorageClass)
}

func TestAdmitCommittedManifest_RepositoryUsesTheBatchAdmissionPath(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	namespace := &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000151", Name: "acme", Tier: datastore.NamespaceTierUser}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))

	path := "repositories/catalog.md"
	commit := strings.Repeat("c", 40)
	content := repositoryManifestFor("catalog", "acme", "main", "premium")
	current := commit
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{commit: {path: content}}))

	result, err := srv.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
		RepositoryID: testRepoID, Namespace: "acme", ActorSubject: "alice",
		CommitSHA: commit, RefName: "refs/heads/main", Path: path, Content: content,
		Operation: admission.OperationCreate,
	})
	require.NoError(t, err)
	assert.Equal(t, "Repository", result.Kind)
	assert.Equal(t, "catalog", result.Name)
	mapping, err := store.LookupRepository(ctx, "acme", "catalog")
	require.NoError(t, err)
	repository, err := store.GetRepository(ctx, mapping.RepositoryID)
	require.NoError(t, err)
	assert.Equal(t, "premium", repository.StorageClass)
	assert.Equal(t, "main@sha1:"+commit, repository.Revision)
	assert.Equal(t, "refs/heads/main", repository.GitRef)
	var refs []catalog.OwnerReference
	require.NoError(t, json.Unmarshal(repository.OwnerReferences, &refs))
	assert.Equal(t, namespace.UID, refs[0].UID)
}

func TestAdmitCommittedManifest_RepositoryRetriesConcurrentStatusConflict(t *testing.T) {
	ctx := context.Background()
	base, err := memdb.New()
	require.NoError(t, err)
	defer base.Close()
	require.NoError(t, base.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000171", Name: "acme", Tier: datastore.NamespaceTierUser}))
	require.NoError(t, base.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	uid := "00000000-0000-0000-0000-000000000172"
	require.NoError(t, base.CreateRepository(ctx, &datastore.Repository{
		UID: uid, ID: uid, RepositoryID: uid, Namespace: "acme", NamespaceID: "acme", Name: "catalog",
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository", DefaultBranch: "main", StorageClass: "standard",
		Generation: 1, ResourceVersion: "1",
	}))
	require.NoError(t, base.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", Name: "catalog", RepositoryID: uid}))

	store := &repositoryConflictOnceStore{Datastore: base}
	store.remaining.Store(1)
	path := "repositories/catalog.md"
	commit := strings.Repeat("d", 40)
	content := repositoryManifestFor("catalog", "acme", "trunk", "premium")
	current := commit
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{commit: {path: content}}))

	_, err = srv.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
		RepositoryID: testRepoID, Namespace: "acme", ActorSubject: "alice",
		CommitSHA: commit, RefName: "refs/heads/main", Path: path, Content: content,
		Operation: admission.OperationUpdate,
	})
	require.NoError(t, err)
	assert.Zero(t, store.remaining.Load(), "the injected status-write conflict was exercised")
	updated, err := base.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "trunk", updated.DefaultBranch)
	assert.Equal(t, "premium", updated.StorageClass)
	assert.Equal(t, commit, updated.GitCommitSHA)
}

func TestAdmitResources_RepositoryRejectsDowngradeAndImmutableIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierUser}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	uid := "00000000-0000-0000-0000-000000000102"
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: uid, ID: uid, RepositoryID: uid, Namespace: "acme", Name: "catalog", DefaultBranch: "main", StorageClass: "premium", Generation: 3, ResourceVersion: "4"}))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", Name: "catalog", RepositoryID: uid}))

	first := strings.Repeat("a", 40)
	downgrade := strings.Repeat("b", 40)
	renamed := strings.Repeat("c", 40)
	current := downgrade
	path := "repositories/catalog.md"
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		first:     {path: repositoryManifestFor("catalog", "acme", "main", "premium")},
		downgrade: {path: repositoryManifestFor("catalog", "acme", "trunk", "standard")},
		renamed:   {path: repositoryManifestFor("renamed", "acme", "main", "premium")},
	}))
	validation, err := srv.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{
		RepositoryId: testRepoID,
		Trees: []*catalogv1.ResourceValidationTree{{
			OldBlobs:      []*catalogv1.ResourceBlob{{Path: path, Content: repositoryManifestFor("catalog", "acme", "main", "premium")}},
			ProposedBlobs: []*catalogv1.ResourceBlob{{Path: path, Content: repositoryManifestFor("catalog", "acme", "trunk", "standard")}},
		}},
	})
	require.NoError(t, err)
	require.False(t, validation.Accepted)
	require.Len(t, validation.Errors, 1)
	assert.Equal(t, "spec.storageClass", validation.Errors[0].Field)
	assert.Equal(t, "immutable_downgrade", validation.Errors[0].Constraint)

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, OldCommitSha: first, NewCommitSha: downgrade, CommitSha: downgrade, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.NoError(t, err)
	unchanged, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "main", unchanged.DefaultBranch)
	assert.Equal(t, "premium", unchanged.StorageClass)
	assert.Equal(t, int64(3), unchanged.Generation)

	current = renamed
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, OldCommitSha: first, NewCommitSha: renamed, CommitSha: renamed, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, grpcstatus.Code(err))
	_, err = store.LookupRepository(ctx, "acme", "renamed")
	assert.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestAdmitResources_RepositoryManifestRemovalStartsForegroundDeletion(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierUser}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	uid := "00000000-0000-0000-0000-000000000102"
	path := "repositories/catalog.md"
	oldCommit, newCommit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID: uid, ID: uid, RepositoryID: uid, Namespace: "acme", NamespaceID: "acme", Name: "catalog",
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository", ResourceVersion: "3", Generation: 1,
		SourcePath: path, GitCommitSHA: oldCommit, GitRef: "refs/heads/main",
	}))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", Name: "catalog", RepositoryID: uid}))
	current := newCommit
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		oldCommit: {path: repositoryManifestFor("catalog", "acme", "main", "standard")},
		newCommit: {},
	}))
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: uid, OldCommitSha: oldCommit, NewCommitSha: newCommit, CommitSha: newCommit,
		RefName: "refs/heads/main", ActorSubject: "mallory", ChangedPaths: []string{path},
	})
	require.NoError(t, err)
	notTerminating, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Nil(t, notTerminating.DeletionTimestamp, "a non-system authoring repository must not delete Repository metadata")
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: oldCommit, NewCommitSha: newCommit, CommitSha: newCommit,
		RefName: "refs/heads/feature", ActorSubject: "mallory", ChangedPaths: []string{path},
	})
	require.NoError(t, err)
	notTerminating, err = store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Nil(t, notTerminating.DeletionTimestamp, "a different ref must not delete the Repository owned by main")

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: oldCommit, NewCommitSha: newCommit, CommitSha: newCommit,
		RefName: "refs/heads/main", ActorSubject: "alice", ChangedPaths: []string{path},
	})
	require.NoError(t, err)
	terminating, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	require.NotNil(t, terminating.DeletionTimestamp)
	assert.Contains(t, terminating.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
	assert.Equal(t, "alice", terminating.UpdateActor)
	assert.Equal(t, "4", terminating.ResourceVersion)
}

func TestAdmitResources_RepositoryRejectsBootstrapAndTerminatingNamespace(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name, manifestName string
		terminating        bool
	}{
		{name: "bootstrap", manifestName: "gitstore-system"},
		{name: "terminating namespace", manifestName: "catalog", terminating: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			defer store.Close()
			now := time.Now().UTC()
			namespace := &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierUser}
			if test.terminating {
				namespace.DeletionTimestamp = &now
			}
			require.NoError(t, store.CreateNamespace(ctx, namespace))
			require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
			commit := strings.Repeat("a", 40)
			path := "repositories/" + test.manifestName + ".md"
			current := commit
			srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{commit: {path: repositoryManifestFor(test.manifestName, "acme", "main", "standard")}}))
			_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, OldCommitSha: strings.Repeat("0", 40), NewCommitSha: commit, CommitSha: commit, RefName: "refs/heads/main", ChangedPaths: []string{path}})
			require.NoError(t, err)
			_, err = store.LookupRepository(ctx, "acme", test.manifestName)
			assert.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}
