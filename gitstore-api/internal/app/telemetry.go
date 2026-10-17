// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/vektah/gqlparser/v2/ast"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var graphQLRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "gitstore_api_graphql_http_request_duration_seconds",
	Help: "GraphQL POST request duration, excluding WebSocket subscription lifetimes.",
}, []string{"result"})

func InitTelemetry(ctx context.Context, cfg config.OTLPConfig, logger *zap.Logger) (*zap.Logger, func(context.Context) error, error) {
	return observability.InitTelemetry(ctx, cfg, observability.APIService, version, logger)
}

func graphQLExecutionPhase(ctx context.Context, next graphql.ResponseHandler) (response *graphql.Response) {
	if !trace.SpanFromContext(ctx).IsRecording() || !graphql.HasOperationContext(ctx) {
		return next(ctx)
	}
	op := graphql.GetOperationContext(ctx).Operation
	if op == nil || op.Operation == ast.Subscription {
		return next(ctx)
	}
	ctx, span := otel.Tracer("gitstore/graphql").Start(ctx, "graphql.execute")
	completed := false
	defer func() {
		switch {
		case !completed:
			span.SetStatus(codes.Error, "panic")
		case ctx.Err() != nil:
			span.SetStatus(codes.Error, "cancelled")
		case response == nil:
			span.SetStatus(codes.Error, "empty response")
		case len(response.Errors) != 0:
			span.SetStatus(codes.Error, "GraphQL errors")
		}
		span.End()
	}()
	response = next(ctx)
	completed = true
	return response
}

func graphQLHTTPCompletion(next http.Handler, logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		completed := false
		defer func() {
			result := "completed"
			if !completed {
				result = "panic"
			} else if r.Context().Err() != nil {
				result = "cancelled"
			}
			elapsed := time.Since(started)
			observability.Observe(r.Context(), graphQLRequestDuration.WithLabelValues(result), elapsed.Seconds())
			if logger != nil && observability.Sampled(r.Context()) && trace.SpanFromContext(r.Context()).IsRecording() {
				observability.Logger(r.Context(), logger).Info("graphql HTTP request completed",
					zap.String("result", result), zap.Duration("duration", elapsed))
			}
		}()
		next.ServeHTTP(w, r)
		completed = true
	})
}
