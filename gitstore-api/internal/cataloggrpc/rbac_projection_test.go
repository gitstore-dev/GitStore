// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type rbacProjectionFixture struct {
	server *catalogTestServer
	ds     datastore.Datastore
	store  datastore.RBACStore
	git    *mockGitReader
	tip    string
	trees  map[string]map[string][]byte
}

func newRBACProjectionFixture(t *testing.T, wrap func(datastore.Datastore) datastore.Datastore) *rbacProjectionFixture {
	t.Helper()
	ds := newNamespacePolicyDatastore(t)
	repository, err := ds.GetRepository(t.Context(), testRepoID)
	require.NoError(t, err)
	require.Equal(t, testRepoID, repository.UID)
	require.NoError(t, ds.CreateNamespaceMapping(t.Context(), &datastore.NamespaceMapping{
		Namespace: repository.Namespace, Name: repository.Name, RepositoryID: testRepoID,
	}))
	if wrap != nil {
		ds = wrap(ds)
	}
	f := &rbacProjectionFixture{ds: ds, store: ds.(datastore.RBACCapable).RBACStore(),
		tip: strings.Repeat("a", 40), trees: map[string]map[string][]byte{}}
	f.trees[f.tip] = map[string][]byte{}
	f.git = &mockGitReader{
		resolveRefFunc: func(context.Context, string, string) (string, error) { return f.tip, nil },
		listFilesFunc: func(_ context.Context, repositoryID, prefix, ref string) ([]string, error) {
			require.Equal(t, testRepoID, repositoryID)
			var paths []string
			for path := range f.trees[ref] {
				if strings.HasPrefix(path, prefix) {
					paths = append(paths, path)
				}
			}
			slices.Sort(paths)
			return paths, nil
		},
		readFileFunc: func(_ context.Context, repositoryID, path, ref string) ([]byte, error) {
			require.Equal(t, testRepoID, repositoryID)
			content, ok := f.trees[ref][path]
			if !ok {
				return nil, grpcstatus.Error(codes.NotFound, "file not found")
			}
			return content, nil
		},
	}
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	engine, err := opa.NewEngine(t.Context())
	require.NoError(t, err)
	registry := auth.NewProviderRegistry(nil, &rbacCallbackProvider{engine}, nil)
	validator := &admission.RBACValidator{Builtins: builtins, ManagedStore: ds, Capture: rbac.ValidationCapture{Registry: registry}}
	f.server = newCatalogServer(t, ds, f.git, func(deps *cataloggrpc.ServerDeps) {
		deps.Registry, deps.RBACValidator = registry, validator
	})
	return f
}

func rbacProjectionRole(name, verb string) []byte {
	return []byte("apiVersion: " + rbac.APIVersion + "\nkind: ClusterRole\nmetadata: {name: " + name +
		"}\nspec:\n  rules: [{resources: [product], verbs: [" + verb + "]}]\n")
}

func (f *rbacProjectionFixture) receive(t *testing.T, old string, paths ...string) error {
	t.Helper()
	_, err := f.server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID,
		RefName: "refs/heads/main", OldCommitSha: old, NewCommitSha: f.tip, ChangedPaths: paths})
	return err
}

func TestRBACCommittedReceivePublishesWholeTierWithoutReauthorization(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	old := f.tip
	f.tip = strings.Repeat("b", 40)
	rolePath, bindingPath := "rbac/clusterroles/reader.yaml", "rbac/clusterrolebindings/access.yaml"
	f.trees[f.tip] = map[string][]byte{
		rolePath: rbacProjectionRole("reader", "read"),
		bindingPath: []byte("apiVersion: " + rbac.APIVersion + "\nkind: ClusterRoleBinding\nmetadata: {name: access}\n" +
			"spec:\n  roleRef: {kind: ClusterRole, name: reader}\n  subjects: [{kind: User, name: absent-user}]\n"),
	}
	require.NoError(t, f.receive(t, old, rolePath, bindingPath))
	snapshot, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Len(t, snapshot.Resources, 2)
	require.Equal(t, "main@sha1:"+f.tip, snapshot.State.Projection.GitRevision)
	require.Equal(t, fixtureCallbackActor().Subject, snapshot.State.Projection.Actor)
	for _, resource := range snapshot.Resources {
		require.NotEmpty(t, resource.Metadata.UID)
		require.Equal(t, int64(1), resource.Metadata.Generation)
		require.Contains(t, resource.Metadata.Finalizers, rbac.LifecycleFinalizer)
		require.Empty(t, resource.Status, "commit projection cannot fabricate a resolved grant")
	}
	require.NoError(t, f.receive(t, old, rolePath, bindingPath))
	replayed, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, snapshot, replayed, "a fresh signed replay after a lost response must be effect-free")

	source := rbac.OriginReader{Store: f.ds, Git: f.git}
	origin, err := source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	captured, err := (rbac.SnapshotReader{Store: f.store, ReadOrigin: source.ReadOrigin,
		CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest()}).Read(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, origin, captured.Origin)
}

