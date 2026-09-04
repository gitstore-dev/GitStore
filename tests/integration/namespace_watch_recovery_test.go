// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// TestNamespaceWatchRecoveryProbe is intentionally environment-driven: the
// replacement endpoint is brought up by the deployment harness after a real
// API restart/rolling replacement and therefore exercises shared Scylla
// history and materializer lease handoff rather than an in-process substitute.
func TestNamespaceWatchRecoveryProbe(t *testing.T) {
	apiA := strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_A"), "/")
	apiB := strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_B"), "/")
	token := namespaceWatchToken(t)
	if apiA == "" || apiB == "" || token == "" {
		t.Skip("set NAMESPACE_WATCH_API_A, NAMESPACE_WATCH_API_B, and NAMESPACE_WATCH_TOKEN")
	}

	bootstrap := openNamespaceWatch(t, apiA, token, "__namespace_watch_bootstrap__")
	cursor := readNamespaceWatchEvent(t, bootstrap).ResourceVersion
	_ = bootstrap.Close()

	name := uniqueName("watch-recovery")
	createNamespaceThrough(t, apiB, token, name)
	resumed := openNamespaceWatch(t, apiB, token, cursor)
	event := readNamespaceWatchTransition(t, resumed)
	require.Equal(t, name, event.Name)
	require.NotEqual(t, cursor, event.ResourceVersion)
	cursor = event.ResourceVersion
	_ = resumed.Close()

	t.Run("forced epoch expiry", func(t *testing.T) {
		expired := openNamespaceWatch(t, apiA, token, "nwv1:00000000-0000-4000-8000-000000000001:0")
		requireNamespaceWatchWireError(t, expired, "WATCH_EXPIRED", "EPOCH_MISMATCH")
	})

	t.Run("rolling replacement and lease handoff", func(t *testing.T) {
		replacement := strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_REPLACEMENT"), "/")
		trigger := os.Getenv("NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE")
		require.NotEmpty(t, replacement, "deployment harness must select a replacement endpoint")
		require.NotEqual(t, apiA, apiB, "recovery endpoints must identify distinct replicas")
		require.Contains(t, []string{apiA, apiB}, replacement, "replacement must identify one of the live replicas")
		require.NotEmpty(t, trigger, "deployment harness must provide NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE")
		_, statErr := os.Stat(trigger)
		require.ErrorIs(t, statErr, os.ErrNotExist, "replacement trigger must not exist before the probe")

		client := &http.Client{Timeout: 5 * time.Second}
		before, err := fetchCapacityMetrics(client, replacement)
		require.NoError(t, err, "scrape replacement identity before trigger")
		require.NoError(t, os.WriteFile(trigger, []byte("replace namespace recovery probe\n"), 0o600))

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
		require.NotZero(t, after.processStart, "replacement must return with a different process_start_time_seconds")
		t.Logf("observed replacement endpoint=%s process_start_before=%.0f process_start_after=%.0f", replacement, before.processStart, after.processStart)

		watch := openNamespaceWatch(t, replacement, token, cursor)
		name := uniqueName("watch-handoff")
		peer := apiA
		if replacement == apiA {
			peer = apiB
		}
		createNamespaceThrough(t, peer, token, name)
		event := readNamespaceWatchTransition(t, watch)
		require.Equal(t, name, event.Name)
	})

	t.Run("subscriber overflow", func(t *testing.T) {
		count, err := strconv.Atoi(os.Getenv("NAMESPACE_WATCH_OVERFLOW_TRANSITIONS"))
		if err != nil || count < 1 {
			t.Skip("set NAMESPACE_WATCH_OVERFLOW_TRANSITIONS for the deliberately slow-consumer probe")
		}
		watch := openNamespaceWatch(t, apiA, token, cursor)
		for i := 0; i < count; i++ {
			createNamespaceThrough(t, apiB, token, uniqueName("watch-overflow"))
		}
		requireNamespaceWatchWireError(t, watch, "WATCH_EXPIRED", "SUBSCRIBER_OVERFLOW")
	})
}

func namespaceWatchToken(t *testing.T) string {
	t.Helper()
	if token := strings.TrimSpace(os.Getenv("NAMESPACE_WATCH_TOKEN")); token != "" {
		return token
	}
	path := strings.TrimSpace(os.Getenv("NAMESPACE_WATCH_TOKEN_FILE"))
	if path == "" {
		return ""
	}
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "read NAMESPACE_WATCH_TOKEN_FILE")
	return strings.TrimSpace(string(contents))
}

func requireNamespaceWatchWireError(t *testing.T, conn *websocket.Conn, code, reason string) {
	t.Helper()
	for i := 0; i < 10000; i++ {
		var message struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		require.NoError(t, conn.ReadJSON(&message))
		var errorsList []struct {
			Extensions map[string]any `json:"extensions"`
		}
		switch message.Type {
		case "error":
			require.NoError(t, json.Unmarshal(message.Payload, &errorsList))
		case "next":
			var payload struct {
				Errors []struct {
					Extensions map[string]any `json:"extensions"`
				} `json:"errors"`
			}
			require.NoError(t, json.Unmarshal(message.Payload, &payload))
			errorsList = payload.Errors
			if len(errorsList) == 0 {
				continue
			}
		case "complete":
			t.Fatal("watch completed normally while an explicit terminal error was expected")
		default:
			continue
		}
		require.Equal(t, code, errorsList[0].Extensions["code"])
		require.Equal(t, reason, errorsList[0].Extensions["reason"])
		return
	}
	t.Fatal("terminal watch error not observed within bounded read window")
}
