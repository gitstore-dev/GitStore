// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	storefrontgenerated "github.com/gitstore-dev/gitstore/api/internal/storefront/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type rbacResourceProvider struct {
	*opa.Engine
	allowLifecycle atomic.Bool
}

func (*rbacResourceProvider) Name() string { return "opa" }
func (p *rbacResourceProvider) Authorize(_ context.Context, _ *auth.Principal, action string, _ auth.ResourceContext) (auth.Decision, error) {
	if p.allowLifecycle.Load() && (strings.HasSuffix(action, ".status.write") || strings.HasSuffix(action, ".purge") || strings.HasSuffix(action, ".watch")) {
		return auth.Decision{Outcome: auth.OutcomeAllow}, nil
	}
	return auth.Deny("ordinary-cache", "authoring must use authoritative capture"), nil
}

type rbacProjectionFailure struct {
	rbac.Store
	fail atomic.Bool
}

type rbacLifecycleDatastore struct {
	datastore.Datastore
	rbac rbac.Store
}

func (s rbacLifecycleDatastore) RBACStore() datastore.RBACStore { return s.rbac }

func (s *rbacProjectionFailure) ApplyRBAC(ctx context.Context, write rbac.TierWrite) (rbac.TierState, error) {
	if s.fail.Load() {
		return rbac.TierState{}, rbac.ErrUnavailable
	}
	return s.Store.ApplyRBAC(ctx, write)
}

type rbacResourceGit struct {
	GitWriter
	mu            sync.Mutex
	origins       map[string]rbac.SnapshotOrigin
	tiers         map[string]string
	files         map[string]map[string][]byte
	commits       int
	actor         string
	beforeCommit  func() error
	afterCommit   func()
	responseError error
}

func (g *rbacResourceGit) origin(_ context.Context, tier string) (rbac.SnapshotOrigin, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	origin, ok := g.origins[tier]
	if !ok {
		return origin, rbac.ErrUnavailable
	}
	return origin, nil
}

func (g *rbacResourceGit) CommitFileAtRevisionForRepo(_ context.Context, repository string, params gitclient.CommitFileAtRevisionParams) (string, error) {
	return g.commit(repository, params.RefName, params.ExpectedCommitSHA, params.Path, params.Content, params.AuthorName, false)
}

func (g *rbacResourceGit) DeleteFileForRepo(_ context.Context, repository string, params gitclient.DeleteFileParams) (string, error) {
	return g.commit(repository, params.RefName, params.ExpectedCommitSHA, params.Path, nil, params.AuthorName, true)
}

func (g *rbacResourceGit) commit(repository, ref, expected, path string, content []byte, actor string, remove bool) (string, error) {
	if g.beforeCommit != nil {
		callback := g.beforeCommit
		g.beforeCommit = nil
		if err := callback(); err != nil {
			return "", err
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	tier := g.tiers[repository]
	origin := g.origins[tier]
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if origin.GitRef != ref || origin.GitRevision != branch+"@sha1:"+expected {
		return "", grpcstatus.Error(codes.Aborted, "fixture tip changed")
	}
	if remove {
		delete(g.files[repository], path)
	} else {
		g.files[repository][path] = append([]byte(nil), content...)
	}
	var resources []*rbac.Resource
	for file, body := range g.files[repository] {
		resource, err := rbac.ParseManifest(body, file, rbac.RepositoryScope{
			Namespace: origin.Namespace, NamespaceUID: origin.NamespaceUID, Root: origin.Root,
		})
		if err != nil {
			return "", err
		}
		resources = append(resources, resource)
	}
	digest, err := rbac.ManifestTreeDigest(resources)
	if err != nil {
		return "", err
	}
	g.commits++
	sha := fmt.Sprintf("%040x", 100+g.commits)
	origin.GitRevision, origin.TreeDigest = branch+"@sha1:"+sha, digest
	g.origins[tier], g.actor = origin, actor
	if g.afterCommit != nil {
		g.afterCommit()
	}
	return sha, g.responseError
}

func newRBACResourceFixture(t *testing.T) (*Resolver, *rbacResourceGit, *rbacProjectionFailure) {
	t.Helper()
	ds, err := memdb.New()
	require.NoError(t, err)
	store := &rbacProjectionFailure{Store: ds.(datastore.RBACCapable).RBACStore()}
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	git := &rbacResourceGit{origins: map[string]rbac.SnapshotOrigin{}, tiers: map[string]string{}, files: map[string]map[string][]byte{}}
	for _, name := range []string{SystemRepositoryName, "shop"} {
		namespaceUID, repository := uuid.NewString(), uuid.NewString()
		require.NoError(t, ds.CreateNamespace(t.Context(), &datastore.Namespace{
			UID: namespaceUID, Name: name, CreationTimestamp: time.Now().UTC(),
		}))
		tier := namespaceUID
		if name == SystemRepositoryName {
			tier = rbac.ClusterTierID
		}
		origin := rbac.SnapshotOrigin{NamespaceUID: namespaceUID, Namespace: name, NamespaceVersion: "1",
			Active: true, Root: tier == rbac.ClusterTierID, RepositoryID: repository,
			GitRef: "refs/heads/main", GitRevision: "main@sha1:" + strings.Repeat("a", 40)}
		var changes []rbac.Change
		git.files[repository] = map[string][]byte{}
		if origin.Root {
			binding := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRoleBinding,
				Metadata: rbac.Metadata{Name: "administrator", UID: uuid.NewString(), Finalizers: []string{rbac.LifecycleFinalizer}},
				Spec:     json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"cluster-admin"},"subjects":[{"kind":"User","name":"alice"}]}`)}
			body, err := rbac.CanonicalManifest(binding)
			require.NoError(t, err)
			git.files[repository]["rbac/clusterrolebindings/administrator.yaml"] = body
			changes = []rbac.Change{{Resource: binding}}
		}
		var resources []*rbac.Resource
		for _, change := range changes {
			resources = append(resources, change.Resource)
		}
		origin.TreeDigest, err = rbac.ManifestTreeDigest(resources)
		require.NoError(t, err)
		git.origins[tier], git.tiers[repository] = origin, tier
		_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier,
			Projection: &rbac.ProjectionIdentity{RepositoryID: repository, GitRef: origin.GitRef,
				GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "fixture"},
			Changes: changes})
		require.NoError(t, err)
		if origin.Root {
			key := rbac.ResourceKey{TierID: tier, Kind: rbac.ClusterRoleBinding, Name: "administrator"}
			binding, err := store.GetRBAC(t.Context(), key)
			require.NoError(t, err)
			_, err = (rbac.References{Store: store, Builtins: builtins}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
			require.NoError(t, err)
		}
	}
	engine, err := opa.NewEngine(t.Context())
	require.NoError(t, err)
	registry := auth.NewProviderRegistry(nil, &rbacResourceProvider{Engine: engine}, nil)
	validator := &admission.RBACValidator{Builtins: builtins, Capture: rbac.ValidationCapture{
		Registry: registry, Reader: rbac.SnapshotReader{Store: store, ReadOrigin: git.origin,
			CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest()},
	}}
	root, err := NewResolver(ResolverDeps{Store: ds, GitWriter: git, Registry: registry,
		RBACValidator: validator, Logger: zap.NewNop()})
	require.NoError(t, err)
	return root, git, store
}

func rbacMutationRequest(t *testing.T, root *Resolver, operation, kind string, input map[string]any) (map[string]any, gqlerror.List) {
	t.Helper()
	field := strings.ToLower(kind[:1]) + kind[1:]
	mutation := strings.ToLower(operation) + kind
	query := fmt.Sprintf(`mutation($input:%s%sInput!){%s(input:$input){%s{id metadata{name namespace uid resourceVersion generation revision labels finalizers deletionTimestamp}}}}`,
		operation, kind, mutation, field)
	data, errors := rbacGraphQLRequest(t, root, query, input)
	node, _ := data[mutation][field].(map[string]any)
	return node, errors
}

func rbacGraphQLRequest(t *testing.T, root *Resolver, query string, input any) (map[string]map[string]any, gqlerror.List) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"input": input}})
	require.NoError(t, err)
	server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
	guard := security.NewAuthorizeWithStore(root.registry, root.store, zap.NewNop())
	server.AroundOperations(guard.GraphQLAuthorizer)
	server.AroundFields(guard.GraphQLFieldAuthorizer)
	server.AroundResponses(guard.GraphQLResponseAuthorizer)
	request := httptest.NewRequest(http.MethodPost, "/admin/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.ContextWithPrincipal(request.Context(), &auth.Principal{
		Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"},
	}))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var result struct {
		Data   map[string]map[string]any `json:"data"`
		Errors gqlerror.List             `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result), response.Body.String())
	return result.Data, result.Errors
}

