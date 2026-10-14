// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type repositoryHistoryReader struct {
	*mockGitReader
	proof func(context.Context, string, string, string, string) ([]byte, error)
}

func (r *repositoryHistoryReader) ReadCurrentUnchangedFile(ctx context.Context, repository, path, refName, ancestor string) (bool, []byte, error) {
	current, err := r.ResolveRef(ctx, repository, refName)
	if err != nil {
		return false, nil, err
	}
	if current == ancestor {
		return true, nil, nil
	}
	content, err := r.proof(ctx, repository, path, current, ancestor)
	return false, content, err
}

func TestCommittedRepositoryDisjointCommitsPreserveRequestAudit(t *testing.T) {
	for _, advanceAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("advance-at-check-%d", advanceAt), func(t *testing.T) {
			ctx := context.Background()
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierOrganization}))
			require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
			seed, mine, theirs := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			path := "repositories/catalog.md"
			content := repositoryManifestFor("catalog", "acme", "trunk", "premium")
			files := map[string]map[string][]byte{
				seed:   {path: repositoryManifestFor("catalog", "acme", "main", "standard")},
				mine:   {path: content},
				theirs: {path: content, "repositories/other.md": repositoryManifestFor("other", "acme", "main", "standard")},
			}
			current := seed
			reader := &repositoryHistoryReader{mockGitReader: newTreeGitReader(&current, files)}
			reader.proof = func(_ context.Context, repository, requestedPath, tip, ancestor string) ([]byte, error) {
				assert.Equal(t, testRepoID, repository)
				assert.Equal(t, path, requestedPath)
				require.Equal(t, mine, ancestor)
				require.Equal(t, theirs, tip)
				require.True(t, bytes.Equal(files[ancestor][path], files[tip][path]))
				return files[tip][path], nil
			}
			srv := newCatalogServer(t, store, reader)
			request := admission.CommittedManifestRequest{RepositoryID: testRepoID, Namespace: "acme", ActorSubject: "alice", CommitSHA: seed, RefName: "refs/heads/main", Path: path, Content: files[seed][path], Operation: admission.OperationCreate}
			_, err = srv.AdmitCommittedManifest(ctx, request)
			require.NoError(t, err)
			current = mine
			check := 0
			reader.resolveRefFunc = func(context.Context, string, string) (string, error) {
				check++
				if check >= advanceAt {
					current = theirs
				}
				return current, nil
			}
			request.CommitSHA, request.Content, request.Operation = mine, content, admission.OperationUpdate
			result, err := srv.AdmitCommittedManifest(ctx, request)
			require.NoError(t, err)
			require.Equal(t, mine, result.CommitSHA)
			mapping, err := store.LookupRepository(ctx, "acme", "catalog")
			require.NoError(t, err)
			stored, err := store.GetRepository(ctx, mapping.RepositoryID)
			require.NoError(t, err)
			assert.Equal(t, "trunk", stored.DefaultBranch)
			assert.Equal(t, mine, stored.GitCommitSHA)
			assert.Equal(t, "alice", stored.UpdateActor)
			assert.Equal(t, int64(2), stored.Generation)
		})
	}
}

type repositoryUpdateHookStore struct {
	datastore.Datastore
	beforeUpdate func(context.Context) error
}

func (s *repositoryUpdateHookStore) UpdateRepository(ctx context.Context, repository *datastore.Repository, expected string) error {
	if hook := s.beforeUpdate; hook != nil {
		s.beforeUpdate = nil
		if err := hook(ctx); err != nil {
			return err
		}
	}
	return s.Datastore.UpdateRepository(ctx, repository, expected)
}

