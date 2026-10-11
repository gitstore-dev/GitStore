// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
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

func TestCatalogBoundariesUseStoredOwnersAndNamespaceOwnerForCreate(t *testing.T) {
	store := &testutil.StubStore{
		GetNamespaceByNameFunc: func(context.Context, string) (*datastore.Namespace, error) {
			return &datastore.Namespace{Name: "acme", CreationActor: "namespace-owner"}, nil
		},
		GetCategoryTaxonomyByNameFunc: func(context.Context, string, string) (*datastore.CategoryTaxonomy, error) {
			return &datastore.CategoryTaxonomy{Namespace: "acme", Name: "category", CreationActor: "category-owner"}, nil
		},
		GetFileByNameFunc: func(context.Context, string, string) (*datastore.File, error) {
			return &datastore.File{Namespace: "acme", Name: "file", CreationActor: "file-owner"}, nil
		},
	}
	for _, tc := range []struct {
		object, field, owner string
		args                 map[string]any
	}{
		{"Mutation", "createProduct", "namespace-owner", map[string]any{"input": model.CreateProductInput{Metadata: &model.ObjectMetaInput{Name: "widget", Namespace: "acme"}}}},
		{"Mutation", "createCategory", "namespace-owner", map[string]any{"input": model.CreateCategoryInput{Metadata: &model.ObjectMetaInput{Name: "category", Namespace: "acme"}}}},
		{"Query", "category", "category-owner", map[string]any{"by": struct {
			NamespacePath *struct{ Namespace, Name string }
		}{&struct{ Namespace, Name string }{"acme", "category"}}}},
		{"Query", "file", "file-owner", map[string]any{"namespace": "acme", "name": "file"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			recorder := testutil.NewAllowAllAuthZ()
			middleware := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: tc.object, Field: graphql.CollectedField{Field: &ast.Field{Name: tc.field}}, Args: tc.args})
			_, err := middleware.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { return nil, nil })
			require.NoError(t, err)
			require.Equal(t, tc.owner, recorder.Resource.OwnerSub)
		})
	}
}

// assertAuthorizationCalls checks invocation count as well as exact canonical
// action, kind and facts; a later successful call must not hide a missing or
// incorrect earlier decision on aliased/nested/node fields.
func assertAuthorizationCalls(t *testing.T, recorder *testutil.RecordingAuthZ, expected ...testutil.AuthorizationCall) {
	t.Helper()
	require.Equal(t, expected, recorder.Calls)
}

