// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type metrics struct {
	attempts        metric.Float64Counter
	duration        metric.Float64Histogram
	lookups         metric.Float64Counter
	writes          metric.Float64Counter
	handoffs        metric.Float64Counter
	handoffDuration metric.Float64Histogram
}

var defaultMetrics = newMetrics(otel.Meter("gitstore/translation/controller"))

func newMetrics(meter metric.Meter) *metrics {
	return &metrics{
		attempts: observability.MustInstrument(meter.Float64Counter("gitstore_translation_reconciliation_total",
			metric.WithDescription("Translation reconciliation attempts by bounded result, not a readiness certification."))),
		duration: observability.MustInstrument(meter.Float64Histogram("gitstore_translation_reconciliation_duration_seconds",
			metric.WithDescription("Translation reconciliation attempt duration."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5))),
		lookups: observability.MustInstrument(meter.Float64Counter("gitstore_translation_lookup_total",
			metric.WithDescription("Translation projection, authorized snapshot and binding results."))),
		writes: observability.MustInstrument(meter.Float64Counter("gitstore_translation_status_writes_total",
			metric.WithDescription("Translation status apply and no-op outcomes."))),
		handoffs: observability.MustInstrument(meter.Float64Counter("gitstore_translation_target_handoff_total",
			metric.WithDescription("Durable Translation target invalidation handoffs, not completed dependent work."))),
		handoffDuration: observability.MustInstrument(meter.Float64Histogram("gitstore_translation_target_handoff_duration_seconds",
			metric.WithDescription("Translation target proof and durable invalidation handoff duration."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5))),
	}
}

func failureOutcome(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, types.ErrConflict):
		return "conflict"
	case errors.Is(err, types.ErrNotFound):
		return "missing"
	default:
		return "unavailable"
	}
}

func (m *metrics) observeAttempt(ctx context.Context, started time.Time, result types.ReconcileResult) {
	if m == nil {
		return
	}
	outcome := "terminal"
	switch result := result.(type) {
	case types.Success:
		outcome = "success"
	case types.RequeueAfter:
		outcome = "requeue"
	case types.TransientFailure:
		outcome = failureOutcome(result.Err)
		if outcome == "unavailable" {
			outcome = "transient"
		}
	}
	attributes := metric.WithAttributes(attribute.String("outcome", outcome))
	m.attempts.Add(ctx, 1, attributes)
	m.duration.Record(ctx, time.Since(started).Seconds(), attributes)
}

func (m *metrics) observeLookup(ctx context.Context, stage string, found bool, err error) {
	if m == nil {
		return
	}
	if stage != "projection" && stage != "snapshot" && stage != "binding" && stage != "target" {
		stage = "other"
	}
	outcome := "missing"
	if found {
		outcome = "found"
	}
	if err != nil {
		outcome = failureOutcome(err)
	}
	m.lookups.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", stage), attribute.String("outcome", outcome)))
}

func (m *metrics) observeHandoff(ctx context.Context, scope string, started time.Time, err error) {
	if m == nil {
		return
	}
	outcome := "queued"
	switch scope {
	case "":
		scope, outcome = "none", "no_op"
	case "material", "pinned":
	default:
		scope = "unknown"
	}
	if err != nil {
		outcome = failureOutcome(err)
	}
	attributes := metric.WithAttributes(attribute.String("scope", scope), attribute.String("outcome", outcome))
	m.handoffs.Add(ctx, 1, attributes)
	m.handoffDuration.Record(ctx, time.Since(started).Seconds(), attributes)
}

func (m *metrics) observeStatus(ctx context.Context, noOp bool, err error) {
	if m == nil {
		return
	}
	outcome := "applied"
	if noOp {
		outcome = "no_op"
	}
	if err != nil {
		outcome = failureOutcome(err)
	}
	m.writes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
