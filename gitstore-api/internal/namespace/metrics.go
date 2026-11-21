// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	validationRejections metric.Float64Counter
	deletionRejections   metric.Float64Counter
	deletionOutcomes     metric.Float64Counter
	validationDuration   metric.Float64Histogram
	admissionDuration    metric.Float64Histogram
}

var capacityLatencyBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5}

var defaultMetrics = NewMetrics(otel.Meter("gitstore/namespace"))

func DefaultMetrics() *Metrics {
	return defaultMetrics
}

func NewMetrics(meter metric.Meter) *Metrics {
	metrics := &Metrics{
		validationRejections: observability.MustInstrument(meter.Float64Counter("gitstore_namespace_validation_rejections_total", metric.WithDescription("Namespace validation rejections by bounded error code and reason."))),
		deletionRejections:   observability.MustInstrument(meter.Float64Counter("gitstore_namespace_deletion_rejections_total", metric.WithDescription("Namespace deletion rejections by bounded reason."))),
		deletionOutcomes:     observability.MustInstrument(meter.Float64Counter("gitstore_namespace_deletion_outcomes_total", metric.WithDescription("Successful Namespace deletion requests by bounded outcome."))),
		validationDuration:   observability.MustInstrument(meter.Float64Histogram("gitstore_namespace_validation_duration_seconds", metric.WithDescription("Namespace validation latency by stage."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(capacityLatencyBuckets...))),
		admissionDuration:    observability.MustInstrument(meter.Float64Histogram("gitstore_namespace_admission_stage_duration_seconds", metric.WithDescription("Namespace GraphQL admission latency at bounded internal stages."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(capacityLatencyBuckets...))),
	}

	return metrics
}

func (m *Metrics) ObserveAdmissionStage(stage string, duration time.Duration) {
	if m == nil {
		return
	}
	m.admissionDuration.Record(context.Background(), duration.Seconds(), metric.WithAttributes(attribute.String("stage", stage)))
}

func (m *Metrics) ObserveRejection(reason Reason) {
	if m == nil {
		return
	}
	m.validationRejections.Add(context.Background(), 1, metric.WithAttributes(attribute.String("code", string(CodeForReason(reason))), attribute.String("reason", string(reason))))
}

func (m *Metrics) ObserveDeletionBlocked(reason Reason) {
	if m == nil {
		return
	}
	m.deletionRejections.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", string(reason))))
}

func (m *Metrics) ObserveDeletionOutcome(outcome DeletionOutcome) {
	if m == nil {
		return
	}
	m.deletionOutcomes.Add(context.Background(), 1, metric.WithAttributes(attribute.String("outcome", string(outcome))))
}

func (m *Metrics) ObserveValidationDuration(stage ValidationStage, duration time.Duration) {
	if m == nil {
		return
	}
	m.validationDuration.Record(context.Background(), duration.Seconds(), metric.WithAttributes(attribute.String("stage", string(stage))))
}
