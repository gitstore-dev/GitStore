// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"net/http"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
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

func TestGraphQLAuthorizerAllowsLoginMutationWithRootTypenameForAnonymous(t *testing.T) {
	registry, _ := newTestRegistry(t)
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "login"},
				&ast.Field{Name: "__typename"},
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

func TestGraphQLAuthorizerRequiresAuthForMutationInNamedFragment(t *testing.T) {
	fragment := &ast.FragmentDefinition{
		Name:          "MutationFields",
		TypeCondition: "Mutation",
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "createRepository"},
		},
	}
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Doc: &ast.QueryDocument{
			Fragments: ast.FragmentDefinitionList{fragment},
		},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.FragmentSpread{Name: fragment.Name},
			},
		},
	}

	assertAnonymousOperationRejected(t, opCtx)
}

func TestGraphQLAuthorizerRequiresAuthForMutationInInlineFragment(t *testing.T) {
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.InlineFragment{
					TypeCondition: "Mutation",
					SelectionSet: ast.SelectionSet{
						&ast.Field{Name: "createRepository"},
					},
				},
			},
		},
	}

	assertAnonymousOperationRejected(t, opCtx)
}

func TestGraphQLAuthorizerAllowsLoginViaFragmentSpreadForAnonymous(t *testing.T) {
	fragment := &ast.FragmentDefinition{
		Name:          "Login",
		TypeCondition: "Mutation",
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "login"},
		},
	}
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Doc: &ast.QueryDocument{
			Fragments: ast.FragmentDefinitionList{fragment},
		},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.FragmentSpread{Name: fragment.Name},
			},
		},
	}

	registry, _ := newTestRegistry(t)
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

func TestGraphQLAuthorizerRequiresAuthForSecondRootFieldAfterLogin(t *testing.T) {
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "login"},
				&ast.Field{Name: "createNamespace"},
			},
		},
	}

	assertAnonymousOperationRejected(t, opCtx)
}

func TestGraphQLAuthorizerHonorsSkipDirectiveOnMutationField(t *testing.T) {
	opCtx := &graphql.OperationContext{
		Headers:   http.Header{},
		Variables: map[string]any{"skipCreate": true},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "login"},
				&ast.Field{
					Name: "createNamespace",
					Directives: ast.DirectiveList{
						&ast.Directive{
							Name: "skip",
							Arguments: ast.ArgumentList{
								{Name: "if", Value: &ast.Value{Kind: ast.Variable, Raw: "skipCreate"}},
							},
						},
					},
				},
			},
		},
	}

	registry, _ := newTestRegistry(t)
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

func TestGraphQLAuthorizerRequiresAuthWhenSkipConditionFalse(t *testing.T) {
	opCtx := &graphql.OperationContext{
		Headers:   http.Header{},
		Variables: map[string]any{"skipCreate": false},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "login"},
				&ast.Field{
					Name: "createNamespace",
					Directives: ast.DirectiveList{
						&ast.Directive{
							Name: "skip",
							Arguments: ast.ArgumentList{
								{Name: "if", Value: &ast.Value{Kind: ast.Variable, Raw: "skipCreate"}},
							},
						},
					},
				},
			},
		},
	}

	assertAnonymousOperationRejected(t, opCtx)
}

func assertAnonymousOperationRejected(t *testing.T, opCtx *graphql.OperationContext) {
	t.Helper()
	registry, _ := newTestRegistry(t)
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

type stubAuthZProvider struct {
	decision auth.Decision
	err      error
	action   string
}

func (s *stubAuthZProvider) Name() string { return "stub-authz" }
func (s *stubAuthZProvider) Authorize(_ context.Context, _ *auth.Principal, action string, _ auth.ResourceContext) (auth.Decision, error) {
	s.action = action
	return s.decision, s.err
}

func TestGraphQLFieldAuthorizerCreateNamespaceOrganizationUsesPolicy(t *testing.T) {
	authz := &stubAuthZProvider{decision: auth.Allow("stub-authz", "allowed")}
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "bob", AuthMethod: "static-admin", Roles: []string{"developer"}})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "createNamespace"}},
		Args: map[string]any{
			"input": model.CreateNamespaceInput{
				Identifier: "acme",
				Tier:       model.NamespaceTierOrganization,
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
	assert.Equal(t, "namespace.create.organization", authz.action)
}

func TestGraphQLFieldAuthorizerCreateNamespaceOrganizationFailsWithoutAuthZ(t *testing.T) {
	registry := auth.NewProviderRegistry(nil, nil, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "bob", AuthMethod: "static-admin"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "createNamespace"}},
		Args: map[string]any{
			"input": model.CreateNamespaceInput{
				Identifier: "acme",
				Tier:       model.NamespaceTierOrganization,
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
	assert.Contains(t, err.Error(), "authorization service unavailable")
}

func TestGraphQLFieldAuthorizerDeleteNamespaceDenyFromPolicy(t *testing.T) {
	authz := &stubAuthZProvider{decision: auth.Deny("stub-authz", "no access")}
	registry := auth.NewProviderRegistry(nil, authz, nil)
	store := &testutil.StubStore{
		GetNamespaceByIdentifierFunc: func(_ context.Context, identifier string) (*datastore.Namespace, error) {
			return &datastore.Namespace{Identifier: identifier, CreatedBy: "alice"}, nil
		},
	}
	mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "bob", AuthMethod: "static-admin"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "deleteNamespace"}},
		Args: map[string]any{
			"input": model.DeleteNamespaceInput{Identifier: "acme"},
		},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Contains(t, err.Error(), "permission denied")
	assert.Equal(t, "namespace.delete.any", authz.action)
}

func TestGraphQLFieldAuthorizerDeleteNamespacePassesAuthorizedRecord(t *testing.T) {
	authz := &stubAuthZProvider{decision: auth.Allow("stub-authz", "allowed")}
	registry := auth.NewProviderRegistry(nil, authz, nil)
	authorized := &datastore.Namespace{ID: "namespace-id", Identifier: "acme", CreatedBy: "alice"}
	store := &testutil.StubStore{
		GetNamespaceByIdentifierFunc: func(_ context.Context, _ string) (*datastore.Namespace, error) {
			return authorized, nil
		},
	}
	mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "alice", AuthMethod: "static-admin"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "deleteNamespace"}},
		Args: map[string]any{
			"input": model.DeleteNamespaceInput{Identifier: "acme"},
		},
	})

	_, err := mw.GraphQLFieldAuthorizer(ctx, func(nextCtx context.Context) (any, error) {
		got, ok := AuthorizedNamespaceForDeletion(nextCtx)
		require.True(t, ok)
		assert.Same(t, authorized, got)
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "namespace.delete.own", authz.action)
}
