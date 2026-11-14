// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	rbacRebuildDuration  = observability.MustInstrument(otel.Meter("gitstore/auth").Float64Histogram("gitstore_authz_rbac_rebuild_duration_seconds", metric.WithUnit("s")))
	rbacRecoveryDuration = observability.MustInstrument(otel.Meter("gitstore/auth").Float64Histogram("gitstore_authz_rbac_recovery_duration_seconds", metric.WithUnit("s")))
	rbacSourceAge        = observability.MustInstrument(otel.Meter("gitstore/auth").Float64Histogram("gitstore_authz_rbac_source_age_seconds", metric.WithUnit("s")))
	rbacUnavailable      = observability.MustInstrument(otel.Meter("gitstore/auth").Int64Counter("gitstore_authz_rbac_unavailable_total"))
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