func TestRBACGraphQLCRUDUsesGitAndAuthoritativeCapture(t *testing.T) {
	for _, kind := range []rbac.Kind{rbac.Role, rbac.ClusterRole, rbac.RoleBinding, rbac.ClusterRoleBinding} {
		t.Run(string(kind), func(t *testing.T) {
			root, git, store := newRBACResourceFixture(t)
			metadata := map[string]any{"name": "custom", "labels": map[string]any{"team": "original"}}
			if kind.Namespaced() {
				metadata["namespace"] = "shop"
			}
			spec := map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"read"}}}}
			if !kind.IsRole() {
				spec = map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": "view"},
					"subjects": []any{map[string]any{"kind": "USER", "name": "future-user"}}}
			}
			input := map[string]any{"apiVersion": rbac.APIVersion, "kind": string(kind), "metadata": metadata, "spec": spec}
			created, errors := rbacMutationRequest(t, root, "Create", string(kind), input)
			require.Empty(t, errors)
			require.NotNil(t, created)
			require.Equal(t, "alice", git.actor)
			meta := created["metadata"].(map[string]any)
			require.NotEmpty(t, meta["revision"])
			uid, err := decodeNodeIDAs(string(kind), created["id"].(string))
			require.NoError(t, err)
			stored, err := store.GetRBACByUID(t.Context(), uid)
			require.NoError(t, err)
			require.Equal(t, meta["uid"], stored.Metadata.UID)
			stale := meta["resourceVersion"]
			metadata["resourceVersion"] = stale
			metadata["labels"] = map[string]any{"team": "updated"}
			updated, errors := rbacMutationRequest(t, root, "Update", string(kind), input)
			require.Empty(t, errors)
			require.Equal(t, created["id"], updated["id"])
			require.Equal(t, "updated", updated["metadata"].(map[string]any)["labels"].(map[string]any)["team"])
			_, errors = rbacMutationRequest(t, root, "Update", string(kind), input)
			require.Len(t, errors, 1)
			require.Equal(t, "CONFLICT", errors[0].Extensions["code"])
			require.Equal(t, 2, git.commits)
			deletion := map[string]any{"id": updated["id"], "resourceVersion": updated["metadata"].(map[string]any)["resourceVersion"]}
			field := strings.ToLower(string(kind[:1])) + string(kind[1:])
			mutation := "delete" + string(kind)
			query := fmt.Sprintf(`mutation($input:Delete%sInput!){%s(input:$input){outcome %s{id metadata{resourceVersion deletionTimestamp}}}}`, kind, mutation, field)
			data, errors := rbacGraphQLRequest(t, root, query, deletion)
			require.Empty(t, errors)
			require.Equal(t, "TERMINATION_STARTED", data[mutation]["outcome"])
			deleted := data[mutation][field].(map[string]any)
			require.NotNil(t, deleted["metadata"].(map[string]any)["deletionTimestamp"])
			require.Equal(t, 3, git.commits)
			_, errors = rbacGraphQLRequest(t, root, query, deletion)
			require.Len(t, errors, 1)
			require.Equal(t, "CONFLICT", errors[0].Extensions["code"])
			deletion["resourceVersion"] = deleted["metadata"].(map[string]any)["resourceVersion"]
			data, errors = rbacGraphQLRequest(t, root, query, deletion)
			require.Empty(t, errors)
			require.Equal(t, "ALREADY_TERMINATING", data[mutation]["outcome"])
			require.Equal(t, 3, git.commits, "termination retry cannot author another commit")
		})
	}
}

func TestRBACGraphQLResourceVersionPreconditions(t *testing.T) {
	root, git, _ := newRBACResourceFixture(t)
	metadata := map[string]any{"name": "versioned"}
	input := map[string]any{"metadata": metadata, "spec": map[string]any{"rules": []any{}}}
	node, errors := rbacMutationRequest(t, root, "Create", "ClusterRole", input)
	require.Empty(t, errors)
	for _, test := range []struct {
		name      string
		operation string
		version   *string
	}{
		{"create-supplied", "Create", new("supplied")},
		{"update-missing", "Update", nil},
		{"update-empty", "Update", new("")},
	} {
		t.Run(test.name, func(t *testing.T) {
			delete(metadata, "resourceVersion")
			if test.version != nil {
				metadata["resourceVersion"] = *test.version
			}
			_, errors := rbacMutationRequest(t, root, test.operation, "ClusterRole", input)
			require.Len(t, errors, 1)
			require.Equal(t, "BAD_USER_INPUT", errors[0].Extensions["code"])
			require.Contains(t, errors[0].Message, "metadata.resourceVersion")
			require.Equal(t, 1, git.commits)
		})
	}
	delete(metadata, "resourceVersion")
	input["resourceVersion"] = node["metadata"].(map[string]any)["resourceVersion"]
	_, errors = rbacMutationRequest(t, root, "Update", "ClusterRole", input)
	require.NotEmpty(t, errors, "the old top-level update field is not an alias")
	require.Equal(t, 1, git.commits)
}

func TestRBACDeleteOutcomeFollowsRecapturedCanonicalState(t *testing.T) {
	for _, anotherRequestDeletes := range []bool{false, true} {
		t.Run(fmt.Sprint(anotherRequestDeletes), func(t *testing.T) {
			root, git, _ := newRBACResourceFixture(t)
			node, errors := rbacMutationRequest(t, root, "Create", "ClusterRole", map[string]any{
				"metadata": map[string]any{"name": "contended"}, "spec": map[string]any{"rules": []any{}},
			})
			require.Empty(t, errors)
			input := map[string]any{"id": node["id"], "resourceVersion": node["metadata"].(map[string]any)["resourceVersion"]}
			query := `mutation($input:DeleteClusterRoleInput!){deleteClusterRole(input:$input){outcome clusterRole{metadata{resourceVersion deletionTimestamp}}}}`
			var currentVersion any
			git.beforeCommit = func() error {
				if anotherRequestDeletes {
					data, errors := rbacGraphQLRequest(t, root, query, input)
					require.Empty(t, errors)
					require.Equal(t, "TERMINATION_STARTED", data["deleteClusterRole"]["outcome"])
					currentVersion = data["deleteClusterRole"]["clusterRole"].(map[string]any)["metadata"].(map[string]any)["resourceVersion"]
				}
				return grpcstatus.Error(codes.Aborted, "another writer won the ref")
			}
			data, errors := rbacGraphQLRequest(t, root, query, input)
			want := "TERMINATION_STARTED"
			if anotherRequestDeletes {
				require.Len(t, errors, 1)
				require.Equal(t, "CONFLICT", errors[0].Extensions["code"])
				input["resourceVersion"] = currentVersion
				data, errors = rbacGraphQLRequest(t, root, query, input)
				want = "ALREADY_TERMINATING"
			}
			require.Empty(t, errors)
			require.Equal(t, want, data["deleteClusterRole"]["outcome"])
			require.Equal(t, 2, git.commits, "only one delete commit may be authored")
		})
	}
}

