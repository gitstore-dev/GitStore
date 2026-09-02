// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	webSocketTestNamespace = "controllers"
	webSocketTestName      = "controller"
	webSocketTestUID       = "00000000-0000-0000-0000-000000000101"
)

type webSocketAuthProvider struct {
	store     datastore.Datastore
	expiresAt time.Time
}

func (p *webSocketAuthProvider) Name() string { return "websocket-test" }

func (p *webSocketAuthProvider) Capabilities() auth.Capability {
	return auth.CapAuthenticate
}

func (p *webSocketAuthProvider) Authenticate(ctx context.Context, request auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	switch request.Header.Get("Authorization") {
	case "Bearer expired":
		return p.principal(time.Now().Add(-time.Second)), auth.Allow(p.Name(), "expired test credential"), nil
	case "Bearer valid":
		account, err := p.store.GetServiceAccountByUID(ctx, webSocketTestUID)
		if err != nil || account.Disabled {
			return nil, auth.Deny(p.Name(), "service account unavailable"), nil
		}
		return p.principal(p.expiresAt), auth.Allow(p.Name(), "valid test credential"), nil
	default:
		return nil, auth.Deny(p.Name(), "invalid credential"), nil
	}
}

func (p *webSocketAuthProvider) principal(expiresAt time.Time) *auth.Principal {
	return &auth.Principal{
		Subject:           datastore.ServiceAccountSubject(webSocketTestNamespace, webSocketTestName),
		AuthMethod:        "serviceaccount-jwt",
		ServiceAccountUID: webSocketTestUID,
		ExpiresAt:         expiresAt,
	}
}

func (p *webSocketAuthProvider) RevokeSession(context.Context, string, time.Time) error {
	return auth.ErrNotSupported
}

func (p *webSocketAuthProvider) RefreshSession(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

func (p *webSocketAuthProvider) IssueSession(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

type webSocketTestEnvironment struct {
	server      *httptest.Server
	store       datastore.Datastore
	connections *wsregistry.Registry
	eventBus    *eventbus.Bus
}

func newWebSocketTestEnvironment(t *testing.T, expiresAt time.Time) *webSocketTestEnvironment {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	now := time.Now().UTC()
	require.NoError(t, store.CreateServiceAccount(context.Background(), &datastore.ServiceAccount{
		UID:               webSocketTestUID,
		Namespace:         webSocketTestNamespace,
		Name:              webSocketTestName,
		ResourceVersion:   "1",
		CreationTimestamp: now,
		UpdateTimestamp:   now,
	}))

	connections := wsregistry.New()
	eventBus := eventbus.New(8)
	provider := &webSocketAuthProvider{store: store, expiresAt: expiresAt}
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(provider),
		allowall.New(zap.NewNop()),
		nil,
	)
	handler, err := NewGraphQLHandler(GraphQLHandlerDeps{
		Store:              store,
		Logger:             zap.NewNop(),
		Registry:           registry,
		IDs:                apiruntime.NewSequenceIDGenerator(),
		EventBus:           eventBus,
		ConnectionRegistry: connections,
	})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &webSocketTestEnvironment{server: server, store: store, connections: connections, eventBus: eventBus}
}

func (e *webSocketTestEnvironment) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, response, err := dialer.Dial(strings.Replace(e.server.URL, "http", "ws", 1)+"/graphql", nil)
	status := "<nil>"
	if response != nil {
		status = response.Status
		t.Cleanup(func() { _ = response.Body.Close() })
	}
	require.NoErrorf(t, err, "response status: %s", status)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	return conn
}

func startWebSocketInit(t *testing.T, conn *websocket.Conn, payload any) (map[string]any, error) {
	t.Helper()
	message := map[string]any{"type": "connection_init"}
	if payload != nil {
		message["payload"] = payload
	}
	require.NoError(t, conn.WriteJSON(message))
	var response map[string]any
	return response, conn.ReadJSON(&response)
}

func initWebSocket(t *testing.T, conn *websocket.Conn, payload any) map[string]any {
	t.Helper()
	response, err := startWebSocketInit(t, conn, payload)
	require.NoError(t, err)
	return response
}

