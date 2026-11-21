// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var sdkTestMetrics prometheus.Gatherer

func TestRBACRuntimeMetricsHaveOnlyFiniteLabels(t *testing.T) {
	resources, err := auth.NewRBACResourceMetrics(func() auth.RBACResourceUsage { return auth.RBACResourceUsage{} })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resources.Close()) })
	for _, cluster := range []bool{false, true} {
		auth.ObserveRBACRebuild(t.Context(), cluster, true, time.Millisecond)
		auth.ObserveRBACRebuild(t.Context(), cluster, false, time.Second)
		auth.ObserveRBACRecovery(t.Context(), cluster, time.Second)
		auth.ObserveRBACUnavailable(t.Context(), "queue", cluster)
		for i := range 3 {
			auth.ObserveRBACUnavailable(t.Context(), fmt.Sprintf("private-namespace-%d", i), cluster)
		}
	}
	auth.ObserveRBACSourceAge(t.Context(), time.Second)
	auth.ObserveRBACSourceFailure(t.Context(), "journal-read")
	for _, stage := range []string{"cluster-origin", "namespace-origin", "serving-progress",
		"rebuild-scratch", "rebuild-snapshot", "rebuild-journal", "rebuild-prepare",
		"rebuild-origin", "rebuild-origin-busy", "rebuild-continuity", "rebuild-install"} {
		auth.ObserveRBACSourceFailure(t.Context(), stage)
	}
	for i := range 3 {
		auth.ObserveRBACSourceFailure(t.Context(), fmt.Sprintf("private-source-%d", i))
	}
	families, err := sdkTestMetrics.Gather()
	require.NoError(t, err)
	found := 0
	stages := map[string]bool{}
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "gitstore_authz_rbac_") {
			continue
		}
		found++
		for _, sample := range family.Metric {
			for _, label := range sample.Label {
				switch label.GetName() {
				case "tier_class":
					require.Contains(t, []string{"cluster", "namespace"}, label.GetValue())
				case "outcome":
					require.Contains(t, []string{"success", "error"}, label.GetValue())
				case "reason":
					require.Contains(t, []string{"tier", "source", "queue", "evaluation", "snapshot", "budget", "unknown"}, label.GetValue())
				case "stage":
					require.Contains(t, []string{"journal-bounds", "frontier-read", "frontier-stale", "journal-epoch", "journal-high-water",
						"journal-retention", "journal-read", "event-sequence", "event-stale", "event-payload",
						"cluster-origin", "namespace-origin", "serving-progress",
						"rebuild-scratch", "rebuild-snapshot", "rebuild-journal", "rebuild-prepare",
						"rebuild-origin", "rebuild-origin-busy", "rebuild-continuity", "rebuild-install", "unknown"}, label.GetValue())
					stages[label.GetValue()] = true
				case "pool":
					require.Contains(t, []string{"snapshot", "rebuild"}, label.GetValue())
				default:
					t.Errorf("unbounded RBAC metric label: %s", label.GetName())
				}
				require.NotContains(t, label.GetValue(), "private")
			}
		}
	}
	require.Equal(t, 12, found)
	for _, stage := range []string{"cluster-origin", "namespace-origin", "serving-progress",
		"rebuild-scratch", "rebuild-snapshot", "rebuild-journal", "rebuild-prepare",
		"rebuild-origin", "rebuild-origin-busy", "rebuild-continuity", "rebuild-install"} {
		require.True(t, stages[stage], "serving failures must distinguish the actual failed proof: %s", stage)
	}
}

func TestRBACResourceMetricsFollowReservationsAndReleaseTheirCallback(t *testing.T) {
	budget := rbac.NewMemoryBudget(100)
	old, err := budget.Reserve(60)
	require.NoError(t, err)
	t.Cleanup(old.Close)
	var reads atomic.Int64
	resources, err := auth.NewRBACResourceMetrics(func() auth.RBACResourceUsage {
		reads.Add(1)
		return auth.RBACResourceUsage{
			Snapshot:           auth.RBACBudgetUsage(budget.Usage()),
			Rebuild:            auth.RBACBudgetUsage{Reserved: 128, PeakReserved: 192, Limit: 256},
			RebuildBufferLimit: 64, DirtyQueue: 3, DirtyQueuePeak: 8, DirtyQueueLimit: 10,
		}
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resources.Close()) })
	gather := func() map[string]map[string]float64 {
		t.Helper()
		families, err := sdkTestMetrics.Gather()
		require.NoError(t, err)
		values := make(map[string]map[string]float64)
		for _, family := range families {
			if !strings.HasPrefix(family.GetName(), "gitstore_authz_rbac_") || len(family.Metric) == 0 || family.Metric[0].Gauge == nil {
				continue
			}
			values[family.GetName()] = make(map[string]float64)
			for _, sample := range family.Metric {
				pool := ""
				for _, label := range sample.Label {
					require.Equal(t, "pool", label.GetName())
					pool = label.GetValue()
				}
				require.NotContains(t, values[family.GetName()], pool, "retired runtimes must not retain duplicate observations")
				values[family.GetName()][pool] = sample.GetGauge().GetValue()
			}
		}
		return values
	}
	values := gather()
	require.EqualValues(t, 60, values["gitstore_authz_rbac_reserved_bytes"]["snapshot"])
	require.EqualValues(t, 100, values["gitstore_authz_rbac_reservation_limit_bytes"]["snapshot"])
	require.EqualValues(t, 256, values["gitstore_authz_rbac_reservation_limit_bytes"]["rebuild"])
	require.EqualValues(t, 64, values["gitstore_authz_rbac_rebuild_buffer_limit_bytes"][""],
		"the per-buffer ceiling must not be confused with the aggregate worker reservation")
	require.EqualValues(t, 3, values["gitstore_authz_rbac_dirty_queue_entries"][""])
	require.EqualValues(t, 8, values["gitstore_authz_rbac_dirty_queue_peak_entries"][""])
	require.EqualValues(t, 10, values["gitstore_authz_rbac_dirty_queue_limit_entries"][""])
	replacement, err := budget.Reserve(40)
	require.NoError(t, err)
	t.Cleanup(replacement.Close)
	old.Close()
	values = gather()
	require.EqualValues(t, 40, values["gitstore_authz_rbac_reserved_bytes"]["snapshot"])
	require.EqualValues(t, 100, values["gitstore_authz_rbac_peak_reserved_bytes"]["snapshot"])
	require.NoError(t, resources.Close())
	before := reads.Load()
	require.NotContains(t, gather(), "gitstore_authz_rbac_reserved_bytes")
	require.Equal(t, before, reads.Load(), "shutdown must unregister the callback rather than retain the old runtime")
	next, err := auth.NewRBACResourceMetrics(func() auth.RBACResourceUsage {
		return auth.RBACResourceUsage{Snapshot: auth.RBACBudgetUsage{Reserved: 7, PeakReserved: 7, Limit: 100}}
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, next.Close()) })
	values = gather()
	require.Len(t, values["gitstore_authz_rbac_reserved_bytes"], 2)
	require.EqualValues(t, 7, values["gitstore_authz_rbac_reserved_bytes"]["snapshot"])
	require.Equal(t, before, reads.Load(), "a replacement must not revive the retired runtime callback")
}

func TestMain(m *testing.M) {
	_, shutdown, err := observability.InitTelemetry(context.Background(), observability.OTLPConfig{}, observability.APIService, "test", zap.NewNop())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sdkTestMetrics = observabilitytest.HTTPGatherer(observability.MetricsHandler())
	code := m.Run()
	_ = shutdown(context.Background())
	os.Exit(code)
}
