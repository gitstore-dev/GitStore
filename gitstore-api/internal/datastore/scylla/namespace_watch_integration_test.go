// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namespaceCDCTestRunner interface {
	RunNamespaceCDC(context.Context, *watchjournal.Materializer, datastore.NamespaceWatchLease, time.Duration, time.Duration, func()) error
}

func TestNamespaceWatchLeaseFencesJournalAndProgressWrites(t *testing.T) {
	store := newTestStore(t)
	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	ctx := context.Background()
	now := time.Now().UTC()

	stale, acquired, err := journal.AcquireLease(ctx, "replica-a", now, time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	current, acquired, err := journal.AcquireLease(ctx, "replica-b", now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Greater(t, current.FencingToken, stale.FencingToken)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), current)) })

	_, err = journal.Append(ctx, stale, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchBookmark}, time.Hour)
	require.ErrorIs(t, err, datastore.ErrStaleWatchLease)
	err = journal.SaveProgress(ctx, stale, datastore.NamespaceCDCProgress{StreamID: "stream-a", Position: []byte("stale"), UpdatedAt: now})
	require.ErrorIs(t, err, datastore.ErrStaleWatchLease)

	appended, err := journal.Append(ctx, current, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchBookmark}, time.Hour)
	require.NoError(t, err)
	require.Equal(t, current.FencingToken, appended.FencingToken)
	require.NoError(t, journal.SaveProgress(ctx, current, datastore.NamespaceCDCProgress{StreamID: "stream-a", Position: []byte("current"), UpdatedAt: now.Add(2 * time.Second)}))
	progress, err := journal.LoadProgress(ctx, "stream-a")
	require.NoError(t, err)
	require.Equal(t, []byte("current"), progress.Position)
}

func TestNamespaceWatchBoundsAdvanceAfterTTLExpiry(t *testing.T) {
	store := newTestStoreWithWatchBucket(t, 2)
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_clock").Exec())
	raw.Close()

	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	ctx := context.Background()
	now := time.Now().UTC()
	lease, acquired, err := journal.AcquireLease(ctx, "retention-test", now, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { _ = journal.ReleaseLease(context.Background(), lease) })

	var last datastore.NamespaceWatchEvent
	for range 70 {
		last, err = journal.Append(ctx, lease, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchBookmark}, 2*time.Second)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		var count int64
		return namespaceWatchEventCount(t, &count) == nil && count == 0
	}, 10*time.Second, 100*time.Millisecond)

	_, err = journal.Bounds(ctx)
	require.ErrorContains(t, err, "retention reconciliation is incomplete")
	bounds, err := journal.Bounds(ctx)
	require.NoError(t, err)
	require.Equal(t, last.Sequence+1, bounds.Oldest)
	require.Equal(t, last.Sequence, bounds.HighWater)

	second, err := journal.Append(ctx, lease, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchBookmark}, time.Minute)
	require.NoError(t, err)
	bounds, err = journal.Bounds(ctx)
	require.NoError(t, err)
	require.Equal(t, second.Sequence, bounds.Oldest)
}

func namespaceWatchEventCount(t *testing.T, count *int64) error {
	t.Helper()
	raw := newRawSession(t)
	defer raw.Close()
	return raw.Query("SELECT count(*) FROM namespace_watch_events").Scan(count)
}

func TestNamespaceAuthoritativeCommitsProduceCDCButRejectedWritesDoNot(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	namespace := &datastore.Namespace{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", UID: newID(),
		Name: "cdc-" + newID()[:8], Title: "CDC", Tier: datastore.NamespaceTierUser,
		ResourceVersion: "1", Generation: 1, CreationTimestamp: now, UpdateTimestamp: now,
		Labels: map[string]string{"team": "catalog"},
	}

	before := namespaceCDCRowCount(t)
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	afterCreate := waitForNamespaceCDCRows(t, before)
	assert.Greater(t, afterCreate, before)

	err := store.CreateNamespace(ctx, namespace)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, afterCreate, namespaceCDCRowCount(t), "rejected duplicate must not create CDC rows")

	current, err := store.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expected := current.ResourceVersion
	current.Title = "CDC updated"
	datastore.AdvanceNamespaceSpecVersion(current)
	require.NoError(t, store.UpdateNamespace(ctx, current, expected))
	afterUpdate := waitForNamespaceCDCRows(t, afterCreate)
	assert.Greater(t, afterUpdate, afterCreate)

	err = store.UpdateNamespace(ctx, current, expected)
	require.ErrorIs(t, err, datastore.ErrConflict)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, afterUpdate, namespaceCDCRowCount(t), "conflict must not create CDC rows")

	require.NoError(t, store.DeleteNamespaceWithResourceVersion(ctx, current.UID, current.ResourceVersion))
	assert.Greater(t, waitForNamespaceCDCRows(t, afterUpdate), afterUpdate)
}

