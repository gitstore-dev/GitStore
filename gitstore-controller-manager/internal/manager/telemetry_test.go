// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package manager

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type telemetryReconciler struct{ contexts []trace.SpanContext }

func (r *telemetryReconciler) Reconcile(ctx context.Context, _ types.WorkItemKey) types.ReconcileResult {
	r.contexts = append(r.contexts, trace.SpanFromContext(ctx).SpanContext())
	if len(r.contexts) == 1 {
		panic("private-resource-content")
	}
	return types.ResultOK()
}

func TestReconciliationTelemetryEndsEachAttemptAndPreservesPanicRecovery(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	reconciler := &telemetryReconciler{}
	key := WorkItemKey{Kind: "Product", Namespace: "private-namespace", Name: "private-resource"}
	result := reconcileAttempt(t.Context(), reconciler, key, zap.NewNop())
	require.IsType(t, types.TransientFailure{}, result)
	require.Len(t, recorder.Ended(), 1)
	require.IsType(t, types.Success{}, reconcileAttempt(t.Context(), reconciler, key, zap.NewNop()))
	require.Len(t, recorder.Ended(), 2)
	require.NotEqual(t, reconciler.contexts[0].SpanID(), reconciler.contexts[1].SpanID())
	for _, span := range recorder.Ended() {
		require.Empty(t, span.Events())
		require.Len(t, span.Attributes(), 1)
		require.Equal(t, "Product", span.Attributes()[0].Value.AsString())
	}
}
