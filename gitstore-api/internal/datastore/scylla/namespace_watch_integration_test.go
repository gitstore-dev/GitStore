// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
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

	bookmarkAt := now.Add(3 * time.Second).Truncate(time.Millisecond)
	appended, err := journal.Append(ctx, current, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchBookmark, At: bookmarkAt}, time.Hour)
	require.NoError(t, err)
	require.Equal(t, current.FencingToken, appended.FencingToken)
	bounds, err := journal.Bounds(ctx)
	require.NoError(t, err)
	require.Equal(t, bookmarkAt, bounds.BookmarkAt)

	dataAt := bookmarkAt.Add(time.Second)
	_, err = journal.Append(ctx, current, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchAdded, Name: "after-bookmark", At: dataAt}, time.Hour)
	require.NoError(t, err)
	bounds, err = journal.Bounds(ctx)
	require.NoError(t, err)
	require.Equal(t, bookmarkAt, bounds.BookmarkAt, "ordinary events must not refresh the durable bookmark timestamp")
	require.Equal(t, dataAt, bounds.UpdatedAt)

	require.NoError(t, journal.SaveProgress(ctx, current, datastore.NamespaceCDCProgress{StreamID: "stream-a", Position: []byte("current"), UpdatedAt: now.Add(2 * time.Second)}))
	progress, err := journal.LoadProgress(ctx, "stream-a")
	require.NoError(t, err)
	require.Equal(t, []byte("current"), progress.Position)
}

func TestNamespaceWatchReadinessTracksPublishedFrontierNotPerStreamProgress(t *testing.T) {
	store := newTestStore(t)
	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	lease, acquired, err := journal.AcquireLease(ctx, "progress-monotonic", now, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	newer := now.Add(-time.Second)
	older := newer.Add(-time.Hour)

	require.NoError(t, journal.SaveProgress(ctx, lease, datastore.NamespaceCDCProgress{
		StreamID: "stream-newer", Position: []byte("newer"), UpdatedAt: newer,
	}))
	beforeLate, err := journal.Bounds(ctx)
	require.NoError(t, err)
	require.NoError(t, journal.SaveProgress(ctx, lease, datastore.NamespaceCDCProgress{
		StreamID: "stream-late", Position: []byte("older"), UpdatedAt: older,
	}))

	bounds, err := journal.Bounds(ctx)
	require.NoError(t, err)
	assert.Equal(t, beforeLate.ProgressAt, bounds.ProgressAt)
	late, err := journal.LoadProgress(ctx, "stream-late")
	require.NoError(t, err)
	assert.Equal(t, []byte("older"), late.Position)
	assert.Equal(t, older, late.UpdatedAt)

	frontierAt := time.Now().UTC().Truncate(time.Millisecond)
	frontier := gocql.MinTimeUUID(frontierAt)
	require.NoError(t, journal.SaveProgress(ctx, lease, datastore.NamespaceCDCProgress{
		StreamID: "__namespace_cdc_published_frontier__", Position: frontier.Bytes(), UpdatedAt: frontierAt,
	}))
	bounds, err = journal.Bounds(ctx)
	require.NoError(t, err)
	assert.Equal(t, frontierAt, bounds.ProgressAt)
}

func TestNamespaceWatchBoundsPerStreamProgressAcrossGenerations(t *testing.T) {
	store := newTestStore(t)
	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	ctx := context.Background()
	now := time.Now().UTC()
	lease, acquired, err := journal.AcquireLease(ctx, "progress-ttl", now, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })

	require.NoError(t, journal.SaveProgress(ctx, lease, datastore.NamespaceCDCProgress{
		StreamID: "generation:table:stream", Position: []byte("dynamic"), UpdatedAt: now,
	}))
	require.NoError(t, journal.SaveProgress(ctx, lease, datastore.NamespaceCDCProgress{
		StreamID: "__namespace_cdc_generation__", Position: []byte(fmt.Sprintf("%d", now.UnixNano())), UpdatedAt: now,
	}))

	raw := newRawSession(t)
	defer raw.Close()
	dynamic := map[string]any{}
	require.NoError(t, raw.Query(
		"SELECT TTL(position) AS ttl FROM namespace_watch_clock WHERE journal=? AND stream_id=?",
	).Bind("namespace", "generation:table:stream").MapScan(dynamic))
	require.NotNil(t, dynamic["ttl"])
	durable := map[string]any{}
	require.NoError(t, raw.Query(
		"SELECT TTL(position) AS ttl FROM namespace_watch_clock WHERE journal=? AND stream_id=?",
	).Bind("namespace", "__namespace_cdc_generation__").MapScan(durable))
	require.Equal(t, 0, durable["ttl"])
}

