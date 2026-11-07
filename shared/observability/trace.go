// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
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

// Observe records through the SDK so sampled span context can become an
// exemplar without adding trace identities to ordinary series attributes.
func Observe(ctx context.Context, observer metric.Float64Histogram, value float64, options ...metric.RecordOption) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	}
	observer.Record(ctx, value, options...)
}

// Inject propagates only W3C trace context, never baggage or credentials.
func Inject(ctx context.Context, header http.Header) {
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(header))
}

// HTTPHandler links finite API POST operations to their caller. It leaves the
// response writer untouched and never holds a server span over a WebSocket
// subscription or exports request paths, headers or bodies.
func HTTPHandler(next http.Handler, loggers ...*zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("gitstore/http-server").Start(ctx, "http.request",
			trace.WithSpanKind(trace.SpanKindServer))
		started := time.Now()
		completed := false
		defer func() {
			result := "completed"
			if !completed {
				result = "panic"
				span.SetStatus(codes.Error, "panic")
			} else if ctx.Err() != nil {
				result = "cancelled"
				span.SetStatus(codes.Error, "cancelled")
			}
			if span.IsRecording() && len(loggers) > 0 && loggers[0] != nil {
				Logger(ctx, loggers[0]).Info("HTTP request completed",
					zap.String("result", result), zap.Duration("duration", time.Since(started)))
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
