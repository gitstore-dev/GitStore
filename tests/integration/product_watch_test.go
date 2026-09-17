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
	"github.com/stretchr/testify/require"
)

// TestProductWatchCrossReplicaBootstrapAndResume proves Product's typed
// durable stream can bootstrap on one API replica and resume on the other.
// The capacity stack supplies the endpoints and bearer token; ordinary single
// replica integration runs skip this deployment-specific probe.
func TestProductWatchCrossReplicaBootstrapAndResume(t *testing.T) {
	apiA := strings.TrimSuffix(os.Getenv("PRODUCT_WATCH_API_A"), "/")
	apiB := strings.TrimSuffix(os.Getenv("PRODUCT_WATCH_API_B"), "/")
	token := productWatchToken(t)
	if apiA == "" || apiB == "" || token == "" {
		t.Skip("set PRODUCT_WATCH_API_A, PRODUCT_WATCH_API_B, and PRODUCT_WATCH_TOKEN for the two-replica probe")
	}
	require.NotEqual(t, apiA, apiB, "Product watch probe requires distinct API replicas")

	watchA := openProductWatch(t, apiA, token, "__product_watch_bootstrap__")
	bookmark := readProductWatchEvent(t, watchA)
	require.Equal(t, "BOOKMARK", bookmark.Type)

	first := uniqueName("product-watch-a")
	createProductThrough(t, apiB, token, first)
	added := readProductWatchTransition(t, watchA)
	require.Equal(t, "ADDED", added.Type)
	require.Equal(t, first, added.Name)
	require.NotEmpty(t, added.ResourceVersion)
	require.NoError(t, watchA.Close())

	watchB := openProductWatch(t, apiB, token, added.ResourceVersion)
	second := uniqueName("product-watch-b")
	createProductThrough(t, apiA, token, second)
	resumed := readProductWatchTransition(t, watchB)
	require.Equal(t, "ADDED", resumed.Type)
	require.Equal(t, second, resumed.Name)
	require.NotEqual(t, added.ResourceVersion, resumed.ResourceVersion)
}

func productWatchToken(t *testing.T) string {
	t.Helper()
	if token := strings.TrimSpace(os.Getenv("PRODUCT_WATCH_TOKEN")); token != "" {
		return token
	}
	path := strings.TrimSpace(os.Getenv("PRODUCT_WATCH_TOKEN_FILE"))
	if path == "" {
		return ""
	}
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "read PRODUCT_WATCH_TOKEN_FILE")
	return strings.TrimSpace(string(contents))
}

type productWatchWireEvent struct {
	Type            string         `json:"type"`
	Name            string         `json:"name"`
	ResourceVersion string         `json:"resourceVersion"`
	Product         map[string]any `json:"product"`
}

func openProductWatch(t *testing.T, apiURL, token, cursor string) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(apiURL, "http", "ws", 1) + "/graphql"
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, _, err := (&websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}).Dial(wsURL, header)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + token}}))
	var ack map[string]any
	require.NoError(t, conn.ReadJSON(&ack))
	require.Equal(t, "connection_ack", ack["type"])
	require.NoError(t, conn.WriteJSON(map[string]any{"id": "product-watch", "type": "subscribe", "payload": map[string]any{
		"query":     `subscription($cursor: String) { watchProducts(resourceVersion: $cursor) { type name resourceVersion product { metadata { name } } } }`,
		"variables": map[string]any{"cursor": cursor},
	}}))
	return conn
}

func readProductWatchEvent(t *testing.T, conn *websocket.Conn) productWatchWireEvent {
	t.Helper()
	var message struct {
		Type    string `json:"type"`
		Payload struct {
			Data struct {
				Watch productWatchWireEvent `json:"watchProducts"`
			} `json:"data"`
			Errors []json.RawMessage `json:"errors"`
		} `json:"payload"`
	}
	require.NoError(t, conn.ReadJSON(&message))
	require.Equal(t, "next", message.Type)
	require.Empty(t, message.Payload.Errors)
	return message.Payload.Data.Watch
}

func readProductWatchTransition(t *testing.T, conn *websocket.Conn) productWatchWireEvent {
	t.Helper()
	for {
		event := readProductWatchEvent(t, conn)
		if event.Type != "BOOKMARK" {
			return event
		}
	}
}

func createProductThrough(t *testing.T, apiURL, token, name string) {
	t.Helper()
	response := gqlQueryWithURL(t, apiURL, token, `mutation($input: CreateProductInput!) {
  createProduct(input: $input) { product { metadata { name } } }
}`, map[string]any{"input": map[string]any{
		"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": "Product",
		"metadata": map[string]any{"name": name, "namespace": "default"},
		"spec":     map[string]any{"title": name},
	}})
	require.Empty(t, response.Errors, string(response.Data))
}
