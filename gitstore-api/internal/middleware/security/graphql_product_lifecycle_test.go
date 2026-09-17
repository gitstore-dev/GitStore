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
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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

// TestProductAuthorizationCapabilitiesAreAttributedAcrossPrincipalProviders
// keeps Product lifecycle authorization provider-neutral: human sessions,
// service-account JWTs, and the controller's forwarded identity must reach
// the configured provider with the same resource action.  The decision logger
// is deliberately part of the configured path so the audit record contains
// identity and action but never serializes principal claims (which may contain
// credentials or upstream tokens).
func TestProductAuthorizationCapabilitiesAreAttributedAcrossPrincipalProviders(t *testing.T) {
	productID := "Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC9wcm9kLXVpZA=="
	store := &testutil.StubStore{
		GetProductFunc: func(context.Context, string) (*datastore.Product, error) {
			return &datastore.Product{UID: "prod-uid", Name: "widget", Namespace: "acme", CreationActor: "alice"}, nil
		},
		GetProductByNameFunc: func(context.Context, string, string) (*datastore.Product, error) {
			return &datastore.Product{UID: "prod-uid", Name: "widget", Namespace: "acme", CreationActor: "alice"}, nil
		},
	}

	for _, tc := range []struct {
		name      string
		principal *auth.Principal
		field     string
		args      map[string]any
		action    string
	}{
		{
			name: "human author creates product",
			principal: &auth.Principal{Subject: "alice", AuthMethod: "static-users", Claims: map[string]any{"access_token": "must-not-appear"}},
			field: "createProduct", args: map[string]any{"input": map[string]any{"metadata": map[string]any{"namespace": "acme", "name": "widget"}}}, action: "product.create",
		},
		{
			name: "service account updates product",
			principal: &auth.Principal{Subject: "serviceaccount:store:author", AuthMethod: "serviceaccount-jwt", Claims: map[string]any{"client_assertion": "must-not-appear"}},
			field: "updateProduct", args: map[string]any{"input": map[string]any{"metadata": map[string]any{"namespace": "acme", "name": "widget"}}}, action: "product.update",
		},
		{
			name: "controller writes status",
			principal: &auth.Principal{Subject: "serviceaccount:controllers:gitstore-controller-manager", AuthMethod: "grpc-forwarded", Claims: map[string]any{"authorization": "must-not-appear"}},
			field: "updateProductStatus", args: map[string]any{"input": map[string]any{"namespace": "acme", "name": "widget"}}, action: "product.status.write",
		},
		{
			name: "human deletes product",
			principal: &auth.Principal{Subject: "alice", AuthMethod: "static-users"},
			field: "deleteProduct", args: map[string]any{"input": model.DeleteProductInput{ID: &productID}}, action: "product.delete",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.InfoLevel)
			recording := testutil.NewAllowAllAuthZ()
			configured := auth.NewDecisionLogger(recording, zap.New(core))
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, configured, nil), store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), tc.principal)
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: tc.field}}, Args: tc.args})

			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
			require.NoError(t, err)
			assert.True(t, called)
			assert.Equal(t, tc.action, recording.Action)
			assert.Equal(t, tc.principal.Subject, logs.All()[0].ContextMap()["subject"])
			assert.Equal(t, tc.action, logs.All()[0].ContextMap()["action"])
			assert.NotContains(t, logs.All()[0].ContextMap(), "claims", "audit fields must be redacted")
			assert.NotContains(t, logs.All()[0].ContextMap(), "access_token", "audit fields must be redacted")
			assert.NotContains(t, logs.All()[0].ContextMap(), "client_assertion", "audit fields must be redacted")
			assert.NotContains(t, logs.All()[0].ContextMap(), "authorization", "audit fields must be redacted")
		})
	}
}
