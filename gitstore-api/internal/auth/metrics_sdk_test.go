// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var sdkTestMetrics prometheus.Gatherer

func TestRBACRuntimeMetricsHaveOnlyFiniteLabels(t *testing.T) {
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
	families, err := sdkTestMetrics.Gather()
	require.NoError(t, err)
	found := 0
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
				default:
					t.Errorf("unbounded RBAC metric label: %s", label.GetName())
				}
				require.NotContains(t, label.GetValue(), "private")
			}
		}
	}
	require.Equal(t, 4, found)
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