func TestRBACGraphQLEnumsPreserveManifestKinds(t *testing.T) {
	for _, ref := range []struct{ enum, manifest, name string }{
		{"ROLE", "Role", "reader"},
		{"CLUSTER_ROLE", "ClusterRole", "view"},
	} {
		t.Run(ref.enum, func(t *testing.T) {
			root, _, store := newRBACResourceFixture(t)
			_, rejected := rbacMutationRequest(t, root, "Create", "Role", map[string]any{
				"metadata": map[string]any{"name": "reader", "namespace": "shop"},
				"spec":     map[string]any{"rules": []any{}},
			})
			require.Empty(t, rejected)
			input := map[string]any{
				"metadata": map[string]any{"name": "enum-binding", "namespace": "shop"},
				"spec": map[string]any{
					"roleRef": map[string]any{"kind": ref.enum, "name": ref.name},
					"subjects": []any{
						map[string]any{"kind": "USER", "name": "future-user"},
						map[string]any{"kind": "GROUP", "name": "future-group"},
						map[string]any{"kind": "SERVICE_ACCOUNT", "name": "future-service"},
					},
				},
			}
			data, rejected := rbacGraphQLRequest(t, root, `mutation($input: CreateRoleBindingInput!) {
				createRoleBinding(input: $input) { roleBinding {
					id spec { roleRef { kind name } subjects { kind name } }
				} }
			}`, input)
			require.Empty(t, rejected)
			node := data["createRoleBinding"]["roleBinding"].(map[string]any)
			output := node["spec"].(map[string]any)
			require.Equal(t, input["spec"].(map[string]any)["roleRef"], output["roleRef"])
			require.ElementsMatch(t, input["spec"].(map[string]any)["subjects"], output["subjects"])
			uid, err := decodeNodeIDAs("RoleBinding", node["id"].(string))
			require.NoError(t, err)
			binding, err := store.GetRBACByUID(t.Context(), uid)
			require.NoError(t, err)
			spec, err := rbac.DecodeBindingSpec(binding)
			require.NoError(t, err)
			require.Equal(t, rbac.Kind(ref.manifest), spec.RoleRef.Kind)
			require.ElementsMatch(t, []rbac.Subject{
				{Kind: "User", Name: "future-user"}, {Kind: "Group", Name: "future-group"},
				{Kind: "ServiceAccount", Name: "future-service"},
			}, spec.Subjects)
			_, err = (rbac.References{Store: store, Builtins: root.rbacValidator.Builtins}).Resolve(t.Context(),
				rbac.ResourceKey{TierID: binding.NamespaceUID, Kind: binding.Kind, Name: binding.Metadata.Name},
				binding.Metadata.UID, binding.Metadata.ResourceVersion)
			require.NoError(t, err)
			refreshInspectionModel(t, root)
			data, rejected = rbacGraphQLRequest(t, root, `query {
				roleBinding(by: {namespacePath: {namespace: "shop", name: "enum-binding"}}) {
					status { resolved { roleKind } }
				}
			}`, nil)
			require.Empty(t, rejected)
			require.Equal(t, ref.enum, data["roleBinding"]["status"].(map[string]any)["resolved"].(map[string]any)["roleKind"])
		})
	}
}

func TestRBACGraphQLRejectsManifestSpellingsAsEnums(t *testing.T) {
	root, git, _ := newRBACResourceFixture(t)
	for _, kinds := range [][2]string{{"ClusterRole", "USER"}, {"CLUSTER_ROLE", "User"}, {"CLUSTER_ROLE", "ServiceAccount"}} {
		_, rejected := rbacGraphQLRequest(t, root, `mutation($input: CreateRoleBindingInput!) {
			createRoleBinding(input: $input) { roleBinding { id } }
		}`, map[string]any{
			"metadata": map[string]any{"name": "invalid", "namespace": "shop"},
			"spec": map[string]any{
				"roleRef":  map[string]any{"kind": kinds[0], "name": "view"},
				"subjects": []any{map[string]any{"kind": kinds[1], "name": "future"}},
			},
		})
		require.NotEmpty(t, rejected)
	}
	require.Zero(t, git.commits)
}

func TestRBACCommittedFailuresPreserveGitOutcomeWithoutReauthoring(t *testing.T) {
	for _, projectionFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(projectionFailure), func(t *testing.T) {
			root, git, store := newRBACResourceFixture(t)
			if projectionFailure {
				git.afterCommit = func() { store.fail.Store(true) }
			} else {
				git.responseError = grpcstatus.Error(codes.Unavailable, "lost response")
			}
			input := map[string]any{"apiVersion": rbac.APIVersion, "kind": "ClusterRole",
				"metadata": map[string]any{"name": "committed"}, "spec": map[string]any{"rules": []any{}}}
			node, errors := rbacMutationRequest(t, root, "Create", "ClusterRole", input)
			require.Nil(t, node)
			require.Len(t, errors, 1)
			require.Equal(t, 1, git.commits)
			if projectionFailure {
				require.Equal(t, "ADMISSION_REJECTED", errors[0].Extensions["code"])
				require.Equal(t, string(admission.PhasePostReceive), errors[0].Extensions["phase"])
				require.Equal(t, fmt.Sprintf("%040x", 101), errors[0].Extensions["commit"])
			} else {
				require.Equal(t, "FAILED_PRECONDITION", errors[0].Extensions["code"])
				require.NotContains(t, errors[0].Message, "lost response")
			}
			_, err := store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRole, Name: "committed"})
			require.ErrorIs(t, err, rbac.ErrNotFound)
			_, errors = rbacMutationRequest(t, root, "Create", "ClusterRole", input)
			require.Len(t, errors, 1)
			require.Equal(t, 1, git.commits, "unknown or pending commits cannot become fresh creates")
		})
	}
}

func TestRBACGitConflictRecapturesRevokedAuthority(t *testing.T) {
	root, git, store := newRBACResourceFixture(t)
	git.beforeCommit = func() error {
		origin, err := git.origin(t.Context(), rbac.ClusterTierID)
		require.NoError(t, err)
		state, err := store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
		require.NoError(t, err)
		binding, err := store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRoleBinding, Name: "administrator"})
		require.NoError(t, err)
		origin.GitRevision = "main@sha1:" + strings.Repeat("f", 40)
		origin.TreeDigest, err = rbac.ManifestTreeDigest(nil)
		require.NoError(t, err)
		_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID,
			ExpectedRevision: state.Revision, ExpectedGitRevision: state.Projection.GitRevision,
			Projection: &rbac.ProjectionIdentity{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
				GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "revoker"},
			Changes: []rbac.Change{{Resource: binding, ExpectedUID: binding.Metadata.UID,
				ExpectedResourceVersion: binding.Metadata.ResourceVersion, Terminate: true}}})
		require.NoError(t, err)
		git.mu.Lock()
		delete(git.files[origin.RepositoryID], "rbac/clusterrolebindings/administrator.yaml")
		git.origins[rbac.ClusterTierID] = origin
		git.mu.Unlock()
		return grpcstatus.Error(codes.Aborted, "another writer changed the target tip")
	}
	_, errors := rbacMutationRequest(t, root, "Create", "Role", map[string]any{
		"apiVersion": rbac.APIVersion, "kind": "Role", "metadata": map[string]any{"name": "retry", "namespace": "shop"},
		"spec": map[string]any{"rules": []any{}},
	})
	require.Len(t, errors, 1)
	require.Equal(t, "FORBIDDEN", errors[0].Extensions["code"])
	require.Zero(t, git.commits, "a conflict retry must not reuse the previous authorization")
}

