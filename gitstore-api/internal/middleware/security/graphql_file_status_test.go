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
	authz := &stubAuthZProvider{decision: auth.Allow("stub-authz", "allowed")}
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-admin", Roles: []string{"controller"}})
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
	assert.Equal(t, "file.status.write", authz.action)
	assert.Equal(t, "File", authz.resource.Kind)
	assert.Equal(t, "hero", authz.resource.Name)
}

// TestGraphQLFieldAuthorizerUpdateResourceStatusFileDenyReturnsForbidden
// proves the real boundary actually blocks the resolver: a policy denial
// for "file.status.write" must stop updateResourceStatus from ever reaching
// the File status-write resolver, surfaced as a FORBIDDEN GraphQL error
// (spec 051 T041).
func TestGraphQLFieldAuthorizerUpdateResourceStatusFileDenyReturnsForbidden(t *testing.T) {
	authz := &stubAuthZProvider{decision: auth.Deny("stub-authz", "no controller role")}
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-admin"})
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
	assert.Equal(t, "file.status.write", authz.action)
}

// TestGraphQLFieldAuthorizerDoesNotAuthorizeWatchFilesSubscription documents,
// rather than papers over, a real current gap: GraphQLFieldAuthorizer only
// runs its switch when fc.Object == "Mutation" (see graphql.go's early
// `if fc == nil || fc.Object != "Mutation" { return next(ctx) }`). A
// "watchFiles"/"watchResources" subscription field's Object is
// "Subscription", so it never reaches the switch and AuthZProvider.Authorize
// is never called for it. There is no other field-level authorization seam
// for subscriptions anywhere in this codebase today (WatchFiles's own
// resolver in gitstore-api/internal/graph/resolver/file.resolvers.go calls
// straight into the event bus with no principal/authz check either).
//
// This test does not claim that gap is closed — it pins down the current,
// observable behavior (the resolver proceeds and Authorize is never
// invoked) so a future change that adds subscription-level authorization
// will have to consciously update this test, rather than the gap silently
// persisting undetected. Per spec 051 T041's real scope: File's
// namespace-isolation guarantees for watch traffic come from
// eventbus/resolver-level namespace filtering (see
// TestWatchFiles_DeliversTypedPayloadAndFiltersNamespace in
// gitstore-api/tests/contract/watch_status_test.go), not from any
// authorization check — there is currently no authenticated-boundary
// enforcement point for watch subscriptions to test.
func TestGraphQLFieldAuthorizerDoesNotAuthorizeWatchFilesSubscription(t *testing.T) {
	authz := &stubAuthZProvider{decision: auth.Deny("stub-authz", "would deny if ever asked")}
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Anonymous())
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchFiles"}},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.NoError(t, err)
	assert.True(t, called, "a Subscription-object field bypasses GraphQLFieldAuthorizer's switch entirely today")
	assert.Empty(t, authz.action, "AuthZProvider.Authorize must never be called for a Subscription field by this middleware")
}
