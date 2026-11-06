// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestAdmitResourcesDelayedUnchangedManifestSurvivesNewerCommit(t *testing.T) {
	for _, tc := range []struct {
		kind, directory string
		manifest        func(string) []byte
		lookup          func(context.Context, datastore.Datastore, string) error
	}{
		{"Product", "products", makeProduct, func(ctx context.Context, store datastore.Datastore, name string) error {
			_, err := store.GetProductByName(ctx, "gitstore", name)
			return err
		}},
		{"CategoryTaxonomy", "categories", makeCategoryTaxonomy, func(ctx context.Context, store datastore.Datastore, name string) error {
			_, err := store.GetCategoryTaxonomyByName(ctx, "gitstore", name)
			return err
		}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			store := newTestDatastore(t)
			zero, first, second := strings.Repeat("0", 40), strings.Repeat("a", 40), strings.Repeat("b", 40)
			earlierPath, laterPath := tc.directory+"/earlier.md", tc.directory+"/later.md"
			earlier, later := tc.manifest("earlier"), tc.manifest("later")
			current := second
			git := newTreeGitReader(&current, map[string]map[string][]byte{
				first:  {earlierPath: earlier},
				second: {earlierPath: earlier, laterPath: later},
			})
			newerServer, delayedServer := newCatalogServer(t, store, git), newCatalogServer(t, store, git)
			// The later callback only owns its delta. It cannot materialize the earlier
			// resource on behalf of a different callback actor.
			_, err := newerServer.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: first,
				NewCommitSha: second, ChangedPaths: []string{laterPath},
			})
			require.NoError(t, err)
			require.NoError(t, tc.lookup(t.Context(), store, "later"))
			// The first push was acknowledged, and its unchanged manifest remains in
			// the current tree. A delayed callback must not silently lose admission.
			_, err = delayedServer.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: zero,
				NewCommitSha: first, ChangedPaths: []string{earlierPath},
			})
			require.NoError(t, err)
			require.NoError(t, tc.lookup(t.Context(), store, "earlier"), "acknowledged unchanged manifest was lost after the branch advanced")
			switch tc.kind {
			case "Product":
				stored, err := store.GetProductByName(t.Context(), "gitstore", "earlier")
				require.NoError(t, err)
				require.Equal(t, first, stored.GitCommitSHA)
				require.NotEmpty(t, stored.UID)
			case "CategoryTaxonomy":
				stored, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "earlier")
				require.NoError(t, err)
				require.Equal(t, first, stored.GitCommitSHA)
				require.NotEmpty(t, stored.UID)
			}

		})
	}
}

func TestAdmitResourcesChangedPathsDoesNotEnumerateTree(t *testing.T) {
	store := newTestDatastore(t)
	commit := strings.Repeat("a", 40)
	lists := 0
	git := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			lists++
			return nil, errors.New("delta admission must not enumerate the repository")
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return makeCategoryTaxonomy("delta"), nil
		},
		resolveRefFunc: func(context.Context, string, string) (string, error) { return commit, nil },
	}
	server := newCatalogServer(t, store, git)
	_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, RefName: "refs/heads/main", NewCommitSha: commit,
		ChangedPaths: []string{"categories/delta.md"},
	})
	require.NoError(t, err)
	require.Zero(t, lists, "a one-path admission must do bounded direct reads")
	_, err = store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "delta")
	require.NoError(t, err)
}

func TestAdmitResourcesReadFailureCannotBecomeDeletion(t *testing.T) {
	store := newTestDatastore(t)
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	path := "categories/preserve.md"
	content := makeCategoryTaxonomy("preserve")
	current := first
	git := newTreeGitReader(&current, map[string]map[string][]byte{
		first: {path: content}, second: {path: content},
	})
	server := newCatalogServer(t, store, git)
	admitDelta(t, server, strings.Repeat("0", 40), first)
	before, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "preserve")
	require.NoError(t, err)
	read := git.readFileFunc
	git.readFileFunc = func(ctx context.Context, repo, path, ref string) ([]byte, error) {
		if ref == second {
			return nil, grpcstatus.Error(codes.Unavailable, "temporary Git read failure")
		}
		return read(ctx, repo, path, ref)
	}
	current = second
	_, err = server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: first,
		NewCommitSha: second, ChangedPaths: []string{path},
	})
	require.Error(t, err, "unreadable state is not proof that the manifest was removed")
	after, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "preserve")
	require.NoError(t, err)
	require.Equal(t, before.UID, after.UID)
	require.Nil(t, after.DeletionTimestamp)
}