func TestRBACGitConflictRebuildsAgainstConcurrentPushProjection(t *testing.T) {
	root, git, store := newRBACResourceFixture(t)
	parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	git.beforeCommit = func() error {
		git.beforeCommit = nil
		origin, err := git.origin(t.Context(), parent.UID)
		require.NoError(t, err)
		state, err := store.ReadRBACTier(t.Context(), parent.UID)
		require.NoError(t, err)
		external := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: parent.UID,
			Metadata: rbac.Metadata{Name: "from-push", Namespace: parent.Name, UID: uuid.NewString(),
				Finalizers: []string{rbac.LifecycleFinalizer}}, Spec: json.RawMessage(`{"rules":[]}`)}
		content, err := rbac.CanonicalManifest(external)
		require.NoError(t, err)
		origin.GitRevision = "main@sha1:" + strings.Repeat("f", 40)
		origin.TreeDigest, err = rbac.ManifestTreeDigest([]*rbac.Resource{external})
		require.NoError(t, err)
		_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: parent.UID, ExpectedRevision: state.Revision,
			ExpectedGitRevision: state.Projection.GitRevision, Changes: []rbac.Change{{Resource: external}},
			Projection: &rbac.ProjectionIdentity{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
				GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "push-actor"}})
		require.NoError(t, err)
		git.mu.Lock()
		git.files[origin.RepositoryID]["rbac/roles/from-push.yaml"] = content
		git.origins[parent.UID] = origin
		git.mu.Unlock()
		return grpcstatus.Error(codes.Aborted, "concurrent push won the ref")
	}
	node, errors := rbacMutationRequest(t, root, "Create", "Role", map[string]any{
		"metadata": map[string]any{"name": "from-graphql", "namespace": "shop"}, "spec": map[string]any{"rules": []any{}},
	})
	require.Empty(t, errors)
	require.NotNil(t, node)
	page, err := store.ListRBAC(t.Context(), rbac.ListRequest{TierID: parent.UID, Kind: rbac.Role, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Resources, 2)
	origin, err := git.origin(t.Context(), parent.UID)
	require.NoError(t, err)
	digest, err := rbac.ManifestTreeDigest(page.Resources)
	require.NoError(t, err)
	require.Equal(t, digest, origin.TreeDigest)
	require.Equal(t, 1, git.commits, "only the rebuilt GraphQL candidate is authored")
}

func TestRBACBindingRoleRefChangeAndForgedIncarnationAreRejected(t *testing.T) {
	root, git, _ := newRBACResourceFixture(t)
	metadata := map[string]any{"name": "immutable"}
	input := map[string]any{"apiVersion": rbac.APIVersion, "kind": "ClusterRoleBinding", "metadata": metadata,
		"spec": map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": "view"},
			"subjects": []any{map[string]any{"kind": "USER", "name": "future-user"}}}}
	node, errors := rbacMutationRequest(t, root, "Create", "ClusterRoleBinding", input)
	require.Empty(t, errors)
	metadata["resourceVersion"] = node["metadata"].(map[string]any)["resourceVersion"]
	input["spec"].(map[string]any)["roleRef"] = map[string]any{"kind": "CLUSTER_ROLE", "name": "edit"}
	_, errors = rbacMutationRequest(t, root, "Update", "ClusterRoleBinding", input)
	require.Len(t, errors, 1)
	require.Equal(t, "ADMISSION_REJECTED", errors[0].Extensions["code"])
	input["spec"].(map[string]any)["roleRef"] = map[string]any{"kind": "CLUSTER_ROLE", "name": "view"}
	_, nativeVersion, err := decodeRBACResourceVersion(metadata["resourceVersion"].(string))
	require.NoError(t, err)
	metadata["resourceVersion"] = base64.RawURLEncoding.EncodeToString([]byte(uuid.NewString() + "\n" + nativeVersion))
	_, errors = rbacMutationRequest(t, root, "Update", "ClusterRoleBinding", input)
	require.Len(t, errors, 1)
	require.Equal(t, "CONFLICT", errors[0].Extensions["code"])
	require.Equal(t, 1, git.commits)
}

func TestRBACLifecycleRequiresSeparateAuthorityAndUIDVersion(t *testing.T) {
	for _, kind := range []rbac.Kind{rbac.Role, rbac.ClusterRole, rbac.RoleBinding, rbac.ClusterRoleBinding} {
		t.Run(string(kind), func(t *testing.T) {
			root, git, store := newRBACResourceFixture(t)
			namespace := ""
			metadata := map[string]any{"name": "lifecycle"}
			if kind.Namespaced() {
				namespace = "shop"
				metadata["namespace"] = namespace
			}
			spec := map[string]any{"rules": []any{}}
			if !kind.IsRole() {
				spec = map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": "view"},
					"subjects": []any{map[string]any{"kind": "USER", "name": "alice"}}}
			}
			node, errors := rbacMutationRequest(t, root, "Create", string(kind), map[string]any{
				"apiVersion": rbac.APIVersion, "kind": string(kind), "metadata": metadata, "spec": spec})
			require.Empty(t, errors)
			id := node["id"].(string)
			version := node["metadata"].(map[string]any)["resourceVersion"].(string)
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
			statusQuery := `mutation($input:UpdateResourceStatusInput!){updateResourceStatus(input:$input){object}}`
			// Status and purge are independent of the authoritative spec permission.
			_, errors = rbacGraphQLRequest(t, root, statusQuery, model.UpdateResourceStatusInput{
				Kind: string(kind), Name: "lifecycle", Namespace: namespace, ResourceVersion: version})
			require.Len(t, errors, 1)
			require.Equal(t, "FORBIDDEN", errors[0].Extensions["code"])
			root.registry.AuthZ().(*rbacResourceProvider).allowLifecycle.Store(true)
			purge := func(currentVersion string) error {
				return root.completeRBACDeletion(ctx, kind, id, namespace, "lifecycle", currentVersion)
			}
			require.Error(t, purge(version), "live resources cannot be purged")
			node, errors = rbacMutationRequest(t, root, "Delete", string(kind), map[string]any{"id": id, "resourceVersion": version})
			require.Empty(t, errors)
			version = node["metadata"].(map[string]any)["resourceVersion"].(string)
			require.Error(t, purge(version), "the lifecycle finalizer must still block purge")
			input := map[string]any{"kind": string(kind), "name": "lifecycle", "namespace": namespace,
				"resourceVersion": version, "finalizers": []any{}}
			data, errors := rbacGraphQLRequest(t, root, statusQuery, input)
			require.Empty(t, errors)
			updated := data["updateResourceStatus"]["object"].(map[string]any)
			nextVersion := updated["metadata"].(map[string]any)["resourceVersion"].(string)
			require.NotEqual(t, version, nextVersion)
			_, errors = rbacGraphQLRequest(t, root, statusQuery, input)
			require.Len(t, errors, 1)
			require.Equal(t, "CONFLICT", errors[0].Extensions["code"])
			root.store = rbacLifecycleDatastore{Datastore: root.store, rbac: store}
			store.fail.Store(true)
			require.Error(t, purge(nextVersion), "a crash after guard finalization must remain retryable")
			store.fail.Store(false)
			require.NoError(t, purge(nextVersion))
			uid, err := decodeNodeIDAs(string(kind), id)
			require.NoError(t, err)
			_, err = store.GetRBACByUID(ctx, uid)
			require.ErrorIs(t, err, rbac.ErrNotFound)
			require.Equal(t, 2, git.commits, "controller-only updates must not author Git spec")
		})
	}
}

