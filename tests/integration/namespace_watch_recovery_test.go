// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// TestNamespaceWatchRecoveryProbe is intentionally environment-driven: the
// replacement endpoint is brought up by the deployment harness after a real
// API restart/rolling replacement and therefore exercises shared Scylla
// history and materializer lease handoff rather than an in-process substitute.
func TestNamespaceWatchRecoveryProbe(t *testing.T) {
	apiA := os.Getenv("NAMESPACE_WATCH_API_A")
	apiB := os.Getenv("NAMESPACE_WATCH_API_B")
	token := os.Getenv("NAMESPACE_WATCH_TOKEN")
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
		replacement := os.Getenv("NAMESPACE_WATCH_API_REPLACEMENT")
		if replacement == "" {
			t.Skip("deployment harness must set NAMESPACE_WATCH_API_REPLACEMENT after replacing a replica")
		}
		watch := openNamespaceWatch(t, replacement, token, cursor)
		name := uniqueName("watch-handoff")
		createNamespaceThrough(t, apiA, token, name)
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
