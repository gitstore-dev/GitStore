// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// namespaceScopedAuthZ denies watch actions for any namespace other than
// allowedNamespace, mirroring a tenant boundary an operator would configure
// via a real AuthZProvider. rbac-local itself doesn't evaluate the
// namespace attribute today (it only matches on the action string), but
// this stub proves the enforcement seam GraphQLFieldAuthorizer now wires
// through actually reaches an attribute-aware provider correctly.
type namespaceScopedAuthZ struct {
	allowedNamespace string
}

func (n *namespaceScopedAuthZ) Name() string { return "namespace-scoped-stub" }

func (n *namespaceScopedAuthZ) Authorize(_ context.Context, _ *auth.Principal, action string, res auth.ResourceContext) (auth.Decision, error) {
	if ns, _ := res.Attrs["namespace"].(string); ns != "" && ns != n.allowedNamespace {
		return auth.Deny("namespace-scoped-stub", "principal has no rights to namespace "+ns), nil
	}
	return auth.Allow("namespace-scoped-stub", "action "+action+" permitted for namespace"), nil
}

// subscribeThroughFieldAuthorizer runs GraphQLFieldAuthorizer exactly the
// way gqlgen's AroundFields hook runs it for a Subscription root field
// (once, at subscribe time — see graphql.go's GraphQLFieldAuthorizer
// doc comment), then invokes resolve to obtain the subscription's event
// channel. Mirrors gitstore-api/internal/middleware/security/graphql_test.go's
// existing GraphQLFieldAuthorizer test pattern for mutations.
func subscribeThroughFieldAuthorizer(
	ctx context.Context,
	mw security.Authorize,
	fieldName string,
	args map[string]any,
	resolve func(ctx context.Context) (any, error),
) (any, error) {
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: fieldName}},
		Args:   args,
	})
	return mw.GraphQLFieldAuthorizer(ctx, resolve)
}

func TestWatchFiles_UnauthorizedNamespaceRejectsSubscribeAttempt(t *testing.T) {
	r, _, _ := newWatchTestResolver(t)
	authz := &namespaceScopedAuthZ{allowedNamespace: "acme"}
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())

	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-admin"})
	resolved := false
	result, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchFiles", map[string]any{"namespace": strptr("other-tenant")},
		func(innerCtx context.Context) (any, error) {
			resolved = true
			return r.Subscription().WatchFiles(innerCtx, strptr("other-tenant"), nil, nil)
		})

	require.Error(t, err)
	require.Nil(t, result)
	require.False(t, resolved, "resolver must never run — the subscription attempt itself must be rejected, not silently return an empty channel")
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
}

func TestWatchResources_UnauthorizedNamespaceRejectsSubscribeAttempt(t *testing.T) {
	r, _, _ := newWatchTestResolver(t)
	authz := &namespaceScopedAuthZ{allowedNamespace: "acme"}
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())

	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-admin"})
	resolved := false
	_, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchResources", map[string]any{"kind": "File", "namespace": strptr("other-tenant")},
		func(innerCtx context.Context) (any, error) {
			resolved = true
			return r.Subscription().WatchResources(innerCtx, "File", strptr("other-tenant"), nil, nil)
		})

	require.Error(t, err)
	require.False(t, resolved)
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
}

func TestWatchCategories_UnauthorizedNamespaceRejectsSubscribeAttempt(t *testing.T) {
	r, _, _ := newWatchTestResolver(t)
	authz := &namespaceScopedAuthZ{allowedNamespace: "acme"}
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())

	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-admin"})
	resolved := false
	_, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchCategories", map[string]any{"namespace": strptr("other-tenant")},
		func(innerCtx context.Context) (any, error) {
			resolved = true
			return r.Subscription().WatchCategories(innerCtx, strptr("other-tenant"), nil, nil)
		})

	require.Error(t, err)
	require.False(t, resolved)
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
}

func TestWatchProducts_UnauthorizedNamespaceRejectsSubscribeAttempt(t *testing.T) {
	r, _, _ := newWatchTestResolver(t)
	authz := &namespaceScopedAuthZ{allowedNamespace: "acme"}
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())

	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-admin"})
	resolved := false
	_, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchProducts", map[string]any{"namespace": strptr("other-tenant")},
		func(innerCtx context.Context) (any, error) {
			resolved = true
			return r.Subscription().WatchProducts(innerCtx, strptr("other-tenant"), nil, nil)
		})

	require.Error(t, err)
	require.False(t, resolved)
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
}

