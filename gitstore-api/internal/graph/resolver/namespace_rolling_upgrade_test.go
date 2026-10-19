// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/validator/rules"
	"go.uber.org/zap"
)

func TestSameVersionNamespaceGraphQLRetainsShippedPolicy(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	namespace := namespaceContractFixture(globalIDTestNamespaceID, "private-namespace")
	namespace.Tier, namespace.CreationActor = datastore.NamespaceTierOrganization, "alice"
	require.NoError(t, store.CreateNamespace(t.Context(), namespace))
	id := mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID)
	const otherUID = "00000000-0000-0000-0000-000000000899"
	other := namespaceContractFixture(otherUID, "other-private-namespace")
	other.Tier, other.CreationActor = datastore.NamespaceTierOrganization, "charlie"
	require.NoError(t, store.CreateNamespace(t.Context(), other))
	otherID := mustEncodeNodeID(nodeKindNamespace, otherUID)
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "policy.yaml"))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, policy, 0600))
	var servers [2]*handler.Server
	var providers [2]*rbaclocal.RBACLocalProvider
	var baseline auth.AuthorizationSnapshot
	replace := func(i int) {
		t.Helper()
		providers[i], err = rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
		require.NoError(t, err)
		registry := auth.NewProviderRegistry(nil, providers[i], nil)
		root, err := NewResolver(ResolverDeps{Store: store, Registry: registry, Logger: zap.NewNop()})
		require.NoError(t, err)
		servers[i] = handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
		middleware := security.NewAuthorizeWithStore(registry, store, zap.NewNop())
		servers[i].AroundFields(middleware.GraphQLFieldAuthorizer)
		snapshot, ok := providers[i].AuthorizationSnapshot()
		require.True(t, ok)
		require.True(t, snapshot.Loaded)
		if baseline.Digest == "" {
			baseline = snapshot
		}
		require.Equal(t, baseline, snapshot)
	}
	replace(0)
	replace(1)
	check := func(phase, owner string) {
		t.Helper()
		t.Run(phase, func(t *testing.T) {
			for i, server := range servers {
				snapshot, ok := providers[i].AuthorizationSnapshot()
				require.True(t, ok)
				require.Equal(t, baseline, snapshot)
				for _, principal := range []*auth.Principal{
					{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:authenticated"}},
					{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}},
					{Subject: "bob", AuthMethod: "static-users", Groups: []string{"system:authenticated"}},
					auth.Anonymous(),
				} {
					for _, tc := range []struct {
						name, query, expected string
						list, mixed           bool
					}{
						{"name", `{detail:namespace(by:{name:"private-namespace"}){id}}`, fmt.Sprintf(`{"id":%q}`, id), false, false},
						{"id", fmt.Sprintf(`{detail:namespace(by:{id:%q}){id}}`, id), fmt.Sprintf(`{"id":%q}`, id), false, false},
						{"node", fmt.Sprintf(`{detail:node(id:%q){id}}`, id), fmt.Sprintf(`{"id":%q}`, id), false, false},
						{"nodes", fmt.Sprintf(`{detail:nodes(ids:[%q]){id}}`, id), fmt.Sprintf(`[{"id":%q}]`, id), false, false},
						{"mixed-nodes", fmt.Sprintf(`{detail:nodes(ids:[%q,%q]){id}}`, id, otherID), fmt.Sprintf(`[{"id":%q},{"id":%q}]`, id, otherID), false, true},
						{"list", `{detail:namespaces(first:2){edges{node{id}}}}`, "", true, false},
					} {
						t.Run(fmt.Sprintf("api-%d/%s/%s", i, principal.Subject, tc.name), func(t *testing.T) {
							t.Parallel()
							payload, err := json.Marshal(map[string]string{"query": tc.query})
							require.NoError(t, err)
							request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
							request.Header.Set("Content-Type", "application/json")
							request = request.WithContext(auth.ContextWithPrincipal(request.Context(), principal))
							response := httptest.NewRecorder()
							server.ServeHTTP(response, request)
							require.Equal(t, http.StatusOK, response.Code)
							var result struct {
								Data   map[string]json.RawMessage `json:"data"`
								Errors gqlerror.List              `json:"errors"`
							}
							require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
							allowed := principal.Subject == "admin" || (!tc.list && !tc.mixed && principal.Subject == owner)
							if allowed {
								require.Empty(t, result.Errors)
								if tc.list {
									var connection struct {
										Edges []struct{ Node struct{ ID string } }
									}
									require.NoError(t, json.Unmarshal(result.Data["detail"], &connection))
									var ids []string
									for _, edge := range connection.Edges {
										ids = append(ids, edge.Node.ID)
									}
									require.ElementsMatch(t, []string{id, otherID}, ids)
								} else {
									require.JSONEq(t, tc.expected, string(result.Data["detail"]))
								}
							} else {
								require.Len(t, result.Errors, 1)
								require.Equal(t, "FORBIDDEN", result.Errors[0].Extensions["code"])
								require.Equal(t, "detail", result.Errors[0].Path.String())
								if result.Data != nil {
									require.JSONEq(t, "null", string(result.Data["detail"]))
								}
								require.NotContains(t, response.Body.String(), namespace.Name)
								require.NotContains(t, response.Body.String(), id)
								require.NotContains(t, response.Body.String(), other.Name)
								require.NotContains(t, response.Body.String(), otherID)
							}
						})
					}
				}
			}
		})
	}
	check("initial", "alice")
	require.NoError(t, os.WriteFile(path, []byte("apiVersion: rbac.authorization.gitstore.dev/v1\n"), 0600))
	require.Error(t, providers[0].Reload())
	check("rejected-reload", "alice")
	require.NoError(t, os.WriteFile(path, policy, 0600))
	require.NoError(t, providers[0].Reload())
	check("same-policy-reload", "alice")
	replace(0)
	check("replacement-a", "alice")
	replace(1)
	check("replacement-b", "alice")
	stored, err := store.GetNamespaceByName(t.Context(), namespace.Name)
	require.NoError(t, err)
	expected := stored.ResourceVersion
	stored.Annotations = map[string]string{datastore.OwnerAnnotationKey: "bob"}
	datastore.AdvanceNamespaceSpecVersion(stored)
	require.NoError(t, store.UpdateNamespace(t.Context(), stored, expected))
	check("fresh-owner-after-replacement", "bob")
	current, err := store.GetNamespaceByName(t.Context(), namespace.Name)
	require.NoError(t, err)
	require.Equal(t, "bob", current.EffectiveOwnerSub())
	require.Equal(t, stored.ResourceVersion, current.ResourceVersion)
}

