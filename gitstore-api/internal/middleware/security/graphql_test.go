// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
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
	assert.Equal(t, "static-users", principal.AuthMethod)
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

func TestGraphQLAuthorizerAllowsRefreshTokenMutationForAnonymous(t *testing.T) {
	registry, _ := newTestRegistry(t)
	opCtx := &graphql.OperationContext{
		Headers: http.Header{},
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "refreshToken"},
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

func TestGraphQLResponseAuthorizerPreservesPayloadForCompletedAndIncompleteDecisions(t *testing.T) {
	mw := NewAuthorize(nil, zap.NewNop())
	for _, test := range []struct {
		name      string
		completed bool
	}{
		{name: "completed protected field", completed: true},
		{name: "incomplete protected field", completed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := &authorizationLedger{}
			finish := ledger.begin()
			if test.completed {
				finish(true)
			}
			ctx := context.WithValue(context.Background(), authorizationLedgerContextKey{}, ledger)
			expected := &graphql.Response{Data: []byte(`{"repository":{"name":"catalog"}}`), Errors: gqlerror.List{gqlerror.Errorf("partial field error")}}
			actual := mw.GraphQLResponseAuthorizer(ctx, func(context.Context) *graphql.Response { return expected })
			assert.Same(t, expected, actual)
			assert.Equal(t, expected.Data, actual.Data)
			assert.Equal(t, expected.Errors, actual.Errors)
		})
	}
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

func TestGraphQLFieldAuthorizerCreateNamespaceOrganizationUsesPolicy(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "bob", AuthMethod: "static-users", Roles: []string{"developer"}})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "createNamespace"}},
		Args: map[string]any{
			"input": model.CreateNamespaceInput{
				APIVersion: "gitstore.dev/v1beta1",
				Kind:       "Namespace",
				Metadata:   &model.NamespaceMetadataInput{Name: "acme"},
				Spec:       &model.NamespaceSpecInput{Tier: model.NamespaceTierOrganization},
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
	assert.Equal(t, "namespace.create.organization", authz.Action)
}

func TestGraphQLFieldAuthorizerCreateNamespaceOrganizationFailsWithoutAuthZ(t *testing.T) {
	registry := auth.NewProviderRegistry(nil, nil, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "createNamespace"}},
		Args: map[string]any{
			"input": model.CreateNamespaceInput{
				APIVersion: "gitstore.dev/v1beta1",
				Kind:       "Namespace",
				Metadata:   &model.NamespaceMetadataInput{Name: "acme"},
				Spec:       &model.NamespaceSpecInput{Tier: model.NamespaceTierOrganization},
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
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)
	store := &testutil.StubStore{
		GetNamespaceByNameFunc: func(_ context.Context, name string) (*datastore.Namespace, error) {
			return &datastore.Namespace{Name: name, CreationActor: "alice"}, nil
		},
	}
	mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
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
	assert.Equal(t, "namespace.delete.any", authz.Action)
}

func TestGraphQLFieldAuthorizerDeleteNamespaceDenialHidesDeletionDetails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace *datastore.Namespace
	}{
		{
			name: "bootstrap and non-empty",
			namespace: &datastore.Namespace{
				ID:            "bootstrap-id",
				Name:          "gitstore-system",
				CreationActor: "system",
			},
		},
		{
			name: "already terminating",
			namespace: &datastore.Namespace{
				ID:                "terminating-id",
				Name:              "terminating",
				CreationActor:     "alice",
				DeletionTimestamp: func() *time.Time { now := time.Now().UTC(); return &now }(),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authz := testutil.NewDenyAllAuthZ(t)
			registry := auth.NewProviderRegistry(nil, authz, nil)
			store := &testutil.StubStore{
				GetNamespaceByNameFunc: func(_ context.Context, _ string) (*datastore.Namespace, error) {
					return tc.namespace, nil
				},
			}
			mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "mallory", AuthMethod: "static-users"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: "Mutation",
				Field:  graphql.CollectedField{Field: &ast.Field{Name: "deleteNamespace"}},
				Args: map[string]any{
					"input": model.DeleteNamespaceInput{Identifier: tc.namespace.Name},
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
			assert.NotContains(t, err.Error(), "BOOTSTRAP_NAMESPACE")
			assert.NotContains(t, err.Error(), "NAMESPACE_NOT_EMPTY")
			assert.NotContains(t, err.Error(), "ALREADY_TERMINATING")
		})
	}
}

func TestGraphQLFieldAuthorizerDeleteNamespacePassesAuthorizedRecord(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)
	authorized := &datastore.Namespace{ID: "namespace-id", Name: "acme", CreationActor: "alice"}
	store := &testutil.StubStore{
		GetNamespaceByNameFunc: func(_ context.Context, _ string) (*datastore.Namespace, error) {
			return authorized, nil
		},
	}
	mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
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
	assert.Equal(t, "namespace.delete.own", authz.Action)
}

