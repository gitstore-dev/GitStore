// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namespaceCDCTestRunner interface {
	RunNamespaceCDC(context.Context, *watchjournal.Materializer, datastore.NamespaceWatchLease, time.Duration, time.Duration, func()) error
}

type catalogCDCTestRunner interface {
	RunCatalogCDC(context.Context, string, *watchjournal.Materializer, datastore.ResourceWatchLease, time.Duration, time.Duration, func()) error
}

type namespaceWatchBatchAppender interface {
	AppendBatch(context.Context, datastore.NamespaceWatchLease, []datastore.NamespaceWatchEvent, time.Duration) ([]datastore.NamespaceWatchEvent, error)
}

func TestCDCCheckpointBatchPreservesProgressAndLeaseFencing(t *testing.T) {
	store := newTestStore(t)
	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	ctx := t.Context()
	lease, acquired, err := journal.AcquireLease(ctx, "checkpoint-batch", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })

	for _, source := range []string{"Namespace", "RBAC"} {
		t.Run(source, func(t *testing.T) {
			progress := make(map[string]gocql.UUID, 65)
			for index := range 65 {
				progress[fmt.Sprintf("checkpoint-batch-%03d", index)] = gocql.TimeUUID()
			}
			require.NoError(t, scylla.SaveCDCProgressBatchForTest(ctx, store, lease, source, progress))
			prefix := ""
			if source != "Namespace" {
				prefix = source + ":"
			}
			for key, point := range progress {
				got, err := journal.LoadProgress(ctx, prefix+key)
				require.NoError(t, err)
				require.Equal(t, point.Bytes(), got.Position)
			}
			stale := lease
			stale.FencingToken++
			attempt := make(map[string]gocql.UUID, len(progress)+1)
			for key := range progress {
				attempt[key] = gocql.TimeUUID()
			}
			attempt["checkpoint-batch-new"] = gocql.TimeUUID()
			require.ErrorIs(t, scylla.SaveCDCProgressBatchForTest(ctx, store, stale, source, attempt), datastore.ErrStaleWatchLease)
			for key, point := range progress {
				got, err := journal.LoadProgress(ctx, prefix+key)
				require.NoError(t, err)
				require.Equal(t, point.Bytes(), got.Position, "stale lease must not overwrite checkpoints")
			}
			_, err := journal.LoadProgress(ctx, prefix+"checkpoint-batch-new")
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func TestNamespaceWatchBatchAppendPublishesContiguousRangesAcrossBuckets(t *testing.T) {
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE resource_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE resource_watch_clock").Exec())
	raw.Close()
	t.Cleanup(func() {
		cleanup := newRawSession(t)
		defer cleanup.Close()
		require.NoError(t, cleanup.Query("TRUNCATE resource_watch_events").Exec())
		require.NoError(t, cleanup.Query("TRUNCATE resource_watch_clock").Exec())
	})

	store := newTestStoreWithWatchBucket(t, 3)
	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	batcher := journal.(namespaceWatchBatchAppender)
	ctx := context.Background()
	lease, acquired, err := journal.AcquireLease(ctx, "batch-replica", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })

	events := make([]datastore.NamespaceWatchEvent, 5)
	for index := range events {
		events[index] = datastore.NamespaceWatchEvent{
			Type: datastore.NamespaceWatchAdded, Name: fmt.Sprintf("batch-%d", index+1),
			DeduplicationKey: fmt.Sprintf("batch-key-%d", index+1), At: time.Now().UTC(),
		}
	}
	appended, err := batcher.AppendBatch(ctx, lease, events, time.Hour)
	require.NoError(t, err)
	require.Len(t, appended, len(events))
	for index, event := range appended {
		require.Equal(t, uint64(index+1), event.Sequence)
	}
	read, err := journal.ReadAfter(ctx, datastore.NamespaceWatchCursor{Epoch: appended[0].Epoch}, 10)
	require.NoError(t, err)
	require.Len(t, read, len(events))
	for index, event := range read {
		require.Equal(t, fmt.Sprintf("batch-%d", index+1), event.Name)
	}
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
		"SELECT TTL(position) AS ttl FROM resource_watch_clock WHERE journal=? AND stream_id=?",
	).Bind("resource", "generation:table:stream").MapScan(dynamic))
	require.NotNil(t, dynamic["ttl"])
	durable := map[string]any{}
	require.NoError(t, raw.Query(
		"SELECT TTL(position) AS ttl FROM resource_watch_clock WHERE journal=? AND stream_id=?",
	).Bind("resource", "__namespace_cdc_generation__").MapScan(durable))
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

func TestNamespaceWatchAdoptsPersistedBucketLayout(t *testing.T) {
	legacyStore := newTestStoreWithWatchBucket(t, 3)
	legacy := legacyStore.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE resource_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE resource_watch_clock").Exec())
	raw.Close()
	t.Cleanup(func() {
		cleanup := newRawSession(t)
		defer cleanup.Close()
		require.NoError(t, cleanup.Query("TRUNCATE resource_watch_events").Exec())
		require.NoError(t, cleanup.Query("TRUNCATE resource_watch_clock").Exec())
	})

	lease, acquired, err := legacy.AcquireLease(context.Background(), "replica-a", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, legacy.ReleaseLease(context.Background(), lease))

	upgradedStore := newTestStore(t)
	upgraded := upgradedStore.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	lease, acquired, err = upgraded.AcquireLease(context.Background(), "replica-b", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, upgraded.ReleaseLease(context.Background(), lease)) })
	require.Equal(t, int64(3), scylla.WatchBucketSizeForTest(upgradedStore))
}

