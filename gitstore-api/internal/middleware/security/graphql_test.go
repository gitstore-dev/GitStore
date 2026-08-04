// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"net/http"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
)

func TestGraphQLAuthenticatorValidBearerInjectsPrincipal(t *testing.T) {
	registry, staticAdmin := newTestRegistry(t)
	token, _, err := staticAdmin.IssueSession(t.Context(), "admin")
	require.NoError(t, err)

	opCtx := &graphql.OperationContext{
		Headers: http.Header{"Authorization": []string{"Bearer " + token}},
		Operation: &ast.OperationDefinition{
			Operation: ast.Query,
		},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)
	ctx = ContextWithRemoteAddr(ctx, "127.0.0.1")

	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	var principal *auth.Principal
	next := func(nextCtx context.Context) graphql.ResponseHandler {
		principal = auth.PrincipalFromContext(nextCtx)
		return graphql.OneShot(&graphql.Response{Data: []byte(`{"ok":true}`)})
	}

	resp := authMiddleware.GraphQLAuthenticator(ctx, next)(ctx)
	require.NotNil(t, resp)
	require.Nil(t, resp.Errors)
	require.NotNil(t, principal)
	assert.Equal(t, "admin", principal.Subject)
	assert.Equal(t, "static-admin", principal.AuthMethod)
}

func TestGraphQLAuthenticatorInvalidBearerReturnsGraphQLError(t *testing.T) {
	registry, _ := newTestRegistry(t)
	opCtx := &graphql.OperationContext{
		Headers: http.Header{"Authorization": []string{"Bearer invalid-token"}},
		Operation: &ast.OperationDefinition{
			Operation: ast.Query,
		},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	called := false
	next := func(context.Context) graphql.ResponseHandler {
		called = true
		return graphql.OneShot(&graphql.Response{Data: []byte(`{"ok":true}`)})
	}

	resp := authMiddleware.GraphQLAuthenticator(ctx, next)(ctx)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Errors)
	assert.False(t, called)
	assert.Contains(t, resp.Errors[0].Message, "invalid or expired credentials")
}

func TestGraphQLAuthorizerRequiresAuthForNonLoginMutation(t *testing.T) {
	registry, _ := newTestRegistry(t)
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "createNamespace"},
			},
		},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	authn := NewAuthenticate(registry, zap.NewNop())
	authz := NewAuthorize(registry, zap.NewNop())
	called := false
	final := func(context.Context) graphql.ResponseHandler {
		called = true
		return graphql.OneShot(&graphql.Response{Data: []byte(`{"ok":true}`)})
	}

	resp := authn.GraphQLAuthenticator(ctx, func(inner context.Context) graphql.ResponseHandler {
		return authz.GraphQLAuthorizer(inner, final)
	})(ctx)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Errors)
	assert.False(t, called)
	assert.Contains(t, resp.Errors[0].Message, "authentication required")
}

func TestGraphQLAuthorizerAllowsLoginMutationForAnonymous(t *testing.T) {
	registry, _ := newTestRegistry(t)
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "login"},
			},
		},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	authn := NewAuthenticate(registry, zap.NewNop())
	authz := NewAuthorize(registry, zap.NewNop())
	called := false
	final := func(context.Context) graphql.ResponseHandler {
		called = true
		return graphql.OneShot(&graphql.Response{Data: []byte(`{"ok":true}`)})
	}

	resp := authn.GraphQLAuthenticator(ctx, func(inner context.Context) graphql.ResponseHandler {
		return authz.GraphQLAuthorizer(inner, final)
	})(ctx)
	require.NotNil(t, resp)
	require.Nil(t, resp.Errors)
	assert.True(t, called)
}
