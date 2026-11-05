// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
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

func (p *webSocketAuthProvider) Name() string { return "serviceaccount-jwt" }

func (p *webSocketAuthProvider) Capabilities() auth.Capability {
	return auth.CapAuthenticate
}

func (p *webSocketAuthProvider) Authenticate(ctx context.Context, request auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	switch request.Header.Get("Authorization") {
	case "Bearer file-pool-test":
		return p.principal(p.expiresAt), auth.Allow(p.Name(), "valid File pool test credential"), nil
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
		Subject:           datastore.ServiceAccountSubject(webSocketTestName),
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
}

func newWebSocketTestEnvironment(t *testing.T, expiresAt time.Time, configure ...func(*config.EndpointConfig)) *webSocketTestEnvironment {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	now := time.Now().UTC()
	require.NoError(t, store.CreateServiceAccount(context.Background(), &datastore.ServiceAccount{
		UID:               webSocketTestUID,
		Name:              webSocketTestName,
		ResourceVersion:   "1",
		CreationTimestamp: now,
		UpdateTimestamp:   now,
	}))

	connections := wsregistry.New()
	provider := &webSocketAuthProvider{store: store, expiresAt: expiresAt}
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(provider),
		allowall.New(zap.NewNop()),
		nil,
	)
	handler, err := NewGraphQLHandler(GraphQLHandlerDeps{
		Endpoint: func() config.EndpointConfig {
			p := config.DefaultEndpoint("admin")
			p.ControllerSubject = datastore.ServiceAccountSubject(webSocketTestName)
			for _, apply := range configure {
				apply(&p)
			}
			return p
		}(),
		Store:           store,
		Logger:          zap.NewNop(),
		Registry:        registry,
		IDs:             apiruntime.NewSequenceIDGenerator(),
		ResourceJournal: store.(datastore.ResourceWatchCapable).ResourceWatchJournal(),
		NamespaceWatch: config.WatchJournalConfig{
			Subscriber:   config.SubscriberWatchConfig{Buffer: 16},
			Materializer: config.MaterializerWatchConfig{MaxLag: 60 * time.Second},
		},
		ConnectionRegistry: connections,
	})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &webSocketTestEnvironment{server: server, store: store, connections: connections}
}

func TestAdminWebSocketOriginAndConnectionCapacity(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute), func(p *config.EndpointConfig) { p.Limits.WSConnections = 1 })
	endpoint := strings.Replace(env.server.URL, "http", "ws", 1) + "/admin/graphql"
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	_, response, err := dialer.Dial(endpoint, http.Header{"Origin": []string{"https://untrusted.example"}})
	require.Error(t, err)
	require.Equal(t, 403, response.StatusCode)
	_ = response.Body.Close()
	first := env.dial(t)
	require.Equal(t, "connection_ack", initWebSocket(t, first, map[string]any{"Authorization": "Bearer valid"})["type"])
	_, response, err = dialer.Dial(endpoint, nil)
	require.Error(t, err)
	require.Equal(t, 429, response.StatusCode)
	_ = response.Body.Close()
	require.NoError(t, first.Close())
	require.Eventually(t, func() bool {
		conn, resp, err := dialer.Dial(endpoint, nil)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, time.Second, 10*time.Millisecond)
}

func (e *webSocketTestEnvironment) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, response, err := dialer.Dial(strings.Replace(e.server.URL, "http", "ws", 1)+"/admin/graphql", nil)
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

