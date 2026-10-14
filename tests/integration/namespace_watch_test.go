// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	added := readNamespaceWatchTransition(t, watchA, firstName)
	assert.Equal(t, "ADDED", added.Type)
	assert.Equal(t, firstName, added.Name)
	require.NotEmpty(t, added.ResourceVersion)
	_ = watchA.Close()

	watchB := openNamespaceWatch(t, apiB, token, added.ResourceVersion)
	secondName := uniqueName("watch-replica-a")
	createNamespaceThrough(t, apiA, token, secondName)
	resumed := readNamespaceWatchTransition(t, watchB, secondName)
	assert.Equal(t, "ADDED", resumed.Type)
	assert.Equal(t, secondName, resumed.Name)
	assert.NotEqual(t, added.ResourceVersion, resumed.ResourceVersion)
}

func TestNamespaceWatchMetadataGenerationAndSelectorReplay(t *testing.T) {
	apiA, apiB, token := os.Getenv("NAMESPACE_WATCH_API_A"), os.Getenv("NAMESPACE_WATCH_API_B"), os.Getenv("NAMESPACE_WATCH_TOKEN")
	if apiA == "" || apiB == "" || apiA == apiB || token == "" {
		t.Skip("set two distinct NAMESPACE_WATCH_API endpoints and NAMESPACE_WATCH_TOKEN for the replica probe")
	}
	name := uniqueName("watch-metadata")
	selector := map[string]any{"matchLabels": map[string]any{"watch-metadata-run": name, "selected": "yes"}}
	watchA := openNamespaceWatch(t, apiA, token, "__namespace_watch_bootstrap__", selector)
	require.Equal(t, "BOOKMARK", readNamespaceWatchEvent(t, watchA).Type)
	createNamespaceThrough(t, apiB, token, name)
	update := func(endpoint, phase string, selected bool) string {
		t.Helper()
		labels := map[string]any{"watch-metadata-run": name}
		if selected {
			labels["selected"] = "yes"
		}
		response := gqlQueryWithURL(t, strings.TrimSuffix(endpoint, "/"), token, `
				mutation($input: UpdateNamespaceInput!) {
					updateNamespace(input: $input) { namespace { metadata { generation resourceVersion } } }
				}`, map[string]any{"input": map[string]any{
			"metadata": map[string]any{
				"name": name, "labels": labels, "annotations": map[string]any{"watch-metadata-phase": phase},
			},
			"spec": map[string]any{"title": name, "tier": "ORGANIZATION"},
		}})
		require.Empty(t, response.Errors, namespaceContractErrors(response.Errors))
		var result struct {
			Update struct {
				Namespace struct {
					Metadata struct {
						Generation      int64  `json:"generation"`
						ResourceVersion string `json:"resourceVersion"`
					} `json:"metadata"`
				} `json:"namespace"`
			} `json:"updateNamespace"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &result))
		require.EqualValues(t, 1, result.Update.Namespace.Metadata.Generation, "metadata-only authoring must not advance generation")
		require.NotEmpty(t, result.Update.Namespace.Metadata.ResourceVersion)
		return result.Update.Namespace.Metadata.ResourceVersion
	}
	readPhase := func(conn *websocket.Conn, eventType, phase, revision string) namespaceWatchWireEvent {
		t.Helper()
		for range 100 {
			event := readNamespaceWatchTransition(t, conn, name)
			if event.Name != name || event.Type != eventType {
				continue
			}
			if eventType == "DELETED" {
				require.Empty(t, event.Namespace, "selector exits retain the typed watch's payload-free deletion contract")
				return event
			}
			metadata, ok := event.Namespace["metadata"].(map[string]any)
			require.True(t, ok)
			if revision != "" && metadata["revision"] != revision {
				continue
			}
			if phase != "" {
				annotations, ok := metadata["annotations"].(map[string]any)
				require.True(t, ok)
				if annotations["watch-metadata-phase"] != phase {
					continue
				}
			}
			require.Equal(t, float64(1), metadata["generation"])
			require.NotEmpty(t, metadata["resourceVersion"])
			return event
		}
		t.Fatal("metadata watch did not expose the required transition")
		return namespaceWatchWireEvent{}
	}
	enteredVersion := update(apiA, "entered", true)
	entered := readPhase(watchA, "ADDED", "entered", "")
	require.NoError(t, watchA.Close())
	modifiedVersion := update(apiB, "modified", true)
	exitedVersion := update(apiA, "exited", false)
	require.NotEqual(t, enteredVersion, modifiedVersion)
	require.NotEqual(t, modifiedVersion, exitedVersion)
	watchB := openNamespaceWatch(t, apiB, token, entered.ResourceVersion, selector)
	modified := readPhase(watchB, "MODIFIED", "modified", "")
	exited := readPhase(watchB, "DELETED", "", "")
	require.NotEqual(t, entered.ResourceVersion, modified.ResourceVersion)
	require.NotEqual(t, modified.ResourceVersion, exited.ResourceVersion)
	update(apiB, "provenance", true)
	reentered := readPhase(watchB, "ADDED", "provenance", "")
	h := newPushHelperForRepoWithToken(t, "gitstore-system", "gitstore-system", token)
	content, err := os.ReadFile(filepath.Join(h.workDir, "namespaces", name+".md"))
	require.NoError(t, err)
	provenanceOnly := strings.Replace(string(content), "---\n", "---\n# provenance-only rewrite\n", 1)
	require.NotEqual(t, string(content), provenanceOnly)
	h.commitNamespace(name+".md", provenanceOnly)
	revision, err := secretCapacityGit(t.Context(), h.workDir, token, "rev-parse", "HEAD")
	require.NoError(t, err)
	if output, pushErr := h.push(); pushErr != nil {
		t.Fatalf("push provenance-only Namespace rewrite: %v\n%s", pushErr, output)
	}
	provenance := readPhase(watchB, "MODIFIED", "provenance", "main@sha1:"+strings.TrimSpace(string(revision)))
	require.NotEqual(t, reentered.ResourceVersion, provenance.ResourceVersion)
}

type namespaceWatchWireEvent struct {
	Type            string         `json:"type"`
	Name            string         `json:"name"`
	ResourceVersion string         `json:"resourceVersion"`
	Namespace       map[string]any `json:"namespace"`
}

func openNamespaceWatch(t *testing.T, apiURL, token, cursor string, selectors ...map[string]any) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(strings.TrimSuffix(apiURL, "/"), "http", "ws", 1) + "/graphql"
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, _, err := dialer.Dial(wsURL, header)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	var selector map[string]any
	if len(selectors) != 0 {
		selector = selectors[0]
	}
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
			"query":     `subscription($cursor: String, $selector: LabelSelectorInput) { watchNamespaces(resourceVersion: $cursor, selector: $selector) { type name resourceVersion namespace { metadata { name resourceVersion generation revision finalizers labels annotations } status { observedGeneration conditions { type status reason } } } } }`,
			"variables": map[string]any{"cursor": cursor, "selector": selector},
		},
	}))
	return conn
}

func readNamespaceWatchEvent(t *testing.T, conn *websocket.Conn) namespaceWatchWireEvent {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
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

func readNamespaceWatchTransition(t *testing.T, conn *websocket.Conn, name string) namespaceWatchWireEvent {
	t.Helper()
	for range 100 {
		event := readNamespaceWatchEvent(t, conn)
		if event.Type != "BOOKMARK" && event.Name == name {
			return event
		}
	}
	t.Fatalf("Namespace watch did not expose a transition for %s", name)
	return namespaceWatchWireEvent{}
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
		"spec":       map[string]any{"title": name, "tier": "ORGANIZATION"},
	}})
	require.Empty(t, response.Errors, namespaceContractErrors(response.Errors))
}
