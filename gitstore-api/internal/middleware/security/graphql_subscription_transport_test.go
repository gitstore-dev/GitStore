// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
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
// opens an event-bus subscription.
func TestUnauthorizedFileWatchRejectedThroughWebSocket(t *testing.T) {
	store := &testutil.StubStore{}
	deny := &transportDenyAuthZ{}
	registry := auth.NewProviderRegistry(auth.NewChainedAuthN(anonymous.New()), deny, nil)
	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store: store, Registry: registry, Logger: zap.NewNop(), EventBus: eventbus.New(8),
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
		"payload": map[string]any{"query": `subscription { watchFiles(namespace: "acme-store") { name } }`},
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
	assert.Equal(t, "file.watch", deny.action)
	assert.Equal(t, "File", deny.resource.Kind)
	assert.Equal(t, "acme-store", deny.resource.Attrs["namespace"])
}