func TestAuthorizationRecorderRetainsAllBoundaryCalls(t *testing.T) {
	recorder := testutil.NewAllowAllAuthZ()
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	first := auth.ResourceContext{Kind: "repository", Name: "catalog", OwnerSub: "alice", Attrs: map[string]any{"namespace": "acme"}}
	_, err := recorder.Authorize(t.Context(), principal, "repository.read", first)
	require.NoError(t, err)
	second := auth.ResourceContext{Kind: "product", Attrs: map[string]any{"namespace": "acme"}}
	_, err = recorder.Authorize(t.Context(), principal, "product.list", second)
	require.NoError(t, err)
	assertAuthorizationCalls(t, recorder,
		testutil.AuthorizationCall{Subject: "alice", Action: "repository.read", Resource: first},
		testutil.AuthorizationCall{Subject: "alice", Action: "product.list", Resource: second},
	)
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
	assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
	assert.Empty(t, resp.Errors[0].Path, "credential rejection must precede field execution")
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

func TestGraphQLAdditionalCatalogReadBoundaries(t *testing.T) {
	store := &testutil.StubStore{
		GetCollectionFunc: func(context.Context, string) (*datastore.Collection, error) {
			return &datastore.Collection{Name: "summer", Namespace: "acme", CreationActor: "alice"}, nil
		},
		GetCollectionByNameFunc: func(context.Context, string, string) (*datastore.Collection, error) {
			return &datastore.Collection{Name: "summer", Namespace: "acme", CreationActor: "alice"}, nil
		},
		GetProductVariantFunc: func(context.Context, string) (*datastore.ProductVariant, error) {
			return &datastore.ProductVariant{Name: "blue", Namespace: "acme", CreationActor: "alice"}, nil
		},
		GetProductVariantByNameFunc: func(context.Context, string, string) (*datastore.ProductVariant, error) {
			return &datastore.ProductVariant{Name: "blue", Namespace: "acme", CreationActor: "alice"}, nil
		},
		GetNamespaceByNameFunc: func(context.Context, string) (*datastore.Namespace, error) {
			return &datastore.Namespace{Name: "acme", CreationActor: "alice"}, nil
		},
	}
	for _, tc := range []struct {
		object, field, action, name string
		args                        map[string]any
		parent                      any
	}{
		{"Query", "collection", "collection.read", "summer", map[string]any{"by": model.CollectionBy{NamespacePath: &model.CollectionNamespacePath{Namespace: "acme", Name: "summer"}}}, nil},
		{"Query", "collections", "collection.list", "", map[string]any{"namespace": "acme"}, nil},
		{"Query", "productVariant", "productVariant.read", "blue", map[string]any{"by": model.ProductVariantBy{NamespacePath: &model.ProductVariantNamespacePath{Namespace: "acme", Name: "blue"}}}, nil},
		{"Query", "productVariants", "productVariant.list", "", map[string]any{"namespace": "acme"}, nil},
		{"Query", "node", "collection.read", "summer", map[string]any{"id": base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Collection/id"))}, nil},
		{"Query", "nodes", "productVariant.read", "blue", map[string]any{"ids": []string{base64.StdEncoding.EncodeToString([]byte("gid://GitStore/ProductVariant/id"))}}, nil},
		{"Collection", "products", "product.list", "", nil, &model.Collection{Metadata: &model.ObjectMeta{Namespace: "acme", Name: "summer"}}},
		{"Product", "productVariants", "productVariant.list", "", nil, &model.Product{Metadata: &model.ObjectMeta{Namespace: "acme", Name: "widget"}}},
	} {
		t.Run(tc.object+"."+tc.field, func(t *testing.T) {
			recorder := testutil.NewAllowAllAuthZ()
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Result: tc.parent})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: tc.object, Field: graphql.CollectedField{Field: &ast.Field{Name: tc.field}}, Args: tc.args})
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { return nil, nil })
			require.NoError(t, err)
			require.Len(t, recorder.Calls, 1)
			require.Equal(t, tc.action, recorder.Action)
			require.Equal(t, tc.name, recorder.Resource.Name)
			require.Equal(t, "acme", recorder.Resource.Attrs["namespace"])
		})
	}
}

func TestGraphQLCanonicalNamespaceBoundaries(t *testing.T) {
	ns := &datastore.Namespace{UID: "ns-id", Name: "alice", CreationActor: "alice", Tier: datastore.NamespaceTierUser}
	store := &testutil.StubStore{GetNamespaceFunc: func(context.Context, string) (*datastore.Namespace, error) { return ns, nil }, GetNamespaceByNameFunc: func(context.Context, string) (*datastore.Namespace, error) { return ns, nil }}
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/ns-id"))
	name := "alice"
	for _, tc := range []struct {
		object, field, action, name string
		args                        map[string]any
	}{
		{"Query", "namespace", "namespace.read", "alice", map[string]any{"by": model.NamespaceBy{Name: &name}}},
		{"Query", "namespaces", "namespace.list", "", nil},
		{"Query", "node", "namespace.read", "alice", map[string]any{"id": id}},
		{"Query", "nodes", "namespace.read", "alice", map[string]any{"ids": []string{id}}},
		{"Mutation", "createNamespace", "namespace.create", "alice", map[string]any{"input": model.CreateNamespaceInput{Metadata: &model.NamespaceMetadataInput{Name: name}, Spec: &model.NamespaceSpecInput{Tier: model.NamespaceTierUser}}}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			recorder := testutil.NewAllowAllAuthZ()
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: tc.object, Field: graphql.CollectedField{Field: &ast.Field{Name: tc.field}}, Args: tc.args})
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { return nil, nil })
			require.NoError(t, err)
			require.Len(t, recorder.Calls, 1)
			require.Equal(t, tc.action, recorder.Action)
			require.Equal(t, "namespace", recorder.Resource.Kind)
			require.Equal(t, tc.name, recorder.Resource.Name)
		})
	}
}

