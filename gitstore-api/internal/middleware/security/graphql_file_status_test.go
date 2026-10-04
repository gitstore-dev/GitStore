// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"errors"
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
// writes: GraphQLFieldAuthorizer's "updateResourceStatus" case derives
// action = lowerCamelFirst(kind) + ".status.write". For kind "File" that
// action string is exactly "file.status.write" — no "own"/"any" suffix
// exists for this generic kind-agnostic path (unlike namespace.delete.*,
// which does have that split). This replaces an earlier version of this
// coverage that exercised invented action strings
// ("file.status.write.own"/".any") against the rbac-local policy engine in
// isolation, which never matched what this middleware actually derives or
// calls (spec 051 T041).
func TestGraphQLFieldAuthorizerUpdateResourceStatusFileUsesFileStatusWriteAction(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-users", Roles: []string{"controller"}})
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
	assert.Equal(t, "File", authz.Resource.Kind)
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
	assert.Equal(t, "File", authz.Resource.Kind)
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
			require.Equal(t, "File", authz.Resource.Kind)
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
