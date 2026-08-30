// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
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
