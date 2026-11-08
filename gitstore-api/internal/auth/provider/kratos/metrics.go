// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type metrics struct {
	requests         metric.Int64Counter
	duration         metric.Float64Histogram
	cache            metric.Int64Counter
	saturation       metric.Int64Counter
	deliveryAttempts metric.Int64Counter
	deliveryOutcomes metric.Int64Counter
	deliveryDuration metric.Float64Histogram
	deliveryBuffer   metric.Int64Histogram
}

func newMetrics(meter metric.Meter) *metrics {
	return &metrics{
		requests: observability.MustInstrument(meter.Int64Counter("gitstore_userdir_requests_total",
			metric.WithDescription("Kratos Admin requests by bounded method and outcome."))),
		duration: observability.MustInstrument(meter.Float64Histogram("gitstore_userdir_request_duration_seconds",
			metric.WithUnit("s"), metric.WithDescription("Kratos Admin request latency."),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2))),
		cache: observability.MustInstrument(meter.Int64Counter("gitstore_userdir_cache_total",
			metric.WithDescription("Directory lookup cache hits and misses."))),
		saturation: observability.MustInstrument(meter.Int64Counter("gitstore_userdir_saturation_total",
			metric.WithDescription("Requests rejected at the directory concurrency bound."))),
		deliveryAttempts: observability.MustInstrument(meter.Int64Counter("gitstore_userdir_serviceaccount_attempts_total",
			metric.WithDescription("Best-effort workload mirror application attempts."))),
		deliveryOutcomes: observability.MustInstrument(meter.Int64Counter("gitstore_userdir_serviceaccount_delivery_total",
			metric.WithDescription("Live workload delivery outcomes; not durable acknowledgements."))),
		deliveryDuration: observability.MustInstrument(meter.Float64Histogram("gitstore_userdir_serviceaccount_delivery_duration_seconds",
			metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.01, .1, .5, 1, 2, 5, 10))),
		deliveryBuffer: observability.MustInstrument(meter.Int64Histogram("gitstore_userdir_serviceaccount_buffer_depth",
			metric.WithExplicitBucketBoundaries(0, 1, 8, 16, 32, 64))),
	}
}

func (m *metrics) observe(ctx context.Context, method string, start time.Time, err error) {
	switch method {
	case "GET", "POST", "PATCH", "DELETE":
	default:
		method = "other"
	}
	options := metric.WithAttributes(attribute.String("method", method),
		attribute.String("outcome", auth.DirectoryErrorClass(err)))
	m.requests.Add(ctx, 1, options)
	m.duration.Record(ctx, time.Since(start).Seconds(), options)
}
