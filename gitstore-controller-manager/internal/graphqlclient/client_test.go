// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient_test

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
	"github.com/gorilla/websocket"
)

// ── Query / Mutate ──────────────────────────────────────────────────────────

func TestQuery_SendsPostWithBearerAndDecodesData(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"categories":{"totalCount":3}}}`))
	}))
	defer srv.Close()

	c := graphqlclient.New(srv.URL, "test-token")
	var out struct {
		Categories struct {
			TotalCount int `json:"totalCount"`
		} `json:"categories"`
	}
	if err := c.Query(context.Background(), `query { categories { totalCount } }`, nil, &out); err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if out.Categories.TotalCount != 3 {
		t.Errorf("TotalCount = %d, want 3", out.Categories.TotalCount)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-token")
	}
	if _, ok := gotBody["query"]; !ok {
		t.Error("request body missing 'query' field")
	}
}

func TestMutate_DecodesData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateCategoryStatus":{"category":{"metadata":{"resourceVersion":"2"}}}}}`))
	}))
	defer srv.Close()

	c := graphqlclient.New(srv.URL, "test-token")
	var out struct {
		UpdateCategoryStatus struct {
			Category struct {
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
			} `json:"category"`
		} `json:"updateCategoryStatus"`
	}
	if err := c.Mutate(context.Background(), `mutation { updateCategoryStatus(input: {}) { category { metadata { resourceVersion } } } }`, nil, &out); err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}
	if out.UpdateCategoryStatus.Category.Metadata.ResourceVersion != "2" {
		t.Errorf("ResourceVersion = %q, want %q", out.UpdateCategoryStatus.Category.Metadata.ResourceVersion, "2")
	}
}

func TestQuery_GraphQLErrorSurfacesExtensionsCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"not found","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer srv.Close()

	c := graphqlclient.New(srv.URL, "test-token")
	var out struct{}
	err := c.Query(context.Background(), `query { category(by: {}) { id } }`, nil, &out)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var gqlErr *graphqlclient.Error
	if !errors.As(err, &gqlErr) {
		t.Fatalf("expected *graphqlclient.Error, got %T: %v", err, err)
	}
	if gqlErr.Extensions["code"] != "NOT_FOUND" {
		t.Errorf("Extensions[code] = %v, want NOT_FOUND", gqlErr.Extensions["code"])
	}
	if gqlErr.Message != "not found" {
		t.Errorf("Message = %q, want %q", gqlErr.Message, "not found")
	}
}

func TestQuery_HTTPErrorStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := graphqlclient.New(srv.URL, "test-token")
	var out struct{}
	if err := c.Query(context.Background(), `query { categories { totalCount } }`, nil, &out); err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
}

func TestQuery_VariablesSentInRequestBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	c := graphqlclient.New(srv.URL, "test-token")
	var out struct{}
	if err := c.Query(context.Background(), `query($ns: String!) { categories(namespace: $ns) { totalCount } }`, map[string]any{"ns": "acme"}, &out); err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	vars, ok := gotBody["variables"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'variables' map, got %+v", gotBody)
	}
	if vars["ns"] != "acme" {
		t.Errorf("variables[ns] = %v, want %q", vars["ns"], "acme")
	}
}

// ── Subscribe ────────────────────────────────────────────────────────────────

// stubGraphQLWSServer implements just enough of the graphql-transport-ws
// protocol (https://github.com/enisdenjo/graphql-ws/blob/master/PROTOCOL.md)
// to exercise Client.Subscribe: connection_init -> connection_ack, then
// subscribe -> a scripted sequence of next/error messages.
func stubGraphQLWSServer(t *testing.T, messages []map[string]any) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"graphql-transport-ws"},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		var initMsg map[string]any
		if err := conn.ReadJSON(&initMsg); err != nil {
			t.Errorf("read connection_init failed: %v", err)
			return
		}
		if initMsg["type"] != "connection_init" {
			t.Errorf("first message type = %v, want connection_init", initMsg["type"])
			return
		}
		if err := conn.WriteJSON(map[string]any{"type": "connection_ack"}); err != nil {
			t.Errorf("write connection_ack failed: %v", err)
			return
		}

		var subMsg map[string]any
		if err := conn.ReadJSON(&subMsg); err != nil {
			t.Errorf("read subscribe failed: %v", err)
			return
		}
		if subMsg["type"] != "subscribe" {
			t.Errorf("second message type = %v, want subscribe", subMsg["type"])
			return
		}
		id := subMsg["id"]

		for _, m := range messages {
			out := map[string]any{"id": id}
			maps.Copy(out, m)
			if err := conn.WriteJSON(out); err != nil {
				return // client likely disconnected (e.g. Stop() called)
			}
		}
	}))
}

