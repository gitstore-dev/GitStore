// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"errors"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
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

func TestGraphQLFieldAuthorizerLeavesExistingNonFileWatchesUnchanged(t *testing.T) {
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
	require.NoError(t, err)
	assert.True(t, called)
	assert.Empty(t, authz.Action, "existing Product watch policy must not change in a File-scoped feature")
}