const legacyNamespaceDeletionSchema = `
schema { mutation: Mutation }
type Mutation {
  deleteNamespace(input: DeleteNamespaceInput!): DeleteNamespacePayload!
}
input DeleteNamespaceInput {
  identifier: String!
}
type DeleteNamespacePayload {
  deletedIdentifier: String!
}
`

const legacyDeleteNamespaceSelection = `
mutation {
  deleteNamespace(input: {identifier: "acme"}) {
    deletedIdentifier
  }
}
`

const outcomeDeleteNamespaceSelection = `
mutation {
  deleteNamespace(input: {id: "gid://GitStore/Namespace/acme"}) {
    namespace { id }
    outcome
  }
}
`

func TestNamespaceGraphQLServerRejectsLegacyDeleteSelector(t *testing.T) {
	oldSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "legacy-namespace.graphqls", Input: legacyNamespaceDeletionSchema})
	require.NoError(t, err)
	newSchema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()

	assertNamespaceSelectionValid(t, oldSchema, legacyDeleteNamespaceSelection, true)
	assertNamespaceSelectionValid(t, newSchema, legacyDeleteNamespaceSelection, false)
	assertNamespaceSelectionValid(t, oldSchema, outcomeDeleteNamespaceSelection, false)
	assertNamespaceSelectionValid(t, newSchema, outcomeDeleteNamespaceSelection, true)
}

func TestNamespaceOutcomeActivationWaitsForFullAPIFleetConvergence(t *testing.T) {
	oldSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "legacy-namespace.graphqls", Input: legacyNamespaceDeletionSchema})
	require.NoError(t, err)
	newSchema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()

	assert.False(t, namespaceFleetSupportsOutcome(newSchema, oldSchema), "mixed fleets must keep clients on the legacy selection")
	assert.True(t, namespaceFleetSupportsOutcome(newSchema, newSchema), "clients may activate outcome only after every replica exposes it")
	assert.False(t, namespaceFleetSupportsOutcome(oldSchema, newSchema), "rollback must disable outcome before the first old replica returns")
}

func namespaceFleetSupportsOutcome(schemas ...*ast.Schema) bool {
	if len(schemas) == 0 {
		return false
	}
	for _, schema := range schemas {
		payload := schema.Types["DeleteNamespacePayload"]
		if payload == nil || payload.Fields.ForName("outcome") == nil {
			return false
		}
	}
	return true
}

func assertNamespaceSelectionValid(t *testing.T, schema *ast.Schema, query string, valid bool) {
	t.Helper()
	_, errs := gqlparser.LoadQueryWithRules(schema, query, rules.NewDefaultRules())
	if valid {
		require.Empty(t, errs)
		return
	}
	require.NotEmpty(t, errs)
}
