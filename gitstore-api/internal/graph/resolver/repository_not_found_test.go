// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

const repositoryNotFoundTestNamespaceID = "01960000-0000-7000-8000-000000000140"

func TestQueryRepositoryUsesOnlyItsFieldAuthorizedSnapshot(t *testing.T) {
	const uid = "01960000-0000-7000-8000-000000000141"
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Repository/" + uid))
	for _, selector := range []string{"id", "path"} {
		t.Run(selector, func(t *testing.T) {
			by := model.RepositoryBy{ID: &id}
			if selector == "path" {
				by = model.RepositoryBy{NamespacePath: &model.RepositoryNamespacePath{Namespace: "acme", Name: "catalog"}}
			}
			current := &datastore.Repository{
				UID: uid, ID: uid, Name: "catalog", Namespace: "acme", DefaultBranch: "main",
				ResourceVersion: "1", Generation: 1, CreationActor: "alice", Body: "first",
			}
			namespace := &datastore.Namespace{Name: "acme", CreationActor: "alice"}
			repositoryReads, namespaceReads, mappingReads := 0, 0, 0
			store := &testutil.StubStore{
				GetRepositoryFunc: func(ctx context.Context, requested string) (*datastore.Repository, error) {
					repositoryReads++
					require.Equal(t, uid, requested)
					require.NoError(t, ctx.Err())
					copy := *current
					return &copy, nil
				},
				GetNamespaceByNameFunc: func(_ context.Context, requested string) (*datastore.Namespace, error) {
					namespaceReads++
					require.Equal(t, namespace.Name, requested)
					copy := *namespace
					return &copy, nil
				},
				LookupRepositoryFunc: func(_ context.Context, requestedNamespace, requestedName string) (*datastore.NamespaceMapping, error) {
					mappingReads++
					require.Equal(t, "acme", requestedNamespace)
					require.Equal(t, "catalog", requestedName)
					return &datastore.NamespaceMapping{Namespace: "acme", Name: "catalog", RepositoryID: uid}, nil
				},
			}
			root, err := resolver.NewResolver(resolver.ResolverDeps{Store: store, Logger: zap.NewNop()})
			require.NoError(t, err)
			provider := testutil.NewAllowAllAuthZ()
			middleware := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, provider, nil), store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "bearer"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "repository"}},
				Args: map[string]any{"by": by},
			})
			var firstContext context.Context
			result, err := middleware.GraphQLFieldAuthorizer(ctx, func(next context.Context) (any, error) {
				firstContext = next
				nextVersion := *current
				nextVersion.ResourceVersion, nextVersion.Body, nextVersion.CreationActor = "2", "second", "bob"
				current = &nextVersion
				return root.Query().Repository(next, by)
			})
			require.NoError(t, err)
			first, ok := result.(*model.Repository)
			require.True(t, ok)
			require.Equal(t, "1", first.Metadata.ResourceVersion)
			require.Equal(t, "first", *first.Body)
			require.Equal(t, 1, repositoryReads)
			require.Equal(t, 1, namespaceReads)
			require.Equal(t, "repository.read", provider.Action)
			require.Equal(t, "alice", provider.Resource.OwnerSub)

			_, _, reused := security.AuthorizedRepositoryRead(firstContext, model.RepositoryBy{
				NamespacePath: &model.RepositoryNamespacePath{Namespace: "other", Name: "catalog"},
			})
			require.False(t, reused)
			otherID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Repository/01960000-0000-7000-8000-000000000142"))
			_, _, reused = security.AuthorizedRepositoryRead(firstContext, model.RepositoryBy{ID: &otherID})
			require.False(t, reused)
			sibling := graphql.WithFieldContext(firstContext, &graphql.FieldContext{
				Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "repository"}},
				Args: map[string]any{"by": by},
			})
			_, _, reused = security.AuthorizedRepositoryRead(sibling, by)
			require.False(t, reused, "even the same selector must not reuse another field's decision")
			mutation := graphql.WithFieldContext(firstContext, &graphql.FieldContext{
				Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: "updateRepository"}},
			})
			_, _, reused = security.AuthorizedRepositoryRead(mutation, by)
			require.False(t, reused)
			_, _, reused = security.AuthorizedRepositoryRead(ctx, by)
			require.False(t, reused, "field records must not leak into the parent operation context")
			canceled, cancel := context.WithCancel(firstContext)
			cancel()
			_, err = root.Query().Repository(canceled, by)
			require.ErrorIs(t, err, context.Canceled)

			result, err = middleware.GraphQLFieldAuthorizer(ctx, func(next context.Context) (any, error) {
				return root.Query().Repository(next, by)
			})
			require.NoError(t, err)
			second, ok := result.(*model.Repository)
			require.True(t, ok)
			require.Equal(t, "2", second.Metadata.ResourceVersion)
			require.Equal(t, "second", *second.Body)
			require.Equal(t, 2, repositoryReads)
			require.Equal(t, 2, namespaceReads)
			require.Equal(t, "bob", provider.Resource.OwnerSub)
			if selector == "path" {
				require.Equal(t, 2, mappingReads)
			} else {
				require.Zero(t, mappingReads)
			}

			server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
			server.AroundFields(middleware.GraphQLFieldAuthorizer)
			payload, err := json.Marshal(map[string]any{
				"query":     `query($by:RepositoryBy!){repository(by:$by){metadata{resourceVersion}}}`,
				"variables": map[string]any{"by": by},
			})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			require.JSONEq(t, `{"data":{"repository":{"metadata":{"resourceVersion":"2"}}}}`, response.Body.String())
			require.Equal(t, 3, repositoryReads, "the generated transport must reuse the authorized field record")
			require.Equal(t, 3, namespaceReads)
			if selector == "path" {
				require.Equal(t, 3, mappingReads)
			} else {
				require.Zero(t, mappingReads)
			}
		})
	}
}

