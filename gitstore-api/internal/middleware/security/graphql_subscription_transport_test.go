// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type transportDenyAuthZ struct {
	mu       sync.Mutex
	action   string
	resource auth.ResourceContext
}

func (d *transportDenyAuthZ) Name() string { return "transport-deny" }
func (d *transportDenyAuthZ) Authorize(_ context.Context, _ *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.action = action
	d.resource = resource
	return auth.Deny(d.Name(), "watch access denied"), nil
}

// TestUnauthorizedFileWatchRejectedThroughWebSocket exercises the production
// graphql-transport-ws path, including gqlgen argument decoding and field
// middleware. An anonymous caller must receive FORBIDDEN before WatchFiles
// opens a durable journal subscription.
func TestUnauthorizedFileWatchRejectedThroughWebSocket(t *testing.T) {
	assertWatchDeniedThroughWebSocket(t, `subscription { aliased: watchFiles(namespace: "acme-store") { name } }`, "file", "acme-store")
}

func TestCanonicalWatchesDeniedThroughWebSocket(t *testing.T) {
	for _, tc := range []struct{ kind, canonical, field string }{
		{"Namespace", "namespace", "watchNamespaces"},
		{"Repository", "repository", "watchRepositories"},
		{"Product", "product", "watchProducts"},
		{"CategoryTaxonomy", "categoryTaxonomy", "watchCategories"},
		{"File", "file", "watchFiles"},
	} {
		namespace, args := "acme-store", `(namespace: "acme-store")`
		genericArgs := fmt.Sprintf(`(kind: %q, namespace: "acme-store")`, tc.kind)
		if tc.kind == "Namespace" {
			namespace, args = "", ""
			genericArgs = `(kind: "Namespace")`
		}
		for _, field := range []string{tc.field, "watchResources"} {
			t.Run(field+"/"+tc.kind, func(t *testing.T) {
				fieldArgs := args
				if field == "watchResources" {
					fieldArgs = genericArgs
				}
				query := fmt.Sprintf(`subscription { aliased: %s%s { name } }`, field, fieldArgs)
				assertWatchDeniedThroughWebSocket(t, query, tc.canonical, namespace)
			})
		}
	}
}

func assertWatchDeniedThroughWebSocket(t *testing.T, query, kind, namespace string) {
	t.Helper()
	store := &testutil.StubStore{}
	deny := &transportDenyAuthZ{}
	registry := auth.NewProviderRegistry(auth.NewChainedAuthN(anonymous.New()), deny, nil)
	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store: store, Registry: registry, Logger: zap.NewNop(),
	})
	require.NoError(t, err)

	server := gqlhandler.New(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
	server.AddTransport(transport.Websocket{})
	authenticate := security.NewAuthenticate(registry, zap.NewNop())
	authorize := security.NewAuthorizeWithStore(registry, store, zap.NewNop())
	server.AroundOperations(authenticate.GraphQLAuthenticator)
	server.AroundOperations(authorize.GraphQLAuthorizer)
	server.AroundFields(authorize.GraphQLFieldAuthorizer)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, _, err := dialer.Dial(strings.Replace(httpServer.URL, "http", "ws", 1), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "connection_init"}))

	var message map[string]any
	require.NoError(t, conn.ReadJSON(&message))
	require.Equal(t, "connection_ack", message["type"])
	require.NoError(t, conn.WriteJSON(map[string]any{
		"id": "file-watch", "type": "subscribe",
		"payload": map[string]any{"query": query},
	}))
	require.NoError(t, conn.ReadJSON(&message))
	assert.Equal(t, "next", message["type"])
	payload, ok := message["payload"].(map[string]any)
	require.True(t, ok)
	errors, ok := payload["errors"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, errors)
	first, ok := errors[0].(map[string]any)
	require.True(t, ok)
	extensions, ok := first["extensions"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "FORBIDDEN", extensions["code"])

	deny.mu.Lock()
	defer deny.mu.Unlock()
	assert.Equal(t, kind+".watch", deny.action)
	assert.Equal(t, kind, deny.resource.Kind)
	assert.Empty(t, deny.resource.Name)
	if namespace == "" {
		assert.NotContains(t, deny.resource.Attrs, "namespace")
	} else {
		assert.Equal(t, namespace, deny.resource.Attrs["namespace"])
	}
}