func TestRBACCanonicalResyncRepairsAnEntirelyLostCallback(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	path := "rbac/clusterroles/reader.yaml"
	f.tip = strings.Repeat("b", 40)
	f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read")}
	require.NoError(t, f.server.ReconcileRBACTier(t.Context(), rbac.ClusterTierID))
	before, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Len(t, before.Resources, 1)
	require.Equal(t, "main@sha1:"+f.tip, before.State.Projection.GitRevision)
	require.Equal(t, "system:rbac-projection-repair", before.State.Projection.Actor)
	require.NoError(t, f.server.ReconcileRBACTier(t.Context(), rbac.ClusterTierID))
	replayed, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, before, replayed)
	f.tip = strings.Repeat("c", 40)
	f.trees[f.tip] = map[string][]byte{}
	require.NoError(t, f.server.ReconcileRBACTier(t.Context(), rbac.ClusterTierID))
	after, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Len(t, after.Resources, 1)
	require.Equal(t, before.Resources[0].Metadata.UID, after.Resources[0].Metadata.UID)
	require.NotNil(t, after.Resources[0].Metadata.DeletionTimestamp)
	require.Equal(t, "main@sha1:"+f.tip, after.State.Projection.GitRevision)
}

func TestRBACCanonicalResyncPreservesThenRemovesOrdinaryOwnerCollision(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	namespace := &datastore.Namespace{UID: uuid.NewString(), Name: "shop", Tier: datastore.NamespaceTierUser, CreationActor: "shop"}
	require.NoError(t, f.ds.CreateNamespace(t.Context(), namespace))
	repository := &datastore.Repository{UID: uuid.NewString(), Name: "gitstore-system", Namespace: namespace.Name, DefaultBranch: "main"}
	require.NoError(t, f.ds.CreateRepository(t.Context(), repository))
	require.NoError(t, f.ds.CreateNamespaceMapping(t.Context(), &datastore.NamespaceMapping{
		Namespace: namespace.Name, Name: repository.Name, RepositoryID: repository.UID,
	}))
	f.git.listFilesFunc = func(_ context.Context, id, prefix, ref string) ([]string, error) {
		require.Equal(t, repository.UID, id)
		var paths []string
		for path := range f.trees[ref] {
			if strings.HasPrefix(path, prefix) {
				paths = append(paths, path)
			}
		}
		return paths, nil
	}
	f.git.readFileFunc = func(_ context.Context, id, path, ref string) ([]byte, error) {
		require.Equal(t, repository.UID, id)
		return f.trees[ref][path], nil
	}
	ordinary := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.RoleBinding, NamespaceUID: namespace.UID,
		Metadata: rbac.Metadata{UID: uuid.NewString(), Name: rbac.ManagedBindingName, Namespace: namespace.Name},
		Spec:     json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"namespace-full-control"},"subjects":[{"kind":"User","name":"other"}]}`)}
	path := "rbac/rolebindings/namespace-owner-access.yaml"
	body, err := rbac.CanonicalManifest(ordinary)
	require.NoError(t, err)
	f.trees[f.tip][path] = body
	digest, err := rbac.ManifestTreeDigest([]*rbac.Resource{ordinary})
	require.NoError(t, err)
	identity := rbac.ProjectionIdentity{RepositoryID: repository.UID, GitRef: "refs/heads/main",
		GitRevision: "main@sha1:" + f.tip, TreeDigest: digest, Actor: "legacy-fixture"}
	_, err = f.store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: namespace.UID, Projection: &identity,
		Changes: []rbac.Change{{Resource: ordinary}}})
	require.NoError(t, err)
	f.tip = strings.Repeat("b", 40)
	f.trees[f.tip] = map[string][]byte{path: body}
	require.NoError(t, f.server.ReconcileRBACTier(t.Context(), namespace.UID), "unchanged ordinary collision is not an adopted managed binding")
	before, err := datastore.ReadRBACSnapshot(t.Context(), f.store, namespace.UID)
	require.NoError(t, err)
	require.Equal(t, ordinary.Metadata.UID, before.Resources[0].Metadata.UID)
	require.Nil(t, before.Resources[0].Metadata.DeletionTimestamp)
	f.tip = strings.Repeat("c", 40)
	f.trees[f.tip] = map[string][]byte{}
	require.NoError(t, f.server.ReconcileRBACTier(t.Context(), namespace.UID))
	after, err := datastore.ReadRBACSnapshot(t.Context(), f.store, namespace.UID)
	require.NoError(t, err)
	require.Equal(t, ordinary.Metadata.UID, after.Resources[0].Metadata.UID)
	require.NotNil(t, after.Resources[0].Metadata.DeletionTimestamp)
}

func TestRBACSnapshotRechecksLiveIdentityWithoutRereadingPinnedBlobs(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed-tip-%t", changed), func(t *testing.T) {
			f := newRBACProjectionFixture(t, nil)
			old := f.tip
			path := "rbac/clusterroles/reader.yaml"
			f.tip = strings.Repeat("b", 40)
			f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read")}
			require.NoError(t, f.receive(t, old, path))
			readFile, reads := f.git.readFileFunc, 0
			f.git.readFileFunc = func(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
				reads++
				return readFile(ctx, repositoryID, path, ref)
			}
			source := rbac.OriginReader{Store: f.ds, Git: f.git}
			builtins, err := rbac.ReleaseBuiltins()
			require.NoError(t, err)
			checks := 0
			reader := rbac.SnapshotReader{
				Store: f.store, ReadOrigin: source.ReadOrigin,
				CheckOrigin: func(ctx context.Context, tier string, origin rbac.SnapshotOrigin) error {
					checks++
					if changed {
						f.tip = strings.Repeat("c", 40)
						f.trees[f.tip] = map[string][]byte{}
					}
					return source.CheckOrigin(ctx, tier, origin)
				},
				CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(),
			}
			snapshot, err := reader.Read(t.Context(), rbac.ClusterTierID)
			require.Equal(t, 1, reads, "immutable blobs need only one complete scan")
			require.Equal(t, 1, checks, "live canonical identity must still be rechecked")
			if changed {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
				require.Empty(t, snapshot.Resources)
			} else {
				require.NoError(t, err)
				require.Len(t, snapshot.Resources, 1)
			}
		})
	}
}

func TestRBACCanonicalOriginChecksDoNotReadManifests(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	source := rbac.OriginReader{Store: f.ds, Git: f.git}
	origin, err := source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	f.git.listFilesFunc = func(context.Context, string, string, string) ([]string, error) {
		t.Error("an identity probe must not list manifests")
		return nil, rbac.ErrUnavailable
	}
	f.git.readFileFunc = func(context.Context, string, string, string) ([]byte, error) {
		t.Error("an identity probe must not read manifests")
		return nil, rbac.ErrUnavailable
	}
	require.NoError(t, source.CheckOrigin(t.Context(), rbac.ClusterTierID, origin))
	f.tip = strings.Repeat("b", 40)
	require.ErrorIs(t, source.CheckOrigin(t.Context(), rbac.ClusterTierID, origin), rbac.ErrUnavailable)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, source.CheckOrigin(ctx, rbac.ClusterTierID, origin), rbac.ErrUnavailable)
}

func TestRBACOriginDigestCacheRechecksLiveFactsWithoutRescanningImmutableFiles(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	path := "rbac/clusterroles/reader.yaml"
	f.trees[f.tip][path] = rbacProjectionRole("reader", "read")
	reads := 0
	readFile := f.git.readFileFunc
	f.git.readFileFunc = func(ctx context.Context, id, path, ref string) ([]byte, error) {
		reads++
		return readFile(ctx, id, path, ref)
	}
	facts := &originFactsStore{Datastore: f.ds}
	source := rbac.OriginReader{Store: facts, Git: f.git, Digests: new(rbac.OriginDigestCache)}
	first, err := source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	namespace, err := f.ds.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	version := namespace.ResourceVersion
	namespace.ResourceVersion = "2"
	require.NoError(t, f.ds.UpdateNamespace(t.Context(), namespace, version))
	second, err := source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, first.TreeDigest, second.TreeDigest)
	require.NotEqual(t, first.NamespaceVersion, second.NamespaceVersion)
	require.Equal(t, 1, reads, "unchanged immutable files must not consume more signed Git reads")

	f.git.resolveRefFunc = func(context.Context, string, string) (string, error) { return "", rbac.ErrUnavailable }
	_, err = source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.ErrorIs(t, err, rbac.ErrUnavailable, "a cached digest cannot replace a live ref read")
	f.git.resolveRefFunc = func(context.Context, string, string) (string, error) { return f.tip, nil }
	f.tip = strings.Repeat("b", 40)
	f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "create")}
	third, err := source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.NotEqual(t, first.TreeDigest, third.TreeDigest)
	require.Equal(t, 2, reads, "a new immutable commit must be verified before its digest is reused")
	for range 2 {
		f.tip = strings.Repeat("c", 40)
		f.trees[f.tip] = map[string][]byte{path: []byte("invalid manifest")}
		_, err = source.ReadOrigin(t.Context(), rbac.ClusterTierID)
		require.ErrorIs(t, err, rbac.ErrUnavailable)
	}
	require.Equal(t, 4, reads, "failed scans must not populate the digest cache")
	f.tip = strings.Repeat("b", 40)
	facts.uid = uuid.NewString()
	replacement, err := source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, facts.uid, replacement.NamespaceUID)
	require.Equal(t, 5, reads, "a replacement Namespace UID must not reuse another scope's verification")
	facts.deleting = true
	_, err = source.ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.ErrorIs(t, err, rbac.ErrUnavailable, "a cached digest cannot revive a deleting Namespace")
}

type originFactsStore struct {
	datastore.Datastore
	uid      string
	deleting bool
}

func (s *originFactsStore) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	namespace, err := s.Datastore.GetNamespaceByName(ctx, name)
	if err != nil || namespace == nil {
		return namespace, err
	}
	value := *namespace
	if s.uid != "" {
		value.UID = s.uid
	}
	if s.deleting {
		now := time.Now().UTC()
		value.DeletionTimestamp = &now
	}
	return &value, nil
}

func TestRBACReservedRootNotificationUsesCanonicalBranchWithoutCatalogAdmission(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	repository, err := f.ds.GetRepository(t.Context(), testRepoID)
	require.NoError(t, err)
	repository.DefaultBranch = "trunk"
	require.NoError(t, f.ds.UpdateRepository(t.Context(), repository, repository.ResourceVersion))
	old := f.tip
	f.tip = strings.Repeat("b", 40)
	f.trees[f.tip] = map[string][]byte{
		"rbac/clusterroles/reader.yaml": rbacProjectionRole("reader", "read"),
		"products/widget.md":            []byte("deliberately invalid catalog content"),
	}
	request := &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, RefName: "refs/heads/trunk",
		OldCommitSha: old, NewCommitSha: f.tip, ChangedPaths: []string{"rbac"}}
	_, err = f.server.AdmitResources(t.Context(), request)
	require.NoError(t, err)
	snapshot, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Len(t, snapshot.Resources, 1)
	require.Equal(t, "trunk@sha1:"+f.tip, snapshot.State.Projection.GitRevision)
	request.RefName = "refs/heads/feature"
	_, err = f.server.AdmitResources(t.Context(), request)
	require.NoError(t, err)
	unchanged, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, snapshot, unchanged)
}

func TestRBACCommittedReceivePreservesLifecycleAndAdvancesUnrelatedCommits(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	old := f.tip
	path := "rbac/clusterroles/reader.yaml"
	f.tip = strings.Repeat("b", 40)
	f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read")}
	require.NoError(t, f.receive(t, old, path))
	before, err := f.store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRole, Name: "reader"})
	require.NoError(t, err)

	old, f.tip = f.tip, strings.Repeat("c", 40)
	f.trees[f.tip] = map[string][]byte{path: f.trees[old][path], "README.md": []byte("unrelated\n")}
	require.NoError(t, f.receive(t, old, "README.md"))
	unchanged, err := f.store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRole, Name: "reader"})
	require.NoError(t, err)
	require.Equal(t, before, unchanged, "unrelated Git commits advance only the whole-tier identity")
	state, err := f.store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, "main@sha1:"+f.tip, state.Projection.GitRevision)

	old, f.tip = f.tip, strings.Repeat("d", 40)
	f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "update")}
	require.NoError(t, f.receive(t, old, path))
	updated, err := f.store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRole, Name: "reader"})
	require.NoError(t, err)
	require.Equal(t, before.Metadata.UID, updated.Metadata.UID)
	require.Equal(t, before.Metadata.Finalizers, updated.Metadata.Finalizers)
	require.Equal(t, before.Metadata.CreationTimestamp, updated.Metadata.CreationTimestamp)
	require.Equal(t, int64(2), updated.Metadata.Generation)

	old, f.tip = f.tip, strings.Repeat("e", 40)
	f.trees[f.tip] = map[string][]byte{}
	require.NoError(t, f.receive(t, old, path))
	deleted, err := f.store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRole, Name: "reader"})
	require.NoError(t, err)
	require.NotNil(t, deleted.Metadata.DeletionTimestamp)
	require.Equal(t, before.Metadata.UID, deleted.Metadata.UID)
	old, f.tip = f.tip, strings.Repeat("f", 40)
	f.trees[f.tip] = map[string][]byte{}
	require.NoError(t, f.receive(t, old, "README.md"))
	retained, err := f.store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRole, Name: "reader"})
	require.NoError(t, err)
	require.Equal(t, deleted, retained)
}

func TestRBACCommittedReceiveRejectsPartialTreesAndIgnoresSupersededCallbacks(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	old := f.tip
	path := "rbac/clusterroles/reader.yaml"
	f.tip = strings.Repeat("b", 40)
	f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read"),
		"rbac/clusterroles/broken.yaml": []byte("kind: ClusterRole\n")}
	require.Equal(t, codes.Unavailable, grpcstatus.Code(f.receive(t, old, path)))
	state, err := f.store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Zero(t, state.Revision)
	page, err := f.store.ListRBAC(t.Context(), rbac.ListRequest{TierID: rbac.ClusterTierID, Limit: 100})
	require.NoError(t, err)
	require.Empty(t, page.Resources)

	f.tip = strings.Repeat("c", 40)
	f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read")}
	require.NoError(t, f.receive(t, old, path))
	committed, err := f.store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	_, err = f.server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID,
		RefName: "refs/heads/main", OldCommitSha: strings.Repeat("0", 40), NewCommitSha: old, ChangedPaths: []string{path}})
	require.NoError(t, err)
	after, err := f.store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, committed, after)
}

type failingRBACProjectionStore struct {
	datastore.RBACStore
	fail       bool
	loseAck    bool
	applyCalls int
}

func (s *failingRBACProjectionStore) ApplyRBAC(ctx context.Context, write datastore.RBACTierWrite) (datastore.RBACTierState, error) {
	s.applyCalls++
	if s.fail {
		return datastore.RBACTierState{}, datastore.RBACError(datastore.ErrRBACUnavailable)
	}
	state, err := s.RBACStore.ApplyRBAC(ctx, write)
	if err == nil && s.loseAck {
		s.loseAck = false
		return datastore.RBACTierState{}, datastore.RBACError(datastore.ErrRBACUnavailable)
	}
	return state, err
}

type rbacProjectionDatastore struct {
	datastore.Datastore
	store datastore.RBACStore
}

func (s *rbacProjectionDatastore) RBACStore() datastore.RBACStore { return s.store }

func TestRBACCommittedReceiveRecoversProjectionFailureAndLostAcknowledgment(t *testing.T) {
	for _, loseAck := range []bool{false, true} {
		t.Run(fmt.Sprint("lost-ack=", loseAck), func(t *testing.T) {
			var backing *failingRBACProjectionStore
			f := newRBACProjectionFixture(t, func(ds datastore.Datastore) datastore.Datastore {
				backing = &failingRBACProjectionStore{RBACStore: ds.(datastore.RBACCapable).RBACStore(), fail: !loseAck, loseAck: loseAck}
				return &rbacProjectionDatastore{Datastore: ds, store: backing}
			})
			old := f.tip
			f.tip = strings.Repeat("b", 40)
			path := "rbac/clusterroles/reader.yaml"
			f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read")}
			err := f.receive(t, old, path)
			if loseAck {
				require.NoError(t, err)
				require.Equal(t, 1, backing.applyCalls, "the committed receipt repairs a lost acknowledgment without another write")
			} else {
				require.Equal(t, codes.Unavailable, grpcstatus.Code(err))
				require.Contains(t, err.Error(), f.tip, "partial outcomes retain the known committed revision")
				state, readErr := f.store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
				require.NoError(t, readErr)
				require.Zero(t, state.Revision)
				backing.fail = false
				require.NoError(t, f.receive(t, old, path))
			}
			snapshot, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
			require.NoError(t, err)
			require.Len(t, snapshot.Resources, 1)
			require.Equal(t, "main@sha1:"+f.tip, snapshot.State.Projection.GitRevision)
			require.Equal(t, strings.Repeat("b", 40), f.tip, "projection repair must never roll Git back")
		})
	}
}

func TestRBACCommittedReceiveRepairsSeveralMissedCandidatesAtomically(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	old := f.tip
	f.tip = strings.Repeat("b", 40)
	f.trees[f.tip] = map[string][]byte{}
	for index := range rbac.MaxCandidateResources + 1 {
		name := fmt.Sprintf("reader-%d", index)
		f.trees[f.tip]["rbac/clusterroles/"+name+".yaml"] = rbacProjectionRole(name, "read")
	}
	require.NoError(t, f.receive(t, old, "rbac/clusterroles/reader-256.yaml"))
	snapshot, err := datastore.ReadRBACSnapshot(t.Context(), f.store, rbac.ClusterTierID)
	require.NoError(t, err)
	require.Len(t, snapshot.Resources, rbac.MaxCandidateResources+1)
	require.EqualValues(t, 1, snapshot.State.Revision, "complete catch-up is one publication, not partial batches")
}

func TestRBACCommittedReceiveDoesNotRecheckHistoricalServiceAccount(t *testing.T) {
	f := newRBACProjectionFixture(t, nil)
	old := f.tip
	f.tip = strings.Repeat("b", 40)
	path := "rbac/clusterroles/reader.yaml"
	f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read")}
	actor := &catalogv1.CallbackActor{Subject: "system:serviceaccount:retired-writer", ServiceAccountUid: uuid.NewString(),
		AuthMethod: "serviceaccount-jwt", Groups: []string{"system:authenticated"}}
	_, err := f.server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID,
		RefName: "refs/heads/main", OldCommitSha: old, NewCommitSha: f.tip, ChangedPaths: []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: actor}})
	require.NoError(t, err)
	state, err := f.store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, actor.Subject, state.Projection.Actor)

	old, f.tip = f.tip, strings.Repeat("c", 40)
	f.trees[f.tip] = map[string][]byte{path: f.trees[old][path], "README.md": []byte("unrelated\n")}
	_, err = f.server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID,
		RefName: "refs/heads/main", OldCommitSha: old, NewCommitSha: f.tip, ChangedPaths: []string{"README.md"},
		Authorization: &catalogv1.CallbackAuthorization{Actor: actor}})
	require.Equal(t, codes.Unauthenticated, grpcstatus.Code(err), "ordinary catalog admission retains its live identity check")
	state, err = f.store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, "main@sha1:"+f.tip, state.Projection.GitRevision, "unrelated committed tips still advance the RBAC identity")
}

func TestRBACOriginRejectsNamespaceAndGitChangesDuringScan(t *testing.T) {
	for _, changeNamespace := range []bool{false, true} {
		t.Run(fmt.Sprint("namespace=", changeNamespace), func(t *testing.T) {
			f := newRBACProjectionFixture(t, nil)
			path := "rbac/clusterroles/reader.yaml"
			f.trees[f.tip] = map[string][]byte{path: rbacProjectionRole("reader", "read")}
			readFile := f.git.readFileFunc
			f.git.readFileFunc = func(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
				content, err := readFile(ctx, repositoryID, path, ref)
				if changeNamespace {
					namespace, err := f.ds.GetNamespaceByName(ctx, "gitstore-system")
					require.NoError(t, err)
					namespace.ResourceVersion = "2"
					require.NoError(t, f.ds.UpdateNamespace(ctx, namespace, "1"))
				} else {
					f.tip = strings.Repeat("b", 40)
				}
				return content, err
			}
			_, _, err := (rbac.OriginReader{Store: f.ds, Git: f.git}).ReadTree(t.Context(), rbac.ClusterTierID)
			require.ErrorIs(t, err, rbac.ErrUnavailable)
		})
	}
}

func rbacCallbackBlob(path string, content []byte) *catalogv1.ResourceBlob {
	digest := sha1.New()
	_, _ = fmt.Fprintf(digest, "blob %d\x00", len(content))
	_, _ = digest.Write(content)
	return &catalogv1.ResourceBlob{Path: path, Content: content, BlobOid: fmt.Sprintf("%x", digest.Sum(nil))}
}

func TestRBACCallbackFailsClosedBeforeActivation(t *testing.T) {
	server := newCatalogServer(t, nil, nil)
	for _, deletion := range []bool{false, true} {
		tree := &catalogv1.ResourceValidationTree{}
		blob := rbacCallbackBlob("rbac/roles/reader.yaml", []byte("kind: Role\n"))
		if deletion {
			tree.OldBlobs = []*catalogv1.ResourceBlob{blob}
		} else {
			tree.ProposedBlobs = []*catalogv1.ResourceBlob{blob}
		}
		request := &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID, Trees: []*catalogv1.ResourceValidationTree{tree}}
		call := server.ValidateRBACReceive
		if tree.RbacTree == nil {
			call = server.ValidateResources
		}
		response, err := call(t.Context(), request)
		require.Nil(t, response)
		require.Equal(t, codes.Unavailable, grpcstatus.Code(err))
	}
}

func TestRBACCallbackRejectsWholeMultiRefReceive(t *testing.T) {
	server := newCatalogServer(t, nil, nil)
	response, err := server.ValidateResources(t.Context(), &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID,
		Trees: []*catalogv1.ResourceValidationTree{
			{ProposedBlobs: []*catalogv1.ResourceBlob{rbacCallbackBlob("rbac/roles/reader.yaml", []byte("kind: Role\n"))}},
			{},
		}})
	require.NoError(t, err)
	require.False(t, response.Accepted)
	require.Equal(t, "RBAC_MULTI_REF_UNSUPPORTED", response.Errors[0].Constraint)
}

type rbacCallbackProvider struct{ *opa.Engine }

func (*rbacCallbackProvider) Name() string { return "opa" }
func (*rbacCallbackProvider) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Deny("ordinary-cache", "candidate must use authoritative capture"), nil
}

func TestRBACCallbackUsesSharedWholeCandidateAuthorization(t *testing.T) {
	ds := newNamespacePolicyDatastore(t)
	repository, err := ds.GetRepository(t.Context(), testRepoID)
	require.NoError(t, err)
	repository.RepositoryID = testRepoID
	require.NoError(t, ds.UpdateRepository(t.Context(), repository, repository.ResourceVersion))
	store := ds.(datastore.RBACCapable).RBACStore()
	namespace, err := ds.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	origin := rbac.SnapshotOrigin{NamespaceUID: namespace.UID, Namespace: namespace.Name, NamespaceVersion: namespace.ResourceVersion,
		Root: true, Active: true, RepositoryID: testRepoID, GitRef: "refs/heads/main",
		GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64)}
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID,
		Projection: &rbac.ProjectionIdentity{RepositoryID: testRepoID, GitRef: origin.GitRef, GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "fixture"},
		Changes: []rbac.Change{
			{Resource: &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole, Metadata: rbac.Metadata{Name: "delegator", UID: uuid.NewString()},
				Spec: json.RawMessage(`{"rules":[{"resources":["clusterRole"],"verbs":["create"]},{"resources":["product"],"verbs":["read"]}]}`)}},
			{Resource: &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRoleBinding, Metadata: rbac.Metadata{Name: "delegator-access", UID: uuid.NewString()},
				Spec: json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"delegator"},"subjects":[{"kind":"User","name":"test-admission-actor"}]}`)}},
		}})
	require.NoError(t, err)
	key := rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRoleBinding, Name: "delegator-access"}
	binding, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	_, err = (rbac.References{Store: store, Builtins: builtins}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	engine, err := opa.NewEngine(t.Context())
	require.NoError(t, err)
	registry := auth.NewProviderRegistry(nil, &rbacCallbackProvider{engine}, nil)
	validator := &admission.RBACValidator{Builtins: builtins, Capture: rbac.ValidationCapture{Registry: registry,
		Reader: rbac.SnapshotReader{Store: store, CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(),
			ReadOrigin: func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }}}}
	server := newCatalogServer(t, ds, nil, func(deps *cataloggrpc.ServerDeps) {
		deps.Registry, deps.RBACValidator = registry, validator
	})
	page, err := store.ListRBAC(t.Context(), rbac.ListRequest{TierID: rbac.ClusterTierID, Limit: 1000})
	require.NoError(t, err)
	var existing []*catalogv1.ResourceBlob
	for _, resource := range page.Resources {
		path, err := rbac.CanonicalPath(resource.Kind, resource.Metadata.Name)
		require.NoError(t, err)
		content, err := rbac.CanonicalManifest(resource)
		require.NoError(t, err)
		existing = append(existing, rbacCallbackBlob(path, content))
	}
	for _, test := range []struct {
		name    string
		verb    string
		mutate  func(*catalogv1.ResourceValidationTree)
		allowed bool
	}{
		{name: "covered", verb: "read", allowed: true},
		{name: "broader", verb: "purge"},
		{name: "wrong blob identity", verb: "read", mutate: func(tree *catalogv1.ResourceValidationTree) { tree.ProposedBlobs[0].BlobOid = strings.Repeat("f", 40) }},
		{name: "wrong location", verb: "read", mutate: func(tree *catalogv1.ResourceValidationTree) { tree.ProposedBlobs[0].Path = "rbac/roles/delegated.yaml" }},
		{name: "empty is not deletion", verb: "read", mutate: func(tree *catalogv1.ResourceValidationTree) {
			tree.ProposedBlobs[0] = rbacCallbackBlob(tree.ProposedBlobs[0].Path, nil)
		}},
		{name: "stale canonical tip", verb: "read", mutate: func(tree *catalogv1.ResourceValidationTree) { tree.Ref.OldCommitSha = strings.Repeat("d", 40) }},
		{name: "omitted complete tree", verb: "read", mutate: func(tree *catalogv1.ResourceValidationTree) { tree.RbacTree = nil }},
		{name: "omitted unchanged canonical resource", verb: "read", mutate: func(tree *catalogv1.ResourceValidationTree) { tree.RbacTree.Blobs = tree.RbacTree.Blobs[:1] }},
		{name: "invalid unchanged review resource", verb: "read", mutate: func(tree *catalogv1.ResourceValidationTree) {
			tree.Ref.RefName = "refs/heads/review"
			tree.RbacTree.Blobs = append(tree.RbacTree.Blobs, rbacCallbackBlob("rbac/clusterroles/invalid.yaml", []byte("kind: ClusterRole\n")))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte("apiVersion: " + rbac.APIVersion + "\nkind: ClusterRole\nmetadata: {name: delegated}\nspec:\n  rules: [{resources: [product], verbs: [" + test.verb + "]}]\n")
			tree := &catalogv1.ResourceValidationTree{Ref: &catalogv1.ReceiveRef{RefName: origin.GitRef,
				OldCommitSha: strings.Repeat("a", 40), NewCommitSha: strings.Repeat("c", 40)},
				ProposedBlobs: []*catalogv1.ResourceBlob{rbacCallbackBlob("rbac/clusterroles/delegated.yaml", body)}}
			tree.RbacTree = &catalogv1.RBACValidationTree{Blobs: append([]*catalogv1.ResourceBlob{tree.ProposedBlobs[0]}, existing...)}
			if test.mutate != nil {
				test.mutate(tree)
			}
			before, err := store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
			require.NoError(t, err)
			response, err := server.ValidateResources(t.Context(), &catalogv1.ValidateResourcesRequest{RepositoryId: testRepoID, Trees: []*catalogv1.ResourceValidationTree{tree}})
			require.NoError(t, err)
			require.Equal(t, test.allowed, response.Accepted, "diagnostics: %v", response.Errors)
			after, err := store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
			require.NoError(t, err)
			require.Equal(t, before, after, "pre-receive cannot publish a projection")
		})
	}
}