func TestAdmitResourcesDelayedCreateCannotOverwriteOrResurrect(t *testing.T) {
	for _, mode := range []string{"modified", "removed", "already-admitted"} {
		t.Run(mode, func(t *testing.T) {
			store := newTestDatastore(t)
			first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
			path := "categories/guarded.md"
			original := makeCategoryTaxonomy("guarded")
			latest := original
			if mode == "modified" {
				latest = append(append([]byte{}, original...), []byte("newer body")...)
			}
			trees := map[string]map[string][]byte{first: {path: original}, second: {path: latest}}
			if mode == "removed" {
				trees[second] = map[string][]byte{}
			}
			current := second
			git := newTreeGitReader(&current, trees)
			server := newCatalogServer(t, store, git)
			if mode != "removed" {
				seed := second
				if mode == "already-admitted" {
					seed = first
				}
				current = seed
				admitDelta(t, server, strings.Repeat("0", 40), seed)
				current = second
			}
			var uid, version, body, source string
			if mode != "removed" {
				before, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "guarded")
				require.NoError(t, err)
				uid, version, body, source = before.UID, before.ResourceVersion, before.Body, before.GitCommitSHA
			}
			_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: strings.Repeat("0", 40),
				NewCommitSha: first, ChangedPaths: []string{path},
			})
			require.NoError(t, err)
			after, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "guarded")
			if mode == "removed" {
				require.ErrorIs(t, err, datastore.ErrNotFound)
				return
			}
			require.NoError(t, err)
			require.Equal(t, uid, after.UID)
			require.Equal(t, version, after.ResourceVersion)
			require.Equal(t, body, after.Body)
			require.Equal(t, source, after.GitCommitSHA)
		})
	}
}

func TestAdmitResourcesDelayedManifestRechecksAuthorization(t *testing.T) {
	store := newTestDatastore(t)
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	path := "categories/protected.md"
	content := makeCategoryTaxonomy("protected")
	current := second
	reader := newTreeGitReader(&current, map[string]map[string][]byte{
		first: {path: content}, second: {path: content},
	})
	deny := testutil.NewDenyAllAuthZ(t)
	server := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) {
		deps.Registry = auth.NewProviderRegistry(nil, deny, nil)
	})
	_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: strings.Repeat("0", 40),
		NewCommitSha: first, ChangedPaths: []string{path},
	})
	require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err))
	_, err = store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "protected")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

type advanceTipDenyPolicy struct{ advance func() }

func (advanceTipDenyPolicy) Name() string { return "advance-tip-before-denial" }
func (p advanceTipDenyPolicy) Validate(context.Context, admission.AdmissionRequest) admission.AdmissionDecision {
	p.advance()
	return admission.DecisionDeny("old snapshot denied", "spec")
}

