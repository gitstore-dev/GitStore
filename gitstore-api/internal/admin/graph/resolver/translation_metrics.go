// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type translationReadMetrics struct {
	requests  metric.Float64Counter
	duration  metric.Float64Histogram
	resources metric.Float64Counter
	events    metric.Float64Counter
}

var defaultTranslationReadMetrics = newTranslationReadMetrics(otel.Meter("gitstore/translation/read"))

func newTranslationReadMetrics(meter metric.Meter) *translationReadMetrics {
	return &translationReadMetrics{
		requests: observability.MustInstrument(meter.Float64Counter("gitstore_translation_guard_requests_total",
			metric.WithDescription("Translation guard requests by bounded route and completion outcome."))),
		duration: observability.MustInstrument(meter.Float64Histogram("gitstore_translation_guard_duration_seconds",
			metric.WithDescription("Translation guard request duration, including page work."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5))),
		resources: observability.MustInstrument(meter.Float64Counter("gitstore_translation_guard_resources_total",
			metric.WithDescription("Translation observations returned by the guard, by bounded evidence outcome."))),
		events: observability.MustInstrument(meter.Float64Counter("gitstore_translation_watch_events_total",
			metric.WithDescription("Translation watch event conversion outcomes; not client delivery acknowledgements."))),
	}
}

func translationReadOutcome(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case translationGuardForbidden(err):
		return "forbidden"
	case err != nil:
		return "unavailable"
	default:
		return "completed"
	}
}

func (m *translationReadMetrics) observeWatch(ctx context.Context, mode string, err error) {
	if m == nil {
		return
	}
	if mode != "live" && mode != "historical" && mode != "bookmark" {
		mode = "other"
	}
	outcome := translationReadOutcome(err)
	if err == nil {
		outcome = "converted"
	}
	m.events.Add(ctx, 1, metric.WithAttributes(attribute.String("mode", mode), attribute.String("outcome", outcome)))
}

func (m *translationReadMetrics) observeGuard(ctx context.Context, route string, started time.Time, items []guardedTranslation, err error) {
	if m == nil {
		return
	}
	if route != "single" && route != "page" {
		route = "other"
	}
	outcome := translationReadOutcome(err)
	attributes := metric.WithAttributes(attribute.String("route", route), attribute.String("outcome", outcome))
	m.requests.Add(ctx, 1, attributes)
	m.duration.Record(ctx, time.Since(started).Seconds(), attributes)
	if err != nil {
		return
	}
	for _, item := range items {
		evidence := "unavailable"
		if item.status != nil {
			for _, condition := range item.status.Conditions {
				if condition.Type != catalog.ConditionReady {
					continue
				}
				switch condition.Reason {
				case "StaleEvidence":
					evidence = "stale"
				case "Forbidden":
					evidence = "forbidden"
				case "Unavailable":
					evidence = "unavailable"
				default:
					evidence = "not_ready"
					if condition.Status == catalog.ConditionTrue {
						evidence = "current"
					}
				}
			}
		}
		m.resources.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", evidence)))
	}
}
