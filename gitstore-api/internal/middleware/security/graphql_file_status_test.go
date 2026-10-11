// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// TestGraphQLFieldAuthorizerUpdateResourceStatusFileUsesFileStatusWriteAction
// exercises the REAL production authorization boundary for File status
// writes: GraphQLFieldAuthorizer resolves the exact registered status action.
// For kind "File" that action is "file.status.write", with no ownership suffix.
// This replaces an earlier version of this
// coverage that exercised invented action strings
// ("file.status.write.own"/".any") against the rbac-local policy engine in
// isolation, which never matched what this middleware actually derives or
// calls (spec 051 T041).
func TestGraphQLFieldAuthorizerUpdateResourceStatusFileUsesFileStatusWriteAction(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "updateResourceStatus"}},
		Args: map[string]any{
			"input": model.UpdateResourceStatusInput{
				Kind:            "File",
				Name:            "hero",
				Namespace:       "acme-store",
				ResourceVersion: "1",
			},
		},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "file.status.write", authz.Action)
	assert.Equal(t, "file", authz.Resource.Kind)
	assert.Equal(t, "hero", authz.Resource.Name)
}

// TestGraphQLFieldAuthorizerUpdateResourceStatusFileDenyReturnsForbidden
// proves the real boundary actually blocks the resolver: a policy denial
// for "file.status.write" must stop updateResourceStatus from ever reaching
// the File status-write resolver, surfaced as a FORBIDDEN GraphQL error
// (spec 051 T041).
func TestGraphQLFieldAuthorizerUpdateResourceStatusFileDenyReturnsForbidden(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "updateResourceStatus"}},
		Args: map[string]any{
			"input": model.UpdateResourceStatusInput{
				Kind:            "File",
				Name:            "hero",
				Namespace:       "acme-store",
				ResourceVersion: "1",
			},
		},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
	assert.Equal(t, "file.status.write", authz.Action)
}

// TestGraphQLFieldAuthorizerRejectsUnauthorizedFileWatch proves the
// subscription field cannot reach its resolver unless the caller has the
// namespace-scoped file.watch permission.
func TestGraphQLFieldAuthorizerRejectsUnauthorizedFileWatch(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Anonymous())
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchFiles"}},
		Args:   map[string]any{"namespace": "acme-store"},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "file.watch", authz.Action)
	assert.Equal(t, "file", authz.Resource.Kind)
	assert.Equal(t, "acme-store", authz.Resource.Attrs["namespace"])
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
}

func TestGraphQLFieldAuthorizerAuthorizesGenericFileWatch(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchResources"}},
		Args:   map[string]any{"kind": "File", "namespace": "acme-store"},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "file.watch", authz.Action)
	assert.Equal(t, "acme-store", authz.Resource.Attrs["namespace"])
}

func TestFileReadsAuthorizeLookupAndRelayNodesBeforeResolver(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	file := &datastore.File{UID: "00000000-0000-0000-0000-000000000123", Namespace: "private", Name: "hero"}
	require.NoError(t, store.CreateFile(t.Context(), file))
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/File/" + file.UID))
	for _, field := range []string{"file", "node", "nodes"} {
		t.Run(field, func(t *testing.T) {
			authz := testutil.NewDenyAllAuthZ(t)
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
			args := map[string]any{"namespace": "private", "name": "hero", "id": id, "ids": []string{id}}
			ctx := graphql.WithFieldContext(t.Context(), &graphql.FieldContext{
				Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}}, Args: args,
			})
			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
			require.Error(t, err)
			require.False(t, called)
			require.Equal(t, "file.read", authz.Action)
			require.Equal(t, "file", authz.Resource.Kind)
			require.Equal(t, "private", authz.Resource.Attrs["namespace"])
		})
	}
}

func TestGraphQLFieldAuthorizerAuthorizesProductWatches(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := graphql.WithFieldContext(context.Background(), &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchProducts"}},
		Args:   map[string]any{"namespace": "acme-store"},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "product.watch", authz.Action)
}

func TestCanonicalWatchAndStatusBoundaryMatrix(t *testing.T) {
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	for _, tc := range []struct{ kind, canonical, watch, status string }{
		{"Namespace", "namespace", "watchNamespaces", "updateNamespaceStatus"},
		{"Repository", "repository", "watchRepositories", "updateRepositoryStatus"},
		{"CategoryTaxonomy", "categoryTaxonomy", "watchCategories", "updateCategoryStatus"},
		{"Product", "product", "watchProducts", "updateProductStatus"},
		{"File", "file", "watchFiles", ""},
	} {
		for _, field := range []string{tc.watch, "watchResources", tc.status, "updateResourceStatus"} {
			if field == "" || (tc.kind == "Product" && field == "updateResourceStatus") {
				continue
			}
			for _, allowed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/allow=%t", tc.kind, field, allowed), func(t *testing.T) {
					recorder := testutil.NewAllowAllAuthZ()
					if !allowed {
						recorder = testutil.NewDenyAllAuthZ(t)
					}
					mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), &testutil.StubStore{}, zap.NewNop())
					object, verb := "Subscription", "watch"
					args := map[string]any{"kind": tc.kind, "namespace": "acme"}
					want := auth.ResourceContext{Kind: tc.canonical, Attrs: map[string]any{}}
					if tc.kind != "Namespace" {
						want.Attrs["namespace"] = "acme"
					}
					if field == tc.status || field == "updateResourceStatus" {
						object, verb = "Mutation", "status.write"
						args = map[string]any{"input": model.UpdateResourceStatusInput{Kind: tc.kind, Namespace: "acme", Name: "item"}}
						want.Name = "item"
						if tc.kind == "Namespace" && field != "updateResourceStatus" {
							want.Attrs = nil
						} else {
							want.Attrs["namespace"] = "acme"
						}
					}
					ctx := auth.ContextWithPrincipal(t.Context(), principal)
					ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: object, Field: graphql.CollectedField{Field: &ast.Field{Name: field, Alias: "aliased"}}, Args: args})
					called := false
					_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
					require.Equal(t, allowed, called)
					if allowed {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					assertAuthorizationCalls(t, recorder, testutil.AuthorizationCall{Subject: "alice", Action: tc.canonical + "." + verb, Resource: want})
				})
			}
		}
	}
}

func TestGenericStatusRejectsUnknownAndUnimplementedKindsBeforeAuthorizer(t *testing.T) {
	for _, kind := range []string{"Unknown", "ServiceAccount", "Collection", "ProductVariant", "Product", "category", ""} {
		t.Run(kind, func(t *testing.T) {
			recorder := testutil.NewAllowAllAuthZ()
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), &testutil.StubStore{}, zap.NewNop())
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "admin", AuthMethod: "static-users"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: "updateResourceStatus"}}, Args: map[string]any{"input": model.UpdateResourceStatusInput{Kind: kind}}})
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { t.Fatal("unsupported status reached resolver"); return nil, nil })
			require.Error(t, err)
			require.Empty(t, recorder.Calls)
			var gqlErr *gqlerror.Error
			require.ErrorAs(t, err, &gqlErr)
			require.Equal(t, "UNSUPPORTED_KIND", gqlErr.Extensions["code"])
		})
	}
}