func TestGraphQLWebSocketConnectionInitAuthenticates(t *testing.T) {
	cases := []struct {
		name    string
		expires time.Time
		payload any
		disable bool
		accept  bool
	}{
		{
			name:    "valid access token",
			expires: time.Now().Add(time.Minute),
			payload: map[string]any{"Authorization": "Bearer valid"},
			accept:  true,
		},
		{
			name:    "missing authorization",
			expires: time.Now().Add(time.Minute),
			payload: map[string]any{},
		},
		{
			name:    "invalid access token",
			expires: time.Now().Add(time.Minute),
			payload: map[string]any{"Authorization": "Bearer invalid"},
		},
		{
			name:    "expired access token",
			expires: time.Now().Add(time.Minute),
			payload: map[string]any{"Authorization": "Bearer expired"},
		},
		{
			name:    "malformed authorization payload",
			expires: time.Now().Add(time.Minute),
			payload: map[string]any{"Authorization": map[string]string{"token": "valid"}},
		},
		{
			name:    "disabled service account",
			expires: time.Now().Add(time.Minute),
			payload: map[string]any{"Authorization": "Bearer valid"},
			disable: true,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			env := newWebSocketTestEnvironment(t, test.expires)
			if test.disable {
				require.NoError(t, env.store.SetServiceAccountDisabled(context.Background(), webSocketTestUID, true))
			}
			response, err := startWebSocketInit(t, env.dial(t), test.payload)
			if test.accept {
				require.NoError(t, err)
				assert.Equal(t, "connection_ack", response["type"])
				return
			}
			if err == nil {
				assert.Equal(t, "connection_error", response["type"])
				return
			}
			var closeErr *websocket.CloseError
			require.ErrorAs(t, err, &closeErr)
			assert.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
		})
	}
}

func TestGraphQLWebSocketConnectionClosesAtTokenExpiry(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(100*time.Millisecond))
	conn := env.dial(t)
	response := initWebSocket(t, conn, map[string]any{"Authorization": "Bearer valid"})
	require.Equal(t, "connection_ack", response["type"])

	_, _, err := conn.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
}

func TestGraphQLWebSocketConnectionInitPrincipalAuthorizesSubscription(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	conn := env.dial(t)
	require.Equal(t, "connection_ack", initWebSocket(t, conn, map[string]any{"Authorization": "Bearer valid"})["type"])

	env.eventBus.Publish(eventbus.Event{
		Type:      eventbus.Added,
		Kind:      "File",
		Namespace: webSocketTestNamespace,
		Name:      "watched-file",
		Object: &datastore.File{
			UID:       "00000000-0000-0000-0000-000000000102",
			Namespace: webSocketTestNamespace,
			Name:      "watched-file",
		},
	})
	require.NoError(t, conn.WriteJSON(map[string]any{
		"id":   "file-watch",
		"type": "subscribe",
		"payload": map[string]any{
			"query": `subscription { watchFiles(namespace: "controllers", resourceVersion: "0") { name } }`,
		},
	}))

	var response map[string]any
	require.NoError(t, conn.ReadJSON(&response))
	assert.Equal(t, "next", response["type"])
}

func TestGraphQLWebSocketConnectionClosesWhenServiceAccountDisabled(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	conn := env.dial(t)
	require.Equal(t, "connection_ack", initWebSocket(t, conn, map[string]any{"Authorization": "Bearer valid"})["type"])

	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:              env.store,
		Logger:             zap.NewNop(),
		ConnectionRegistry: env.connections,
	})
	require.NoError(t, err)
	require.NoError(t, root.SetServiceAccountDisabled(context.Background(), webSocketTestUID, true))

	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
}

func TestGraphQLWebSocketConnectionClosesWhenServiceAccountDeleted(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	conn := env.dial(t)
	require.Equal(t, "connection_ack", initWebSocket(t, conn, map[string]any{"Authorization": "Bearer valid"})["type"])

	body := strings.NewReader(`{"query":"mutation { deleteServiceAccount(input: {apiVersion: \"v1\", kind: \"ServiceAccount\", metadata: {namespace: \"controllers\", name: \"controller\"}}) { metadata { uid } } }"}`)
	request, err := http.NewRequest(http.MethodPost, env.server.URL+"/graphql", body)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer valid")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, http.StatusOK, response.StatusCode)
	var payload struct {
		Errors []any `json:"errors"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
	assert.Empty(t, payload.Errors)

	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
}
