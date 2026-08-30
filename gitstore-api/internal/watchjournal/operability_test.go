// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"math"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceWatchMetricsRegisterBoundedSignals(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)
	metrics.SetLeader(true)
	metrics.SetSubscribers("typed", 2)
	metrics.IncExpiry(ReasonRetentionExpired)

	assert.Equal(t, 3, testutil.CollectAndCount(metrics,
		"gitstore_namespace_watch_materializer_leader",
		"gitstore_namespace_watch_subscribers",
		"gitstore_namespace_watch_expired_total",
	))
}

func TestNamespaceWatchCDCLagAdvancesBetweenScrapes(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)
	metrics.ObserveCDCProgress(time.Now().Add(-time.Second))

	first := testutil.ToFloat64(metrics.cdcLag)
	time.Sleep(20 * time.Millisecond)
	second := testutil.ToFloat64(metrics.cdcLag)

	assert.GreaterOrEqual(t, first, 1.0)
	assert.Greater(t, second, first)
}

func TestNamespaceWatchMissingProgressAndBookmarkAreStale(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)

	assert.True(t, math.IsInf(testutil.ToFloat64(metrics.cdcLag), 1))
	assert.True(t, math.IsInf(testutil.ToFloat64(metrics.bookmarkAge), 1))
}

func TestNamespaceWatchBookmarkAgeAdvancesBetweenScrapes(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)
	metrics.ObserveBookmark(time.Now().Add(-time.Second))

	first := testutil.ToFloat64(metrics.bookmarkAge)
	time.Sleep(20 * time.Millisecond)
	second := testutil.ToFloat64(metrics.bookmarkAge)

	assert.GreaterOrEqual(t, first, 1.0)
	assert.Greater(t, second, first)
}

func TestNamespaceWatchBoundsTrackOnlyActualBookmarks(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)
	bookmarkAt := time.Now().Add(-time.Minute)

	metrics.SetBounds(datastore.NamespaceWatchBounds{
		UpdatedAt: time.Now(), BookmarkAt: bookmarkAt, ProgressAt: time.Now(),
	}, time.Now())
	first := testutil.ToFloat64(metrics.bookmarkAge)
	time.Sleep(20 * time.Millisecond)
	metrics.SetBounds(datastore.NamespaceWatchBounds{
		UpdatedAt: time.Now(), BookmarkAt: bookmarkAt, ProgressAt: time.Now(),
	}, time.Now())
	second := testutil.ToFloat64(metrics.bookmarkAge)

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