func TestRBACLifecycleCannotPurgeAClaimedRole(t *testing.T) {
	root, git, store := newRBACResourceFixture(t)
	root.registry.AuthZ().(*rbacResourceProvider).allowLifecycle.Store(true)
	role, errors := rbacMutationRequest(t, root, "Create", "Role", map[string]any{
		"metadata": map[string]any{"name": "claimed", "namespace": "shop"}, "spec": map[string]any{"rules": []any{}},
	})
	require.Empty(t, errors)
	binding, errors := rbacMutationRequest(t, root, "Create", "RoleBinding", map[string]any{
		"metadata": map[string]any{"name": "claim", "namespace": "shop"},
		"spec": map[string]any{"roleRef": map[string]any{"kind": "ROLE", "name": "claimed"},
			"subjects": []any{map[string]any{"kind": "USER", "name": "alice"}}},
	})
	require.Empty(t, errors)
	statusQuery := `mutation($input:UpdateResourceStatusInput!){updateResourceStatus(input:$input){object}}`
	statusInput := map[string]any{"kind": "RoleBinding", "name": "claim", "namespace": "shop",
		"resourceVersion": binding["metadata"].(map[string]any)["resourceVersion"],
		"resolved":        map[string]any{"roleUID": uuid.NewString(), "roleKind": "Role", "roleName": "claimed"}}
	_, errors = rbacGraphQLRequest(t, root, statusQuery, statusInput)
	require.Len(t, errors, 1)
	require.Equal(t, "BAD_USER_INPUT", errors[0].Extensions["code"])
	roleUID := role["metadata"].(map[string]any)["uid"].(string)
	statusInput["resolved"].(map[string]any)["roleUID"] = roleUID
	data, errors := rbacGraphQLRequest(t, root, statusQuery, statusInput)
	require.Empty(t, errors)
	binding = data["updateResourceStatus"]["object"].(map[string]any)
	owners := binding["metadata"].(map[string]any)["ownerReferences"].([]any)
	require.Len(t, owners, 2)
	namespaceOwner, roleOwner := false, false
	for _, raw := range owners {
		owner := raw.(map[string]any)
		switch owner["kind"] {
		case "Namespace":
			parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
			require.NoError(t, err)
			require.Equal(t, parent.UID, owner["uid"])
			require.Equal(t, false, owner["blockOwnerDeletion"])
			namespaceOwner = true
		case "Role":
			require.Equal(t, roleUID, owner["uid"])
			require.Equal(t, true, owner["blockOwnerDeletion"])
			roleOwner = true
		}
	}
	require.True(t, namespaceOwner && roleOwner, "resolution publishes both scoped lifecycle links")
	guard, err := store.ReadRBACRoleGuard(t.Context(), roleUID)
	require.NoError(t, err)
	require.EqualValues(t, 1, guard.Claims)
	terminateAndClear := func(kind string, node map[string]any) map[string]any {
		t.Helper()
		deleted, errors := rbacMutationRequest(t, root, "Delete", kind, map[string]any{
			"id": node["id"], "resourceVersion": node["metadata"].(map[string]any)["resourceVersion"]})
		require.Empty(t, errors)
		status, errors := rbacGraphQLRequest(t, root, statusQuery, map[string]any{
			"kind": kind, "namespace": "shop", "name": deleted["metadata"].(map[string]any)["name"],
			"resourceVersion": deleted["metadata"].(map[string]any)["resourceVersion"], "finalizers": []any{}})
		require.Empty(t, errors)
		return status["updateResourceStatus"]["object"].(map[string]any)
	}
	role = terminateAndClear("Role", role)
	purge := func(kind string, node map[string]any) gqlerror.List {
		t.Helper()
		query := fmt.Sprintf(`mutation($input:Complete%sDeletionInput!){complete%sDeletion(input:$input){id}}`, kind, kind)
		_, errors := rbacGraphQLRequest(t, root, query, map[string]any{
			"id": node["id"], "namespace": "shop", "name": node["metadata"].(map[string]any)["name"],
			"resourceVersion": node["metadata"].(map[string]any)["resourceVersion"]})
		return errors
	}
	errors = purge("Role", role)
	require.Len(t, errors, 1)
	require.Equal(t, "CONFLICT", errors[0].Extensions["code"])
	binding = terminateAndClear("RoleBinding", binding)
	require.Empty(t, purge("RoleBinding", binding))
	require.Empty(t, purge("Role", role))
	require.Empty(t, purge("Role", role), "completion replay must not touch a different incarnation")
	guard, err = store.ReadRBACRoleGuard(t.Context(), roleUID)
	require.NoError(t, err)
	require.EqualValues(t, 0, guard.Claims)
	require.Equal(t, rbac.GuardFinalized, guard.State)
	require.Equal(t, 4, git.commits)
}

func TestRBACOperationsAreRejectedByStorefrontTransport(t *testing.T) {
	server := handler.NewDefaultServer(storefrontgenerated.NewExecutableSchema(storefrontgenerated.Config{}))
	for _, field := range []string{"createRole", "createClusterRole", "createRoleBinding", "createClusterRoleBinding"} {
		request := httptest.NewRequest(http.MethodPost, "/storefront/graphql",
			strings.NewReader(fmt.Sprintf(`{"query":"mutation {%s(input:{}){__typename}} "}`, field)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		var result struct{ Errors gqlerror.List }
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		require.NotEmpty(t, result.Errors)
		require.Equal(t, "GRAPHQL_VALIDATION_FAILED", result.Errors[0].Extensions["code"])
	}
}

func receiveRBACWatchEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event, open := <-events:
		require.True(t, open, "RBAC watch closed unexpectedly")
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RBAC journal event")
		var zero T
		return zero
	}
}

