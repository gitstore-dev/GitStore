// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "github.com/prometheus/client_golang/prometheus"

type datastoreMetrics struct {
	duration             *prometheus.HistogramVec
	errors               *prometheus.CounterVec
	projectionFailures   *prometheus.CounterVec
	compensationAttempts *prometheus.CounterVec
	compensationFailures *prometheus.CounterVec
	findings             *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *datastoreMetrics {
	metrics := &datastoreMetrics{}
	metrics.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "operation_duration_seconds",
		Help:      "Latency of datastore operations.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"operation", "backend"})

	metrics.errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "operation_errors_total",
		Help:      "Total datastore operation errors.",
	}, []string{"operation", "backend"})

	metrics.projectionFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "projection_write_failures_total",
		Help:      "Projection write failures requiring compensation or repair.",
	}, []string{"operation", "backend", "resource_kind", "projection"})

	metrics.compensationAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "compensation_attempts_total",
		Help:      "Mutation compensation attempts.",
	}, []string{"operation", "backend", "resource_kind", "projection"})

	metrics.compensationFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "compensation_failures_total",
		Help:      "Mutation compensation failures requiring operator repair.",
	}, []string{"operation", "backend", "resource_kind", "projection"})

	metrics.findings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "projection_findings_total",
		Help:      "Observed missing, dangling, duplicate, or stale projections.",
	}, []string{"operation", "backend", "resource_kind", "projection", "finding_type"})

	reg.MustRegister(
		metrics.duration,
		metrics.errors,
		metrics.projectionFailures,
		metrics.compensationAttempts,
		metrics.compensationFailures,
		metrics.findings,
	)
	return metrics
}