func TestCommittedRepositoryConflictNeverOverwritesNewerReplica(t *testing.T) {
	ctx := context.Background()
	base, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierOrganization}))
	require.NoError(t, base.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	seed, older, newer := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	path := "repositories/catalog.md"
	files := map[string]map[string][]byte{
		seed:  {path: repositoryManifestFor("catalog", "acme", "main", "standard")},
		older: {path: repositoryManifestFor("catalog", "acme", "older", "premium")},
		newer: {path: repositoryManifestFor("catalog", "acme", "newer", "premium")},
	}
	current := seed
	reader := &repositoryHistoryReader{mockGitReader: newTreeGitReader(&current, files),
		proof: func(context.Context, string, string, string, string) ([]byte, error) {
			return nil, grpcstatus.Error(codes.Aborted, "same path changed")
		},
	}
	store := &repositoryUpdateHookStore{Datastore: base}
	peer := newCatalogServer(t, base, reader)
	stale := newCatalogServer(t, store, reader)
	request := admission.CommittedManifestRequest{RepositoryID: testRepoID, Namespace: "acme", ActorSubject: "alice", CommitSHA: seed, RefName: "refs/heads/main", Path: path, Content: files[seed][path], Operation: admission.OperationCreate}
	_, err = stale.AdmitCommittedManifest(ctx, request)
	require.NoError(t, err)
	current = older
	store.beforeUpdate = func(ctx context.Context) error {
		current = newer
		newRequest := request
		newRequest.CommitSHA, newRequest.Content, newRequest.Operation, newRequest.ActorSubject = newer, files[newer][path], admission.OperationUpdate, "bob"
		_, err := peer.AdmitCommittedManifest(ctx, newRequest)
		return err
	}
	request.CommitSHA, request.Content, request.Operation = older, files[older][path], admission.OperationUpdate
	_, err = stale.AdmitCommittedManifest(ctx, request)
	require.ErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
	require.Nil(t, store.beforeUpdate, "the newer replica raced after the old path check and before CAS")
	mapping, err := base.LookupRepository(ctx, "acme", "catalog")
	require.NoError(t, err)
	stored, err := base.GetRepository(ctx, mapping.RepositoryID)
	require.NoError(t, err)
	assert.Equal(t, newer, stored.GitCommitSHA)
	assert.Equal(t, "newer", stored.DefaultBranch)
	assert.Equal(t, "bob", stored.UpdateActor)
	assert.Equal(t, int64(2), stored.Generation)
}

func TestCommittedRepositoryHistoryFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		content    []byte
		proofError error
		superseded bool
	}{
		{name: "same path changed", proofError: grpcstatus.Error(codes.Aborted, "changed"), superseded: true},
		{name: "path removed", proofError: grpcstatus.Error(codes.NotFound, "missing"), superseded: true},
		{name: "proof content mismatch", content: []byte("different"), superseded: true},
		{name: "unavailable Git", proofError: grpcstatus.Error(codes.Unavailable, "unavailable")},
		{name: "history time budget", proofError: grpcstatus.Error(codes.ResourceExhausted, "budget exhausted")},
		{name: "corrupt history", proofError: grpcstatus.Error(codes.Internal, "corrupt object")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			mine, theirs := strings.Repeat("a", 40), strings.Repeat("b", 40)
			current := theirs
			reader := &repositoryHistoryReader{mockGitReader: newTreeGitReader(&current, nil),
				proof: func(context.Context, string, string, string, string) ([]byte, error) {
					return tc.content, tc.proofError
				},
			}
			srv := newCatalogServer(t, store, reader)
			_, err = srv.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
				RepositoryID: testRepoID, Namespace: "acme", ActorSubject: "alice", CommitSHA: mine,
				RefName: "refs/heads/main", Path: "repositories/catalog.md",
				Content: repositoryManifestFor("catalog", "acme", "main", "standard"), Operation: admission.OperationCreate,
			})
			require.Error(t, err)
			if tc.superseded {
				require.ErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
			} else {
				require.NotErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
			}
			_, err = store.LookupRepository(ctx, "acme", "catalog")
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

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
	newOwner  string
}