func TestRBACTypedAndGenericWatchesShareDurableEvents(t *testing.T) {
	for _, kind := range []rbac.Kind{rbac.Role, rbac.ClusterRole, rbac.RoleBinding, rbac.ClusterRoleBinding} {
		t.Run(string(kind), func(t *testing.T) {
			original, git, _ := newRBACResourceFixture(t)
			original.registry.AuthZ().(*rbacResourceProvider).allowLifecycle.Store(true)
			journal := original.store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
			lease, acquired, err := journal.AcquireLease(t.Context(), "rbac-watch-test", time.Now(), time.Minute)
			require.NoError(t, err)
			require.True(t, acquired)
			_, err = journal.Append(t.Context(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark, At: time.Now()}, time.Hour)
			require.NoError(t, err)
			root, err := NewResolver(ResolverDeps{Store: original.store, GitWriter: git, Registry: original.registry,
				RBACValidator: original.rbacValidator, ResourceJournal: journal, Logger: zap.NewNop(),
				NamespaceWatch: config.WatchJournalConfig{
					Read:         config.ReadWatchConfig{BatchSize: 64, MaxReplayEvents: 1000},
					Subscriber:   config.SubscriberWatchConfig{Buffer: 8, Backpressure: time.Second},
					Poll:         config.PollWatchConfig{Min: time.Millisecond, Max: 10 * time.Millisecond},
					Materializer: config.MaterializerWatchConfig{MaxLag: time.Minute},
				}})
			require.NoError(t, err)
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
			namespace := "shop"
			var scope *string
			metadata := map[string]any{"name": "watched", "labels": map[string]any{"team": "rbac"}}
			if kind.Namespaced() {
				scope, metadata["namespace"] = &namespace, namespace
			}
			spec := map[string]any{"rules": []any{}}
			if !kind.IsRole() {
				spec = map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": "view"},
					"subjects": []any{map[string]any{"kind": "USER", "name": "future-user"}}}
			}
			selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "rbac"}}
			cursor := watchjournal.BootstrapCursor
			generic, err := root.Subscription().WatchResources(ctx, string(kind), scope, selector, &cursor)
			require.NoError(t, err)
			var nextTyped func() map[string]any
			project := func(event any) map[string]any {
				t.Helper()
				data, err := json.Marshal(event)
				require.NoError(t, err)
				var value map[string]any
				require.NoError(t, json.Unmarshal(data, &value))
				return value
			}
			switch kind {
			case rbac.Role:
				events, err := root.Subscription().WatchRoles(ctx, scope, selector, &cursor)
				require.NoError(t, err)
				nextTyped = func() map[string]any { return project(receiveRBACWatchEvent(t, events)) }
			case rbac.ClusterRole:
				events, err := root.Subscription().WatchClusterRoles(ctx, selector, &cursor)
				require.NoError(t, err)
				nextTyped = func() map[string]any { return project(receiveRBACWatchEvent(t, events)) }
			case rbac.RoleBinding:
				events, err := root.Subscription().WatchRoleBindings(ctx, scope, selector, &cursor)
				require.NoError(t, err)
				nextTyped = func() map[string]any { return project(receiveRBACWatchEvent(t, events)) }
			case rbac.ClusterRoleBinding:
				events, err := root.Subscription().WatchClusterRoleBindings(ctx, selector, &cursor)
				require.NoError(t, err)
				nextTyped = func() map[string]any { return project(receiveRBACWatchEvent(t, events)) }
			}
			bookmark, typedBookmark := receiveRBACWatchEvent(t, generic), nextTyped()
			require.Equal(t, model.WatchEventTypeBookmark, bookmark.Type)
			require.Equal(t, bookmark.ResourceVersion, typedBookmark["resourceVersion"])
			nextGenericData := func(events <-chan *model.WatchEvent) *model.WatchEvent {
				t.Helper()
				for range 8 {
					event := receiveRBACWatchEvent(t, events)
					if event.Type != model.WatchEventTypeBookmark {
						return event
					}
					require.Equal(t, string(kind), event.Kind)
					require.Nil(t, event.Object, "internal progress must never leak as a resource")
				}
				t.Fatal("too many bookmarks without resource progress")
				return nil
			}
			nextTypedData := func() map[string]any {
				t.Helper()
				for range 8 {
					event := nextTyped()
					if event["type"] != string(model.WatchEventTypeBookmark) {
						return event
					}
				}
				t.Fatal("too many typed bookmarks without resource progress")
				return nil
			}
			input := map[string]any{"metadata": metadata, "spec": spec}
			node, errors := rbacMutationRequest(t, root, "Create", string(kind), input)
			require.Empty(t, errors)
			added, typedAdded := nextGenericData(generic), nextTypedData()
			require.Equal(t, model.WatchEventTypeAdded, added.Type)
			require.Equal(t, string(kind), added.Kind)
			require.Equal(t, added.ResourceVersion, typedAdded["resourceVersion"])
			require.Equal(t, node["metadata"].(map[string]any)["resourceVersion"], added.Object["metadata"].(map[string]any)["resourceVersion"])
			metadata["labels"] = map[string]any{"team": "other"}
			metadata["resourceVersion"] = node["metadata"].(map[string]any)["resourceVersion"]
			_, errors = rbacMutationRequest(t, root, "Update", string(kind), input)
			require.Empty(t, errors)
			deleted, typedDeleted := nextGenericData(generic), nextTypedData()
			require.Equal(t, model.WatchEventTypeDeleted, deleted.Type)
			require.Equal(t, deleted.ResourceVersion, typedDeleted["resourceVersion"])
			require.Nil(t, deleted.Object)
			resumed, err := root.Subscription().WatchResources(ctx, string(kind), scope, selector, &added.ResourceVersion)
			require.NoError(t, err)
			replay := nextGenericData(resumed)
			require.Equal(t, deleted.ResourceVersion, replay.ResourceVersion)
			require.Equal(t, deleted.Type, replay.Type)
		})
	}
}

func TestRBACDeclarativeSchemaContract(t *testing.T) {
	schema := namespaceContractSchema(t)
	for _, kind := range []string{"Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding"} {
		t.Run(kind, func(t *testing.T) {
			require.Len(t, schema.Types[kind].Fields, 6, "RBAC resources expose only their canonical envelope")
			definition := schema.Types[kind]
			require.NotNil(t, definition)
			assert.Contains(t, definition.Interfaces, "Node")
			for field, fieldType := range map[string]string{
				"id": "ID!", "apiVersion": "String!", "kind": "String!",
				"metadata": "ObjectMeta!", "spec": kind + "Spec!", "status": "RBACStatus!",
			} {
				requireGraphQLField(t, schema, kind, field, fieldType)
			}
			fieldName := strings.ToLower(kind[:1]) + kind[1:]
			lookup := requireGraphQLField(t, schema, "Query", fieldName, kind)
			require.NotNil(t, lookup.Arguments.ForName("by"))
			selector := schema.Types[kind+"By"]
			require.NotNil(t, selector)
			require.NotNil(t, selector.Directives.ForName("oneOf"))
			requireGraphQLField(t, schema, kind+"By", "id", "ID")
			if strings.HasPrefix(kind, "Cluster") {
				requireGraphQLField(t, schema, kind+"By", "name", "String")
				assert.Nil(t, selector.Fields.ForName("namespacePath"))
			} else {
				requireGraphQLField(t, schema, kind+"By", "namespacePath", "RBACNamespacePath")
				assert.Nil(t, selector.Fields.ForName("name"))
			}
			for _, operation := range []string{"Create", "Update"} {
				input := operation + kind + "Input"
				version := requireGraphQLField(t, schema, input, "apiVersion", "String!")
				require.NotNil(t, version.DefaultValue)
				assert.Equal(t, "rbac.authorization.gitstore.dev/v1beta1", version.DefaultValue.Raw)
				discriminator := requireGraphQLField(t, schema, input, "kind", "String!")
				assert.Equal(t, kind, discriminator.DefaultValue.Raw)
				requireGraphQLField(t, schema, input, "metadata", "ObjectMetaInput!")
				requireGraphQLField(t, schema, input, "spec", kind+"SpecInput!")
				assert.Nil(t, schema.Types[input].Fields.ForName("status"))
			}
			for _, operation := range []string{"Create", "Update", "Delete", "Complete"} {
				suffix := kind
				if operation == "Complete" {
					suffix += "Deletion"
				}
				name := strings.ToLower(operation) + suffix
				field := requireGraphQLField(t, schema, "Mutation", name, operation+suffix+"Payload!")
				require.NotNil(t, field.Arguments.ForName("input"))
			}
			requireGraphQLField(t, schema, "Delete"+kind+"Payload", "outcome", "ResourceDeletionOutcome!")
			assert.Nil(t, schema.Types["Update"+kind+"Input"].Fields.ForName("resourceVersion"))
			for _, operation := range []string{"Delete", "Complete"} {
				suffix := kind
				if operation == "Complete" {
					suffix += "Deletion"
				}
				requireGraphQLField(t, schema, operation+suffix+"Input", "resourceVersion", "String!")
			}
			requireGraphQLField(t, schema, "Complete"+kind+"DeletionInput", "id", "ID!")
			requireGraphQLField(t, schema, kind+"Connection", "edges", "["+kind+"Edge!]!")
			requireGraphQLField(t, schema, kind+"Connection", "pageInfo", "PageInfo!")
		})
	}
	requireGraphQLField(t, schema, "RBACRuleInput", "when", "[RBACConditionInput!]")
	requireGraphQLField(t, schema, "ObjectMetaInput", "resourceVersion", "String")
	require.NotNil(t, schema.Types["RBACConditionInput"].Directives.ForName("oneOf"))
	assert.Nil(t, schema.Types["RBACRuleInput"].Fields.ForName("effect"))
	requireGraphQLField(t, schema, "UpdateResourceStatusInput", "finalizers", "[String!]")
}

