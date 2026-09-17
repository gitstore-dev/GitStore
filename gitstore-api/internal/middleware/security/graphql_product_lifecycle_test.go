// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"errors"
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

func TestProductWatchAuthorizationRunsBeforeCursorHandling(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		args  map[string]any
	}{
		{name: "typed", field: "watchProducts", args: map[string]any{"namespace": "acme", "resourceVersion": "revealing-invalid-cursor"}},
		{name: "generic", field: "watchResources", args: map[string]any{"kind": "Product", "namespace": "acme", "resourceVersion": "revealing-invalid-cursor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authz := testutil.NewDenyAllAuthZ(t)
			registry := auth.NewProviderRegistry(nil, authz, nil)
			mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "denied", AuthMethod: "bearer"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Subscription", Field: graphql.CollectedField{Field: &ast.Field{Name: tc.field}}, Args: tc.args})

			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
				called = true
				return nil, errors.New("cursor parser was reached")
			})
			require.Error(t, err)
			assert.False(t, called)
			assert.Equal(t, "product.watch", authz.Action)
			assert.Equal(t, "Product", authz.Resource.Kind)
			assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
		})
	}
}

func TestProductMutationAuthorizationMatrix(t *testing.T) {
	productID := "Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC9wcm9kLXVpZA=="
	metadata := &model.MetadataInput{Namespace: "acme", Name: "widget"}
	store := &testutil.StubStore{
		GetProductFunc: func(context.Context, string) (*datastore.Product, error) {
			return &datastore.Product{UID: "prod-uid", Name: "widget", Namespace: "acme", CreationActor: "author"}, nil
		},
		GetProductByNameFunc: func(context.Context, string, string) (*datastore.Product, error) {
			return &datastore.Product{UID: "prod-uid", Name: "widget", Namespace: "acme", CreationActor: "author"}, nil
		},
	}
	for _, tc := range []struct {
		field  string
		args   map[string]any
		action string
	}{
		{"createProduct", map[string]any{"input": model.CreateProductInput{Metadata: metadata}}, "product.create"},
		{"updateProduct", map[string]any{"input": model.UpdateProductInput{Metadata: metadata}}, "product.update"},
		{"deleteProduct", map[string]any{"input": model.DeleteProductInput{ID: &productID}}, "product.delete"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			authz := testutil.NewDenyAllAuthZ(t)
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "denied", AuthMethod: "bearer"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: tc.field}}, Args: tc.args})
			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
			require.Error(t, err)
			assert.False(t, called)
			assert.Equal(t, tc.action, authz.Action)
			assert.Equal(t, "Product", authz.Resource.Kind)
			assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
		})
	}
}

func TestProductReadAuthorizationRunsBeforeResolver(t *testing.T) {
	store := &testutil.StubStore{GetProductByNameFunc: func(context.Context, string, string) (*datastore.Product, error) {
		return &datastore.Product{UID: "prod-uid", Name: "widget", Namespace: "acme", CreationActor: "author"}, nil
	}}
	authz := testutil.NewDenyAllAuthZ(t)
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
	by := struct {
		NamespacePath *struct{ Namespace, Name string }
	}{NamespacePath: &struct{ Namespace, Name string }{Namespace: "acme", Name: "widget"}}
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "denied", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "product"}}, Args: map[string]any{"by": by}})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "product.read", authz.Action)
	assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
}

func TestProductListAuthorizationDoesNotDiscloseCrossNamespaceExistence(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "other-namespace", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "products"}}, Args: map[string]any{"namespace": "private"}})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
	require.Error(t, err)
	assert.False(t, called, "the list resolver must not reveal whether private Products exist")
	assert.Equal(t, "product.read", authz.Action)
	assert.Equal(t, "private", authz.Resource.Attrs["namespace"])
}

func TestProductNodeAuthorizationRunsBeforeResolver(t *testing.T) {
	productID := "Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC9wcm9kLXVpZA=="
	store := &testutil.StubStore{GetProductFunc: func(context.Context, string) (*datastore.Product, error) {
		return &datastore.Product{UID: "prod-uid", Name: "widget", Namespace: "acme", CreationActor: "author"}, nil
	}}
	authz := testutil.NewDenyAllAuthZ(t)
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "denied", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "node"}}, Args: map[string]any{"id": productID}})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "product.read", authz.Action)
}
