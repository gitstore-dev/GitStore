// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package health exposes per-kind operational metrics over HTTP.
package health

import (
	"crypto/rand"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var processInstanceID = rand.Text()

var (
	RecoveryInProgress = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_recovery_in_progress", Help: "1 while dispatch is gated on list/watch recovery.",
	}, []string{"kind"})
	RecoveryPages = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_recovery_pages", Help: "High-water number of enumeration pages in the current recovery.",
	}, []string{"kind"})
	RecoveryRows = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_recovery_rows", Help: "Rows enumerated at the recovery page high-water mark.",
	}, []string{"kind"})
	RecoveryLastProgress = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_recovery_last_progress_timestamp_seconds", Help: "Last actual list progress in this recovery.",
	}, []string{"kind"})
	ConflictRequeues = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gitstore_controller_conflict_requeues_total", Help: "Requeues caused by optimistic-concurrency conflicts.",
	}, []string{"kind"})
	processInstanceInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_process_instance_info",
		Help: "Collision-safe identity of this controller-manager process.",
	}, []string{"instance_id"})

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

	CheckpointLastWriteTimestamp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_checkpoint_last_write_timestamp_seconds",
		Help: "Unix timestamp of the last successful checkpoint write per kind.",
	}, []string{"kind"})

	CheckpointWriteFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gitstore_controller_checkpoint_write_failures_total",
		Help: "Total failed checkpoint write attempts per kind.",
	}, []string{"kind"})

	CheckpointReplayBacklog = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_controller_checkpoint_replay_backlog",
		Help: "Number of watch events enqueued as work items but not yet dispatched, per kind.",
	}, []string{"kind"})

	CategoryDeletionProductPagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "category_deletion",
		Name:      "product_pages_total",
		Help:      "Bounded Product dependent pages processed by CategoryTaxonomy deletion.",
	})

	CategoryDeletionConflictsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "category_deletion",
		Name:      "completion_conflicts_total",
		Help:      "Optimistic concurrency conflicts while completing CategoryTaxonomy deletion.",
	})

	CategoryDeletionRetriesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "category_deletion",
		Name:      "retries_total",
		Help:      "Transient CategoryTaxonomy deletion reconciliation retries.",
	})
)

func init() {
	processInstanceInfo.WithLabelValues(processInstanceID).Set(1)
}
