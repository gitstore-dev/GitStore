// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Product uses the shared Resource Watch journal. Its overload contract is
// intentionally bounded-cardinality: an alert may identify delivery path or
// terminal reason, never a Product identity, namespace, or durable cursor.
func TestProductObservabilityContract_BoundedJournalAndAlertSignals(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := watchjournal.NewMetrics(registry)
	require.NoError(t, err)
	metrics.SetLeader(true)
	metrics.SetSubscribers("typed", 2)
	metrics.IncOverflow()
	metrics.IncExpiry(watchjournal.ReasonSubscriberOverflow)

	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP gitstore_namespace_watch_materializer_leader Whether this replica owns the fenced Namespace CDC materializer lease.
# TYPE gitstore_namespace_watch_materializer_leader gauge
gitstore_namespace_watch_materializer_leader 1
# HELP gitstore_namespace_watch_subscribers Active Namespace watch subscribers.
# TYPE gitstore_namespace_watch_subscribers gauge
gitstore_namespace_watch_subscribers{path="typed"} 2
# HELP gitstore_namespace_watch_expired_total Namespace watches terminated because continuity was not provable.
# TYPE gitstore_namespace_watch_expired_total counter
gitstore_namespace_watch_expired_total{reason="SUBSCRIBER_OVERFLOW"} 1
# HELP gitstore_namespace_watch_overflow_total Namespace subscriber buffer overflows.
# TYPE gitstore_namespace_watch_overflow_total counter
gitstore_namespace_watch_overflow_total 1
`), "gitstore_namespace_watch_materializer_leader", "gitstore_namespace_watch_subscribers", "gitstore_namespace_watch_expired_total", "gitstore_namespace_watch_overflow_total"))

	_, current, _, _ := runtime.Caller(0)
	docPath := filepath.Join(filepath.Dir(current), "..", "..", "..", "docs", "runbooks", "controller-watch-status.md")
	doc, err := os.ReadFile(docPath)
	require.NoError(t, err)
	for _, required := range []string{
		"gitstore_namespace_watch_materializer_leader",
		"gitstore_namespace_watch_overflow_total",
		"gitstore_namespace_watch_expired_total{reason}",
		"delivery p95 exceeds 1 second",
	} {
		require.Contains(t, string(doc), required)
	}
}
