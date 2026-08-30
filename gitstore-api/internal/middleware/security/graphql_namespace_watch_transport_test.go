// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type transportWatchJournal struct {
	bounds datastore.NamespaceWatchBounds
	read   func(datastore.NamespaceWatchCursor, int) ([]datastore.NamespaceWatchEvent, error)
}

func (j *transportWatchJournal) Bounds(context.Context) (datastore.NamespaceWatchBounds, error) {
	return j.bounds, nil
}
func (j *transportWatchJournal) ReadAfter(_ context.Context, cursor datastore.NamespaceWatchCursor, limit int) ([]datastore.NamespaceWatchEvent, error) {
	return j.read(cursor, limit)
}
func (*transportWatchJournal) Append(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceWatchEvent, time.Duration) (datastore.NamespaceWatchEvent, error) {
	panic("unused")
}
func (*transportWatchJournal) AcquireLease(context.Context, string, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	panic("unused")
}
func (*transportWatchJournal) RenewLease(context.Context, datastore.NamespaceWatchLease, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	panic("unused")
}
func (*transportWatchJournal) ReleaseLease(context.Context, datastore.NamespaceWatchLease) error {
	panic("unused")
}
func (*transportWatchJournal) LoadProgress(context.Context, string) (datastore.NamespaceCDCProgress, error) {
	panic("unused")
}
func (*transportWatchJournal) SaveProgress(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceCDCProgress) error {
	panic("unused")
}

func namespaceWatchConfig() config.NamespaceWatchConfig {
	return config.NamespaceWatchConfig{
		ReadersEnabled: true, ReadBatchSize: 256, MaxReplayEvents: 100000,
		SubscriberBuffer: 64, PollMinMillis: 1, PollMaxMillis: 2,
		MaxMaterializerLagSeconds: 60,
	}
}

func openNamespaceWatch(t *testing.T, journal datastore.NamespaceWatchJournal, cursor string) *websocket.Conn {
	t.Helper()
	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store: &testutil.StubStore{}, Logger: zap.NewNop(),
		NamespaceJournal: journal, NamespaceWatch: namespaceWatchConfig(),
	})
	require.NoError(t, err)
	server := gqlhandler.New(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
	server.AddTransport(transport.Websocket{})
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, _, err := dialer.Dial(strings.Replace(httpServer.URL, "http", "ws", 1), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "connection_init"}))
	var ack map[string]any
	require.NoError(t, conn.ReadJSON(&ack))
	require.Equal(t, "connection_ack", ack["type"])
	query := `subscription($cursor: String) { watchNamespaces(resourceVersion: $cursor) { type resourceVersion } }`
	require.NoError(t, conn.WriteJSON(map[string]any{
		"id": "namespace-watch", "type": "subscribe",
		"payload": map[string]any{"query": query, "variables": map[string]any{"cursor": cursor}},
	}))
	return conn
}

func requireWatchTransportError(t *testing.T, conn *websocket.Conn, code, reason string) {
	t.Helper()
	var message map[string]any
	require.NoError(t, conn.ReadJSON(&message))
	var errorsList []any
	switch message["type"] {
	case "next":
		payload := message["payload"].(map[string]any)
		errorsList = payload["errors"].([]any)
	case "error":
		errorsList = message["payload"].([]any)
	default:
		t.Fatalf("message type = %v, want next or error", message["type"])
	}
	first := errorsList[0].(map[string]any)
	extensions := first["extensions"].(map[string]any)
	require.Equal(t, code, extensions["code"])
	require.Equal(t, reason, extensions["reason"])
}

func TestNamespaceWatchExpiredSurvivesWebSocketTransport(t *testing.T) {
	epoch := uuid.NewString()
	journal := &transportWatchJournal{
		bounds: datastore.NamespaceWatchBounds{Epoch: epoch, HighWater: 1, UpdatedAt: time.Now().UTC()},
		read:   func(datastore.NamespaceWatchCursor, int) ([]datastore.NamespaceWatchEvent, error) { return nil, nil },
	}
	conn := openNamespaceWatch(t, journal, "nwv2:"+epoch+":1")
	requireWatchTransportError(t, conn, watchjournal.CodeExpired, string(watchjournal.ReasonIncompatibleCursor))
}

func TestNamespaceWatchUnavailableSurvivesWebSocketTransport(t *testing.T) {
	journal := &transportWatchJournal{
		bounds: datastore.NamespaceWatchBounds{Epoch: uuid.NewString()},
		read:   func(datastore.NamespaceWatchCursor, int) ([]datastore.NamespaceWatchEvent, error) { return nil, nil },
	}
	conn := openNamespaceWatch(t, journal, "")
	requireWatchTransportError(t, conn, watchjournal.CodeUnavailable, string(watchjournal.ReasonMaterializerNotReady))
}

func TestNamespaceWatchRuntimeDiscontinuityIsNotNormalClosure(t *testing.T) {
	epoch := uuid.NewString()
	journal := &transportWatchJournal{
		bounds: datastore.NamespaceWatchBounds{Epoch: epoch, HighWater: 2, UpdatedAt: time.Now().UTC()},
		read: func(datastore.NamespaceWatchCursor, int) ([]datastore.NamespaceWatchEvent, error) {
			return []datastore.NamespaceWatchEvent{{Epoch: epoch, Sequence: 2}}, nil
		},
	}
	conn := openNamespaceWatch(t, journal, watchjournal.EncodeCursor(epoch, 0))
	requireWatchTransportError(t, conn, watchjournal.CodeExpired, string(watchjournal.ReasonJournalDiscontinuity))
}

func TestNamespaceWatchOverflowIsNotNormalClosure(t *testing.T) {
	epoch := uuid.NewString()
	events := make([]datastore.NamespaceWatchEvent, 5000)
	for i := range events {
		events[i] = datastore.NamespaceWatchEvent{
			Epoch: epoch, Sequence: uint64(i + 1), Type: datastore.NamespaceWatchBookmark,
		}
	}
	journal := &transportWatchJournal{
		bounds: datastore.NamespaceWatchBounds{Epoch: epoch, HighWater: uint64(len(events)), UpdatedAt: time.Now().UTC()},
		read: func(cursor datastore.NamespaceWatchCursor, _ int) ([]datastore.NamespaceWatchEvent, error) {
			if cursor.Sequence == 0 {
				return events, nil
			}
			return nil, nil
		},
	}
	conn := openNamespaceWatch(t, journal, watchjournal.EncodeCursor(epoch, 0))
	for i := 0; i < 512; i++ {
		var message map[string]any
		require.NoError(t, conn.ReadJSON(&message))
		switch message["type"] {
		case "next":
			payload := message["payload"].(map[string]any)
			if _, hasErrors := payload["errors"]; !hasErrors {
				continue
			}
		case "error":
		default:
			t.Fatalf("overflow ended with %v instead of an error", message["type"])
		}
		var errorsList []any
		if message["type"] == "error" {
			errorsList = message["payload"].([]any)
		} else {
			errorsList = message["payload"].(map[string]any)["errors"].([]any)
		}
		extensions := errorsList[0].(map[string]any)["extensions"].(map[string]any)
		require.Equal(t, watchjournal.CodeExpired, extensions["code"])
		require.Equal(t, string(watchjournal.ReasonSubscriberOverflow), extensions["reason"])
		return
	}
	t.Fatal("overflow error was not delivered within the bounded frame window")
}