func (s *repositoryConflictOnceStore) UpdateRepository(ctx context.Context, repository *datastore.Repository, expectedResourceVersion string) error {
	if s.remaining.CompareAndSwap(1, 0) {
		if s.newOwner != "" {
			current, err := s.Datastore.GetRepository(ctx, repository.UID)
			if err != nil {
				return err
			}
			expected := current.ResourceVersion
			current.Annotations = map[string]string{datastore.OwnerAnnotationKey: s.newOwner}
			datastore.AdvanceRepositorySystemVersion(current)
			if err := s.Datastore.UpdateRepository(ctx, current, expected); err != nil {
				return err
			}
		}
		return datastore.ErrConflict
	}
	return s.Datastore.UpdateRepository(ctx, repository, expectedResourceVersion)
}

func TestRepositoryCASRetryReauthorizesChangedOwner(t *testing.T) {
	ctx := context.Background()
	base, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000171", Name: "acme", Tier: datastore.NamespaceTierOrganization}))
	require.NoError(t, base.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	uid := "00000000-0000-0000-0000-000000000172"
	require.NoError(t, base.CreateRepository(ctx, &datastore.Repository{UID: uid, ID: uid, RepositoryID: uid, Namespace: "acme", Name: "catalog", APIVersion: "gitstore.dev/v1beta1", Kind: "Repository", DefaultBranch: "main", StorageClass: "standard", Generation: 1, ResourceVersion: "1", CreationActor: "alice"}))
	require.NoError(t, base.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", Name: "catalog", RepositoryID: uid}))
	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(policyPath, []byte(`apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
defaultDeny: true
clusterRoles:
  owner:
    rules:
      - resources: [repository]
        verbs: [update, transfer]
        when: [owner]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: owner}
    subjects: [{kind: User, name: alice}]
`), 0600))
	provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: policyPath}, zap.NewNop())
	require.NoError(t, err)
	store := &repositoryConflictOnceStore{Datastore: base, newOwner: "bob"}
	store.remaining.Store(1)
	path, commit := "repositories/catalog.md", strings.Repeat("d", 40)
	content := repositoryManifestFor("catalog", "acme", "trunk", "premium")
	current := commit
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{commit: {path: content}}), func(deps *cataloggrpc.ServerDeps) { deps.Registry = auth.NewProviderRegistry(nil, provider, nil) })
	_, err = srv.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{RepositoryID: testRepoID, Namespace: "acme", ActorSubject: "alice", CommitSHA: commit, RefName: "refs/heads/main", Path: path, Content: content, Operation: admission.OperationUpdate})
	require.Error(t, err)
	require.Zero(t, store.remaining.Load())
	stored, err := base.GetRepository(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, "bob", stored.EffectiveOwnerSub())
	require.Equal(t, "main", stored.DefaultBranch)
}

func TestValidateResources_RepositoryAuthoringTarget(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000101", Name: "acme", Tier: datastore.NamespaceTierUser}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
	srv := newCatalogServer(t, store, nil)

	valid, err := srv.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID, Trees: []*catalogv1.ResourceValidationTree{{ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "repositories/catalog.md", Content: []byte(repositoryManifest)}}}}})
	require.NoError(t, err)
	assert.True(t, valid.Accepted)

	wrongPath, err := srv.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID, Trees: []*catalogv1.ResourceValidationTree{{ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "catalog/catalog.md", Content: []byte(repositoryManifest)}}}}})
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

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "test-admission-actor", AuthMethod: "static-users"}}, RepositoryId: testRepoID, OldCommitSha: zero, NewCommitSha: first, RefName: "refs/heads/main", ChangedPaths: []string{path}})
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
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "test-admission-actor", AuthMethod: "static-users"}}, RepositoryId: testRepoID, OldCommitSha: first, NewCommitSha: second, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.NoError(t, err)
	updated, err := store.GetRepository(ctx, created.UID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, "trunk", updated.DefaultBranch)
	assert.Equal(t, "premium", updated.StorageClass)
}

