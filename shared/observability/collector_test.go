// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

// Run only against an explicitly owned, loopback collector. Backend queries
// must independently verify these markers; a successful export is not delivery.
func TestCollectorSmoke(t *testing.T) {
	endpoint := os.Getenv("GITSTORE_TEST_OTLP_SMOKE_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires an owned collector and backend delivery verification")
	}
	uri, err := url.Parse(endpoint)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "::1"}, uri.Hostname())
	for _, service := range []string{APIService, ControllerService} {
		preserveGlobals(t)
		logger, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{URI: endpoint, TraceSampleRatio: 1}, service, "smoke", discardLogger())
		require.NoError(t, err)
		ctx, span := otel.Tracer("gitstore/smoke").Start(t.Context(), "collector.smoke")
		sdkCounter, err := otel.Meter("gitstore/smoke").Int64Counter("gitstore_observability_sdk_smoke_total")
		require.NoError(t, err)
		sdkCounter.Add(ctx, 1)
		instance, _ := span.(sdktrace.ReadOnlySpan).Resource().Set().Value(attribute.Key("service.instance.id"))
		counter := MustInstrument(otel.Meter("gitstore/smoke").Int64Counter("gitstore_observability_smoke_total"))
		counter.Add(ctx, 1)
		Logger(ctx, logger).Info("private payload must not be forwarded", zap.String("kind", "Smoke"))
		fmt.Printf("OTEL_SMOKE service=%s trace_id=%s instance_id=%s\n", service, span.SpanContext().TraceID(), instance.AsString())
		span.End()
		require.NoError(t, shutdown(t.Context()))
	}
}