func TestGraphQLFieldAuthorizerCreateNamespaceOrganizationUsesPolicy(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

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
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "namespace.create", authz.Action)
	assert.Equal(t, "acme", authz.Resource.Name)
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
		GetNamespaceFunc: func(_ context.Context, name string) (*datastore.Namespace, error) {
			return &datastore.Namespace{Name: name, CreationActor: "alice"}, nil
		},
	}
	mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "deleteNamespace"}},
		Args: map[string]any{
			"input": model.DeleteNamespaceInput{ID: base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/acme"))},
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
	assert.Equal(t, "namespace.delete", authz.Action)
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
			tc.namespace.UID = tc.namespace.ID
			authz := testutil.NewDenyAllAuthZ(t)
			registry := auth.NewProviderRegistry(nil, authz, nil)
			store := &testutil.StubStore{
				GetNamespaceFunc: func(_ context.Context, _ string) (*datastore.Namespace, error) {
					return tc.namespace, nil
				},
			}
			mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "mallory", AuthMethod: "static-users"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: "Mutation",
				Field:  graphql.CollectedField{Field: &ast.Field{Name: "deleteNamespace"}},
				Args: map[string]any{
					"input": model.DeleteNamespaceInput{ID: base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/" + tc.namespace.UID))},
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
	authorized.UID = authorized.ID
	store := &testutil.StubStore{
		GetNamespaceFunc: func(_ context.Context, _ string) (*datastore.Namespace, error) {
			return authorized, nil
		},
	}
	mw := NewAuthorizeWithStore(registry, store, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "deleteNamespace"}},
		Args: map[string]any{
			"input": model.DeleteNamespaceInput{ID: base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/acme"))},
		},
	})

	_, err := mw.GraphQLFieldAuthorizer(ctx, func(nextCtx context.Context) (any, error) {
		got, ok := AuthorizedNamespaceForDeletion(nextCtx)
		require.True(t, ok)
		assert.Same(t, authorized, got)
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "namespace.delete", authz.Action)
}

func TestGraphQLFieldAuthorizerUpdateCategoryStatusUsesPolicy(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-users"})
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
	assert.Equal(t, "categoryTaxonomy.status.write", authz.Action)
}

func TestGraphQLFieldAuthorizerCompleteNamespaceDeletionUsesControllerPolicy(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "untrusted", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "completeNamespaceDeletion"}},
		Args:   map[string]any{"input": model.CompleteNamespaceDeletionInput{ID: "Z2lkOi8vR2l0U3RvcmUvTmFtZXNwYWNlL25hbWVzcGFjZS0x", Name: "obsolete", ResourceVersion: "7"}},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "namespace.purge", authz.Action)
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, "FORBIDDEN", graphErr.Extensions["code"])
}