func TestAdmitResources_InfrastructureProvenanceOnlyUpdate(t *testing.T) {
	for _, kind := range []string{"Namespace", "Repository"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := newNamespacePolicyDatastore(t)
			path := "namespaces/provenance.md"
			content := namespaceManifest("provenance", "Provenance", "ORGANIZATION")
			if kind == "Repository" {
				path = "repositories/provenance.md"
				content = repositoryManifestFor("provenance", "gitstore-system", "main", "standard")
			}
			rewritten := []byte(strings.Replace(string(content), "---\n", "---\n# provenance-only rewrite\n", 1))
			first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
			current := first
			reader := newTreeGitReader(&current, map[string]map[string][]byte{
				first: {path: content}, second: {path: rewritten},
			})
			registry := auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)
			srv := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) { deps.Registry = registry })
			request := &catalogv1.AdmitResourcesRequest{
				Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
				RepositoryId:  testRepoID, OldCommitSha: strings.Repeat("0", 40), NewCommitSha: first,
				RefName: "refs/heads/main", ChangedPaths: []string{path},
			}
			_, err := srv.AdmitResources(ctx, request)
			require.NoError(t, err)
			check := func(version, commit string) {
				t.Helper()
				var generation int64
				var resourceVersion, gitCommit, revision string
				if kind == "Namespace" {
					resource, readErr := store.GetNamespaceByName(ctx, "provenance")
					require.NoError(t, readErr)
					generation, resourceVersion, gitCommit, revision = resource.Generation, resource.ResourceVersion, resource.GitCommitSHA, resource.Revision
				} else {
					mapping, readErr := store.LookupRepository(ctx, "gitstore-system", "provenance")
					require.NoError(t, readErr)
					resource, readErr := store.GetRepository(ctx, mapping.RepositoryID)
					require.NoError(t, readErr)
					generation, resourceVersion, gitCommit, revision = resource.Generation, resource.ResourceVersion, resource.GitCommitSHA, resource.Revision
				}
				require.EqualValues(t, 1, generation)
				require.Equal(t, version, resourceVersion)
				require.Equal(t, commit, gitCommit)
				require.Equal(t, "main@sha1:"+commit, revision)
			}
			check("1", first)
			deny := testutil.NewDenyAllAuthZ(t)
			registry.Swap(nil, deny, nil)
			validation := &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID, Trees: []*catalogv1.ResourceValidationTree{{
				OldBlobs:      []*catalogv1.ResourceBlob{{Path: path, Content: content}},
				ProposedBlobs: []*catalogv1.ResourceBlob{{Path: path, Content: rewritten}},
			}}}
			_, err = srv.ValidateResources(ctx, validation)
			require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err), "pre-receive must require update permission for a provenance-only rewrite")
			require.NotEmpty(t, deny.Calls)
			require.Equal(t, strings.ToLower(kind[:1])+kind[1:]+".update", deny.Calls[len(deny.Calls)-1].Action)
			current = second
			request.OldCommitSha, request.NewCommitSha = first, second
			_, err = srv.AdmitResources(ctx, request)
			require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err), "post-receive must reauthorize the current policy")
			check("1", first)
			registry.Swap(nil, testutil.NewAllowAllAuthZ(), nil)
			_, err = srv.AdmitResources(ctx, request)
			require.NoError(t, err)
			check("2", second)
			_, err = srv.AdmitResources(ctx, request)
			require.NoError(t, err)
			check("2", second)
		})
	}
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

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "test-admission-actor", AuthMethod: "static-users"}}, RepositoryId: testRepoID, OldCommitSha: first, NewCommitSha: downgrade, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.NoError(t, err)
	unchanged, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "main", unchanged.DefaultBranch)
	assert.Equal(t, "premium", unchanged.StorageClass)
	assert.Equal(t, int64(3), unchanged.Generation)

	current = renamed
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "test-admission-actor", AuthMethod: "static-users"}}, RepositoryId: testRepoID, OldCommitSha: first, NewCommitSha: renamed, RefName: "refs/heads/main", ChangedPaths: []string{path}})
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
		RepositoryId: uid, OldCommitSha: oldCommit, NewCommitSha: newCommit,
		RefName: "refs/heads/main", Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "mallory", AuthMethod: "static-users"}}, ChangedPaths: []string{path},
	})
	require.NoError(t, err)
	notTerminating, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Nil(t, notTerminating.DeletionTimestamp, "a non-system authoring repository must not delete Repository metadata")
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: oldCommit, NewCommitSha: newCommit,
		RefName: "refs/heads/feature", Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "mallory", AuthMethod: "static-users"}}, ChangedPaths: []string{path},
	})
	require.NoError(t, err)
	notTerminating, err = store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Nil(t, notTerminating.DeletionTimestamp, "a different ref must not delete the Repository owned by main")

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: oldCommit, NewCommitSha: newCommit,
		RefName: "refs/heads/main", Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}}, ChangedPaths: []string{path},
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
			require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: testRepoID, ID: testRepoID, RepositoryID: testRepoID, Namespace: "acme", Name: "gitstore-system"}))
			require.NoError(t, store.CreateNamespace(ctx, namespace))
			commit := strings.Repeat("a", 40)
			path := "repositories/" + test.manifestName + ".md"
			current := commit
			srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{commit: {path: repositoryManifestFor(test.manifestName, "acme", "main", "standard")}}))
			_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "test-admission-actor", AuthMethod: "static-users"}}, RepositoryId: testRepoID, OldCommitSha: strings.Repeat("0", 40), NewCommitSha: commit, RefName: "refs/heads/main", ChangedPaths: []string{path}})
			require.NoError(t, err)
			_, err = store.LookupRepository(ctx, "acme", test.manifestName)
			assert.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

