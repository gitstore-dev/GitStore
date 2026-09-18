// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	categoryDeletionDependentLookupDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "gitstore",
			Subsystem: "category_deletion",
			Name:      "dependent_lookup_duration_seconds",
			Help:      "Latency of the limit-one blocking child lookup.",
		},
	)
	categoryDeletionBlockedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "gitstore",
			Subsystem: "category_deletion",
			Name:      "blocked_total",
			Help:      "Category deletion requests blocked by a child owner reference.",
		},
	)
	productDeletionDependentLookupDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "gitstore", Subsystem: "product_deletion", Name: "dependent_lookup_duration_seconds",
			Help: "Latency of the bounded blocking ProductVariant lookup.",
		},
	)
	productDeletionBlockedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "gitstore", Subsystem: "product_deletion", Name: "blocked_total",
			Help: "Product deletion requests blocked by a ProductVariant owner reference.",
		},
	)
)