// lookupRepositoryErrStore wraps a Datastore and forces LookupRepository to
// return an arbitrary, non-ErrNotFound error, simulating a genuine failure
// (e.g. a Scylla timeout) rather than a missing repository.
type lookupRepositoryErrStore struct {
	datastore.Datastore
	err error
}

func (s *lookupRepositoryErrStore) LookupRepository(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
	return nil, s.err
}

func newRepositoryNotFoundHarness(t *testing.T, store datastore.Datastore) (*resolver.Resolver, *datastore.Namespace) {
	t.Helper()
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               repositoryNotFoundTestNamespaceID,
		Name:              "repository-not-found-contract",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC),
		CreationActor:     "fixture",
		UpdateActor:       "fixture",
	}
	require.NoError(t, store.CreateNamespace(ctx, namespace))

	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:  store,
		Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	return root, namespace
}

// TestQueryRepositoryByNamespacePath_MissingRepositoryReportsNotFoundCode
// confirms the repository(by: namespacePath) query surfaces a genuinely
// missing repository as a GraphQL error carrying extensions.code
// "NOT_FOUND" — the contract gitstore-controller-manager's
// systemRepositoryExists relies on to distinguish "doesn't exist yet" from
// a hard failure (see EnsureSystemRepository, spec 046).
func TestQueryRepositoryByNamespacePath_MissingRepositoryReportsNotFoundCode(t *testing.T) {
	baseStore, err := memdb.New()
	require.NoError(t, err)
	root, namespace := newRepositoryNotFoundHarness(t, baseStore)

	_, err = root.Query().Repository(context.Background(), model.RepositoryBy{
		NamespacePath: &model.RepositoryNamespacePath{
			Namespace: namespace.Name,
			Name:      "gitstore-system",
		},
	})

	require.Error(t, err)
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr), "expected *gqlerror.Error, got %T", err)
	assert.Equal(t, "repository not found", gqlErr.Message)
	assert.Equal(t, "NOT_FOUND", gqlErr.Extensions["code"])
}

// TestQueryRepositoryByNamespacePath_GenuineLookupFailurePropagates confirms
// that a non-ErrNotFound LookupRepository failure (e.g. a datastore
// timeout) is NOT mislabeled as "repository not found" / NOT_FOUND — it
// must propagate as a distinct error so callers do not mistake a real
// failure for "safe to create".
func TestQueryRepositoryByNamespacePath_GenuineLookupFailurePropagates(t *testing.T) {
	baseStore, err := memdb.New()
	require.NoError(t, err)
	genuineErr := errors.New("datastore: connection reset")
	store := &lookupRepositoryErrStore{Datastore: baseStore, err: genuineErr}
	root, namespace := newRepositoryNotFoundHarness(t, store)

	_, err = root.Query().Repository(context.Background(), model.RepositoryBy{
		NamespacePath: &model.RepositoryNamespacePath{
			Namespace: namespace.Name,
			Name:      "gitstore-system",
		},
	})

	require.Error(t, err)
	assert.NotEqual(t, "repository not found", err.Error())
	var gqlErr *gqlerror.Error
	if errors.As(err, &gqlErr) {
		assert.NotEqual(t, "NOT_FOUND", gqlErr.Extensions["code"])
	}
}
