// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "github.com/prometheus/client_golang/prometheus"

// newMetrics creates a latency histogram and error counter registered on reg.
func newMetrics(reg prometheus.Registerer) (*prometheus.HistogramVec, *prometheus.CounterVec) {
	dur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "operation_duration_seconds",
		Help:      "Latency of datastore operations.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"operation", "backend"})

	errs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "operation_errors_total",
		Help:      "Total datastore operation errors.",
	}, []string{"operation", "backend"})

	reg.MustRegister(dur, errs)
	return dur, errs
}