func TestRBACEnvelopeConversionPreservesTypedConditionsAndIdentity(t *testing.T) {
	value := "public"
	created := time.Now().UTC()
	uid := uuid.NewString()
	spec, err := json.Marshal(rbac.RoleSpec{Rules: []rbac.Rule{{
		Resources: []string{"product"}, Verbs: []string{"read"},
		When: []rbac.Condition{{Token: "owner"}, {Attr: "visibility", Op: "eq", Value: &value}},
	}}})
	require.NoError(t, err)
	resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: uuid.NewString(),
		Metadata: rbac.Metadata{Name: "reader", Namespace: "shop", UID: uid, Generation: 2,
			ResourceVersion: "7", CreationTimestamp: &created, Finalizers: []string{rbac.LifecycleFinalizer}},
		Spec: spec, Status: json.RawMessage(`{"observedGeneration":2,"conditions":[]}`)}
	node, err := rbacResourceToGraphQL(resource)
	require.NoError(t, err)
	role := node.(*model.Role)
	kind, decoded, err := DecodeNodeID(role.ID)
	require.NoError(t, err)
	require.Equal(t, "Role", kind)
	require.Equal(t, uid, decoded)
	versionUID, version, err := decodeRBACResourceVersion(role.Metadata.ResourceVersion)
	require.NoError(t, err)
	require.Equal(t, uid, versionUID)
	require.Equal(t, "7", version)
	require.Equal(t, created, role.Metadata.CreationTimestamp)
	require.EqualValues(t, 2, role.Status.ObservedGeneration)
	require.Equal(t, "owner", *role.Spec.Rules[0].When[0].Token)
	require.Equal(t, value, *role.Spec.Rules[0].When[1].Attribute.Value)
	require.Equal(t, "visibility", role.Spec.Rules[0].When[1].Attribute.Attr)
	require.NotNil(t, role.Metadata.OwnerReferences)
	require.NotNil(t, role.Status.Conditions)
}

type rbacDenyReads struct{}

func rbacReadFixtureProjection() *rbac.ProjectionIdentity {
	return &rbac.ProjectionIdentity{RepositoryID: "11111111-1111-4111-8111-111111111111",
		GitRef: "refs/heads/main", GitRevision: "main@sha1:" + strings.Repeat("a", 40),
		TreeDigest: strings.Repeat("b", 64), Actor: "fixture"}
}

func (rbacDenyReads) Name() string { return "deny" }
func (rbacDenyReads) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Deny("deny", "fixture"), nil
}

