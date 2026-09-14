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
	"go.uber.org/zap"
)

func TestRepositoryWatchAuthorizationRunsBeforeResolverForBothEntryPoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		args  map[string]any
	}{
		{name: "typed", field: "watchRepositories", args: map[string]any{"namespace": "acme", "resourceVersion": "revealing-invalid-cursor"}},
		{name: "generic", field: "watchResources", args: map[string]any{"kind": "Repository", "namespace": "acme", "resourceVersion": "revealing-invalid-cursor"}},
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
			assert.Equal(t, "repository.watch", authz.Action)
			assert.Equal(t, "Repository", authz.Resource.Kind)
			assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
		})
	}
}
