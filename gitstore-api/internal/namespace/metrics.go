// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	validationRejections *prometheus.CounterVec
	deletionRejections   *prometheus.CounterVec
	deletionOutcomes     *prometheus.CounterVec
	validationDuration   *prometheus.HistogramVec
	admissionDuration    *prometheus.HistogramVec
}

var capacityLatencyBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5}

var defaultMetrics = NewMetrics(prometheus.DefaultRegisterer)

func DefaultMetrics() *Metrics {
	return defaultMetrics
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		validationRejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gitstore",
			Subsystem: "namespace",
			Name:      "validation_rejections_total",
			Help:      "Namespace validation rejections by bounded phase and reason.",
		}, []string{"phase", "reason"}),
		deletionRejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gitstore",
			Subsystem: "namespace",
			Name:      "deletion_rejections_total",
			Help:      "Namespace deletion rejections by bounded reason.",
		}, []string{"reason"}),
		deletionOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gitstore",
			Subsystem: "namespace",
			Name:      "deletion_outcomes_total",
			Help:      "Successful Namespace deletion requests by bounded outcome.",
		}, []string{"outcome"}),
		validationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gitstore",
			Subsystem: "namespace",
			Name:      "validation_duration_seconds",
			Help:      "Namespace validation latency by phase.",
			Buckets:   capacityLatencyBuckets,
		}, []string{"phase"}),
		admissionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gitstore",
			Subsystem: "namespace",
			Name:      "admission_stage_duration_seconds",
			Help:      "Namespace GraphQL admission latency at bounded internal stages.",
			Buckets:   capacityLatencyBuckets,
		}, []string{"stage"}),
	}
	reg.MustRegister(
		metrics.validationRejections,
		metrics.deletionRejections,
		metrics.deletionOutcomes,
		metrics.validationDuration,
		metrics.admissionDuration,
	)
	return metrics
}

func (m *Metrics) ObserveAdmissionStage(stage string, duration time.Duration) {
	if m == nil {
		return
	}
	m.admissionDuration.WithLabelValues(stage).Observe(duration.Seconds())
}

func (m *Metrics) ObserveRejection(phase Phase, reason Reason) {
	if m == nil {
		return
	}
	m.validationRejections.WithLabelValues(string(phase), string(reason)).Inc()
}

func (m *Metrics) ObserveDeletionBlocked(reason Reason) {
	if m == nil {
		return
	}
	m.deletionRejections.WithLabelValues(string(reason)).Inc()
}

func (m *Metrics) ObserveDeletionOutcome(outcome DeletionOutcome) {
	if m == nil {
		return
	}
	m.deletionOutcomes.WithLabelValues(string(outcome)).Inc()
}

func (m *Metrics) ObserveValidationDuration(phase Phase, duration time.Duration) {
	if m == nil {
		return
	}
	m.validationDuration.WithLabelValues(string(phase)).Observe(duration.Seconds())
}
