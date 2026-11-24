// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/stretchr/testify/require"
)

func TestTranslationTargetMetadataPagesAndWatch(t *testing.T) {
	for _, kind := range []string{"ProductVariant", "Collection", "File"} {
		for _, mode := range []string{"complete", "visitor-failed", "wrong-namespace", "wrong-kind", "oversized"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				node := map[string]any{
					"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": kind,
					"metadata": map[string]any{
						"uid": "relay-target-id", "namespace": "shop", "name": "target",
						"resourceVersion": "3", "generation": 2, "revision": "main@target",
					},
				}
				var requests atomic.Int32
				server := resourceListWatchServer(t, []map[string]any{
					{"type": "MODIFIED", "namespace": "shop", "name": "target", "resourceVersion": "journal-4", "resource": node},
					{"type": "DELETED", "namespace": "shop", "name": "target", "resourceVersion": "journal-5"},
				}, func(w http.ResponseWriter, r *http.Request) {
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
					page := requests.Add(1)
					require.NotContains(t, request.Query, "spec")
					require.NotContains(t, request.Query, "body")
					require.NotContains(t, request.Query, "status")
					if mode == "wrong-namespace" {
						node["metadata"].(map[string]any)["namespace"] = "elsewhere"
					}
					if mode == "wrong-kind" {
						node["kind"] = "Product"
					}
					edges := []any{map[string]any{"node": node}}
					if mode == "oversized" {
						edges = make([]any, 101)
						for i := range edges {
							edges[i] = map[string]any{"node": node}
						}
					}
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
						"resources": map[string]any{"edges": edges,
							"pageInfo": map[string]any{"hasNextPage": page == 1, "endCursor": "next"}},
					}}))
				})
				defer server.Close()
				watcher, err := listwatch.NewTranslationTargetListWatcher(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")), kind)
				require.NoError(t, err)
				calls := 0
				visitorErr := errors.New("dependent checkpoint unavailable")
				cursor, err := watcher.ListPages(t.Context(), func(page []listwatch.TargetMetadataResource) error {
					calls++
					require.Len(t, page, 1)
					require.Equal(t, kind, page[0].Kind)
					require.Equal(t, "3", page[0].Metadata.ResourceVersion)
					if mode == "visitor-failed" {
						return visitorErr
					}
					return nil
				})
				if mode != "complete" {
					require.Error(t, err)
					require.Empty(t, cursor)
					require.EqualValues(t, 1, requests.Load())
					if mode == "visitor-failed" {
						require.ErrorIs(t, err, visitorErr)
					} else {
						require.Zero(t, calls)
					}
					return
				}
				require.NoError(t, err)
				require.Equal(t, "journal-before-list", cursor)
				require.Equal(t, 2, calls)
				watch, err := watcher.Watch(t.Context(), cursor)
				require.NoError(t, err)
				defer watch.Stop()
				var events []listwatch.WatchEvent[listwatch.TargetMetadataResource]
				for event := range watch.Events() {
					events = append(events, event)
				}
				require.NoError(t, watch.Err())
				require.Len(t, events, 2)
				require.Equal(t, kind, events[0].Object.Kind)
				require.Equal(t, "3", events[0].Object.Metadata.ResourceVersion)
				require.Equal(t, "journal-4", events[0].ResourceVersion)
				require.Equal(t, listwatch.Deleted, events[1].Type)
				require.Equal(t, "target", events[1].Object.Metadata.Name)
				require.Empty(t, events[1].Object.Metadata.ResourceVersion)
			})
		}
	}
}
