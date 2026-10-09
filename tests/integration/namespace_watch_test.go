// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceWatchCrossReplicaBootstrapAndResume(t *testing.T) {
	apiA := os.Getenv("NAMESPACE_WATCH_API_A")
	apiB := os.Getenv("NAMESPACE_WATCH_API_B")
	token := os.Getenv("NAMESPACE_WATCH_TOKEN")
	if apiA == "" || apiB == "" || token == "" {
		t.Skip("set NAMESPACE_WATCH_API_A, NAMESPACE_WATCH_API_B, and NAMESPACE_WATCH_TOKEN for the two-replica probe")
	}

	watchA := openNamespaceWatch(t, apiA, token, "__namespace_watch_bootstrap__")
	bookmark := readNamespaceWatchEvent(t, watchA)
	assert.Equal(t, "BOOKMARK", bookmark.Type)
	assert.Empty(t, bookmark.Namespace)

	firstName := uniqueName("watch-replica-b")
	createNamespaceThrough(t, apiB, token, firstName)
	added := readNamespaceWatchTransition(t, watchA)
	assert.Equal(t, "ADDED", added.Type)
	assert.Equal(t, firstName, added.Name)
	require.NotEmpty(t, added.ResourceVersion)
	_ = watchA.Close()

	watchB := openNamespaceWatch(t, apiB, token, added.ResourceVersion)
	secondName := uniqueName("watch-replica-a")
	createNamespaceThrough(t, apiA, token, secondName)
	resumed := readNamespaceWatchTransition(t, watchB)
	assert.Equal(t, "ADDED", resumed.Type)
	assert.Equal(t, secondName, resumed.Name)
	assert.NotEqual(t, added.ResourceVersion, resumed.ResourceVersion)
}

type namespaceWatchWireEvent struct {
	Type            string         `json:"type"`
	Name            string         `json:"name"`
	ResourceVersion string         `json:"resourceVersion"`
	Namespace       map[string]any `json:"namespace"`
}

func openNamespaceWatch(t *testing.T, apiURL, token, cursor string) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(strings.TrimSuffix(apiURL, "/"), "http", "ws", 1) + "/graphql"
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, _, err := dialer.Dial(wsURL, header)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	require.NoError(t, conn.WriteJSON(map[string]any{
		"type":    "connection_init",
		"payload": map[string]any{"Authorization": "Bearer " + token},
	}))
	var message map[string]any
	require.NoError(t, conn.ReadJSON(&message))
	require.Equal(t, "connection_ack", message["type"])
	require.NoError(t, conn.WriteJSON(map[string]any{
		"id": "namespace-watch", "type": "subscribe",
		"payload": map[string]any{
			"query":     `subscription($cursor: String) { watchNamespaces(resourceVersion: $cursor) { type name resourceVersion namespace { metadata { name resourceVersion generation finalizers } status { observedGeneration conditions { type status reason } } } } }`,
			"variables": map[string]any{"cursor": cursor},
		},
	}))
	return conn
}

func readNamespaceWatchEvent(t *testing.T, conn *websocket.Conn) namespaceWatchWireEvent {
	t.Helper()
	var message struct {
		Type    string `json:"type"`
		Payload struct {
			Data struct {
				Watch namespaceWatchWireEvent `json:"watchNamespaces"`
			} `json:"data"`
			Errors []json.RawMessage `json:"errors"`
		} `json:"payload"`
	}
	require.NoError(t, conn.ReadJSON(&message))
	require.Equal(t, "next", message.Type)
	require.Empty(t, message.Payload.Errors)
	return message.Payload.Data.Watch
}

func readNamespaceWatchTransition(t *testing.T, conn *websocket.Conn) namespaceWatchWireEvent {
	t.Helper()
	for {
		event := readNamespaceWatchEvent(t, conn)
		if event.Type != "BOOKMARK" {
			return event
		}
	}
}

func createNamespaceThrough(t *testing.T, apiURL, token, name string) {
	t.Helper()
	response := gqlQueryWithURL(t, strings.TrimSuffix(apiURL, "/"), token, `
		mutation($input: CreateNamespaceInput!) {
			createNamespace(input: $input) { namespace { metadata { name } } }
		}`, map[string]any{"input": map[string]any{
		"apiVersion": "gitstore.dev/v1beta1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"title": name, "tier": "USER"},
	}})
	require.Empty(t, response.Errors, namespaceContractErrors(response.Errors))
}