func TestAdmitResourcesSupersededDenialCannotChangeNewerStatus(t *testing.T) {
	for _, tc := range []struct {
		kind, directory string
		manifest        func(string) []byte
		unchanged       bool
	}{
		{"Product", "products", makeProduct, false}, {"CategoryTaxonomy", "categories", makeCategoryTaxonomy, false},
		{"ProductUnchanged", "products", makeProduct, true}, {"CategoryUnchanged", "categories", makeCategoryTaxonomy, true},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			store := newTestDatastore(t)
			initial, first, second := strings.Repeat("c", 40), strings.Repeat("a", 40), strings.Repeat("b", 40)
			path := tc.directory + "/guarded.md"
			base := tc.manifest("guarded")
			current := initial
			reader := newTreeGitReader(&current, map[string]map[string][]byte{
				initial: {path: base},
				first:   {path: append(append([]byte{}, base...), []byte("older body")...)},
				second:  {path: append(append([]byte{}, base...), []byte("newer body")...)},
			})
			if tc.unchanged {
				reader.readFileFunc = func(_ context.Context, _, _, ref string) ([]byte, error) {
					if ref == initial {
						return base, nil
					}
					return append(append([]byte{}, base...), []byte("older body")...), nil
				}
			}
			newer := newCatalogServer(t, store, reader)
			admitDelta(t, newer, strings.Repeat("0", 40), initial)
			older := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) {
				deps.ExtraValidatingPolicies = []admission.ValidatingAdmissionPolicy{advanceTipDenyPolicy{advance: func() {
					current = second
					_, err := newer.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
						RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: first, NewCommitSha: second, ChangedPaths: []string{path},
					})
					require.NoError(t, err)
				}}}
			})
			current = first
			_, err := older.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: initial, NewCommitSha: first, ChangedPaths: []string{path},
			})
			require.NoError(t, err)
			var source string
			var raw []byte
			if strings.HasPrefix(tc.kind, "Product") {
				resource, err := store.GetProductByName(t.Context(), "gitstore", "guarded")
				require.NoError(t, err)
				source, raw = resource.GitCommitSHA, resource.Status
			} else {
				resource, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "guarded")
				require.NoError(t, err)
				source, raw = resource.GitCommitSHA, resource.Status
			}
			expectedSource := second
			if tc.unchanged {
				expectedSource = initial
			} // The newer callback has an empty content delta.
			require.Equal(t, expectedSource, source)
			var status struct{ Conditions []catalog.Condition }
			require.NoError(t, json.Unmarshal(raw, &status))
			found := false
			for _, condition := range status.Conditions {
				if condition.Type == catalog.ConditionAdmissionAccepted {
					found = true
					require.Equal(t, catalog.ConditionTrue, condition.Status, "old denied snapshot overwrote accepted newer status")
				}
			}
			require.True(t, found)
		})
	}
}

func TestAdmitResourcesUnrelatedPushesDoNotStarveUnchangedManifest(t *testing.T) {
	store := newTestDatastore(t)
	reads := 0
	git := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return nil, errors.New("unexpected tree enumeration")
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return makeCategoryTaxonomy("continuous"), nil
		},
		resolveRefFunc: func(context.Context, string, string) (string, error) {
			reads++
			return fmt.Sprintf("%040x", reads+100), nil
		},
	}
	server := newCatalogServer(t, store, git)
	_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, RefName: "refs/heads/main", NewCommitSha: strings.Repeat("a", 40), ChangedPaths: []string{"categories/continuous.md"}})
	require.NoError(t, err)
	resource, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "continuous")
	require.NoError(t, err, "unrelated commits must not require a globally idle ref before admission")
	require.Equal(t, strings.Repeat("a", 40), resource.GitCommitSHA)
	require.Less(t, reads, 10, "unchanged manifests must converge with bounded work")
}

// This adapter materializes an independently authored winner at either read
// or atomic-create boundary, reproducing two replicas without timing sleeps.
type delayedCreateRaceStore struct {
	datastore.Datastore
	kind, boundary string
	reads, updates int
	injected       bool
	inject         func()
}

