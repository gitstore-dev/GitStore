// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"errors"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// CategoryTaxonomy watches previously skipped subscription authorization.
// Both projections must now deny before the resolver opens a stream.
func TestGraphQLFieldAuthorizerRejectsUnauthorizedCategoryWatches(t *testing.T) {
	for _, tc := range []struct {
		field string
		args  map[string]any
	}{
		{"watchCategories", map[string]any{"namespace": "acme-store"}},
		{"watchResources", map[string]any{"kind": "CategoryTaxonomy", "namespace": "acme-store"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			authz := testutil.NewDenyAllAuthZ(t)
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), auth.Anonymous())
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: "Subscription",
				Field:  graphql.CollectedField{Field: &ast.Field{Name: tc.field}},
				Args:   tc.args,
			})

			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
				called = true
				return "ok", nil
			})
			require.Error(t, err)
			assert.False(t, called)
			assert.Equal(t, "categoryTaxonomy.watch", authz.Action)
			assert.Equal(t, "CategoryTaxonomy", authz.Resource.Kind)
			assert.Equal(t, "acme-store", authz.Resource.Attrs["namespace"])
			var gqlErr *gqlerror.Error
			require.True(t, errors.As(err, &gqlErr))
			assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
		})
	}
}

func TestGraphQLFieldAuthorizerAllowsAuthorizedCategoryWatch(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchCategories"}},
		Args:   map[string]any{},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "categoryTaxonomy.watch", authz.Action)
}
