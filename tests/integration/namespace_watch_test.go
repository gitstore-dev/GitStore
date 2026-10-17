// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	runNamespaceWatchMetadataGenerationAndSelectorReplay(t, apiA, apiB, token)
}

func runNamespaceWatchMetadataGenerationAndSelectorReplay(t *testing.T, apiA, apiB, token string) {
	t.Helper()
	require.NotEmpty(t, apiA)
	require.NotEmpty(t, apiB)
	require.NotEmpty(t, token)
	require.NotEqual(t, apiA, apiB)
	name := uniqueName("watch-metadata")
	selector := map[string]any{"matchLabels": map[string]any{"watch-metadata-run": name, "selected": "yes"}}
	watchA := openNamespaceWatch(t, apiA, token, "__namespace_watch_bootstrap__", selector)
	require.Equal(t, "BOOKMARK", readNamespaceWatchEvent(t, watchA).Type)
	createWatchMetadataNamespace(t, apiB, apiA, token, name)
	update := func(endpoint, phase string, selected bool) string {
		t.Helper()
		labels := map[string]any{"watch-metadata-run": name}
		if selected {
			labels["selected"] = "yes"
		}
		response := watchMetadataGraphQL(t, strings.TrimSuffix(endpoint, "/"), token, `
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
	readPhase := func(conn *websocket.Conn, eventType, phase, revision, version string) namespaceWatchWireEvent {
		return readNamespaceWatchMetadataPhase(t, conn, name, eventType, phase, revision, version)
	}
	enteredVersion := update(apiA, "entered", true)
	entered := readPhase(watchA, "ADDED", "entered", "", enteredVersion)
	require.NoError(t, watchA.Close())
	modifiedVersion := update(apiB, "modified", true)
	exitedVersion := update(apiA, "exited", false)
	require.NotEqual(t, enteredVersion, modifiedVersion)
	require.NotEqual(t, modifiedVersion, exitedVersion)
	watchB := openNamespaceWatch(t, apiB, token, entered.ResourceVersion, selector)
	modified := readPhase(watchB, "MODIFIED", "modified", "", modifiedVersion)
	exited := readPhase(watchB, "DELETED", "", "", "")
	require.NotEqual(t, entered.ResourceVersion, modified.ResourceVersion)
	require.NotEqual(t, modified.ResourceVersion, exited.ResourceVersion)
	reenteredVersion := update(apiB, "provenance", true)
	require.NotEqual(t, exitedVersion, reenteredVersion)
	reentered := readPhase(watchB, "ADDED", "provenance", "", reenteredVersion)
	h := newPushHelperForRepoWithToken(t, "gitstore-system", "gitstore-system", token, "namespaces/"+name+".md")
	content, err := os.ReadFile(filepath.Join(h.workDir, "namespaces", name+".md"))
	require.NoError(t, err)
	provenanceOnly := strings.Replace(string(content), "---\n", "---\n# provenance-only rewrite\n", 1)
	require.NotEqual(t, string(content), provenanceOnly)
	h.commitNamespace(name+".md", provenanceOnly)
	revision := pushWatchMetadataProvenance(t, h)
	provenance := readPhase(watchB, "MODIFIED", "provenance", revision, "")
	require.NotEqual(t, reentered.ResourceVersion, provenance.ResourceVersion)
	metadata, ok := provenance.Namespace["metadata"].(map[string]any)
	require.True(t, ok)
	require.NotEqual(t, reenteredVersion, metadata["resourceVersion"], "Git-only provenance must advance the resource version")
}

func readNamespaceWatchMetadataPhase(t *testing.T, conn *websocket.Conn, name, eventType, phase, revision, version string) namespaceWatchWireEvent {
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
		if version != "" && metadata["resourceVersion"] != version {
			continue
		}
		if revision != "" && metadata["revision"] != revision {
			t.Logf("metadata watch provenance mismatch resource_version=%s revision=%v expected=%s",
				event.ResourceVersion, metadata["revision"], revision)
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

func TestWatchMetadataPhaseRequiresAcknowledgedVersion(t *testing.T) {
	for _, kind := range []string{"Namespace", "Repository"} {
		t.Run(kind, func(t *testing.T) {
			for _, eventType := range []string{"ADDED", "MODIFIED"} {
				for _, noiseVersion := range []string{"older-authoring-version", "later-status-version"} {
					t.Run(eventType+"/"+noiseVersion, func(t *testing.T) {
						testWatchMetadataPhase(t, kind, eventType, "acknowledged-version", noiseVersion, "expected-revision")
					})
				}
			}
			t.Run("provenance-without-mutation-version", func(t *testing.T) {
				testWatchMetadataPhase(t, kind, "MODIFIED", "", "older-version", "older-revision")
			})
			t.Run("payload-free-selector-exit", func(t *testing.T) {
				testWatchMetadataPhase(t, kind, "DELETED", "", "", "")
			})
		})
	}
}

func testWatchMetadataPhase(t *testing.T, kind, eventType, expectedVersion, noiseVersion, noiseRevision string) {
	t.Helper()
	field, resource := "watchNamespaces", "namespace"
	if kind == "Repository" {
		field, resource = "watchRepositories", "repository"
	}
	event := func(name, eventType, cursor, version, revision string) map[string]any {
		result := map[string]any{"name": name, "type": eventType, "resourceVersion": cursor}
		if eventType == "ADDED" || eventType == "MODIFIED" {
			result[resource] = map[string]any{"metadata": map[string]any{
				"name": name, "generation": 1, "resourceVersion": version, "revision": revision,
				"annotations": map[string]any{"watch-metadata-phase": "expected-phase"},
			}}
		}
		return result
	}
	version := expectedVersion
	if version == "" {
		version = "provenance-version"
	}
	events := []map[string]any{
		event("", "BOOKMARK", "bookmark-cursor", "", ""),
		event("unrelated-resource", eventType, "unrelated-cursor", version, "expected-revision"),
	}
	if eventType != "DELETED" {
		events = append(events, event("probe", eventType, "noise-cursor", noiseVersion, noiseRevision))
	}
	events = append(events, event("probe", eventType, "expected-cursor", version, "expected-revision"))
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer conn.Close()
		for _, event := range events {
			if !assert.NoError(t, conn.WriteJSON(map[string]any{
				"type": "next", "payload": map[string]any{"data": map[string]any{field: event}},
			})) {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	if kind == "Namespace" {
		got := readNamespaceWatchMetadataPhase(t, conn, "probe", eventType, "expected-phase", "expected-revision", expectedVersion)
		require.Equal(t, "expected-cursor", got.ResourceVersion, "a different postimage cannot prove delivery of the acknowledged mutation")
		if eventType != "DELETED" {
			metadata, ok := got.Namespace["metadata"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, version, metadata["resourceVersion"])
		}
	} else {
		got := readRepositoryWatchMetadataPhase(t, conn, "probe", eventType, "expected-revision", expectedVersion)
		require.Equal(t, "expected-cursor", got.ResourceVersion, "a different postimage cannot prove delivery of the acknowledged mutation")
		if eventType != "DELETED" {
			require.Equal(t, version, got.Repository.Metadata.ResourceVersion)
		}
	}
}

func watchMetadataRetryable(errors []json.RawMessage) bool {
	if len(errors) == 0 {
		return false
	}
	for _, raw := range errors {
		if !capacityCreateRetryable(&capacityGraphQLErrors{raw: string(raw)}) {
			return false
		}
	}
	return true
}

func createWatchMetadataNamespace(t *testing.T, apiA, apiB, token, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	cfg := capacityConfig{apiA: apiA, apiB: apiB, token: token}
	require.NoError(t, createCapacityNamespaceRetryingContext(ctx, client, cfg, name, 30*time.Second))
}

func watchMetadataGraphQL(t *testing.T, endpoint, token, query string, variables map[string]any) gqlResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for attempt := 1; ; attempt++ {
		require.NoError(t, ctx.Err(), "metadata mutation exhausted its bounded retry window")
		response := secretTestGraphQLContext(t, ctx, endpoint, token, query, variables)
		require.Equal(t, http.StatusOK, response.Status)
		if len(response.Errors) == 0 {
			return response
		}
		require.True(t, watchMetadataRetryable(response.Errors), namespaceContractErrors(response.Errors))
		t.Logf("metadata mutation retry=%d classification=transient_graphql_conflict", attempt)
		timer := time.NewTimer(time.Duration(1<<min(attempt-1, 4)) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatalf("metadata mutation retry deadline: %v", ctx.Err())
		case <-timer.C:
		}
	}
}

func TestWatchMetadataRetryRequiresEveryErrorToBeTransient(t *testing.T) {
	conflict := json.RawMessage(`{"message":"superseded","extensions":{"code":"CONFLICT","diagnostics":[{"reason":"SUPERSEDED"}]}}`)
	denied := json.RawMessage(`{"message":"denied","extensions":{"code":"FORBIDDEN"}}`)
	require.True(t, watchMetadataRetryable([]json.RawMessage{conflict}))
	require.False(t, watchMetadataRetryable(nil))
	require.False(t, watchMetadataRetryable([]json.RawMessage{denied}))
	require.False(t, watchMetadataRetryable([]json.RawMessage{conflict, denied}))
	require.False(t, watchMetadataRetryable([]json.RawMessage{denied, conflict}))
}

func TestWatchMetadataMutationRetriesSupersededResponses(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"errors":[{"message":"superseded","extensions":{"code":"CONFLICT","diagnostics":[{"reason":"SUPERSEDED"}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"updateNamespace":{"namespace":{"metadata":{"generation":1,"resourceVersion":"accepted"}}}}}`))
	}))
	defer server.Close()
	response := watchMetadataGraphQL(t, server.URL, "test-token", "mutation { updateNamespace }", nil)
	require.Empty(t, response.Errors)
	require.JSONEq(t, `{"updateNamespace":{"namespace":{"metadata":{"generation":1,"resourceVersion":"accepted"}}}}`, string(response.Data))
	require.EqualValues(t, 2, calls.Load())
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