func (s *delayedCreateRaceStore) GetProductByName(ctx context.Context, ns, name string) (*datastore.Product, error) {
	if s.kind == "Product" && name == "guarded" {
		s.reads++
		if s.boundary == "lookup" && s.reads == 2 {
			s.injectWinner()
		}
	}
	return s.Datastore.GetProductByName(ctx, ns, name)
}
func (s *delayedCreateRaceStore) GetCategoryTaxonomyByName(ctx context.Context, ns, name string) (*datastore.CategoryTaxonomy, error) {
	if s.kind == "CategoryTaxonomy" && name == "guarded" {
		s.reads++
		if s.boundary == "lookup" && s.reads == 2 {
			s.injectWinner()
		}
	}
	return s.Datastore.GetCategoryTaxonomyByName(ctx, ns, name)
}
func (s *delayedCreateRaceStore) injectWinner() {
	if !s.injected {
		s.injected = true
		s.inject()
	}
}
func (s *delayedCreateRaceStore) CreateProduct(ctx context.Context, p *datastore.Product) error {
	if s.kind == "Product" && strings.HasPrefix(s.boundary, "create-") && !s.injected {
		s.injectWinner()
		if s.boundary == "create-conflict" {
			return datastore.ErrConflict
		}
	}
	return s.Datastore.CreateProduct(ctx, p)
}
func (s *delayedCreateRaceStore) CreateCategoryTaxonomy(ctx context.Context, c *datastore.CategoryTaxonomy) error {
	if s.kind == "CategoryTaxonomy" && strings.HasPrefix(s.boundary, "create-") && !s.injected {
		s.injectWinner()
		if s.boundary == "create-conflict" {
			return datastore.ErrConflict
		}
	}
	return s.Datastore.CreateCategoryTaxonomy(ctx, c)
}
func (s *delayedCreateRaceStore) UpdateProduct(ctx context.Context, p *datastore.Product, expected string) error {
	s.updates++
	return s.Datastore.UpdateProduct(ctx, p, expected)
}
func (s *delayedCreateRaceStore) UpdateCategoryTaxonomy(ctx context.Context, c *datastore.CategoryTaxonomy, expected string) error {
	s.updates++
	return s.Datastore.UpdateCategoryTaxonomy(ctx, c, expected)
}

func TestDelayedCreateRacePreservesIndependentWinner(t *testing.T) {
	for _, kind := range []string{"Product", "CategoryTaxonomy"} {
		for _, boundary := range []string{"lookup", "create-already-exists", "create-conflict"} {
			for _, grants := range []string{"create", "create, update"} {
				t.Run(kind+"/"+boundary+"/"+grants, func(t *testing.T) {
					base := newTestDatastore(t)
					store := &delayedCreateRaceStore{Datastore: base, kind: kind, boundary: boundary}
					ctx := t.Context()
					var before []byte
					winnerID := "00000000-0000-0000-0000-000000000953"
					winnerRepo := "00000000-0000-0000-0000-000000000954"
					winnerCommit := strings.Repeat("e", 40)
					require.NoError(t, base.CreateRepository(ctx, &datastore.Repository{UID: winnerRepo, ID: winnerRepo, RepositoryID: winnerRepo, Namespace: "gitstore", Name: "winner-source", DefaultBranch: "main", StorageClass: "local", CreationActor: "winner"}))
					lookup := func() any {
						if kind == "Product" {
							v, err := base.GetProductByName(ctx, "gitstore", "guarded")
							require.NoError(t, err)
							return v
						}
						v, err := base.GetCategoryTaxonomyByName(ctx, "gitstore", "guarded")
						require.NoError(t, err)
						return v
					}
					store.inject = func() {
						if kind == "Product" {
							require.NoError(t, base.CreateProduct(ctx, &datastore.Product{UID: winnerID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: kind, Namespace: "gitstore", Name: "guarded", Generation: 7, ResourceVersion: "42", CreationActor: "winner", UpdateActor: "winner", RepositoryID: winnerRepo, SourcePath: "products/guarded.md", GitCommitSHA: winnerCommit, GitRef: "refs/heads/main", Revision: "main@sha1:" + winnerCommit, Spec: json.RawMessage(`{"title":"winner","lifecycle":{"state":"ACTIVE"}}`), Body: "winner body", Status: json.RawMessage(`{}`)}))
						} else {
							require.NoError(t, base.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{UID: winnerID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: kind, Namespace: "gitstore", Name: "guarded", Generation: 7, ResourceVersion: "42", CreationActor: "winner", UpdateActor: "winner", RepositoryID: winnerRepo, SourcePath: "categories/guarded.md", GitCommitSHA: winnerCommit, GitRef: "refs/heads/main", Revision: "main@sha1:" + winnerCommit, Spec: json.RawMessage(`{"title":"winner"}`), Body: "winner body", Status: json.RawMessage(`{}`)}))
						}
						var err error
						before, err = json.Marshal(lookup())
						require.NoError(t, err)
					}
					policy := fmt.Sprintf("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\ndefaultDeny: true\nclusterRoles:\n  creator:\n    rules:\n      - resources: [product, categoryTaxonomy]\n        verbs: [%s]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: creator}\n    subjects: [{kind: User, name: test-admission-actor}]\n", grants)
					policyPath := filepath.Join(t.TempDir(), "policy.yaml")
					require.NoError(t, os.WriteFile(policyPath, []byte(policy), 0600))
					provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: policyPath}, zap.NewNop())
					require.NoError(t, err)
					recording := testutil.NewRecordingAuthZ(provider)
					first, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
					current := tip
					directory := "products"
					content := makeProduct("guarded")
					if kind == "CategoryTaxonomy" {
						directory = "categories"
						content = makeCategoryTaxonomy("guarded")
					}
					path := directory + "/guarded.md"
					reader := newTreeGitReader(&current, map[string]map[string][]byte{first: {path: content}, tip: {path: content}})
					server := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) { deps.Registry = auth.NewProviderRegistry(nil, recording, nil) })
					_, err = server.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: strings.Repeat("0", 40), NewCommitSha: first, ChangedPaths: []string{path}})
					require.NoError(t, err)
					require.True(t, store.injected, "the selected two-replica race must occur")
					after, err := json.Marshal(lookup())
					require.NoError(t, err)
					require.JSONEq(t, string(before), string(after), "delayed CREATE must preserve winner identity, content, status and provenance")
					require.Zero(t, store.updates, "CREATE must never become UPDATE after the winner appears")
				})
			}
		}
	}
}

