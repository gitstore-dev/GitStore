// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ObserveAdmissionRejection records the single admission rejection metric used
// by both asynchronous Git admission and synchronous GraphQL authoring.
func ObserveAdmissionRejection(kind string, phase admission.Phase) {
	admissionRejectionsTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("kind", kind), attribute.String("phase", string(phase))))
}

var (
	categoryDeletionDependentLookupDuration = observability.MustInstrument(otel.Meter("gitstore/cataloggrpc").Float64Histogram("gitstore_category_deletion_dependent_lookup_duration_seconds", metric.WithDescription("Latency of the limit-one blocking child lookup."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10)))
	categoryDeletionBlockedTotal            = observability.MustInstrument(otel.Meter("gitstore/cataloggrpc").Float64Counter("gitstore_category_deletion_blocked_total", metric.WithDescription("Category deletion requests blocked by a child owner reference.")))
	productDeletionDependentLookupDuration  = observability.MustInstrument(otel.Meter("gitstore/cataloggrpc").Float64Histogram("gitstore_product_deletion_dependent_lookup_duration_seconds", metric.WithDescription("Latency of the bounded blocking ProductVariant lookup."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10)))
	productDeletionBlockedTotal             = observability.MustInstrument(otel.Meter("gitstore/cataloggrpc").Float64Counter("gitstore_product_deletion_blocked_total", metric.WithDescription("Product deletion requests blocked by a ProductVariant owner reference.")))
	admissionRejectionsTotal                = observability.MustInstrument(otel.Meter("gitstore/cataloggrpc").Float64Counter("gitstore_admission_rejections_total", metric.WithDescription("Manifests rejected by admission, by kind and phase.")))
)