func TestNamespaceWatchInitializesBucketLayoutForMigrationFirstClock(t *testing.T) {
	journal := newTestStore(t).(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE resource_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE resource_watch_clock").Exec())
	epoch, err := gocql.RandomUUID()
	require.NoError(t, err)
	zeroExpiry := time.Unix(0, 0).UTC()
	require.NoError(t, raw.Query(
		"INSERT INTO resource_watch_clock (journal,stream_id,epoch,high_water,oldest,update_timestamp,cdc_progress_timestamp,lease_holder,fencing_token,lease_expiration_timestamp) VALUES (?,?,?,?,?,?,?,?,?,?)",
	).Bind("resource", "__clock__", epoch, int64(0), int64(0), time.Now().UTC(), zeroExpiry, "", int64(0), zeroExpiry).Exec())
	raw.Close()

	lease, acquired, err := journal.AcquireLease(context.Background(), "new-replica", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })

	verify := newRawSession(t)
	defer verify.Close()
	var bucketSize int64
	require.NoError(t, verify.Query("SELECT bucket_size FROM resource_watch_clock WHERE journal=? LIMIT 1").Bind("resource").Scan(&bucketSize))
	require.Equal(t, int64(watchjournal.DefaultBucketSize), bucketSize)
}

func TestNamespaceWatchBoundsAdvanceAfterTTLExpiry(t *testing.T) {
	store := newTestStoreWithWatchBucket(t, 2)
	raw := newRawSession(t)
	require.NoError(t, raw.Query("TRUNCATE resource_watch_events").Exec())
	require.NoError(t, raw.Query("TRUNCATE resource_watch_clock").Exec())
	raw.Close()
	t.Cleanup(func() {
		cleanup := newRawSession(t)
		defer cleanup.Close()
		require.NoError(t, cleanup.Query("TRUNCATE resource_watch_events").Exec())
		require.NoError(t, cleanup.Query("TRUNCATE resource_watch_clock").Exec())
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
	return raw.Query("SELECT count(*) FROM resource_watch_events").Scan(count)
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

	expected = current.ResourceVersion
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, store.MarkNamespaceDeletion(ctx, current, expected))
	afterMark := waitForNamespaceCDCRows(t, afterUpdate)
	require.NoError(t, store.DeleteNamespaceWithResourceVersion(ctx, current.UID, current.ResourceVersion))
	assert.Greater(t, waitForNamespaceCDCRows(t, afterMark), afterMark)
}

func TestNamespaceCDCDeletionRepairsOrphanedProjectionsWithoutRemovingReplacement(t *testing.T) {
	for _, scenario := range []string{"orphaned name", "replacement name", "authoritative row remains"} {
		t.Run(scenario, func(t *testing.T) {
			store := newTestStore(t)
			raw := newRawSession(t)
			defer raw.Close()
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Millisecond)
			namespace := &datastore.Namespace{
				APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", UID: newID(),
				Name: "cdc-repair-" + newID()[:8], Tier: datastore.NamespaceTierUser,
				ResourceVersion: "1", Generation: 1, CreationTimestamp: now, UpdateTimestamp: now,
			}
			require.NoError(t, store.CreateNamespace(ctx, namespace))
			if scenario != "authoritative row remains" {
				require.NoError(t, store.DeleteNamespace(ctx, namespace.UID))
			} else {
				t.Cleanup(func() { require.NoError(t, store.DeleteNamespace(context.Background(), namespace.UID)) })
			}
			var replacement *datastore.Namespace
			if scenario == "replacement name" {
				copy := *namespace
				copy.UID = newID()
				copy.CreationTimestamp = now.Add(time.Millisecond)
				replacement = &copy
				require.NoError(t, store.CreateNamespace(ctx, replacement))
				t.Cleanup(func() { require.NoError(t, store.DeleteNamespace(context.Background(), replacement.UID)) })
			}
			uid, err := gocql.ParseUUID(namespace.UID)
			require.NoError(t, err)
			bucket := now.Format("2006-01")
			require.NoError(t, raw.Query(
				"INSERT INTO namespaces_by_bucket (bucket,creation_timestamp,uid) VALUES (?,?,?)",
				bucket, now, uid).Exec())
			if scenario == "orphaned name" {
				require.NoError(t, raw.Query("INSERT INTO namespaces_by_name (name,uid) VALUES (?,?)", namespace.Name, uid).Exec())
			}
			ready, err := scylla.NamespaceCDCDeletionReadyForTest(ctx, store, namespace, false)
			require.NoError(t, err)
			require.False(t, ready, "uncommitted deletion must not repair projections")
			ready, err = scylla.NamespaceCDCDeletionReadyForTest(ctx, store, namespace, true)
			if scenario == "authoritative row remains" {
				require.Error(t, err)
				require.False(t, ready)
				current, err := store.GetNamespaceByName(ctx, namespace.Name)
				require.NoError(t, err)
				require.Equal(t, namespace.UID, current.UID)
				return
			}
			require.NoError(t, err)
			require.True(t, ready)
			var found gocql.UUID
			require.ErrorIs(t, raw.Query(
				"SELECT uid FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
				bucket, now, uid).Scan(&found), gocql.ErrNotFound)
			current, err := store.GetNamespaceByName(ctx, namespace.Name)
			if replacement == nil {
				require.ErrorIs(t, err, datastore.ErrNotFound)
				require.ErrorIs(t, raw.Query("SELECT uid FROM namespaces_by_name WHERE name=?", namespace.Name).Scan(&found), gocql.ErrNotFound)
			} else {
				require.NoError(t, err)
				require.Equal(t, replacement.UID, current.UID)
			}
		})
	}
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
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
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

func serviceAccountAmbiguousWriter(t *testing.T) (*gocql.Session, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	dropped := make(chan struct{}, 1)
	accepted := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(accepted)
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer client.Close()
				upstream, err := net.DialTimeout("tcp", scyllaAddr, 5*time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				go func() {
					_, _ = io.Copy(upstream, client)
					_ = upstream.Close()
				}()
				for {
					var header [9]byte
					if _, err := io.ReadFull(upstream, header[:]); err != nil {
						return
					}
					length := binary.BigEndian.Uint32(header[5:])
					if length > 16<<20 {
						return
					}
					body := make([]byte, length)
					if _, err := io.ReadFull(upstream, body); err != nil {
						return
					}
					// Drop native LWT result rows, not connection setup or prepared responses.
					if header[4] == 0x08 && len(body) >= 4 &&
						binary.BigEndian.Uint32(body[:4]) == 2 && bytes.Contains(body, []byte("[applied]")) {
						select {
						case dropped <- struct{}{}:
						default:
						}
						continue
					}
					if _, err := client.Write(append(header[:], body...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		require.NoError(t, listener.Close())
		<-accepted
		stopped := make(chan struct{})
		go func() { workers.Wait(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("owned CQL response proxy did not stop")
		}
	})
	address := listener.Addr().(*net.TCPAddr)
	cluster := gocql.NewCluster("127.0.0.1")
	cluster.Port, cluster.ProtoVersion, cluster.Keyspace = address.Port, 4, scyllaKeyspace
	cluster.Timeout, cluster.ConnectTimeout = 2*time.Second, 5*time.Second
	cluster.DisableShardAwarePort, cluster.IgnorePeerAddr = true, true
	cluster.AddressTranslator = contactPointTranslator("127.0.0.1", address.Port)
	cluster.RetryPolicy = &gocql.SimpleRetryPolicy{NumRetries: 0}
	writer, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(writer.Close)
	return writer, dropped
}

func TestServiceAccountCDCReaderEnrollmentDeletionAndNameReuse(t *testing.T) {
	store := newTestStore(t)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(t.Context(), "serviceaccount-native-reader", time.Now().UTC(), 2*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	ctx, cancel := context.WithCancel(t.Context())
	done, ready := make(chan error, 1), make(chan struct{})
	go func() {
		done <- store.(catalogCDCTestRunner).RunCatalogCDC(ctx, "ServiceAccount",
			watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: time.Hour}),
			lease, 14*24*time.Hour, 500*time.Millisecond, func() { close(ready) })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.True(t, err == nil || errors.Is(err, context.Canceled))
		case <-time.After(5 * time.Second):
			t.Error("owned ServiceAccount CDC reader did not stop")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("ServiceAccount CDC reader failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("ServiceAccount CDC reader did not become ready")
	}
	raw := newRawSession(t)
	partialName := "partial-" + newID()[:8]
	partialUID := gocql.TimeUUID()
	require.NoError(t, raw.Query("INSERT INTO service_accounts_by_cluster_name (name,uid,creation_timestamp,enrollment_complete,generation,resource_version,public_keys) VALUES (?,?,?,?,?,?,?)",
		partialName, partialUID, time.Now().UTC(), false, int64(1), "1", "[]").Exec())
	account := &datastore.ServiceAccount{UID: newID(), Name: "cdc-bot-" + newID()[:8], CreationTimestamp: time.Now().UTC(),
		Generation: 1, ResourceVersion: "1"}
	require.NoError(t, store.CreateServiceAccount(t.Context(), account))
	require.ErrorIs(t, store.CreateServiceAccount(t.Context(), account), datastore.ErrAlreadyExists)
	cursor := datastore.ResourceWatchCursor{}
	var observed []datastore.ResourceWatchEvent
	wait := func(kind datastore.ResourceWatchEventType, uid string) {
		t.Helper()
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		for {
			events, err := journal.ReadAfter(t.Context(), cursor, 256)
			require.NoError(t, err)
			for _, event := range events {
				cursor = datastore.ResourceWatchCursor{Epoch: event.Epoch, Sequence: event.Sequence}
				require.NotEqual(t, partialName, event.Name, "incomplete enrollment must never publish")
				if event.Kind != "ServiceAccount" || event.Name != account.Name {
					continue
				}
				observed = append(observed, event)
				var payload datastore.ServiceAccount
				require.NoError(t, json.Unmarshal(event.Payload, &payload))
				if event.Type == kind && payload.UID == uid {
					return
				}
			}
			select {
			case err := <-done:
				t.Fatalf("ServiceAccount CDC reader failed: %v", err)
			case <-deadline.C:
				t.Fatal("committed ServiceAccount event was not materialized")
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	wait(datastore.ResourceWatchAdded, account.UID)
	_, err = store.UpdateServiceAccountKeys(t.Context(), account.UID, nil, nil, "stale")
	require.ErrorIs(t, err, datastore.ErrConflict)
	writer, dropped := serviceAccountAmbiguousWriter(t)
	statement := fmt.Sprintf("UPDATE service_accounts_by_cluster_name SET disabled=true,generation=2,resource_version='2' WHERE name='%s' IF uid=%s AND enrollment_complete=true AND resource_version='1'",
		account.Name, account.UID)
	applied, err := writer.Query(statement).WithContext(t.Context()).MapScanCAS(map[string]any{})
	require.Error(t, err, "a physically discarded native CAS result must not acknowledge the committed write")
	require.False(t, applied)
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("the proxy did not observe and discard a native conditional result")
	}
	current, err := store.GetServiceAccountByUID(t.Context(), account.UID)
	require.NoError(t, err)
	require.True(t, current.Disabled, "the native source must prove commit independently of the lost acknowledgement")
	require.Equal(t, "2", current.ResourceVersion)
	wait(datastore.ResourceWatchModified, account.UID)
	var modified datastore.ServiceAccount
	require.NoError(t, json.Unmarshal(observed[len(observed)-1].Payload, &modified))
	require.True(t, modified.Disabled)
	require.Equal(t, "2", modified.ResourceVersion, "the shared journal must publish the committed full postimage")
	applied, err = raw.Query(statement).MapScanCAS(map[string]any{})
	require.NoError(t, err)
	require.False(t, applied, "retrying the original conditional write must not apply twice")
	require.NoError(t, store.DeleteServiceAccount(t.Context(), account.UID))
	wait(datastore.ResourceWatchDeleted, account.UID)
	require.JSONEq(t, `{"UID":"`+account.UID+`"}`, string(observed[len(observed)-1].Payload))
	replacement := *account
	replacement.UID = newID()
	require.NoError(t, store.CreateServiceAccount(t.Context(), &replacement))
	wait(datastore.ResourceWatchAdded, replacement.UID)
	require.Len(t, observed, 4, "only enrolled, successful authoritative transitions may publish")
}

// Product foreground deletion is controller-driven, so the terminating
// MODIFIED transition must reach the shared durable journal before a
// controller can complete the finalizer.
func TestProductCDCReaderMaterializesTerminationEvent(t *testing.T) {
	store := newTestStore(t)
	runner, ok := store.(catalogCDCTestRunner)
	require.True(t, ok)
	capable, ok := store.(datastore.ResourceWatchCapable)
	require.True(t, ok)
	journal := capable.ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(context.Background(), "product-integration-reader", time.Now().UTC(), 2*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	materializer := watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: 7 * 24 * time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		errCh <- runner.RunCatalogCDC(ctx, "Product", materializer, lease, 14*24*time.Hour, 500*time.Millisecond, func() { close(ready) })
	}()
	select {
	case <-ready:
	case runErr := <-errCh:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("Product CDC reader did not become ready")
	}

	product := newProduct("cdc-products", "terminating-"+newID()[:8])
	product.ResourceVersion = "1"
	require.NoError(t, store.CreateProduct(context.Background(), product))

	deadline := time.Now().Add(75 * time.Second)
	cursor := datastore.ResourceWatchCursor{}
	for time.Now().Before(deadline) {
		bounds, boundsErr := journal.Bounds(context.Background())
		require.NoError(t, boundsErr)
		if cursor.Epoch == "" {
			cursor.Epoch = bounds.Epoch
			if bounds.Oldest > 0 {
				cursor.Sequence = bounds.Oldest - 1
			}
		}
		events, readErr := journal.ReadAfter(context.Background(), cursor, 256)
		require.NoError(t, readErr)
		for _, event := range events {
			cursor.Sequence = event.Sequence
			if event.Kind != "Product" || event.Name != product.Name || event.Type != datastore.ResourceWatchAdded {
				continue
			}
			current, getErr := store.GetProduct(context.Background(), product.UID)
			require.NoError(t, getErr)
			terminating, markErr := store.(datastore.ProductLifecycleStore).MarkProductTerminating(context.Background(), current.UID, current.ResourceVersion, "gitstore.dev/foreground-deletion", time.Now().UTC())
			require.NoError(t, markErr)

			terminationDeadline := time.Now().Add(75 * time.Second)
			for time.Now().Before(terminationDeadline) {
				updates, updateErr := journal.ReadAfter(context.Background(), cursor, 256)
				require.NoError(t, updateErr)
				for _, update := range updates {
					cursor.Sequence = update.Sequence
					if update.Kind != "Product" || update.Name != product.Name || update.Type != datastore.ResourceWatchModified {
						continue
					}
					var observed datastore.Product
					require.NoError(t, json.Unmarshal(update.Payload, &observed))
					require.Equal(t, terminating.ResourceVersion, observed.ResourceVersion)
					require.NotNil(t, observed.DeletionTimestamp)
					require.Contains(t, observed.Finalizers, "gitstore.dev/foreground-deletion")
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatal("Product termination was not materialized into the durable journal")
		}
		select {
		case runErr := <-errCh:
			require.NoError(t, runErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("Product creation was not materialized into the durable journal")
}

func TestFileCDCReaderReplaysCommittedWritesAcrossMaterializerReplacement(t *testing.T) {
	store := newTestStore(t)
	peer := newTestStore(t)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	peerJournal := peer.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	acquire := func(holder string) datastore.ResourceWatchLease {
		lease, acquired, err := journal.AcquireLease(t.Context(), holder, time.Now().UTC(), 5*time.Minute)
		require.NoError(t, err)
		require.True(t, acquired)
		return lease
	}
	start := func(backend datastore.Datastore, lease datastore.ResourceWatchLease) func() {
		runner := backend.(catalogCDCTestRunner)
		materializer := watchjournal.NewMaterializer(backend.(datastore.ResourceWatchCapable).ResourceWatchJournal(), watchjournal.MaterializerConfig{EventTTL: time.Hour})
		ctx, cancel := context.WithCancel(t.Context())
		done, ready := make(chan error, 2), make(chan struct{}, 2)
		go func() {
			done <- runner.RunCatalogCDC(ctx, "File", materializer, lease, 14*24*time.Hour, 500*time.Millisecond, func() { ready <- struct{}{} })
		}()
		go func() {
			done <- runner.RunCatalogCDC(ctx, "Product", materializer, lease, 14*24*time.Hour, 500*time.Millisecond, func() { ready <- struct{}{} })
		}()
		var stopped bool
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			for range 2 {
				select {
				case err := <-done:
					if err != nil {
						require.ErrorIs(t, err, context.Canceled)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("catalog CDC worker did not stop")
				}
			}
			require.NoError(t, journal.ReleaseLease(t.Context(), lease))
		}
		t.Cleanup(stop)
		for range 2 {
			select {
			case <-ready:
			case err := <-done:
				t.Fatalf("catalog CDC reader stopped before readiness: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("catalog CDC reader did not become ready")
			}
		}
		return stop
	}
	lease := acquire("file-cdc-integration-a")
	stop := start(store, lease)
	bounds, err := peerJournal.Bounds(t.Context())
	require.NoError(t, err)
	cursor := datastore.ResourceWatchCursor{Epoch: bounds.Epoch, Sequence: bounds.HighWater}
	file := &datastore.File{UID: newID(), Namespace: "cdc-files", Name: "hero-" + newID()[:8],
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", ResourceVersion: "1", Generation: 1,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), RepositoryID: newID(),
		Labels: map[string]string{"team": "media"}, Spec: json.RawMessage(`{"contentType":"image/jpeg","provider":"s3","source":{"uri":"s3://fixture"}}`)}
	next := func(eventType datastore.ResourceWatchEventType, version string) datastore.ResourceWatchEvent {
		var found datastore.ResourceWatchEvent
		require.Eventually(t, func() bool {
			events, err := peerJournal.ReadAfter(t.Context(), cursor, 256)
			require.NoError(t, err)
			for _, event := range events {
				cursor.Sequence = event.Sequence
				if event.Kind != "File" || event.Name != file.Name || event.Type != eventType {
					continue
				}
				if eventType != datastore.ResourceWatchDeleted {
					var observed datastore.File
					require.NoError(t, json.Unmarshal(event.Payload, &observed))
					if observed.ResourceVersion != version {
						continue
					}
					require.Equal(t, file.UID, observed.UID)
					require.Equal(t, file.RepositoryID, observed.RepositoryID)
					require.JSONEq(t, string(file.Spec), string(observed.Spec))
				}
				found = event
				return true
			}
			return false
		}, 75*time.Second, 100*time.Millisecond, "File CDC event %s version %s was not persisted", eventType, version)
		return found
	}
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: newID(), Name: file.Namespace, CreationTimestamp: file.CreationTimestamp,
	}))
	require.NoError(t, store.CreateRepositoryInActiveNamespace(t.Context(), &datastore.Repository{
		UID: file.RepositoryID, Namespace: file.Namespace, Name: "catalog", CreationTimestamp: file.CreationTimestamp,
	}))
	require.NoError(t, store.CreateFile(t.Context(), file))
	added := next(datastore.ResourceWatchAdded, "1")
	require.Equal(t, file.Labels, added.SelectorLabels)
	stop()

	file.ResourceVersion = "2"
	file.Labels = map[string]string{"team": "catalog"}
	require.NoError(t, store.UpdateFile(t.Context(), file, "1"), "write while no materializer is running")
	lease = acquire("file-cdc-integration-b")
	stopPeer := start(peer, lease)
	modified := next(datastore.ResourceWatchModified, "2")
	require.Equal(t, "media", modified.PreviousSelectorLabels["team"])
	require.Equal(t, "catalog", modified.SelectorLabels["team"])
	_, err = peer.UpdateFileStatus(t.Context(), file.Namespace, file.Name, datastore.FileStatusPatch{ResourceVersion: "2"})
	require.NoError(t, err)
	next(datastore.ResourceWatchModified, "3")
	require.NoError(t, store.DeleteFileWithResourceVersion(t.Context(), file.UID, "3"))
	deleted := next(datastore.ResourceWatchDeleted, "")
	require.Empty(t, deleted.Payload)
	require.Equal(t, "catalog", deleted.SelectorLabels["team"])
	stopPeer()
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

// CategoryTaxonomy CDC decodes the hierarchy columns that the shared catalog
// envelope lacks, and journals the whole foreground-deletion lifecycle.
func TestCategoryTaxonomyCDCReaderMaterializesLifecycle(t *testing.T) {
	store := newTestStore(t)
	runner, ok := store.(catalogCDCTestRunner)
	require.True(t, ok)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(context.Background(), "category-integration-reader", time.Now().UTC(), 2*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	materializer := watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: 7 * 24 * time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		errCh <- runner.RunCatalogCDC(ctx, "CategoryTaxonomy", materializer, lease, 14*24*time.Hour, 500*time.Millisecond, func() { close(ready) })
	}()
	select {
	case <-ready:
	case runErr := <-errCh:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("CategoryTaxonomy CDC reader did not become ready")
	}

	category := newCategoryTaxonomy("cdc-categories", "laptops-"+newID()[:8])
	category.ParentName = "computers"
	category.AncestorPath = "electronics/computers/" + category.Name
	category.ResourceVersion = "1"
	require.NoError(t, store.CreateCategoryTaxonomy(context.Background(), category))

	bounds, err := journal.Bounds(context.Background())
	require.NoError(t, err)
	cursor := datastore.ResourceWatchCursor{Epoch: bounds.Epoch}
	if bounds.Oldest > 0 {
		cursor.Sequence = bounds.Oldest - 1
	}
	next := func(eventType datastore.ResourceWatchEventType) datastore.ResourceWatchEvent {
		t.Helper()
		deadline := time.Now().Add(75 * time.Second)
		for time.Now().Before(deadline) {
			events, readErr := journal.ReadAfter(context.Background(), cursor, 256)
			require.NoError(t, readErr)
			for _, event := range events {
				cursor.Sequence = event.Sequence
				if event.Kind == "CategoryTaxonomy" && event.Name == category.Name && event.Type == eventType {
					require.Equal(t, "cdc-categories", event.Namespace)
					return event
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("CategoryTaxonomy %s event was not materialized into the durable journal", eventType)
		return datastore.ResourceWatchEvent{}
	}

	added := next(datastore.ResourceWatchAdded)
	var observed datastore.CategoryTaxonomy
	require.NoError(t, json.Unmarshal(added.Payload, &observed))
	require.Equal(t, category.UID, observed.UID)
	require.Equal(t, "computers", observed.ParentName)
	require.Equal(t, category.AncestorPath, observed.AncestorPath)

	deletion := store.(datastore.CategoryTaxonomyDeletionStore)
	terminating, err := deletion.MarkCategoryTaxonomyDeletion(context.Background(), category.Namespace, category.Name, category.ResourceVersion, time.Now().UTC())
	require.NoError(t, err)
	modified := next(datastore.ResourceWatchModified)
	require.NoError(t, json.Unmarshal(modified.Payload, &observed))
	require.Equal(t, terminating.ResourceVersion, observed.ResourceVersion)
	require.NotNil(t, observed.DeletionTimestamp)

	_, err = deletion.CompleteCategoryTaxonomyDeletion(context.Background(), category.Namespace, category.Name, terminating.ResourceVersion, category.UID)
	require.NoError(t, err)
	deleted := next(datastore.ResourceWatchDeleted)
	require.Empty(t, deleted.Payload)
}
