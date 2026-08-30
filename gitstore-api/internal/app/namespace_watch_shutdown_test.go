// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type discontinuityNamespaceCDCRunner struct{ calls atomic.Int64 }

func (r *discontinuityNamespaceCDCRunner) RunNamespaceCDC(context.Context, *watchjournal.Materializer, datastore.NamespaceWatchLease, time.Duration, time.Duration, func()) error {
	r.calls.Add(1)
	return datastore.ErrNamespaceWatchDiscontinuity
}

func TestNamespaceWatchLeaderReleasesLeaseBeforeReturning(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	journal := store.(datastore.NamespaceWatchJournal)
	clock := apiruntime.SystemClock{}
	leader := watchjournal.NewLeaseManager(journal, "leader", time.Minute, time.Second, clock)
	lease, acquired, err := leader.Acquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	metrics, err := watchjournal.NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	runtime := &namespaceWatchRuntime{
		journal:      journal,
		materializer: watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: time.Hour, Clock: clock, Metrics: metrics}),
		leaseManager: leader,
		metrics:      metrics,
		cfg:          config.NamespaceWatchConfig{BookmarkIntervalSeconds: 60},
		log:          zap.NewNop(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.runAsLeader(ctx, lease)
	}()
	require.Eventually(t, func() bool {
		bounds, boundsErr := journal.Bounds(context.Background())
		return boundsErr == nil && bounds.HighWater > 0
	}, time.Second, 10*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("namespace watch leader did not stop")
	}

	contender := watchjournal.NewLeaseManager(journal, "contender", time.Minute, time.Second, clock)
	_, acquired, err = contender.Acquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquired, "leader lease must be released before runAsLeader returns")
}

func TestServerCloseWaitsForNamespaceWatchCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		time.Sleep(25 * time.Millisecond)
		close(done)
	}()
	server := &Server{
		log: zap.NewNop(),
		namespaceWatch: &namespaceWatchRuntime{
			cancel: cancel,
			done:   done,
		},
	}

	started := time.Now()
	server.Close()
	require.GreaterOrEqual(t, time.Since(started), 25*time.Millisecond)
}

func TestNamespaceWatchRuntimeStopsRetryingAfterOrderingDiscontinuity(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	journal := store.(datastore.NamespaceWatchJournal)
	metrics, err := watchjournal.NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	runner := &discontinuityNamespaceCDCRunner{}
	runtime := &namespaceWatchRuntime{
		journal: journal, materializer: watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: time.Hour, Metrics: metrics}),
		leaseManager: watchjournal.NewLeaseManager(journal, "leader", time.Minute, time.Second, apiruntime.SystemClock{}),
		metrics:      metrics, runner: runner,
		cfg: config.NamespaceWatchConfig{BookmarkIntervalSeconds: 60, CDCRetentionSeconds: 60, CDCConfidenceWindowMillis: 1},
		log: zap.NewNop(),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.run(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime kept retrying an unrecoverable CDC discontinuity")
	}
	require.EqualValues(t, 1, runner.calls.Load())
}

func TestNamespaceWatchReadinessRefreshesFollowerMetricsFromSharedBounds(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	journal := store.(datastore.NamespaceWatchJournal)
	now := time.Now().UTC()
	lease, acquired, err := journal.AcquireLease(context.Background(), "leader", now, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = journal.Append(context.Background(), lease, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchBookmark, At: now}, time.Hour)
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	metrics, err := watchjournal.NewMetrics(registry)
	require.NoError(t, err)
	runtime := &namespaceWatchRuntime{
		journal: journal, metrics: metrics,
		cfg: config.NamespaceWatchConfig{MaxMaterializerLagSeconds: 60},
	}

	require.NoError(t, namespaceWatchReadiness(context.Background(), runtime, now.Add(time.Second)))
	families, err := registry.Gather()
	require.NoError(t, err)
	observed := 0
	for _, family := range families {
		if family.GetName() != "gitstore_namespace_watch_cdc_lag_seconds" && family.GetName() != "gitstore_namespace_watch_bookmark_age_seconds" {
			continue
		}
		observed++
		require.Len(t, family.Metric, 1)
		require.False(t, math.IsInf(family.Metric[0].GetGauge().GetValue(), 1), "%s must reflect shared bounds", family.GetName())
	}
	require.Equal(t, 2, observed)
}