func TestCompletionMutationsRequireIndependentPurgeGrant(t *testing.T) {
	for _, tc := range []struct {
		field, kind string
		input       any
	}{
		{"completeNamespaceDeletion", "namespace", model.CompleteNamespaceDeletionInput{Name: "obsolete", ResourceVersion: "7"}},
		{"completeRepositoryDeletion", "repository", model.CompleteRepositoryDeletionInput{Namespace: "acme", Name: "obsolete", ResourceVersion: "7"}},
		{"completeCategoryDeletion", "categoryTaxonomy", model.CompleteCategoryDeletionInput{Namespace: "acme", Name: "obsolete", ResourceVersion: "7"}},
		{"completeProductDeletion", "product", model.CompleteProductDeletionInput{Namespace: "acme", Name: "obsolete", ResourceVersion: "7"}},
	} {
		for _, grant := range []string{"delete", "status.write", "purge"} {
			t.Run(tc.field+"/"+grant, func(t *testing.T) {
				resource, verb := tc.kind, grant
				if grant == "status.write" {
					resource, verb = tc.kind+"/status", "write"
				}
				path := filepath.Join(t.TempDir(), "policy.yaml")
				policy := fmt.Sprintf("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  finalizer:\n    rules: [{resources: [%s], verbs: [%s]}]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: finalizer}\n    subjects: [{kind: User, name: alice}]\n", resource, verb)
				require.NoError(t, os.WriteFile(path, []byte(policy), 0600))
				provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
				require.NoError(t, err)
				recorder := testutil.NewRecordingAuthZ(provider)
				store := &testutil.StubStore{GetProductByNameFunc: func(context.Context, string, string) (*datastore.Product, error) {
					return &datastore.Product{Namespace: "acme", Name: "obsolete", CreationActor: "alice"}, nil
				}}
				mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
				ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
				ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: tc.field, Alias: "finish"}}, Args: map[string]any{"input": tc.input}})
				called := false
				_, err = mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
				if grant == "purge" {
					require.NoError(t, err)
				} else {
					var graphErr *gqlerror.Error
					require.ErrorAs(t, err, &graphErr)
					require.Equal(t, "FORBIDDEN", graphErr.Extensions["code"])
				}
				require.Equal(t, grant == "purge", called)
				require.Len(t, recorder.Calls, 1)
				require.Equal(t, tc.kind+".purge", recorder.Action)
				require.Equal(t, tc.kind, recorder.Resource.Kind)
				require.Equal(t, "obsolete", recorder.Resource.Name)
				if tc.kind != "namespace" {
					require.Equal(t, "acme", recorder.Resource.Attrs["namespace"])
				}
			})
		}
	}
}

func TestGraphQLFieldAuthorizerCompleteRepositoryDeletionUsesControllerPolicy(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "untrusted", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "completeRepositoryDeletion"}},
		Args:   map[string]any{"input": model.CompleteRepositoryDeletionInput{ID: "Z2lkOi8vR2l0U3RvcmUvUmVwb3NpdG9yeS9yZXBvLTE=", Namespace: "acme", Name: "catalog", ResourceVersion: "9"}},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "repository.purge", authz.Action)
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, "FORBIDDEN", graphErr.Extensions["code"])
}

func TestGraphQLFieldAuthorizerProvisionRepositoryStorageUsesControllerPolicy(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "untrusted", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "provisionRepositoryStorage"}},
		Args:   map[string]any{"input": model.ProvisionRepositoryStorageInput{Namespace: "acme", Name: "catalog"}},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "repository.status.write", authz.Action)
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, "FORBIDDEN", graphErr.Extensions["code"])
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
	assert.Equal(t, "categoryTaxonomy.delete", authz.Action)
	assert.Equal(t, "phones", authz.Resource.Name)
	assert.Equal(t, "alice", authz.Resource.OwnerSub)
	assert.Equal(t, "shop", authz.Resource.Attrs["namespace"])
	assert.Equal(t, "catalog-repo", authz.Resource.Attrs["repositoryID"])
}

func TestGraphQLFieldAuthorizerUpdateResourceStatusUsesLowerCamelKindAction(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-users"})
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
	require.Error(t, err)
	assert.Empty(t, authz.Calls, "unknown kinds must not be guessed into actions")
}

func TestGraphQLFieldAuthorizerConfinesAssertionPrincipalToTokenIssuance(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	principal := &auth.Principal{
		Subject:    datastore.ServiceAccountSubject("manager"),
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
		{field: "createServiceAccount", action: "serviceAccount.create"},
		{field: "rotateServiceAccountKey", action: "serviceAccount.key.rotate"},
		{field: "deleteServiceAccount", action: "serviceAccount.delete"},
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
