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

func TestNamespaceWatchAuthorizationRunsBeforeResolverForBothEntryPoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		args  map[string]any
	}{
		{name: "typed", field: "watchNamespaces", args: map[string]any{"resourceVersion": "revealing-invalid-cursor"}},
		{name: "generic", field: "watchResources", args: map[string]any{"kind": "Namespace", "resourceVersion": "revealing-invalid-cursor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authz := &stubAuthZProvider{decision: auth.Deny("stub-authz", "denied")}
			registry := auth.NewProviderRegistry(nil, authz, nil)
			mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "denied", AuthMethod: "bearer"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: "Subscription",
				Field:  graphql.CollectedField{Field: &ast.Field{Name: tc.field}},
				Args:   tc.args,
			})

			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
				called = true
				return nil, errors.New("cursor parser was reached")
			})
			require.Error(t, err)
			assert.False(t, called)
			assert.Equal(t, "namespace.watch", authz.action)
			assert.Equal(t, "Namespace", authz.resource.Kind)
			assert.Empty(t, authz.resource.Name)
			assert.Empty(t, authz.resource.Attrs)
			var gqlErr *gqlerror.Error
			require.ErrorAs(t, err, &gqlErr)
			assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
			assert.NotContains(t, gqlErr.Message, "cursor")
		})
	}
}

func TestNamespaceWatchAuthorizationAllowsPluggableProviderDecision(t *testing.T) {
	authz := &stubAuthZProvider{decision: auth.Allow("stub-authz", "controller role")}
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchNamespaces"}},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return "ok", nil })
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "namespace.watch", authz.action)
}
