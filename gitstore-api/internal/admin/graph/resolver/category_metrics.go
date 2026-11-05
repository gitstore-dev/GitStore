// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	categoryMutationTotal    = observability.MustInstrument(otel.Meter("gitstore/resolver").Float64Counter("gitstore_category_mutation_total", metric.WithDescription("Category mutations by operation and outcome (success, error, or the error code).")))
	categoryMutationDuration = observability.MustInstrument(otel.Meter("gitstore/resolver").Float64Histogram("gitstore_category_mutation_duration_seconds", metric.WithDescription("Category mutation latency, dominated by one Git commit and one admission."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.01, .025, .05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5, 10)))
)
