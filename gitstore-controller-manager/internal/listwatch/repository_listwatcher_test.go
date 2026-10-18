// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gorilla/websocket"
)

func repositoryNodeJSON(namespace, name, rv string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"uid": "uid-" + name, "namespace": namespace, "name": name, "resourceVersion": rv, "generation": 2, "revision": "main@sha1:" + strings.Repeat(rv, 40), "finalizers": []any{}},
		"spec":     map[string]any{"defaultBranch": "main"},
		"status":   map[string]any{"observedGeneration": 2, "lastAppliedRevision": "stale-status-revision", "conditions": []any{map[string]any{"type": "AdmissionAccepted", "status": "TRUE", "observedGeneration": 2}}, "resolved": map[string]any{"storageClass": "default"}},
	}
}

func TestRepositoryListWatcherListsRepositoriesAcrossNamespaces(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var init map[string]any
			if conn.ReadJSON(&init) != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
			var subscribe struct {
				ID      string `json:"id"`
				Payload struct {
					Variables map[string]any `json:"variables"`
					Query     string         `json:"query"`
				} `json:"payload"`
			}
			if conn.ReadJSON(&subscribe) != nil {
				return
			}
			if _, found := subscribe.Payload.Variables["namespace"]; found {
				t.Error("global repository watch must not send namespace")
			}
			if !strings.Contains(subscribe.Payload.Query, " revision ") {
				t.Error("watch must select metadata.revision")
			}
			_ = conn.WriteJSON(map[string]any{"id": subscribe.ID, "type": "next", "payload": map[string]any{"data": map[string]any{"watchRepositories": map[string]any{"type": "BOOKMARK", "name": "", "resourceVersion": "cursor-7"}}}})
			return
		}
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.Query, "namespaces(") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"namespaces": map[string]any{"edges": []any{
				map[string]any{"node": map[string]any{"metadata": map[string]any{"name": "acme"}}},
				map[string]any{"node": map[string]any{"metadata": map[string]any{"name": "beta"}}},
			}, "pageInfo": map[string]any{"hasNextPage": false}}}})
			return
		}
		namespace, _ := request.Variables["namespace"].(string)
		if !strings.Contains(request.Query, " revision ") {
			t.Error("list must select metadata.revision")
		}
		if namespace != "acme" && namespace != "beta" {
			t.Fatalf("namespace = %#v, want acme or beta", request.Variables["namespace"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repositories": map[string]any{"edges": []any{map[string]any{"node": repositoryNodeJSON(namespace, "catalog", "8")}}, "pageInfo": map[string]any{"hasNextPage": false}}}})
	}))
	defer srv.Close()

	lw := listwatch.NewRepositoryListWatcher(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	response, err := lw.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(response.Items) != 2 || response.Items[0].Namespace != "acme" || response.Items[1].Namespace != "beta" || response.Items[0].StorageClass != "default" {
		t.Fatalf("Items = %+v, want repositories from acme and beta", response.Items)
	}
	if response.ResourceVersion != "cursor-7" {
		t.Fatalf("ResourceVersion = %q, want cursor-7", response.ResourceVersion)
	}
	for _, item := range response.Items {
		if item.Revision != "main@sha1:"+strings.Repeat("8", 40) || item.Status.LastAppliedRevision != "stale-status-revision" {
			t.Fatalf("authored/status revisions = %+v", item)
		}
	}
}

func TestRepositoryListWatcherMapsTypedWatchEvents(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		var init map[string]any
		if conn.ReadJSON(&init) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
		var subscribe struct {
			ID      string `json:"id"`
			Payload struct {
				Variables map[string]any `json:"variables"`
				Query     string         `json:"query"`
			} `json:"payload"`
		}
		if conn.ReadJSON(&subscribe) != nil {
			return
		}
		if _, found := subscribe.Payload.Variables["namespace"]; found {
			t.Error("global repository watch must not send namespace")
		}
		if !strings.Contains(subscribe.Payload.Query, " revision ") {
			t.Error("watch must select metadata.revision")
		}
		if subscribe.Payload.Variables["resourceVersion"] != "8" {
			t.Errorf("resourceVersion = %#v, want 8", subscribe.Payload.Variables["resourceVersion"])
		}
		_ = conn.WriteJSON(map[string]any{"id": subscribe.ID, "type": "next", "payload": map[string]any{"data": map[string]any{"watchRepositories": map[string]any{"type": "MODIFIED", "namespace": "acme", "name": "catalog", "resourceVersion": "9", "repository": repositoryNodeJSON("acme", "catalog", "9")}}}})
	}))
	defer srv.Close()
	lw := listwatch.NewRepositoryListWatcher(graphqlclient.New("ws"+strings.TrimPrefix(srv.URL, "http"), graphqlclient.NewStaticToken("token")))
	w, err := lw.Watch(context.Background(), "8")
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	defer w.Stop()
	select {
	case event := <-w.Events():
		if event.Type != listwatch.Modified || event.Object.Namespace != "acme" || event.Object.Name != "catalog" || event.Object.ResourceVersion != "9" {
			t.Fatalf("event = %+v, want modified acme/catalog/9", event)
		}
		if event.Object.Revision != "main@sha1:"+strings.Repeat("9", 40) || event.Object.Status.LastAppliedRevision != "stale-status-revision" {
			t.Fatalf("authored/status revisions = %+v", event.Object)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for repository event")
	}
}

func TestRepositoryListWatcherMapsExpiredCursor(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		var message map[string]any
		if conn.ReadJSON(&message) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
		if conn.ReadJSON(&message) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": message["id"], "type": "error", "payload": []any{map[string]any{"message": "expired", "extensions": map[string]any{"code": "WATCH_EXPIRED"}}}})
	}))
	defer srv.Close()
	w, err := listwatch.NewRepositoryListWatcher(graphqlclient.New("ws"+strings.TrimPrefix(srv.URL, "http"), graphqlclient.NewStaticToken("token"))).Watch(context.Background(), "stale")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	select {
	case <-w.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for expiry")
	}
	if !errors.Is(w.Err(), listwatch.ErrWatchExpired) {
		t.Fatalf("Err() = %v, want WATCH_EXPIRED", w.Err())
	}
}
