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

// TestProductWatchRecoveryProbe uses the same shared-Scylla deployment as the
// alpha capacity gate. It proves that an expired cursor is rejected without
// disclosure and that a replacement API process can resume a Product cursor
// after the materializer lease is handed off.
func TestProductWatchRecoveryProbe(t *testing.T) {
	apiA := strings.TrimSuffix(os.Getenv("PRODUCT_WATCH_API_A"), "/")
	apiB := strings.TrimSuffix(os.Getenv("PRODUCT_WATCH_API_B"), "/")
	token := productWatchToken(t)
	if apiA == "" || apiB == "" || token == "" {
		t.Skip("set PRODUCT_WATCH_API_A, PRODUCT_WATCH_API_B, and PRODUCT_WATCH_TOKEN")
	}

	bootstrap := openProductWatch(t, apiA, token, "__product_watch_bootstrap__")
	cursor := readProductWatchEvent(t, bootstrap).ResourceVersion
	require.NoError(t, bootstrap.Close())

	t.Run("forced epoch expiry", func(t *testing.T) {
		expired := openProductWatch(t, apiA, token, "rwv1:00000000-0000-4000-8000-000000000001:0")
		requireNamespaceWatchWireError(t, expired, "WATCH_EXPIRED", "EPOCH_MISMATCH")
	})

	t.Run("rolling replacement and lease handoff", func(t *testing.T) {
		replacement := strings.TrimSuffix(os.Getenv("PRODUCT_WATCH_API_REPLACEMENT"), "/")
		trigger := os.Getenv("PRODUCT_WATCH_REPLACEMENT_TRIGGER_FILE")
		require.NotEmpty(t, replacement, "deployment harness must select a replacement endpoint")
		require.Contains(t, []string{apiA, apiB}, replacement, "replacement must identify one of the live replicas")
		require.NotEmpty(t, trigger, "deployment harness must provide PRODUCT_WATCH_REPLACEMENT_TRIGGER_FILE")
		_, statErr := os.Stat(trigger)
		require.ErrorIs(t, statErr, os.ErrNotExist, "replacement trigger must not exist before the probe")

		client := &http.Client{Timeout: 5 * time.Second}
		before, err := fetchCapacityMetrics(client, replacement)
		require.NoError(t, err, "scrape replacement identity before trigger")
		require.NoError(t, os.WriteFile(trigger, []byte("replace product recovery probe\n"), 0o600))

		probeClient := &http.Client{Timeout: 500 * time.Millisecond}
		outageDeadline := time.Now().Add(30 * time.Second)
		for endpointReady(probeClient, replacement) && time.Now().Before(outageDeadline) {
			time.Sleep(100 * time.Millisecond)
		}
		require.False(t, endpointReady(probeClient, replacement), "replacement trigger must produce an observed outage")

		var after capacityProcessMetrics
		recoveryDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(recoveryDeadline) {
			if endpointReady(probeClient, replacement) {
				candidate, metricsErr := fetchCapacityMetrics(client, replacement)
				if metricsErr == nil && candidate.processStart != before.processStart {
					after = candidate
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.NotZero(t, after.processStart, "replacement must return with a changed process_start_time_seconds")

		watch := openProductWatch(t, replacement, token, cursor)
		peer := apiA
		if replacement == apiA {
			peer = apiB
		}
		name := uniqueName("product-watch-handoff")
		createProductThrough(t, peer, token, name)
		event := readProductWatchTransition(t, watch)
		require.Equal(t, name, event.Name)
	})
}

// TestProductCapacityGitPushParity is intentionally deployment-driven.  It
// proves a real Git push is admitted by the same shared Scylla-backed catalog
// seen by the peer API replica; the GraphQL workload and durable-watch probes
// in the alpha gate cover the other two Product authoring boundaries.
func TestProductCapacityGitPushParity(t *testing.T) {
	apiA := strings.TrimSuffix(os.Getenv("PRODUCT_WATCH_API_A"), "/")
	apiB := strings.TrimSuffix(os.Getenv("PRODUCT_WATCH_API_B"), "/")
	token := productWatchToken(t)
	gitEndpoint := strings.TrimSuffix(os.Getenv("PRODUCT_CAPACITY_GIT_URL"), "/")
	namespace := os.Getenv("PRODUCT_CAPACITY_NAMESPACE")
	repository := os.Getenv("PRODUCT_CAPACITY_REPOSITORY")
	if apiA == "" || apiB == "" || token == "" || gitEndpoint == "" || namespace == "" || repository == "" {
		t.Skip("set Product capacity API, token, Git endpoint, namespace, and repository variables")
	}

	name := uniqueName("product-capacity-git")
	previousGitURL := gitURL
	gitURL = gitEndpoint
	t.Cleanup(func() { gitURL = previousGitURL })
	// The capacity API's smart-HTTP endpoint is protected by the same bearer
	// policy as GraphQL. Git honors this scoped process configuration for the
	// probe's ls-remote, clone, and push commands without persisting a token in
	// the temporary checkout.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: Bearer "+token)
	h := newPushHelperForRepo(t, namespace, repository)
	h.commitProduct(name+".md", validProductFixture(name, namespace))
	if output, err := h.push(); err != nil {
		t.Fatalf("Product capacity Git push failed: %v\n%s", err, output)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		response := gqlQueryWithURL(t, apiB, token, `query($namespace: String!, $name: String!) {
  product(by: {namespacePath: {namespace: $namespace, name: $name}}) { metadata { name } }
}`, map[string]any{"namespace": namespace, "name": name})
		if len(response.Errors) > 0 {
			if strings.Contains(string(response.Errors[0]), `"code":"NOT_FOUND"`) && time.Now().Before(deadline) {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			require.Empty(t, response.Errors, string(response.Data))
		}
		var data struct {
			Product *struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"product"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &data))
		if data.Product != nil && data.Product.Metadata.Name == name {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Git-pushed Product %q did not materialize on peer API %s", name, apiB)
		}
		time.Sleep(200 * time.Millisecond)
	}
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
