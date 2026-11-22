// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/translation"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func translationNode() map[string]any {
	node := categoryNodeJSON("uid", "fr", "shop", "3", 1, "")
	node["spec"] = map[string]any{"locale": "fr", "targetRef": map[string]any{"name": "target"}}
	return node
}

func resourceListWatchServer(t *testing.T, events []map[string]any, httpHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			httpHandler(w, r)
			return
		}
		upgrader := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		var init map[string]any
		if conn.ReadJSON(&init) != nil {
			return
		}
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "connection_ack"}))
		var subscribe struct {
			ID      string `json:"id"`
			Payload struct {
				Variables map[string]any `json:"variables"`
			} `json:"payload"`
		}
		if conn.ReadJSON(&subscribe) != nil {
			return
		}
		batch := events
		if subscribe.Payload.Variables["resourceVersion"] == "__resource_watch_bootstrap__" {
			batch = []map[string]any{{"type": "BOOKMARK", "name": "", "resourceVersion": "journal-before-list"}}
		}
		for _, event := range batch {
			require.NoError(t, conn.WriteJSON(map[string]any{"id": subscribe.ID, "type": "next",
				"payload": map[string]any{"data": map[string]any{"event": event}}}))
		}
		_ = conn.WriteJSON(map[string]any{"id": subscribe.ID, "type": "complete"})
	}))
}

func TestTranslationListPagesBootstrapAndBackpressure(t *testing.T) {
	for _, mode := range []string{"complete", "visitor-failed", "stuck-cursor", "missing-page"} {
		t.Run(mode, func(t *testing.T) {
			var pages atomic.Int32
			server := resourceListWatchServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				w.Header().Set("Content-Type", "application/json")
				if _, namespaced := request.Variables["namespace"]; !namespaced {
					_, _ = w.Write([]byte(`{"data":{"namespaces":{"edges":[{"node":{"metadata":{"name":"shop"}}}],"pageInfo":{"hasNextPage":false}}}}`))
					return
				}
				page := pages.Add(1)
				require.Equal(t, "shop", request.Variables["namespace"])
				if mode == "missing-page" {
					_, _ = w.Write([]byte(`{"data":{"resources":null}}`))
					return
				}
				if page == 2 {
					require.Equal(t, "next", request.Variables["after"])
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"resources": map[string]any{"edges": []any{map[string]any{"node": translationNode()}},
						"pageInfo": map[string]any{"hasNextPage": page == 1 || mode == "stuck-cursor", "endCursor": "next"}},
				}}))
			})
			defer server.Close()
			watcher := listwatch.NewTranslationListWatcher(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
			called := 0
			visitorErr := errors.New("checkpoint unavailable")
			cursor, err := watcher.ListPages(t.Context(), func(page []translation.Resource) error {
				called++
				require.Len(t, page, 1)
				require.Equal(t, "3", page[0].Metadata.ResourceVersion)
				if mode == "visitor-failed" {
					return visitorErr
				}
				return nil
			})
			switch mode {
			case "complete":
				require.NoError(t, err)
				require.Equal(t, "journal-before-list", cursor)
				require.Equal(t, 2, called)
			case "visitor-failed":
				require.ErrorIs(t, err, visitorErr)
				require.Empty(t, cursor)
				require.Equal(t, int32(1), pages.Load(), "do not fetch ahead of persisted page work")
			default:
				require.Error(t, err)
				require.Empty(t, cursor)
			}
		})
	}
}

func TestTranslationWatchKeepsJournalAndSourceVersionsSeparate(t *testing.T) {
	server := resourceListWatchServer(t, []map[string]any{
		{"type": "ADDED", "namespace": "shop", "name": "fr", "resourceVersion": "journal-4", "resource": translationNode(), "historical": true},
		{"type": "DELETED", "namespace": "shop", "name": "fr", "resourceVersion": "journal-5", "resource": nil, "historical": true},
	}, nil)
	defer server.Close()
	watch, err := listwatch.NewTranslationListWatcher(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token"))).Watch(t.Context(), "journal-3")
	require.NoError(t, err)
	defer watch.Stop()
	var events []listwatch.WatchEvent[translation.Resource]
	for event := range watch.Events() {
		events = append(events, event)
	}
	require.NoError(t, watch.Err())
	require.Len(t, events, 2)
	require.Equal(t, "3", events[0].Object.Metadata.ResourceVersion)
	require.Equal(t, "journal-4", events[0].ResourceVersion)
	require.Equal(t, listwatch.Deleted, events[1].Type)
	require.Equal(t, "fr", events[1].Object.Metadata.Name)
	require.Equal(t, "shop", events[1].Object.Metadata.Namespace)
	require.Empty(t, events[1].Object.Metadata.ResourceVersion, "a tombstone cursor is not a source CAS version")
}

func TestTranslationWatchRejectsIncompleteOrInternalPayloads(t *testing.T) {
	for _, mode := range []string{"missing-object", "unknown-event", "yaml-condition"} {
		t.Run(mode, func(t *testing.T) {
			event := map[string]any{"type": "ADDED", "namespace": "shop", "name": "fr", "resourceVersion": "cursor"}
			switch mode {
			case "unknown-event":
				event["type"] = "Changed"
			case "yaml-condition":
				node := translationNode()
				node["status"] = map[string]any{"conditions": []any{map[string]any{"status": "True"}}}
				event["resource"] = node
			}

			server := resourceListWatchServer(t, []map[string]any{event}, nil)
			defer server.Close()
			watch, err := listwatch.NewTranslationListWatcher(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token"))).Watch(t.Context(), "previous")
			require.NoError(t, err)
			defer watch.Stop()
			_, open := <-watch.Events()
			require.False(t, open)
			require.Error(t, watch.Err())
		})
	}
}

func TestTranslationWatchDecodesCurrentGraphQLEnumsAfterHistoricalSource(t *testing.T) {
	for _, condition := range []string{"TRUE", "FALSE", "UNKNOWN"} {
		t.Run(condition, func(t *testing.T) {
			current := translationNode()
			current["status"] = map[string]any{
				"observedGeneration": 1, "lastAppliedRevision": "main@sha1:accepted",
				"conditions": []any{map[string]any{"type": "Ready", "status": condition}},
			}
			server := resourceListWatchServer(t, []map[string]any{
				{"type": "MODIFIED", "namespace": "shop", "name": "fr", "resourceVersion": "journal-4", "resource": translationNode(), "historical": true},
				{"type": "MODIFIED", "namespace": "shop", "name": "fr", "resourceVersion": "journal-5", "resource": current, "historical": false},
			}, nil)
			defer server.Close()
			watch, err := listwatch.NewTranslationListWatcher(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token"))).Watch(t.Context(), "journal-3")
			require.NoError(t, err)
			defer watch.Stop()
			var events []listwatch.WatchEvent[translation.Resource]
			for event := range watch.Events() {
				events = append(events, event)
			}
			require.NoError(t, watch.Err())
			require.Len(t, events, 2)
			require.Empty(t, events[0].Object.Status.Conditions)
			require.Len(t, events[1].Object.Status.Conditions, 1)
			require.Equal(t, condition, events[1].Object.Status.Conditions[0].Status)
			require.Equal(t, "3", events[1].Object.Status.ResourceVersion, "status CAS uses the source version, not its watch cursor")
			require.Equal(t, "main@sha1:accepted", events[1].Object.Status.LastAppliedRevision)
		})
	}
}
