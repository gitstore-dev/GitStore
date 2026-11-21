// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"net/http"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type endpointMetrics struct {
	requests metric.Int64Counter
	inflight metric.Int64UpDownCounter
	duration metric.Float64Histogram
}

func observeEndpointResources(endpoint string, limits config.GraphQLLimits, ip, principal *security.RateLimit, connections *wsregistry.Registry) (func(), error) {
	meter := otel.Meter("gitstore/api/endpoints")
	keys, err := meter.Int64ObservableGauge("gitstore.api.endpoint.limiter_keys")
	if err != nil {
		return nil, err
	}
	sockets, err := meter.Int64ObservableGauge("gitstore.api.endpoint.connections")
	if err != nil {
		return nil, err
	}
	ceilings, err := meter.Int64ObservableGauge("gitstore.api.endpoint.limits")
	if err != nil {
		return nil, err
	}
	registration, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		for reason, value := range map[string]int{"http": limits.InFlight, "websocket": limits.WSConnections, "limiter_keys": limits.LimiterKeys} {
			observer.ObserveInt64(ceilings, int64(value), metric.WithAttributes(attribute.String("endpoint", endpoint), attribute.String("reason", reason)))
		}
		for _, table := range []struct {
			reason  string
			limiter *security.RateLimit
		}{{"ip", ip}, {"principal", principal}} {
			if table.limiter != nil {
				observer.ObserveInt64(keys, int64(table.limiter.KeyCount()), metric.WithAttributes(attribute.String("endpoint", endpoint), attribute.String("reason", table.reason)))
			}
		}
		if connections != nil {
			observer.ObserveInt64(sockets, int64(connections.Count()), metric.WithAttributes(attribute.String("endpoint", endpoint)))
		}
		return nil
	}, keys, sockets, ceilings)
	if err != nil {
		return nil, err
	}
	return func() { _ = registration.Unregister() }, nil
}

func newEndpointMetrics() endpointMetrics {
	meter := otel.Meter("gitstore/api/endpoints")
	requests, _ := meter.Int64Counter("gitstore.api.endpoint.requests_total", metric.WithDescription("Endpoint admission and terminal outcomes"))
	inflight, _ := meter.Int64UpDownCounter("gitstore.api.endpoint.in_flight", metric.WithDescription("Admitted active endpoint requests/connections"))
	duration, _ := meter.Float64Histogram("gitstore.api.endpoint.duration_seconds", metric.WithUnit("s"))
	return endpointMetrics{requests, inflight, duration}
}

func (m endpointMetrics) outcome(ctx context.Context, endpoint, outcome, reason string) {
	m.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("endpoint", endpoint), attribute.String("outcome", outcome), attribute.String("reason", reason)))
}

func (m endpointMetrics) start(ctx context.Context, endpoint, transport string) func(context.Context, int) {
	options := metric.WithAttributes(attribute.String("endpoint", endpoint), attribute.String("reason", transport))
	start := time.Now()
	m.inflight.Add(ctx, 1, options)
	m.outcome(ctx, endpoint, "admitted", "accepted")
	return func(ctx context.Context, status int) {
		m.inflight.Add(ctx, -1, options)
		m.duration.Record(ctx, time.Since(start).Seconds(), options)
		outcome, reason := "completed", "response"
		if ctx.Err() != nil {
			outcome, reason = "canceled", "context"
		} else if status == 503 {
			reason = "unavailable"
		} else if status >= 400 {
			outcome, reason = "rejected", "request"
		}
		m.outcome(ctx, endpoint, outcome, reason)
	}
}

func InitTelemetry(ctx context.Context, cfg observability.OTLPConfig, logger *zap.Logger) (*zap.Logger, func(context.Context) error, error) {
	return observability.InitTelemetry(ctx, cfg, observability.APIService, version, logger)
}

func catalogTelemetry(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	header := http.Header{}
	for _, key := range []string{"traceparent", "tracestate"} {
		if values := md.Get(key); len(values) == 1 && len(values[0]) <= 256 {
			header.Set(key, values[0])
		}
	}
	ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(header))
	ctx, span := otel.Tracer("gitstore/catalog").Start(ctx, "catalog.rpc", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()
	response, err := handler(ctx, request)
	if err != nil {
		span.SetStatus(codes.Error, "RPC failed")
	}
	return response, err
}
