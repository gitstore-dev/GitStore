// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestEndpointTelemetryDistinguishesIngressRejectionCompletionAndCancellation(t *testing.T) {
	old := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(old); _ = provider.Shutdown(context.Background()) })
	public, err := NewStorefrontHandler(config.DefaultEndpoint("storefront"), config.RateLimitConfig{PerSecond: 0.000001, Burst: 1}, nil)
	require.NoError(t, err)
	for _, test := range []struct {
		origin string
		status int
	}{{"http://denied.example", 403}, {"", 503}, {"", 429}} {
		request := httptest.NewRequest(http.MethodPost, "/storefront/graphql", nil)
		if test.origin != "" {
			request.Header.Set("Origin", test.origin)
		}
		response := httptest.NewRecorder()
		public.ServeHTTP(response, request)
		require.Equal(t, test.status, response.Code)
		require.Equal(t, "no-store", response.Result().Header.Get("Cache-Control"))
	}
	gate := newEndpointAdmission("admin", config.DefaultEndpoint("admin").Limits)
	admin := gin.New()
	admin.Use(gin.Recovery())
	admin.POST("/admin/graphql", gate.middleware(), func(c *gin.Context) {
		if c.GetHeader("X-Test-Panic") == "yes" {
			panic("fixture")
		}
		c.Status(200)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	admin.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/admin/graphql", nil).WithContext(ctx))
	panicRequest := httptest.NewRequest(http.MethodPost, "/admin/graphql", nil)
	panicRequest.Header.Set("X-Test-Panic", "yes")
	admin.ServeHTTP(httptest.NewRecorder(), panicRequest)
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &data))
	observed := map[string]int64{}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "gitstore.api.endpoint.requests_total" {
				continue
			}
			for _, point := range metric.Data.(metricdata.Sum[int64]).DataPoints {
				endpoint, _ := point.Attributes.Value("endpoint")
				outcome, _ := point.Attributes.Value("outcome")
				reason, _ := point.Attributes.Value("reason")
				observed[endpoint.AsString()+"/"+outcome.AsString()+"/"+reason.AsString()] += point.Value
			}
		}
	}
	require.Equal(t, int64(1), observed["storefront/rejected/origin"])
	require.Equal(t, int64(1), observed["storefront/rejected/ip"])
	require.Equal(t, int64(1), observed["storefront/completed/unavailable"])
	require.Zero(t, observed["storefront/canceled/context"], "request cleanup must not turn a completed response into cancellation")
	require.Equal(t, int64(1), observed["admin/canceled/context"])
	require.Equal(t, int64(1), observed["admin/rejected/request"])
}

func TestEndpointTelemetryUsesSDKWithBoundedAttributes(t *testing.T) {
	old := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()); otel.SetMeterProvider(old) })
	gate := newEndpointAdmission("admin", config.DefaultEndpoint("admin").Limits)
	r := gin.New()
	r.POST("/", gate.middleware(), func(c *gin.Context) { c.Status(200) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &data))
	seen := map[string]bool{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			seen[m.Name] = true
			if values, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, point := range values.DataPoints {
					for _, attribute := range point.Attributes.ToSlice() {
						require.Contains(t, []string{"endpoint", "outcome", "reason"}, string(attribute.Key))
					}
					if m.Name == "gitstore.api.endpoint.in_flight" {
						require.Zero(t, point.Value)
					}
				}
			}
		}
	}
	for _, name := range []string{"gitstore.api.endpoint.requests_total", "gitstore.api.endpoint.in_flight", "gitstore.api.endpoint.duration_seconds"} {
		require.True(t, seen[name], name)
	}
}

func TestResourceObservationsHaveOwnedCallbackAndNoIdentityLabels(t *testing.T) {
	old := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()); otel.SetMeterProvider(old) })
	limiter := security.NewRateLimit(1, 1)
	require.True(t, limiter.Allow("private-principal-or-ip", 1))
	connections := wsregistry.New()
	ctx, cancel := context.WithCancel(context.Background())
	connections.Register(ctx, "private-account", cancel)
	defer cancel()
	stop, err := observeEndpointResources("admin", config.DefaultEndpoint("admin").Limits, limiter, nil, connections)
	require.NoError(t, err)
	defer stop()
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &data))
	observations := 0
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok {
				for _, point := range gauge.DataPoints {
					observations++
					if m.Name != "gitstore.api.endpoint.limits" {
						require.EqualValues(t, 1, point.Value)
					} else {
						require.Positive(t, point.Value)
					}
					for _, attribute := range point.Attributes.ToSlice() {
						require.Contains(t, []string{"endpoint", "reason"}, string(attribute.Key))
					}
				}
			}
		}
	}
	require.Equal(t, 5, observations)
	stop()
	data = metricdata.ResourceMetrics{}
	require.NoError(t, reader.Collect(context.Background(), &data))
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok {
				require.Empty(t, gauge.DataPoints)
			}
		}
	}
}

func TestCatalogTelemetryPreservesRPCInputsAndEndsSanitizedChildSpan(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(
		"traceparent", "00-01000000000000000000000000000000-0200000000000000-01",
		"x-gitstore-rpc-signature", "private-signature", "baggage", "private-payload",
	))
	request, result := &struct{}{}, &struct{}{}
	cause := errors.New("private upstream error")
	reply, err := catalogTelemetry(ctx, request, &grpc.UnaryServerInfo{}, func(ctx context.Context, got any) (any, error) {
		require.Same(t, request, got)
		md, _ := metadata.FromIncomingContext(ctx)
		require.Equal(t, []string{"private-signature"}, md.Get("x-gitstore-rpc-signature"))
		require.True(t, trace.SpanFromContext(ctx).SpanContext().IsValid())
		return result, cause
	})
	require.Same(t, result, reply)
	require.ErrorIs(t, err, cause)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "0200000000000000", spans[0].Parent().SpanID().String())
	require.Equal(t, "RPC failed", spans[0].Status().Description)
	require.Empty(t, spans[0].Attributes())
}