func TestAdminWebSocketKeepsAuthenticatedSchemaBoundary(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	conn := env.dial(t)
	response := initWebSocket(t, conn, map[string]any{"Authorization": "Bearer valid"})
	require.Equal(t, "connection_ack", response["type"])
	require.NoError(t, conn.WriteJSON(map[string]any{"id": "schema", "type": "subscribe", "payload": map[string]any{"query": `{ __type(name:"StorefrontProduct") { name } }`}}))
	var message map[string]any
	require.NoError(t, conn.ReadJSON(&message))
	require.Equal(t, "next", message["type"])
	payload := message["payload"].(map[string]any)
	require.NotContains(t, payload, "errors")
	require.Nil(t, payload["data"].(map[string]any)["__type"])
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

func TestGraphQLWebSocketUpgradeRejectsInvalidBearerBeforeHandshake(t *testing.T) {
	for _, token := range []string{"invalid", "expired"} {
		t.Run(token, func(t *testing.T) {
			env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
			dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
			conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(env.server.URL, "http")+"/admin/graphql", http.Header{"Authorization": []string{"Bearer " + token}})
			if conn != nil {
				_ = conn.Close()
			}
			require.Error(t, err)
			require.NotNil(t, response)
			defer response.Body.Close()
			require.Equal(t, http.StatusUnauthorized, response.StatusCode)
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

func TestGraphQLFilePoolLookupAcceptsBoundedAliasesAndTypedProjection(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	for i := range 100 {
		file := &datastore.File{
			UID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1000), Namespace: "pool", Name: fmt.Sprintf("file-%d", i),
			Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
			Annotations: map[string]string{"secret-capacity.gitstore.dev/sequence": "0"},
			Spec:        json.RawMessage(`{"contentType":"image/jpeg","type":"gitstore.dev/media","provider":"s3","source":{"uri":"s3://fixture","credentialsRef":{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"capacity-media"}}}}`),
		}
		require.NoError(t, env.store.CreateFile(t.Context(), file))
	}
	for offset := 0; offset < 100; offset += 10 {
		var query strings.Builder
		query.WriteString("query {")
		for index := range 10 {
			fmt.Fprintf(&query, `f%d:file(by:{namespacePath:{namespace:"pool",name:"file-%d"}}){id metadata{name namespace annotations} spec{contentType type provider source{uri credentialsRef{kind type secretRef{kind name key namespace}}}}}`, index, offset+index)
		}
		query.WriteString(`missing:file(by:{namespacePath:{namespace:"other-namespace",name:"file-0"}}){id}}`)
		body, err := json.Marshal(map[string]any{"query": query.String()})
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.server.URL+"/admin/graphql", strings.NewReader(string(body)))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer file-pool-test")
		response, err := env.server.Client().Do(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		var result struct {
			Data map[string]*struct {
				ID       string
				Metadata struct{ Name, Namespace string }
				Spec     struct {
					ContentType string
					Provider    string
					Source      struct {
						CredentialsRef *struct {
							Type      string
							SecretRef struct {
								Name           string
								Key, Namespace *string
							}
						}
					}
				}
			}
			Errors gqlerror.List
		}
		err = json.NewDecoder(response.Body).Decode(&result)
		require.NoError(t, response.Body.Close())
		require.NoError(t, err)
		require.Len(t, result.Errors, 1)
		require.Equal(t, "missing", result.Errors[0].Path.String())
		require.Len(t, result.Errors[0].Extensions, 2)
		require.Equal(t, "NOT_FOUND", result.Errors[0].Extensions["code"])
		diagnostics, err := json.Marshal(result.Errors[0].Extensions["diagnostics"])
		require.NoError(t, err)
		require.JSONEq(t, `[{"reason":"FILE_NOT_FOUND","message":"file not found","level":"FAILURE"}]`, string(diagnostics))
		require.Nil(t, result.Data["missing"])
		for index := range 10 {
			file := result.Data[fmt.Sprintf("f%d", index)]
			require.NotNil(t, file)
			require.NotEmpty(t, file.ID)
			require.Equal(t, fmt.Sprintf("file-%d", offset+index), file.Metadata.Name)
			require.Equal(t, "pool", file.Metadata.Namespace)
			require.Equal(t, "image/jpeg", file.Spec.ContentType)
			require.Equal(t, "s3", file.Spec.Provider)
			require.NotNil(t, file.Spec.Source.CredentialsRef)
			require.Equal(t, "aws-access-key/v1", file.Spec.Source.CredentialsRef.Type)
			require.Equal(t, "capacity-media", file.Spec.Source.CredentialsRef.SecretRef.Name)
			require.Nil(t, file.Spec.Source.CredentialsRef.SecretRef.Key)
			require.Nil(t, file.Spec.Source.CredentialsRef.SecretRef.Namespace)
		}
	}
}

func TestGraphQLWebSocketConnectionInitPrincipalAuthorizesSubscription(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	conn := env.dial(t)
	require.Equal(t, "connection_ack", initWebSocket(t, conn, map[string]any{"Authorization": "Bearer valid"})["type"])

	journal := env.store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	cursor := watchjournal.EncodeCursor(bounds.Epoch, bounds.HighWater)
	require.NoError(t, env.store.CreateFile(t.Context(), &datastore.File{
		UID:       "00000000-0000-0000-0000-000000000102",
		Namespace: webSocketTestNamespace,
		Name:      "watched-file",
	}))
	require.NoError(t, conn.WriteJSON(map[string]any{
		"id":   "file-watch",
		"type": "subscribe",
		"payload": map[string]any{
			"query":     `subscription($cursor:String!) { watchFiles(namespace: "controllers", resourceVersion: $cursor) { name } }`,
			"variables": map[string]any{"cursor": cursor},
		},
	}))

	var response map[string]any
	require.NoError(t, conn.ReadJSON(&response))
	assert.Equal(t, "next", response["type"])
}

func TestAdminWebSocketOperationCapacityAcrossConnections(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute), func(p *config.EndpointConfig) { p.Limits.WSOperations = 1 })
	first := env.dial(t)
	require.Equal(t, "connection_ack", initWebSocket(t, first, map[string]any{"Authorization": "Bearer valid"})["type"])
	journal := env.store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	require.NoError(t, env.store.CreateFile(t.Context(), &datastore.File{UID: "00000000-0000-0000-0000-000000000102", Namespace: webSocketTestNamespace, Name: "active-watch"}))
	require.NoError(t, first.WriteJSON(map[string]any{"id": "watch", "type": "subscribe", "payload": map[string]any{"query": `subscription($cursor:String!){watchFiles(namespace:"controllers",resourceVersion:$cursor){name}}`, "variables": map[string]any{"cursor": watchjournal.EncodeCursor(bounds.Epoch, bounds.HighWater)}}}))
	var response map[string]any
	require.NoError(t, first.ReadJSON(&response))
	require.Equal(t, "next", response["type"])
	second := env.dial(t)
	require.Equal(t, "connection_ack", initWebSocket(t, second, map[string]any{"Authorization": "Bearer valid"})["type"])
	request := func(id string) map[string]any {
		require.NoError(t, second.WriteJSON(map[string]any{"id": id, "type": "subscribe", "payload": map[string]any{"query": "{__typename}"}}))
		var next, complete map[string]any
		require.NoError(t, second.ReadJSON(&next))
		require.NoError(t, second.ReadJSON(&complete))
		require.Equal(t, "complete", complete["type"])
		return next
	}
	response = request("blocked")
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "THROTTLED")
	require.NoError(t, first.Close())
	attempt := 0
	require.Eventually(t, func() bool {
		attempt++
		next := request(fmt.Sprintf("released-%d", attempt))
		encoded, err := json.Marshal(next)
		require.NoError(t, err)
		return strings.Contains(string(encoded), `"__typename":"Query"`)
	}, time.Second, 10*time.Millisecond)
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

	body := strings.NewReader(`{"query":"mutation { deleteServiceAccount(input: {apiVersion: \"v1\", kind: \"ServiceAccount\", metadata: {name: \"controller\"}}) { serviceAccount { metadata { uid } } } }"}`)
	request, err := http.NewRequest(http.MethodPost, env.server.URL+"/admin/graphql", body)
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