func TestRBACReadsAuthorizeBeforeExistenceAndMergeVirtualResources(t *testing.T) {
	ds, root := newGlobalIDTestResolver(t)
	store := ds.(datastore.RBACCapable).RBACStore()
	query := &queryResolver{Resolver: root}
	name := "custom-read"
	resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
		Metadata: rbac.Metadata{Name: name, UID: uuid.NewString()},
		Spec:     json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`)}
	_, err := store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID,
		Projection: rbacReadFixtureProjection(),
		Changes:    []rbac.Change{{Resource: resource}}})
	require.NoError(t, err)
	role, err := query.ClusterRole(t.Context(), model.ClusterRoleBy{Name: &name})
	require.NoError(t, err)
	node, err := query.Node(t.Context(), role.ID)
	require.NoError(t, err)
	require.Equal(t, role, node)
	one := int32(1)
	page, err := query.ClusterRoles(t.Context(), &one, nil, nil)
	require.NoError(t, err)
	require.Len(t, page.Edges, 1)
	require.Equal(t, name, page.Edges[0].Node.Metadata.Name)
	require.True(t, page.PageInfo.HasNextPage)
	var names []string
	for page.PageInfo.HasNextPage {
		page, err = query.ClusterRoles(t.Context(), &one, page.PageInfo.EndCursor, nil)
		require.NoError(t, err)
		require.Len(t, page.Edges, 1)
		names = append(names, page.Edges[0].Node.Metadata.Name)
	}
	require.Contains(t, names, "cluster-admin")
	require.NotContains(t, names, name)
	expected := 0
	for _, builtin := range root.rbacBuiltinsResources {
		if builtin.Kind == rbac.ClusterRole {
			expected++
		}
	}
	require.Len(t, names, expected)
	root.registry = auth.NewProviderRegistry(nil, rbacDenyReads{}, nil)
	for _, current := range []string{name, "absent"} {
		_, err := query.ClusterRole(t.Context(), model.ClusterRoleBy{Name: &current})
		var denied *gqlerror.Error
		require.ErrorAs(t, err, &denied)
		require.Equal(t, "FORBIDDEN", denied.Extensions["code"])
	}
	_, err = query.Node(t.Context(), role.ID)
	require.Error(t, err)
	_, err = query.ClusterRoles(t.Context(), nil, nil, nil)
	require.Error(t, err)
}

func TestRBACListCursorRejectsChangedRevisionScopeAndSelector(t *testing.T) {
	ds, root := newGlobalIDTestResolver(t)
	query := &queryResolver{Resolver: root}
	one := int32(1)
	first, err := query.ClusterRoles(t.Context(), &one, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, first.PageInfo.EndCursor)
	_, err = query.ClusterRoleBindings(t.Context(), &one, first.PageInfo.EndCursor, nil)
	require.Error(t, err)
	_, err = query.ClusterRoles(t.Context(), &one, first.PageInfo.EndCursor,
		&model.LabelSelectorInput{MatchLabels: map[string]any{"team": "other"}})
	require.Error(t, err)
	store := ds.(datastore.RBACCapable).RBACStore()
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID,
		Projection: rbacReadFixtureProjection(),
		Changes: []rbac.Change{{Resource: &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
			Metadata: rbac.Metadata{Name: "new-role", UID: uuid.NewString()},
			Spec:     json.RawMessage(`{"rules":[]}`)}}}})
	require.NoError(t, err)
	_, err = query.ClusterRoles(t.Context(), &one, first.PageInfo.EndCursor, nil)
	var conflict *gqlerror.Error
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "CONFLICT", conflict.Extensions["code"])
}

func TestRBACGraphQLBuiltinMutationAttemptsNeverAuthorGit(t *testing.T) {
	root, git, store := newRBACResourceFixture(t)
	for _, builtin := range root.rbacBuiltinsResources {
		t.Run(string(builtin.Kind)+"/"+builtin.Metadata.Name, func(t *testing.T) {
			spec := map[string]any{"rules": []any{}}
			if !builtin.Kind.IsRole() {
				spec = map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": "view"},
					"subjects": []any{map[string]any{"kind": "USER", "name": "alice"}}}
			}
			input := map[string]any{"apiVersion": rbac.APIVersion, "kind": string(builtin.Kind),
				"metadata": map[string]any{"name": builtin.Metadata.Name}, "spec": spec}
			_, errors := rbacMutationRequest(t, root, "Create", string(builtin.Kind), input)
			require.NotEmpty(t, errors)
			node, err := rbacResourceToGraphQL(builtin)
			require.NoError(t, err)
			metadata := input["metadata"].(map[string]any)
			if builtin.Kind.IsRole() {
				metadata["resourceVersion"] = node.(*model.ClusterRole).Metadata.ResourceVersion
			} else {
				metadata["resourceVersion"] = node.(*model.ClusterRoleBinding).Metadata.ResourceVersion
			}
			_, errors = rbacMutationRequest(t, root, "Update", string(builtin.Kind), input)
			require.NotEmpty(t, errors)
			_, errors = rbacMutationRequest(t, root, "Delete", string(builtin.Kind), map[string]any{
				"id": node.GetID(), "resourceVersion": metadata["resourceVersion"],
			})
			require.NotEmpty(t, errors)
			require.Zero(t, git.commits)
		})
	}
	resource, err := store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRoleBinding, Name: "administrator"})
	require.NoError(t, err)
	node, err := rbacResourceToGraphQL(resource)
	require.NoError(t, err)
	ordinary := node.(*model.ClusterRoleBinding)
	deleted, errors := rbacMutationRequest(t, root, "Delete", "ClusterRoleBinding", map[string]any{
		"id": ordinary.ID, "resourceVersion": ordinary.Metadata.ResourceVersion,
	})
	require.Empty(t, errors, "the persisted administrator binding must remain removable")
	require.NotNil(t, deleted["metadata"].(map[string]any)["deletionTimestamp"])
	require.Equal(t, 1, git.commits)
}

func TestRBACBuiltinBindingsPaginateWithoutStorageOrDuplicateNodes(t *testing.T) {
	_, root := newGlobalIDTestResolver(t)
	query := &queryResolver{Resolver: root}
	one := int32(1)
	var after *string
	seen := map[string]bool{}
	var ids []string
	for {
		page, err := query.ClusterRoleBindings(t.Context(), &one, after, nil)
		require.NoError(t, err)
		for _, edge := range page.Edges {
			require.False(t, seen[edge.Node.Metadata.UID])
			seen[edge.Node.Metadata.UID] = true
			ids = append(ids, edge.Node.ID)
			role, err := query.ClusterRole(t.Context(), model.ClusterRoleBy{Name: &edge.Node.Spec.RoleRef.Name})
			require.NoError(t, err)
			require.Equal(t, role.Metadata.UID, edge.Node.Status.Resolved.RoleUID)
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		require.NotNil(t, page.PageInfo.EndCursor)
		require.NotEqual(t, after, page.PageInfo.EndCursor)
		after = page.PageInfo.EndCursor
	}
	for _, builtin := range root.rbacBuiltinsResources {
		if builtin.Kind == rbac.ClusterRoleBinding {
			require.True(t, seen[builtin.Metadata.UID], builtin.Metadata.Name)
		}
	}
	nodes, err := query.Nodes(t.Context(), ids)
	require.NoError(t, err)
	require.Len(t, nodes, len(seen))
	for i, node := range nodes {
		require.Equal(t, ids[i], node.GetID())
	}
}

func TestRBACBuiltinBindingConversionPreservesResolvedUID(t *testing.T) {
	_, root := newGlobalIDTestResolver(t)
	query := &queryResolver{Resolver: root}
	name := "namespace-owner"
	binding, err := query.ClusterRoleBinding(t.Context(), model.ClusterRoleBindingBy{Name: &name})
	require.NoError(t, err)
	role, err := query.ClusterRole(t.Context(), model.ClusterRoleBy{Name: &name})
	require.NoError(t, err)
	require.Equal(t, role.Metadata.UID, binding.Status.Resolved.RoleUID)
	require.Equal(t, name, binding.Status.Resolved.RoleName)
	require.Equal(t, "system:authenticated", binding.Spec.Subjects[0].Name)
	require.NotEmpty(t, binding.Metadata.Revision)
}

func TestRBACAllResourceFamiliesSupportTypedAndBatchedUIDReads(t *testing.T) {
	ds, root := newGlobalIDTestResolver(t)
	namespaceUID := uuid.NewString()
	require.NoError(t, ds.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: namespaceUID, Name: "shop", CreationTimestamp: time.Now().UTC(),
	}))
	store := ds.(datastore.RBACCapable).RBACStore()
	var ids []string
	var expected []model.Node
	query := &queryResolver{Resolver: root}
	for _, kind := range []rbac.Kind{rbac.Role, rbac.RoleBinding, rbac.ClusterRole, rbac.ClusterRoleBinding} {
		t.Run(string(kind), func(t *testing.T) {
			name := strings.ToLower(string(kind))
			resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: kind,
				Metadata: rbac.Metadata{Name: name, UID: uuid.NewString()}, Spec: json.RawMessage(`{"rules":[]}`)}
			tier := rbac.ClusterTierID
			if kind.Namespaced() {
				tier, resource.NamespaceUID, resource.Metadata.Namespace = namespaceUID, namespaceUID, "shop"
			}
			if !kind.IsRole() {
				resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"view"},"subjects":[{"kind":"User","name":"future-user"}]}`)
			}
			state, err := store.ReadRBACTier(t.Context(), tier)
			require.NoError(t, err)
			projection := rbacReadFixtureProjection()
			projection.GitRevision = "main@sha1:" + fmt.Sprintf("%040x", state.Revision+1)
			_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier,
				ExpectedRevision: state.Revision, ExpectedGitRevision: state.Projection.GitRevision,
				Projection: projection, Changes: []rbac.Change{{Resource: resource}}})
			require.NoError(t, err)
			var node model.Node
			path := &model.RBACNamespacePath{Namespace: "shop", Name: name}
			switch kind {
			case rbac.Role:
				node, err = query.Role(t.Context(), model.RoleBy{NamespacePath: path})
			case rbac.RoleBinding:
				node, err = query.RoleBinding(t.Context(), model.RoleBindingBy{NamespacePath: path})
			case rbac.ClusterRole:
				node, err = query.ClusterRole(t.Context(), model.ClusterRoleBy{Name: &name})
			case rbac.ClusterRoleBinding:
				node, err = query.ClusterRoleBinding(t.Context(), model.ClusterRoleBindingBy{Name: &name})
			}
			require.NoError(t, err)
			ids, expected = append(ids, node.GetID()), append(expected, node)
			byUID, err := query.Node(t.Context(), node.GetID())
			require.NoError(t, err)
			require.Equal(t, node, byUID)
		})
	}
	nodes, err := query.Nodes(t.Context(), ids)
	require.NoError(t, err)
	require.Equal(t, expected, nodes)
	roles, err := query.Roles(t.Context(), "shop", nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, roles.Edges, 1)
	bindings, err := query.RoleBindings(t.Context(), "shop", nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, bindings.Edges, 1)
}

func TestRBACNodeRejectsPreviousNamespaceIncarnation(t *testing.T) {
	ds, root := newGlobalIDTestResolver(t)
	require.NoError(t, ds.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: uuid.NewString(), Name: "reused", CreationTimestamp: time.Now().UTC(),
	}))
	oldUID := uuid.NewString()
	role := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: oldUID,
		Metadata: rbac.Metadata{Name: "reader", Namespace: "reused", UID: uuid.NewString()}, Spec: json.RawMessage(`{"rules":[]}`)}
	store := ds.(datastore.RBACCapable).RBACStore()
	_, err := store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: oldUID,
		Projection: rbacReadFixtureProjection(), Changes: []rbac.Change{{Resource: role}}})
	require.NoError(t, err)
	query := &queryResolver{Resolver: root}
	node, err := query.Node(t.Context(), mustEncodeNodeID(nodeKindRole, role.Metadata.UID))
	require.Nil(t, node)
	requireReadNotFoundEnvelope(t, err, nodeKindRole)
}
