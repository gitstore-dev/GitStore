// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package health exposes per-kind operational metrics over HTTP.
package health

import (
	"context"
	"crypto/rand"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var processInstanceID = rand.Text()

var (
	RecoveryInProgress   = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_recovery_in_progress", metric.WithDescription("1 while dispatch is gated on list/watch recovery.")))
	RecoveryPages        = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_recovery_pages", metric.WithDescription("High-water number of enumeration pages in the current recovery.")))
	RecoveryRows         = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_recovery_rows", metric.WithDescription("Rows enumerated at the recovery page high-water mark.")))
	RecoveryLastProgress = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_recovery_last_progress_timestamp_seconds", metric.WithDescription("Last actual list progress in this recovery."), metric.WithUnit("s")))
	ConflictRequeues     = observability.MustInstrument(otel.Meter("gitstore/health").Float64Counter("gitstore_controller_conflict_requeues_total", metric.WithDescription("Requeues caused by optimistic-concurrency conflicts.")))
	_                    = observability.MustInstrument(otel.Meter("gitstore/health").Float64ObservableGauge(
		"gitstore_controller_process_instance_info", metric.WithDescription("Collision-safe identity of this controller-manager process."),
		metric.WithFloat64Callback(func(ctx context.Context, observer metric.Float64Observer) error {
			observer.Observe(1, metric.WithAttributes(attribute.String("instance_id", processInstanceID)))
			return nil
		})))

	QueueDepth = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_queue_depth", metric.WithDescription("Number of items waiting in the work queue per kind.")))

	OldestPendingTimestamp = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_oldest_pending_timestamp_seconds", metric.WithDescription("Oldest timestamped unfinished disk obligation, including delayed and quarantined work; zero when none is timestamped. Complete oldest-age evidence requires pending_without_timestamp to be zero."), metric.WithUnit("s")))

	PendingWithoutTimestamp = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_pending_without_timestamp", metric.WithDescription("Unfinished disk obligations without an original enqueue timestamp; age is unknown, not zero.")))

	KindReady = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_kind_ready", metric.WithDescription("1 when this kind is synced, not recovering or stalled, and has no poison work. Excludes credential readiness and feature activation or target validity certification.")))

	WorkCompletionLag = observability.MustInstrument(otel.Meter("gitstore/health").Float64Histogram("gitstore_controller_work_completion_lag_seconds",
		metric.WithDescription("Original enqueue to durable acknowledgement for timestamped reconciliation work, including queueing and retries. Not target Ready or index audit certification."),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.5, 1, 3, 10, 30, 60, 300, 1800, 3600)))

	WorkCompletions = observability.MustInstrument(otel.Meter("gitstore/health").Float64Counter("gitstore_controller_work_completions_total",
		metric.WithDescription("Durably acknowledged work by kind and known or unknown enqueue age; superseded leases and failed writes are not completions.")))

	ActiveWorkers = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_active_workers", metric.WithDescription("Number of goroutines actively reconciling items per kind.")))

	PoisonItemsTotal = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_poison_items_total", metric.WithDescription("Number of quarantined (poison) items per kind.")))

	StalledWorkers = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_stalled_workers", metric.WithDescription("1 if the reconciler for a kind is stalled (no successful reconcile within StallThreshold).")))

	ReconcileTotal = observability.MustInstrument(otel.Meter("gitstore/health").Float64Counter("gitstore_controller_reconcile_total", metric.WithDescription("Total reconcile attempts per kind and result.")))

	CheckpointLastWriteTimestamp = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_checkpoint_last_write_timestamp_seconds", metric.WithDescription("Unix timestamp of the last successful checkpoint write per kind."), metric.WithUnit("s")))

	CheckpointWriteFailuresTotal = observability.MustInstrument(otel.Meter("gitstore/health").Float64Counter("gitstore_controller_checkpoint_write_failures_total", metric.WithDescription("Total failed checkpoint write attempts per kind.")))

	CheckpointReplayBacklog = observability.MustInstrument(otel.Meter("gitstore/health").Float64Gauge("gitstore_controller_checkpoint_replay_backlog", metric.WithDescription("Number of watch events enqueued as work items but not yet dispatched, per kind.")))

	CategoryDeletionProductPagesTotal = observability.MustInstrument(otel.Meter("gitstore/health").Float64Counter("gitstore_category_deletion_product_pages_total", metric.WithDescription("Bounded Product dependent pages processed by CategoryTaxonomy deletion.")))

	CategoryDeletionConflictsTotal = observability.MustInstrument(otel.Meter("gitstore/health").Float64Counter("gitstore_category_deletion_completion_conflicts_total", metric.WithDescription("Optimistic concurrency conflicts while completing CategoryTaxonomy deletion.")))

	CategoryDeletionRetriesTotal = observability.MustInstrument(otel.Meter("gitstore/health").Float64Counter("gitstore_category_deletion_retries_total", metric.WithDescription("Transient CategoryTaxonomy deletion reconciliation retries.")))
)