// A current callback may converge an existing identity only with UPDATE authority.
func TestCurrentCreateReplayRequiresUpdateAuthority(t *testing.T) {
	for _, kind := range []string{"Product", "CategoryTaxonomy"} {
		t.Run(kind, func(t *testing.T) {
			store := newTestDatastore(t)
			commit := strings.Repeat("a", 40)
			current := commit
			directory, content := "products", makeProduct("guarded")
			if kind == "CategoryTaxonomy" {
				directory, content = "categories", makeCategoryTaxonomy("guarded")
			}
			path := directory + "/guarded.md"
			reader := newTreeGitReader(&current, map[string]map[string][]byte{commit: {path: content}})
			request := &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: strings.Repeat("0", 40), NewCommitSha: commit, ChangedPaths: []string{path}}
			_, err := newCatalogServer(t, store, reader).AdmitResources(t.Context(), request)
			require.NoError(t, err)
			lookup := func() []byte {
				var value any
				var err error
				if kind == "Product" {
					value, err = store.GetProductByName(t.Context(), "gitstore", "guarded")
				} else {
					value, err = store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "guarded")
				}
				require.NoError(t, err)
				result, err := json.Marshal(value)
				require.NoError(t, err)
				return result
			}
			before := lookup()
			policy := "apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\ndefaultDeny: true\nclusterRoles:\n  creator:\n    rules:\n      - resources: [product, categoryTaxonomy]\n        verbs: [create]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: creator}\n    subjects: [{kind: User, name: test-admission-actor}]\n"
			policyPath := filepath.Join(t.TempDir(), "policy.yaml")
			require.NoError(t, os.WriteFile(policyPath, []byte(policy), 0600))
			provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: policyPath}, zap.NewNop())
			require.NoError(t, err)
			server := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) { deps.Registry = auth.NewProviderRegistry(nil, provider, nil) })
			_, err = server.AdmitResources(t.Context(), request)
			require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err))
			require.JSONEq(t, string(before), string(lookup()))
		})
	}
}