// catalogOwnerRaceStore changes the owner after authorization and before the
// authoritative write, as another API replica could do.
type catalogOwnerRaceStore struct {
	datastore.Datastore
	injected atomic.Bool
}

func (s *catalogOwnerRaceStore) UpdateProduct(ctx context.Context, proposed *datastore.Product, expected string) error {
	if s.injected.CompareAndSwap(false, true) {
		current, err := s.Datastore.GetProduct(ctx, proposed.UID)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.ResourceVersion = "2"
		current.Annotations = map[string]string{datastore.OwnerAnnotationKey: "bob"}
		if err := s.Datastore.UpdateProduct(ctx, current, previous); err != nil {
			return err
		}
	}
	return s.Datastore.UpdateProduct(ctx, proposed, expected)
}

func (s *catalogOwnerRaceStore) UpdateCategoryTaxonomy(ctx context.Context, proposed *datastore.CategoryTaxonomy, expected string) error {
	if s.injected.CompareAndSwap(false, true) {
		current, err := s.Datastore.GetCategoryTaxonomy(ctx, proposed.UID)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.ResourceVersion = "2"
		current.Annotations = map[string]string{datastore.OwnerAnnotationKey: "bob"}
		if err := s.Datastore.UpdateCategoryTaxonomy(ctx, current, previous); err != nil {
			return err
		}
	}
	return s.Datastore.UpdateCategoryTaxonomy(ctx, proposed, expected)
}

func (s *catalogOwnerRaceStore) UpdateCollection(ctx context.Context, proposed *datastore.Collection, expected string) error {
	if s.injected.CompareAndSwap(false, true) {
		current, err := s.Datastore.GetCollection(ctx, proposed.UID)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.ResourceVersion = "2"
		current.Annotations = map[string]string{datastore.OwnerAnnotationKey: "bob"}
		if err := s.Datastore.UpdateCollection(ctx, current, previous); err != nil {
			return err
		}
	}
	return s.Datastore.UpdateCollection(ctx, proposed, expected)
}