func TestSubscribe_HandshakeAndNextMessages(t *testing.T) {
	srv := stubGraphQLWSServer(t, []map[string]any{
		{"type": "next", "payload": map[string]any{"data": map[string]any{"watchCategories": map[string]any{"name": "electronics"}}}},
		{"type": "next", "payload": map[string]any{"data": map[string]any{"watchCategories": map[string]any{"name": "furniture"}}}},
	})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := graphqlclient.New(wsURL, "test-token")
	sub, err := c.Subscribe(context.Background(), `subscription { watchCategories { name } }`, nil)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Stop()

	for i := range 2 {
		select {
		case msg, ok := <-sub.Next():
			if !ok {
				t.Fatalf("channel closed early on message %d", i)
			}
			if len(msg) == 0 {
				t.Errorf("message %d is empty", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for subscription message")
		}
	}
}

func TestSubscribe_StopClosesChannel(t *testing.T) {
	srv := stubGraphQLWSServer(t, nil)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := graphqlclient.New(wsURL, "test-token")
	sub, err := c.Subscribe(context.Background(), `subscription { watchCategories { name } }`, nil)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	sub.Stop()

	select {
	case _, ok := <-sub.Next():
		if ok {
			t.Error("channel should be closed after Stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close after Stop")
	}
}

func TestSubscribe_ServerErrorSurfacedViaErr(t *testing.T) {
	srv := stubGraphQLWSServer(t, []map[string]any{
		{"type": "error", "payload": []map[string]any{{"message": "watch cursor expired", "extensions": map[string]any{"code": "WATCH_EXPIRED"}}}},
	})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := graphqlclient.New(wsURL, "test-token")
	sub, err := c.Subscribe(context.Background(), `subscription { watchCategories { name } }`, nil)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Stop()

	select {
	case _, ok := <-sub.Next():
		if ok {
			t.Error("channel should close on server error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close on server error")
	}

	if sub.Err() == nil {
		t.Fatal("expected Err() to be non-nil after server error")
	}
	var gqlErr *graphqlclient.Error
	if !errors.As(sub.Err(), &gqlErr) {
		t.Fatalf("expected *graphqlclient.Error, got %T: %v", sub.Err(), sub.Err())
	}
	if gqlErr.Extensions["code"] != "WATCH_EXPIRED" {
		t.Errorf("Extensions[code] = %v, want WATCH_EXPIRED", gqlErr.Extensions["code"])
	}
}

// TestSubscribe_NextMessageWithErrorsSurfacedViaErr covers gqlgen's actual
// wire behavior: a resolver-returned GraphQL error raised before the
// subscription stream opens (e.g. WATCH_EXPIRED from watchCategories) is
// delivered as a "next" message with a populated errors array and a null
// data field — not as a protocol-level "error" message. A client that only
// checks msg.Type == "error" would silently decode this into a zero-valued
// event instead of surfacing the error.
func TestSubscribe_NextMessageWithErrorsSurfacedViaErr(t *testing.T) {
	srv := stubGraphQLWSServer(t, []map[string]any{
		{"type": "next", "payload": map[string]any{
			"data":   map[string]any{"watchCategories": nil},
			"errors": []map[string]any{{"message": "watch cursor expired; re-list and resume from a fresh cursor", "extensions": map[string]any{"code": "WATCH_EXPIRED"}}},
		}},
	})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := graphqlclient.New(wsURL, "test-token")
	sub, err := c.Subscribe(context.Background(), `subscription { watchCategories { name } }`, nil)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Stop()

	select {
	case _, ok := <-sub.Next():
		if ok {
			t.Error("channel should close when a next message carries errors")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}

	var gqlErr *graphqlclient.Error
	if !errors.As(sub.Err(), &gqlErr) {
		t.Fatalf("expected *graphqlclient.Error, got %T: %v", sub.Err(), sub.Err())
	}
	if gqlErr.Extensions["code"] != "WATCH_EXPIRED" {
		t.Errorf("Extensions[code] = %v, want WATCH_EXPIRED", gqlErr.Extensions["code"])
	}
}
