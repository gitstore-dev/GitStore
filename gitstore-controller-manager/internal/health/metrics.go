// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package health exposes per-kind operational metrics over HTTP.
package health

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	QueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_queue_depth",
		Help: "Number of items waiting in the work queue per kind.",
	}, []string{"kind"})

	ActiveWorkers = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_active_workers",
		Help: "Number of goroutines actively reconciling items per kind.",
	}, []string{"kind"})

	PoisonItemsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_poison_items_total",
		Help: "Number of quarantined (poison) items per kind.",
	}, []string{"kind"})

	StalledWorkers = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_stalled_workers",
		Help: "1 if the reconciler for a kind is stalled (no successful reconcile within StallThreshold).",
	}, []string{"kind"})

	ReconcileTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gitstore_controller_reconcile_total",
		Help: "Total reconcile attempts per kind and result.",
	}, []string{"kind", "result"})
)
