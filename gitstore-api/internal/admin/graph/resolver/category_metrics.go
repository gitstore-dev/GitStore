// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	categoryMutationTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore", Subsystem: "category", Name: "mutation_total",
		Help: "Category mutations by operation and outcome (success, error, or the error code).",
	}, []string{"operation", "outcome"})
	categoryMutationDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "gitstore", Subsystem: "category", Name: "mutation_duration_seconds",
		Help:    "Category mutation latency, dominated by one Git commit and one admission.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5, 10},
	}, []string{"operation"})
)
