// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

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
