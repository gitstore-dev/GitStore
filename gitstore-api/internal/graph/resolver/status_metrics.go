// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// StatusWriteConflictsTotal counts status-write requests rejected due to a
// resourceVersion precondition mismatch, labeled by kind. A sustained
// non-zero rate for a kind indicates either two controllers racing on the
// same status sub-fields or a controller operating on stale cache data
// (spec 040 FR-014).
var StatusWriteConflictsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "gitstore",
	Subsystem: "status_write",
	Name:      "conflicts_total",
	Help:      "Total status-update requests rejected due to a resourceVersion conflict, by kind.",
}, []string{"kind"})
