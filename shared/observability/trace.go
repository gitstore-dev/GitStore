// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger attaches trace context without changing the local logger's fields.
func Logger(ctx context.Context, logger *zap.Logger) *zap.Logger {
	// Carry only span identity. Never serialize or retain application context
	// values (which may include credentials or entire resource objects).
	traceCtx := trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	return logger.With(zap.Field{Key: "context", Type: zapcore.SkipType, Interface: traceCtx})
}

// Sampled reports whether ctx identifies a valid sampled span.
func Sampled(ctx context.Context) bool {
	sc := trace.SpanContextFromContext(ctx)
	return sc.IsValid() && sc.IsSampled()
}

// Observe records a native metric with bounded exemplar metadata when sampled.
// Span identity is never added to the metric's ordinary series labels. Observers
// without exemplar support retain their original observation behavior.
func Observe(ctx context.Context, observer prometheus.Observer, value float64) {
	if Sampled(ctx) {
		if exemplars, ok := observer.(prometheus.ExemplarObserver); ok {
			sc := trace.SpanContextFromContext(ctx)
			exemplars.ObserveWithExemplar(value, prometheus.Labels{
				"trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String(),
			})
			return
		}
	}
	observer.Observe(value)
}

// Inject propagates only W3C trace context, never baggage or credentials.
func Inject(ctx context.Context, header http.Header) {
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(header))
}

// HTTPHandler links finite API POST operations to their caller. It leaves the
// response writer untouched and never holds a server span over a WebSocket
// subscription or exports request paths, headers or bodies.
func HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("gitstore/http-server").Start(ctx, "http.request",
			trace.WithSpanKind(trace.SpanKindServer))
		completed := false
		defer func() {
			if !completed {
				span.SetStatus(codes.Error, "panic")
			} else if ctx.Err() != nil {
				span.SetStatus(codes.Error, "cancelled")
			}
			span.End()
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
		completed = true
	})
}

type transport struct{ base http.RoundTripper }

// Transport instruments request/response headers, not streaming response bodies.
// Endpoints, headers, GraphQL documents and resource identifiers are excluded.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return transport{base}
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, span := otel.Tracer("gitstore/http-client").Start(req.Context(), "api.request",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	copy := req.Clone(ctx)
	Inject(ctx, copy.Header)
	response, err := t.base.RoundTrip(copy)
	if err != nil {
		span.SetStatus(codes.Error, "transport failure")
		return response, err
	}

	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.StatusCode >= 400 {
		span.SetStatus(codes.Error, "HTTP failure")
	}
	return response, nil
}

func (t transport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
