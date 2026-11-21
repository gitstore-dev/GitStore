// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"math"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/observability/observabilitytest"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceWatchMetricsRegisterBoundedSignals(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(observabilitytest.Meter(t, registry, "gitstore/watchjournal"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metrics.Close()) })
	metrics.SetLeader(true)
	metrics.SetSubscribers("typed", 2)
	metrics.IncExpiry(ReasonRetentionExpired)
	metrics.ObserveCDCDiscovery(time.Now().Add(-100*time.Millisecond), time.Now())
	metrics.ObserveMaterializerStage("journal_append", 25*time.Millisecond)
	metrics.ObserveMaterializerBatchSize(8)

	count, err := testutil.GatherAndCount(registry,
		"gitstore_resource_watch_materializer_leader",
		"gitstore_resource_watch_subscribers",
		"gitstore_resource_watch_expired_total",
		"gitstore_resource_watch_cdc_discovery_seconds",
		"gitstore_resource_watch_materializer_stage_duration_seconds",
		"gitstore_resource_watch_materializer_batch_size",
	)
	require.NoError(t, err)
	assert.Equal(t, 6, count)
}

func TestNamespaceWatchDuplicateMetricCountsNewCursorOnce(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(observabilitytest.Meter(t, registry, "gitstore/watchjournal"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metrics.Close()) })
	now := time.Now().UTC()
	first := datastore.NamespaceWatchEvent{
		Type: datastore.NamespaceWatchAdded, Sequence: 10, DeduplicationKey: "stream:position", At: now,
	}
	duplicate := first
	duplicate.Sequence = 11

	metrics.ObserveDelivery(first, now)
	metrics.ObserveDelivery(first, now) // Another subscriber sees the same cursor.
	metrics.ObserveDelivery(duplicate, now)
	metrics.ObserveDelivery(duplicate, now)

	assert.Equal(t, float64(1), observabilitytest.Value(t, registry, "gitstore_resource_watch_duplicates_total", nil))
}

func TestNamespaceWatchCDCLagAdvancesBetweenScrapes(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(observabilitytest.Meter(t, registry, "gitstore/watchjournal"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metrics.Close()) })
	metrics.ObserveCDCProgress(time.Now().Add(-time.Second))

	first := observabilitytest.Value(t, registry, "gitstore_resource_watch_cdc_lag_seconds", nil)
	time.Sleep(20 * time.Millisecond)
	second := observabilitytest.Value(t, registry, "gitstore_resource_watch_cdc_lag_seconds", nil)

	assert.GreaterOrEqual(t, first, 1.0)
	assert.Greater(t, second, first)
}

func TestNamespaceWatchCDCLagDoesNotRegressForLateStream(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(observabilitytest.Meter(t, registry, "gitstore/watchjournal"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metrics.Close()) })
	newer := time.Now().Add(-time.Second)

	metrics.ObserveCDCProgress(newer)
	metrics.ObserveCDCProgress(newer.Add(-time.Hour))

	assert.Equal(t, newer.UnixNano(), metrics.cdcProgressNanos.Load())
}

func TestNamespaceWatchMissingProgressAndBookmarkAreStale(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(observabilitytest.Meter(t, registry, "gitstore/watchjournal"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metrics.Close()) })

	assert.True(t, math.IsInf(observabilitytest.Value(t, registry, "gitstore_resource_watch_cdc_lag_seconds", nil), 1))
	assert.True(t, math.IsInf(observabilitytest.Value(t, registry, "gitstore_resource_watch_bookmark_age_seconds", nil), 1))
}

func TestNamespaceWatchBookmarkAgeAdvancesBetweenScrapes(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(observabilitytest.Meter(t, registry, "gitstore/watchjournal"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metrics.Close()) })
	metrics.ObserveBookmark(time.Now().Add(-time.Second))

	first := observabilitytest.Value(t, registry, "gitstore_resource_watch_bookmark_age_seconds", nil)
	time.Sleep(20 * time.Millisecond)
	second := observabilitytest.Value(t, registry, "gitstore_resource_watch_bookmark_age_seconds", nil)

	assert.GreaterOrEqual(t, first, 1.0)
	assert.Greater(t, second, first)
}

func TestNamespaceWatchBoundsTrackOnlyActualBookmarks(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(observabilitytest.Meter(t, registry, "gitstore/watchjournal"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metrics.Close()) })
	bookmarkAt := time.Now().Add(-time.Minute)

	metrics.SetBounds(datastore.NamespaceWatchBounds{
		UpdatedAt: time.Now(), BookmarkAt: bookmarkAt, ProgressAt: time.Now(),
	}, time.Now())
	first := observabilitytest.Value(t, registry, "gitstore_resource_watch_bookmark_age_seconds", nil)
	time.Sleep(20 * time.Millisecond)
	metrics.SetBounds(datastore.NamespaceWatchBounds{
		UpdatedAt: time.Now(), BookmarkAt: bookmarkAt, ProgressAt: time.Now(),
	}, time.Now())
	second := observabilitytest.Value(t, registry, "gitstore_resource_watch_bookmark_age_seconds", nil)

	assert.GreaterOrEqual(t, first, 60.0)
	assert.Greater(t, second, first)
}

func TestReadinessRequiresRecentMaterializerAndContinuousJournal(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	readiness := NewReadiness(60 * time.Second)

	readiness.Update(MaterializerStatus{LastProgressAt: now.Add(-30 * time.Second), JournalContinuous: true})
	assert.NoError(t, readiness.Check(now))

	readiness.Update(MaterializerStatus{LastProgressAt: now.Add(-61 * time.Second), JournalContinuous: true})
	err := readiness.Check(now)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaterializerNotReady)

	readiness.Update(MaterializerStatus{LastProgressAt: now, JournalContinuous: false})
	err = readiness.Check(now)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaterializerNotReady)
}
