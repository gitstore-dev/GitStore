// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch_test

import (
	"context"
	"encoding/json"
	"errors"
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

// ── List ─────────────────────────────────────────────────────────────────────

func categoryNodeJSON(uid, name, namespace, rv string, generation int, parentRefName string) map[string]any {
	spec := map[string]any{}
	if parentRefName != "" {
		spec["parentRef"] = map[string]any{"name": parentRefName}
	} else {
		spec["parentRef"] = nil
	}
	return map[string]any{
		"metadata": map[string]any{
			"uid":             uid,
			"name":            name,
			"namespace":       namespace,
			"resourceVersion": rv,
			"generation":      generation,
		},
		"spec": spec,
	}
}

func TestList_PaginatesToCompletionAndReturnsHighestResourceVersion(t *testing.T) {
	pages := []map[string]any{
		{
			"data": map[string]any{
				"categories": map[string]any{
					"edges": []any{
						map[string]any{"cursor": "c1", "node": categoryNodeJSON("uid-1", "electronics", "acme", "3", 1, "")},
					},
					"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c1"},
				},
			},
		},
		{
			"data": map[string]any{
				"categories": map[string]any{
					"edges": []any{
						map[string]any{"cursor": "c2", "node": categoryNodeJSON("uid-2", "computers", "acme", "7", 1, "electronics")},
					},
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": "c2"},
				},
			},
		},
	}
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "namespaces(") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"namespaces": map[string]any{
						"edges": []any{
							map[string]any{"cursor": "n1", "node": map[string]any{"identifier": "acme"}},
						},
						"pageInfo": map[string]any{"hasNextPage": false, "endCursor": "n1"},
					},
				},
			})
			return
		}
		body := pages[call]
		if call < len(pages)-1 {
			call++
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	lw := listwatch.NewCategoryTaxonomyListWatcher(client)

	resp, err := lw.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(resp.Items))
	}
	if resp.Items[0].Name != "electronics" || resp.Items[1].Name != "computers" {
		t.Errorf("unexpected item names: %+v", resp.Items)
	}
	if resp.Items[1].ParentRefName != "electronics" {
		t.Errorf("Items[1].ParentRefName = %q, want %q", resp.Items[1].ParentRefName, "electronics")
	}
	if resp.ResourceVersion != "7" {
		t.Errorf("ResourceVersion = %q, want %q (highest observed)", resp.ResourceVersion, "7")
	}
}

func TestList_EmptyDatasetReturnsNonEmptySentinelResourceVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "namespaces(") {
			_, _ = w.Write([]byte(`{"data":{"namespaces":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"categories":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	lw := listwatch.NewCategoryTaxonomyListWatcher(client)

	resp, err := lw.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(resp.Items) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(resp.Items))
	}
	if resp.ResourceVersion == "" {
		t.Error("ResourceVersion must not be empty even for a zero-item list (checkpoint.FilesystemStore rejects an empty cursor)")
	}
}

func TestList_HTTPErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	lw := listwatch.NewCategoryTaxonomyListWatcher(client)

	if _, err := lw.List(context.Background()); err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
}

// ── Watch ────────────────────────────────────────────────────────────────────

func stubWatchCategoriesServer(t *testing.T, messages []map[string]any) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		id := subMsg["id"]
		for _, m := range messages {
			out := map[string]any{"id": id}
			maps.Copy(out, m)
			if err := conn.WriteJSON(out); err != nil {
				return
			}
		}
	}))
}

func TestWatch_MapsAddedModifiedDeletedEvents(t *testing.T) {
	srv := stubWatchCategoriesServer(t, []map[string]any{
		{"type": "next", "payload": map[string]any{"data": map[string]any{"watchCategories": map[string]any{
			"type": "ADDED", "namespace": "acme", "name": "electronics", "resourceVersion": "1",
			"category": categoryNodeJSON("uid-1", "electronics", "acme", "1", 1, ""),
		}}}},
		{"type": "next", "payload": map[string]any{"data": map[string]any{"watchCategories": map[string]any{
			"type": "MODIFIED", "namespace": "acme", "name": "electronics", "resourceVersion": "2",
			"category": categoryNodeJSON("uid-1", "electronics", "acme", "2", 2, ""),
		}}}},
		{"type": "next", "payload": map[string]any{"data": map[string]any{"watchCategories": map[string]any{
			"type": "DELETED", "namespace": "acme", "name": "electronics", "resourceVersion": "3",
			"category": nil,
		}}}},
	})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client := graphqlclient.New(wsURL, "test-token")
	lw := listwatch.NewCategoryTaxonomyListWatcher(client)

	watcher, err := lw.Watch(context.Background(), "")
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}
	defer watcher.Stop()

	wantTypes := []listwatch.EventType{listwatch.Added, listwatch.Modified, listwatch.Deleted}
	for i, want := range wantTypes {
		select {
		case ev, ok := <-watcher.Events():
			if !ok {
				t.Fatalf("channel closed early at event %d, err=%v", i, watcher.Err())
			}
			if ev.Type != want {
				t.Errorf("event %d Type = %v, want %v", i, ev.Type, want)
			}
			if ev.Object.Name != "electronics" {
				t.Errorf("event %d Object.Name = %q, want %q", i, ev.Object.Name, "electronics")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func TestWatch_ExpiredCursorMapsToErrWatchExpired(t *testing.T) {
	srv := stubWatchCategoriesServer(t, []map[string]any{
		{"type": "error", "payload": []map[string]any{{"message": "watch cursor expired", "extensions": map[string]any{"code": "WATCH_EXPIRED"}}}},
	})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client := graphqlclient.New(wsURL, "test-token")
	lw := listwatch.NewCategoryTaxonomyListWatcher(client)

	watcher, err := lw.Watch(context.Background(), "999")
	if err != nil {
		t.Fatalf("Watch failed synchronously: %v", err)
	}
	defer watcher.Stop()

	select {
	case _, ok := <-watcher.Events():
		if ok {
			t.Fatal("expected channel to close on server error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}

	if !errors.Is(watcher.Err(), listwatch.ErrWatchExpired) {
		t.Errorf("Err() = %v, want errors.Is(..., ErrWatchExpired)", watcher.Err())
	}
}