// TestWatchFiles_AuthorizedPrincipalSubscribesAndReceivesEvents is the no-
// regression counterpart to the deny tests above: a principal with rights
// to its own namespace still opens the subscription and receives events
// published for that namespace, unchanged from pre-fix behavior.
func TestWatchFiles_AuthorizedPrincipalSubscribesAndReceivesEvents(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	authz := &namespaceScopedAuthZ{allowedNamespace: "acme"}
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = auth.ContextWithPrincipal(ctx, &auth.Principal{Subject: "alice", AuthMethod: "static-admin"})

	resolved := false
	result, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchFiles", map[string]any{"namespace": strptr("acme")},
		func(innerCtx context.Context) (any, error) {
			resolved = true
			return r.Subscription().WatchFiles(innerCtx, strptr("acme"), nil, nil)
		})
	require.NoError(t, err)
	require.True(t, resolved)

	events, ok := result.(<-chan *model.FileWatchEvent)
	require.True(t, ok)

	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000099", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
	}
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "File", Namespace: "acme", Name: "hero", ResourceVersion: "1", Object: file})

	select {
	case ev := <-events:
		require.Equal(t, "hero", ev.Name)
	case <-time.After(time.Second):
		t.Fatal("authorized principal never received its own namespace's event")
	}
}

// TestWatchResources_AuthorizedPrincipalSubscribesAndReceivesEvents is the
// generic-path no-regression counterpart: a principal with rights to its own
// namespace still opens a watchResources subscription and receives events,
// unchanged from pre-fix behavior.
func TestWatchResources_AuthorizedPrincipalSubscribesAndReceivesEvents(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	authz := &namespaceScopedAuthZ{allowedNamespace: "acme"}
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = auth.ContextWithPrincipal(ctx, &auth.Principal{Subject: "alice", AuthMethod: "static-admin"})

	resolved := false
	result, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchResources", map[string]any{"kind": "File", "namespace": strptr("acme")},
		func(innerCtx context.Context) (any, error) {
			resolved = true
			return r.Subscription().WatchResources(innerCtx, "File", strptr("acme"), nil, nil)
		})
	require.NoError(t, err)
	require.True(t, resolved)

	events, ok := result.(<-chan *model.WatchEvent)
	require.True(t, ok)

	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000098", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
	}
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "File", Namespace: "acme", Name: "hero", ResourceVersion: "1", Object: file})

	select {
	case ev := <-events:
		require.Equal(t, "hero", ev.Name)
	case <-time.After(time.Second):
		t.Fatal("authorized principal never received its own namespace's event")
	}
}

// TestWatchCategories_AuthorizedPrincipalSubscribesAndReceivesEvents is the
// no-regression counterpart to the watchCategories deny test above.
func TestWatchCategories_AuthorizedPrincipalSubscribesAndReceivesEvents(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	authz := &namespaceScopedAuthZ{allowedNamespace: "acme"}
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), &testutil.StubStore{}, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = auth.ContextWithPrincipal(ctx, &auth.Principal{Subject: "alice", AuthMethod: "static-admin"})

	resolved := false
	result, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchCategories", map[string]any{"namespace": strptr("acme")},
		func(innerCtx context.Context) (any, error) {
			resolved = true
			return r.Subscription().WatchCategories(innerCtx, strptr("acme"), nil, nil)
		})
	require.NoError(t, err)
	require.True(t, resolved)

	events, ok := result.(<-chan *model.CategoryWatchEvent)
	require.True(t, ok)

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics", ResourceVersion: "1"})

	e := mustReceiveCategoryEvent(t, events)
	require.Equal(t, "electronics", e.Name)
}

// TestWatchFiles_MissingAuthZProviderRejectsSubscribeAttempt matches the
// existing "authorization service unavailable" behavior mutations already
// get in graphql_test.go when no AuthZProvider is configured — subscriptions
// must fail closed too, not fall back to unauthenticated streaming.
func TestWatchFiles_MissingAuthZProviderRejectsSubscribeAttempt(t *testing.T) {
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, nil, nil), &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-admin"})

	resolved := false
	_, err := subscribeThroughFieldAuthorizer(ctx, mw, "watchFiles", map[string]any{"namespace": strptr("acme")},
		func(context.Context) (any, error) {
			resolved = true
			return nil, nil
		})
	require.Error(t, err)
	require.False(t, resolved)
	require.Contains(t, err.Error(), "authorization service unavailable")
}
