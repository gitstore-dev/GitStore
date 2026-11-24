// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type RBACBudgetUsage struct{ Reserved, PeakReserved, Limit int64 }

type RBACResourceUsage struct {
	Snapshot, Rebuild                           RBACBudgetUsage
	RebuildBufferLimit                          int64
	DirtyQueue, DirtyQueuePeak, DirtyQueueLimit int64
}

type RBACResourceMetrics struct{ registration metric.Registration }

func NewRBACResourceMetrics(read func() RBACResourceUsage) (*RBACResourceMetrics, error) {
	if read == nil {
		return nil, errors.New("RBAC resource metrics require an owned runtime observation")
	}
	meter := otel.Meter("gitstore/auth")
	reserved := observability.MustInstrument(meter.Int64ObservableGauge("gitstore_authz_rbac_reserved_bytes", metric.WithUnit("By"),
		metric.WithDescription("Current conservative RBAC memory reservations, not measured heap allocation.")))
	peak := observability.MustInstrument(meter.Int64ObservableGauge("gitstore_authz_rbac_peak_reserved_bytes", metric.WithUnit("By"),
		metric.WithDescription("High-water RBAC memory reservations, including replaced publications held by readers.")))
	limit := observability.MustInstrument(meter.Int64ObservableGauge("gitstore_authz_rbac_reservation_limit_bytes", metric.WithUnit("By")))
	buffer := observability.MustInstrument(meter.Int64ObservableGauge("gitstore_authz_rbac_rebuild_buffer_limit_bytes", metric.WithUnit("By"),
		metric.WithDescription("Configured per-buffer limit, distinct from aggregate rebuild reservations.")))
	queue := observability.MustInstrument(meter.Int64ObservableGauge("gitstore_authz_rbac_dirty_queue_entries"))
	queuePeak := observability.MustInstrument(meter.Int64ObservableGauge("gitstore_authz_rbac_dirty_queue_peak_entries"))
	queueLimit := observability.MustInstrument(meter.Int64ObservableGauge("gitstore_authz_rbac_dirty_queue_limit_entries"))
	registration, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		usage := read()
		for _, pool := range []struct {
			name  string
			usage RBACBudgetUsage
		}{{"snapshot", usage.Snapshot}, {"rebuild", usage.Rebuild}} {
			labels := metric.WithAttributes(attribute.String("pool", pool.name))
			observer.ObserveInt64(reserved, pool.usage.Reserved, labels)
			observer.ObserveInt64(peak, pool.usage.PeakReserved, labels)
			observer.ObserveInt64(limit, pool.usage.Limit, labels)
		}
		observer.ObserveInt64(buffer, usage.RebuildBufferLimit)
		observer.ObserveInt64(queue, usage.DirtyQueue)
		observer.ObserveInt64(queuePeak, usage.DirtyQueuePeak)
		observer.ObserveInt64(queueLimit, usage.DirtyQueueLimit)
		return nil
	}, reserved, peak, limit, buffer, queue, queuePeak, queueLimit)
	if err != nil {
		return nil, err
	}
	return &RBACResourceMetrics{registration: registration}, nil
}

func (m *RBACResourceMetrics) Close() error { return m.registration.Unregister() }

var (
	rbacRebuildDuration  = observability.MustInstrument(otel.Meter("gitstore/auth").Float64Histogram("gitstore_authz_rbac_rebuild_duration_seconds", metric.WithUnit("s")))
	rbacRecoveryDuration = observability.MustInstrument(otel.Meter("gitstore/auth").Float64Histogram("gitstore_authz_rbac_recovery_duration_seconds", metric.WithUnit("s")))
	rbacSourceAge        = observability.MustInstrument(otel.Meter("gitstore/auth").Float64Histogram("gitstore_authz_rbac_source_age_seconds", metric.WithUnit("s")))
	rbacUnavailable      = observability.MustInstrument(otel.Meter("gitstore/auth").Int64Counter("gitstore_authz_rbac_unavailable_total"))
	rbacSourceFailures   = observability.MustInstrument(otel.Meter("gitstore/auth").Int64Counter("gitstore_authz_rbac_source_failures_total"))
)

func rbacTierClass(cluster bool) string {
	if cluster {
		return "cluster"
	}
	return "namespace"
}

func ObserveRBACRebuild(ctx context.Context, cluster, success bool, duration time.Duration) {
	outcome := "error"
	if success {
		outcome = "success"
	}
	rbacRebuildDuration.Record(ctx, max(0, duration.Seconds()), metric.WithAttributes(
		attribute.String("outcome", outcome), attribute.String("tier_class", rbacTierClass(cluster))))
}

func ObserveRBACRecovery(ctx context.Context, cluster bool, duration time.Duration) {
	rbacRecoveryDuration.Record(ctx, max(0, duration.Seconds()), metric.WithAttributes(attribute.String("tier_class", rbacTierClass(cluster))))
}

func ObserveRBACSourceAge(ctx context.Context, age time.Duration) {
	rbacSourceAge.Record(ctx, max(0, age.Seconds()))
}

func ObserveRBACUnavailable(ctx context.Context, reason string, cluster bool) {
	switch reason {
	case "tier", "source", "queue", "evaluation", "snapshot", "budget":
	default:
		reason = "unknown"
	}

	rbacUnavailable.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason),
		attribute.String("tier_class", rbacTierClass(cluster))))
}

func ObserveRBACSourceFailure(ctx context.Context, stage string) {
	switch stage {
	case "journal-bounds", "frontier-read", "frontier-stale", "journal-epoch", "journal-high-water",
		"journal-retention", "journal-read", "event-sequence", "event-stale", "event-payload",
		"cluster-origin", "namespace-origin", "serving-progress",
		"rebuild-scratch", "rebuild-snapshot", "rebuild-journal", "rebuild-prepare",
		"rebuild-origin", "rebuild-origin-busy", "rebuild-continuity", "rebuild-install":
	default:
		stage = "unknown"
	}
	rbacSourceFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", stage)))
}
