// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type tracedReconciler func(context.Context, WorkItemKey) types.ReconcileResult

func (r tracedReconciler) Reconcile(ctx context.Context, key WorkItemKey) types.ReconcileResult {
	return r(ctx, key)
}

func TestReconcileCompletionLogLevelFollowsSampling(t *testing.T) {
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	for _, sampled := range []bool{false, true} {
		for _, level := range []zap.AtomicLevel{zap.NewAtomicLevelAt(zap.InfoLevel), zap.NewAtomicLevelAt(zap.DebugLevel)} {
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
			otel.SetTracerProvider(provider)
			flags := trace.TraceFlags(0)
			if sampled {
				flags = trace.FlagsSampled
			}
			remote := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: flags, Remote: true,
			})
			ctx := trace.ContextWithRemoteSpanContext(t.Context(), remote)
			core, logs := observer.New(level)
			result := safeReconcile(tracedReconciler(func(context.Context, WorkItemKey) types.ReconcileResult {
				return types.ResultOK()
			}), WorkItemKey{Kind: "Namespace"}, zap.New(core))(ctx)
			require.IsType(t, types.Success{}, result)
			if sampled || level.Enabled(zap.DebugLevel) {
				require.Equal(t, 1, logs.Len())
				expected := zap.DebugLevel
				if sampled {
					expected = zap.InfoLevel
				}
				require.Equal(t, expected, logs.All()[0].Level)
			} else {
				require.Zero(t, logs.Len(), "unsampled successes must not add Info logs")
			}
			require.NoError(t, provider.Shutdown(t.Context()))
		}
	}
	otel.SetTracerProvider(noop.NewTracerProvider())
	core, logs := observer.New(zap.InfoLevel)
	safeReconcile(tracedReconciler(func(context.Context, WorkItemKey) types.ReconcileResult {
		return types.ResultOK()
	}), WorkItemKey{Kind: "Namespace"}, zap.New(core))(t.Context())
	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	safeReconcile(tracedReconciler(func(context.Context, WorkItemKey) types.ReconcileResult {
		return types.ResultOK()
	}), WorkItemKey{Kind: "Namespace"}, zap.New(core))(trace.ContextWithRemoteSpanContext(t.Context(), remote))
	require.Zero(t, logs.Len(), "disabled telemetry must not add Info logs")
}

func TestReconcileSpansArePerAttemptBoundedAndCancellationAware(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	for _, tc := range []struct {
		name    string
		result  types.ReconcileResult
		panic   bool
		cancel  bool
		outcome string
	}{
		{name: "success", result: types.ResultOK(), outcome: "success"},
		{name: "requeue", result: types.ResultAfter(time.Second), outcome: "requeue"},
		{name: "transient", result: types.ResultTransient(errors.New("private")), outcome: "transient_failure"},
		{name: "terminal", result: types.ResultTerminal(errors.New("private")), outcome: "terminal_failure"},
		{name: "panic", panic: true, outcome: "transient_failure"},
		{name: "cancelled", cancel: true, result: types.ResultTransient(context.Canceled), outcome: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(t.Context())
			otel.SetTracerProvider(provider)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reconciler := tracedReconciler(func(ctx context.Context, _ WorkItemKey) types.ReconcileResult {
				require.True(t, trace.SpanFromContext(ctx).IsRecording())
				if tc.cancel {
					cancel()
					<-ctx.Done()
				}
				if tc.panic {
					panic("private panic")
				}
				return tc.result
			})
			result := safeReconcile(reconciler, WorkItemKey{Kind: "private-kind", Name: "private-name", Namespace: "private-ns"})(ctx)
			require.NotNil(t, result)
			spans := recorder.Ended()
			require.Len(t, spans, 1)
			require.Len(t, spans[0].Attributes(), 2)
			require.Equal(t, "other", spans[0].Attributes()[0].Value.AsString())
			require.Equal(t, tc.outcome, spans[0].Attributes()[1].Value.AsString())
			for _, attr := range spans[0].Attributes() {
				require.NotContains(t, attr.Value.AsString(), "private")
			}
			if tc.outcome != "success" && tc.outcome != "requeue" {
				require.Equal(t, codes.Error, spans[0].Status().Code)
			}
			require.Empty(t, spans[0].Events(), "raw errors and panic payloads must not become trace events")
		})
	}
	otel.SetTracerProvider(noop.NewTracerProvider())
	called := false
	result := safeReconcile(tracedReconciler(func(ctx context.Context, _ WorkItemKey) types.ReconcileResult {
		called = true
		require.False(t, trace.SpanFromContext(ctx).IsRecording())
		return types.ResultOK()
	}), WorkItemKey{Kind: "Namespace"})(t.Context())
	require.True(t, called)
	require.IsType(t, types.Success{}, result)
}