func TestGraphQLFieldAuthorizerUpdateCategoryStatusUsesPolicy(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-users", Roles: []string{"controller"}})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "updateCategoryStatus"}},
		Args: map[string]any{
			"input": model.UpdateCategoryStatusInput{
				Name:            "electronics",
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
	assert.Equal(t, "category.status.write", authz.Action)
}

func TestGraphQLFieldAuthorizerUpdateCategoryStatusDenyReturnsForbidden(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "updateCategoryStatus"}},
		Args: map[string]any{
			"input": model.UpdateCategoryStatusInput{
				Name:            "electronics",
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
}

func TestGraphQLFieldAuthorizerUpdateProductStatusUsesPolicy(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: "updateProductStatus"}}, Args: map[string]any{
		"input": model.UpdateProductStatusInput{Name: "phone", Namespace: "shop", ResourceVersion: "7"},
	}})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return "ok", nil })
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "product.status.write", authz.Action)
}

func TestGraphQLFieldAuthorizerDeleteCategoryUsesPersistedScope(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	store := &testutil.StubStore{GetCategoryTaxonomyFunc: func(_ context.Context, uid string) (*datastore.CategoryTaxonomy, error) {
		assert.Equal(t, "category-uid", uid)
		return &datastore.CategoryTaxonomy{UID: uid, Name: "phones", Namespace: "shop", RepositoryID: "catalog-repo", CreationActor: "alice"}, nil
	}}
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Category/category-uid"))
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: "deleteCategory"}}, Args: map[string]any{
		"input": model.DeleteCategoryInput{ID: id},
	}})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return "ok", nil })
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "category.delete", authz.Action)
	assert.Equal(t, "phones", authz.Resource.Name)
	assert.Equal(t, "alice", authz.Resource.OwnerSub)
	assert.Equal(t, "shop", authz.Resource.Attrs["namespace"])
	assert.Equal(t, "catalog-repo", authz.Resource.Attrs["repositoryID"])
}

func TestGraphQLFieldAuthorizerUpdateResourceStatusUsesLowerCamelKindAction(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-users", Roles: []string{"controller"}})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "updateResourceStatus"}},
		Args: map[string]any{
			"input": model.UpdateResourceStatusInput{
				Kind:            "BackfillJob",
				Name:            "job-1",
				Namespace:       "acme-store",
				ResourceVersion: "1",
			},
		},
	})

	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "backfillJob.status.write", authz.Action)
}

func TestGraphQLFieldAuthorizerConfinesAssertionPrincipalToTokenIssuance(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	principal := &auth.Principal{
		Subject:    datastore.ServiceAccountSubject("controllers", "manager"),
		AuthMethod: "serviceaccount-assertion",
	}

	for _, root := range []struct {
		object string
		field  string
	}{
		{object: "Query", field: "namespaces"},
		{object: "Mutation", field: "createRepository"},
		{object: "Subscription", field: "watchNamespaces"},
	} {
		t.Run(root.object+"_"+root.field, func(t *testing.T) {
			ctx := auth.ContextWithPrincipal(context.Background(), principal)
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: root.object,
				Field:  graphql.CollectedField{Field: &ast.Field{Name: root.field}},
			})
			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
				called = true
				return "ok", nil
			})
			require.Error(t, err)
			assert.False(t, called)
			assert.Contains(t, err.Error(), "may only issue access tokens")
		})
	}
}

func TestGraphQLFieldAuthorizerRequiresAssertionForTokenIssuance(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:    "admin",
		AuthMethod: "static-admin",
	})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "issueServiceAccountToken"}},
		Args: map[string]any{"input": map[string]any{"metadata": map[string]any{
			"namespace": "controllers",
			"name":      "manager",
		}}},
	})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Contains(t, err.Error(), "assertion authentication is required")
}

func TestGraphQLFieldAuthorizerUsesPolicyForServiceAccountMutations(t *testing.T) {
	for _, test := range []struct {
		field  string
		action string
	}{
		{field: "createServiceAccount", action: "serviceaccount.create"},
		{field: "rotateServiceAccountKey", action: "serviceaccount.key.rotate"},
		{field: "deleteServiceAccount", action: "serviceaccount.delete"},
	} {
		t.Run(test.field, func(t *testing.T) {
			authz := testutil.NewDenyAllAuthZ(t)
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "admin", AuthMethod: "static-admin"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: "Mutation",
				Field:  graphql.CollectedField{Field: &ast.Field{Name: test.field}},
			})
			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
				called = true
				return "ok", nil
			})
			require.Error(t, err)
			assert.False(t, called)
			assert.Equal(t, test.action, authz.Action)
		})
	}
}
