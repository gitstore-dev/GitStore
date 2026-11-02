// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"net/http"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

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
