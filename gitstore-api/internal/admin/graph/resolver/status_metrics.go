// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// StatusWriteConflictsTotal counts status-write requests rejected due to a
// resourceVersion precondition mismatch, labeled by kind. A sustained
// non-zero rate for a kind indicates either two controllers racing on the
// same status sub-fields or a controller operating on stale cache data
// (spec 040 FR-014).
var StatusWriteConflictsTotal = observability.MustInstrument(otel.Meter("gitstore/resolver").Float64Counter("gitstore_status_write_conflicts_total", metric.WithDescription("Total status-update requests rejected due to a resourceVersion conflict, by kind.")))
