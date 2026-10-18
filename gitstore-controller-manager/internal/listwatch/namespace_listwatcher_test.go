// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gorilla/websocket"
)

func namespaceNodeJSON(name, rv string, generation int, finalizers []string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"uid":             "uid-" + name,
			"name":            name,
			"resourceVersion": rv,
			"generation":      generation,
			"revision":        "main@sha1:" + strings.Repeat(rv, 40),
			"finalizers":      finalizers,
		},
		"status": map[string]any{
			"observedGeneration":  generation,
			"lastAppliedRevision": "stale-status-revision",
			"conditions": []any{
				map[string]any{"type": "AdmissionAccepted", "status": "TRUE", "observedGeneration": generation},
			},
		},
	}
}

func TestNamespaceListWatcherListsNamespaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			upgrader := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade failed: %v", err)
				return
			}
			defer conn.Close()
			var initMsg map[string]any
			if err := conn.ReadJSON(&initMsg); err != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
			var subMsg map[string]any
			if err := conn.ReadJSON(&subMsg); err != nil {
				return
			}
			if !strings.Contains(subMsg["payload"].(map[string]any)["query"].(string), " revision ") {
				t.Error("watch must select metadata.revision")
			}
			_ = conn.WriteJSON(map[string]any{
				"id": subMsg["id"], "type": "next",
				"payload": map[string]any{"data": map[string]any{"watchNamespaces": map[string]any{
					"type": "BOOKMARK", "resourceVersion": "17",
				}}},
			})
			return
		}
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(request.Query, " revision ") {
			t.Error("list must select metadata.revision")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"namespaces": map[string]any{
				"edges":    []any{map[string]any{"node": namespaceNodeJSON("acme", "8", 2, nil)}},
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": "n1"},
			},
		}})
	}))
	defer srv.Close()

	lw := listwatch.NewNamespaceListWatcher(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	resp, err := lw.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "acme" {
		t.Fatalf("Items = %+v, want acme", resp.Items)
	}
	if resp.ResourceVersion != "17" {
		t.Fatalf("ResourceVersion = %q, want 17", resp.ResourceVersion)
	}
	if resp.Items[0].Revision != "main@sha1:"+strings.Repeat("8", 40) || resp.Items[0].Status.LastAppliedRevision != "stale-status-revision" {
		t.Fatalf("authored/status revisions = %+v", resp.Items[0])
	}
}

func TestNamespaceListWatcherMapsTypedWatchEvents(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		var initMsg map[string]any
		if err := conn.ReadJSON(&initMsg); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{"type": "connection_ack"}); err != nil {
			return
		}
		var subMsg map[string]any
		if err := conn.ReadJSON(&subMsg); err != nil {
			return
		}
		if !strings.Contains(subMsg["payload"].(map[string]any)["query"].(string), " revision ") {
			t.Error("watch must select metadata.revision")
		}
		out := map[string]any{"id": subMsg["id"]}
		maps.Copy(out, map[string]any{
			"type": "next",
			"payload": map[string]any{"data": map[string]any{"watchNamespaces": map[string]any{
				"type":            "MODIFIED",
				"name":            "acme",
				"resourceVersion": "9",
				"namespace":       namespaceNodeJSON("acme", "9", 3, []string{"gitstore.dev/foreground-deletion"}),
			}}},
		})
		_ = conn.WriteJSON(out)
	}))
	defer srv.Close()

	lw := listwatch.NewNamespaceListWatcher(graphqlclient.New("ws"+strings.TrimPrefix(srv.URL, "http"), graphqlclient.NewStaticToken("token")))
	watcher, err := lw.Watch(context.Background(), "8")
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}
	defer watcher.Stop()

	select {
	case event := <-watcher.Events():
		if event.Type != listwatch.Modified || event.Object.Name != "acme" {
			t.Fatalf("event = %+v, want modified acme", event)
		}
		if len(event.Object.Finalizers) != 1 {
			t.Fatalf("finalizers = %v, want foreground deletion", event.Object.Finalizers)
		}
		if event.Object.Revision != "main@sha1:"+strings.Repeat("9", 40) || event.Object.Status.LastAppliedRevision != "stale-status-revision" {
			t.Fatalf("authored/status revisions = %+v", event.Object)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for namespace watch event")
	}
}
