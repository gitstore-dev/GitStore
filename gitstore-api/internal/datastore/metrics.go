// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel/metric"
)

type datastoreMetrics struct {
	duration                                                                         metric.Float64Histogram
	errors, projectionFailures, compensationAttempts, compensationFailures, findings metric.Float64Counter
}

func newMetrics(meter metric.Meter) *datastoreMetrics {
	return &datastoreMetrics{
		duration:             observability.MustInstrument(meter.Float64Histogram("gitstore_datastore_operation_duration_seconds", metric.WithDescription("Latency of datastore operations."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10))),
		errors:               observability.MustInstrument(meter.Float64Counter("gitstore_datastore_operation_errors_total", metric.WithDescription("Total datastore operation errors."))),
		projectionFailures:   observability.MustInstrument(meter.Float64Counter("gitstore_datastore_projection_write_failures_total", metric.WithDescription("Projection write failures requiring compensation or repair."))),
		compensationAttempts: observability.MustInstrument(meter.Float64Counter("gitstore_datastore_compensation_attempts_total", metric.WithDescription("Mutation compensation attempts."))),
		compensationFailures: observability.MustInstrument(meter.Float64Counter("gitstore_datastore_compensation_failures_total", metric.WithDescription("Mutation compensation failures requiring operator repair."))),
		findings:             observability.MustInstrument(meter.Float64Counter("gitstore_datastore_projection_findings_total", metric.WithDescription("Observed missing, dangling, duplicate, or stale projections."))),
	}
}