func TestNamespaceCDCReaderMaterializesCommittedEvent(t *testing.T) {
	store := newTestStore(t)
	runner, ok := store.(namespaceCDCTestRunner)
	require.True(t, ok)
	capable, ok := store.(datastore.NamespaceWatchCapable)
	require.True(t, ok)
	journal := capable.NamespaceWatchJournal()
	now := time.Now().UTC()
	lease, acquired, err := journal.AcquireLease(context.Background(), "integration-reader", now, 2*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	materializer := watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: 7 * 24 * time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		errCh <- runner.RunNamespaceCDC(ctx, materializer, lease, 14*24*time.Hour, 500*time.Millisecond, func() { close(ready) })
	}()
	select {
	case <-ready:
	case runErr := <-errCh:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("CDC reader did not become ready")
	}
	time.Sleep(500 * time.Millisecond)

	namespace := &datastore.Namespace{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", UID: newID(),
		Name: "materialized-" + newID()[:8], Title: "Materialized", Tier: datastore.NamespaceTierUser,
		ResourceVersion: "1", Generation: 1, CreationTimestamp: now, UpdateTimestamp: now,
		Labels: map[string]string{"team": "catalog"},
	}
	require.NoError(t, store.CreateNamespace(context.Background(), namespace))

	deadline := time.Now().Add(75 * time.Second)
	cursor := datastore.NamespaceWatchCursor{}
	var observed []datastore.NamespaceWatchEvent
	var lastBoundsErr error
	for time.Now().Before(deadline) {
		bounds, boundsErr := journal.Bounds(context.Background())
		lastBoundsErr = boundsErr
		if boundsErr == nil {
			if cursor.Epoch == "" {
				cursor.Epoch = bounds.Epoch
				if bounds.Oldest > 0 {
					cursor.Sequence = bounds.Oldest - 1
				}
			}
			events, readErr := journal.ReadAfter(context.Background(), cursor, 256)
			require.NoError(t, readErr)
			for _, event := range events {
				observed = append(observed, event)
				cursor.Sequence = event.Sequence
				if event.Name != namespace.Name {
					continue
				}
				require.Equal(t, datastore.NamespaceWatchAdded, event.Type)
				var payload datastore.Namespace
				require.NoError(t, json.Unmarshal(event.Payload, &payload))
				require.Equal(t, namespace.Name, payload.Name)
				require.Equal(t, "catalog", event.SelectorLabels["team"])
				cancel()
				return
			}
		}
		select {
		case runErr := <-errCh:
			require.NoError(t, runErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("committed Namespace CDC change was not materialized into the durable journal; bounds_err=%v observed=%+v", lastBoundsErr, observed)
}

func namespaceCDCRowCount(t *testing.T) int64 {
	t.Helper()
	session := newRawSession(t)
	defer session.Close()
	var count int64
	query := fmt.Sprintf("SELECT count(*) FROM %s.namespaces_by_uid_scylla_cdc_log", scyllaKeyspace)
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err = session.Query(query).Scan(&count)
		if err == nil {
			return count
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, err, "CDC log table did not become visible after schema agreement")
	return 0
}

func waitForNamespaceCDCRows(t *testing.T, previous int64) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if current := namespaceCDCRowCount(t); current > previous {
			return current
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Namespace CDC rows did not advance beyond %d", previous)
	return previous
}