func TestNamespaceWatchHidesStagedNamespaceFromBootstrapReads(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond).Add(-3 * time.Second)
	namespaces := make([]*datastore.Namespace, 3)
	for i := range namespaces {
		namespaces[i] = &datastore.Namespace{
			APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", UID: newID(),
			Name: fmt.Sprintf("staged-page-%d-%s", i, newID()[:8]), Title: "Staged", Tier: datastore.NamespaceTierUser,
			ResourceVersion: "1", Generation: 1, CreationTimestamp: now.Add(time.Duration(i) * time.Second), UpdateTimestamp: now.Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, store.CreateNamespace(ctx, namespaces[i]))
	}
	staged := namespaces[1]

	raw := newRawSession(t)
	require.NoError(t, raw.Query("UPDATE namespaces_by_uid SET watch_committed=? WHERE uid=?").
		Bind(false, staged.UID).Exec())
	raw.Close()

	_, err := store.GetNamespace(ctx, staged.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = store.GetNamespaceByName(ctx, staged.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	listed, err := store.ListNamespaces(ctx, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.True(t, listed.HasNext, "staged N+1 row must not hide the next committed Namespace")
	for _, item := range listed.Items {
		require.NotEqual(t, staged.UID, item.UID)
	}
}

func TestNamespaceWatchRejectsBucketLayoutMismatch(t *testing.T) {
	first := newTestStore(t).(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_clock").Exec())
	raw.Close()
	t.Cleanup(func() {
		cleanup := newRawSession(t)
		defer cleanup.Close()
		require.NoError(t, cleanup.Query("TRUNCATE namespace_watch_events").Exec())
		require.NoError(t, cleanup.Query("TRUNCATE namespace_watch_clock").Exec())
	})

	_, acquired, err := first.AcquireLease(context.Background(), "replica-a", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	second := newTestStoreWithWatchBucket(t, 3).(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	_, _, err = second.AcquireLease(context.Background(), "replica-b", time.Now().UTC(), time.Minute)
	require.ErrorContains(t, err, "bucket size is 4096, configured 3")
}

func TestNamespaceWatchInitializesBucketLayoutForMigrationFirstClock(t *testing.T) {
	journal := newTestStore(t).(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_clock").Exec())
	epoch, err := gocql.RandomUUID()
	require.NoError(t, err)
	zeroExpiry := time.Unix(0, 0).UTC()
	require.NoError(t, raw.Query(
		"INSERT INTO namespace_watch_clock (journal,stream_id,epoch,high_water,oldest,update_timestamp,cdc_progress_timestamp,lease_holder,fencing_token,lease_expiration_timestamp) VALUES (?,?,?,?,?,?,?,?,?,?)",
	).Bind("namespace", "__clock__", epoch, int64(0), int64(0), time.Now().UTC(), zeroExpiry, "", int64(0), zeroExpiry).Exec())
	raw.Close()

	lease, acquired, err := journal.AcquireLease(context.Background(), "new-replica", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })

	verify := newRawSession(t)
	defer verify.Close()
	var bucketSize int64
	require.NoError(t, verify.Query("SELECT bucket_size FROM namespace_watch_clock WHERE journal=? LIMIT 1").Bind("namespace").Scan(&bucketSize))
	require.Equal(t, int64(watchjournal.DefaultBucketSize), bucketSize)
}

func TestNamespaceWatchBoundsAdvanceAfterTTLExpiry(t *testing.T) {
	store := newTestStoreWithWatchBucket(t, 2)
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE namespace_watch_clock").Exec())
	raw.Close()
	t.Cleanup(func() {
		cleanup := newRawSession(t)
		defer cleanup.Close()
		require.NoError(t, cleanup.Query("TRUNCATE namespace_watch_events").Exec())
		require.NoError(t, cleanup.Query("TRUNCATE namespace_watch_clock").Exec())
	})

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

	start := make(chan struct{})
	errorsOut := make(chan error, 8)
	var callers sync.WaitGroup
	for range 8 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			_, boundsErr := journal.Bounds(ctx)
			errorsOut <- boundsErr
		}()
	}
	close(start)
	callers.Wait()
	close(errorsOut)
	for boundsErr := range errorsOut {
		if boundsErr != nil {
			require.ErrorContains(t, boundsErr, "retention reconciliation is incomplete")
			require.NotContains(t, boundsErr.Error(), "concurrent update")
		}
	}
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
				// Keep the reader alive briefly so the private commit-marker update
				// cannot leak as a second public MODIFIED event.
				time.Sleep(500 * time.Millisecond)
				_, boundsErr = journal.Bounds(context.Background())
				require.NoError(t, boundsErr)
				more, readErr := journal.ReadAfter(context.Background(), cursor, 256)
				require.NoError(t, readErr)
				for _, next := range more {
					require.NotEqual(t, namespace.Name, next.Name, "commit marker emitted a duplicate public transition")
				}

				current, getErr := store.GetNamespace(context.Background(), namespace.UID)
				require.NoError(t, getErr)
				deletionAt := time.Now().UTC().Truncate(time.Millisecond)
				expectedResourceVersion := current.ResourceVersion
				current.DeletionTimestamp = &deletionAt
				datastore.AdvanceNamespaceSystemVersion(current)
				require.NoError(t, store.MarkNamespaceDeletion(context.Background(), current, expectedResourceVersion))

				deletionDeadline := time.Now().Add(75 * time.Second)
				for time.Now().Before(deletionDeadline) {
					modified, readErr := journal.ReadAfter(context.Background(), cursor, 256)
					require.NoError(t, readErr)
					for _, next := range modified {
						cursor.Sequence = next.Sequence
						if next.Name != namespace.Name {
							continue
						}
						require.Equal(t, datastore.NamespaceWatchModified, next.Type)
						var terminating datastore.Namespace
						require.NoError(t, json.Unmarshal(next.Payload, &terminating))
						require.NotNil(t, terminating.DeletionTimestamp)
						require.Equal(t, deletionAt, *terminating.DeletionTimestamp)
						cancel()
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				cancel()
				t.Fatal("Namespace deletion timestamp was not materialized into the durable journal")
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