func (s *catalogOwnerRaceStore) UpdateProductVariant(ctx context.Context, proposed *datastore.ProductVariant, expected string) error {
	if s.injected.CompareAndSwap(false, true) {
		current, err := s.Datastore.GetProductVariant(ctx, proposed.UID)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.ResourceVersion = "2"
		current.Annotations = map[string]string{datastore.OwnerAnnotationKey: "bob"}
		if err := s.Datastore.UpdateProductVariant(ctx, current, previous); err != nil {
			return err
		}
	}
	return s.Datastore.UpdateProductVariant(ctx, proposed, expected)
}

func TestCatalogCASRepairReauthorizesConcurrentOwner(t *testing.T) {
	for _, tc := range []struct {
		kind, path string
		content    []byte
	}{
		{"Product", "products/widget.md", makeProduct("widget")},
		{"CategoryTaxonomy", "categories/widget.md", makeCategoryTaxonomy("widget")},
		{"Collection", "collections/widget.md", []byte(strings.Replace(string(makeProduct("widget")), "kind: Product", "kind: Collection", 1))},
		{"ProductVariant", "variants/widget.md", makeProductVariant("widget", "race-sku")},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			ctx := context.Background()
			base := newTestDatastore(t)
			store := &catalogOwnerRaceStore{Datastore: base}
			a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
			current := a
			updated := append(append([]byte{}, tc.content...), []byte("\nChanged body\n")...)
			git := newTreeGitReader(&current, map[string]map[string][]byte{a: {tc.path: tc.content}, b: {tc.path: updated}})
			bootstrap := newCatalogServer(t, base, git)
			_, err := bootstrap.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, OldCommitSha: strings.Repeat("0", 40), NewCommitSha: a, RefName: "refs/heads/main",
				Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
			})
			require.NoError(t, err)
			policyPath := filepath.Join(t.TempDir(), "policy.yaml")
			require.NoError(t, os.WriteFile(policyPath, []byte(`apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
defaultDeny: true
clusterRoles:
  owner:
    rules:
      - resources: [product, categoryTaxonomy, collection, productVariant]
        verbs: [update, transfer]
        when: [owner]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: owner}
    subjects: [{kind: User, name: alice}]
`), 0600))
			provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: policyPath}, zap.NewNop())
			require.NoError(t, err)
			srv := newCatalogServer(t, store, git, func(deps *cataloggrpc.ServerDeps) { deps.Registry = auth.NewProviderRegistry(nil, provider, nil) })
			current = b
			_, err = srv.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
				RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice", CommitSHA: b,
				RefName: "refs/heads/main", Path: tc.path, Content: updated, Operation: admission.OperationUpdate,
			})
			require.Error(t, err)
			require.True(t, store.injected.Load(), "test must reach the authorized write: %v", err)
			var owner, version, body, commit string
			switch tc.kind {
			case "Product":
				row, getErr := base.GetProductByName(ctx, "gitstore", "widget")
				require.NoError(t, getErr)
				owner, version, body, commit = row.EffectiveOwnerSub(), row.ResourceVersion, row.Body, row.GitCommitSHA
			case "CategoryTaxonomy":
				row, getErr := base.GetCategoryTaxonomyByName(ctx, "gitstore", "widget")
				require.NoError(t, getErr)
				owner, version, body, commit = row.EffectiveOwnerSub(), row.ResourceVersion, row.Body, row.GitCommitSHA
			case "Collection":
				row, getErr := base.GetCollectionByName(ctx, "gitstore", "widget")
				require.NoError(t, getErr)
				owner, version, body, commit = row.EffectiveOwnerSub(), row.ResourceVersion, row.Body, row.GitCommitSHA
			case "ProductVariant":
				row, getErr := base.GetProductVariantByName(ctx, "gitstore", "widget")
				require.NoError(t, getErr)
				owner, version, body, commit = row.EffectiveOwnerSub(), row.ResourceVersion, row.Body, row.GitCommitSHA
			}
			assert.Equal(t, "bob", owner)
			assert.Equal(t, "2", version)
			assert.NotContains(t, body, "Changed body")
			assert.Equal(t, a, commit)
		})
	}
}
