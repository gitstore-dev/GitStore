// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type cqlQueryObserverFunc func(context.Context, gocql.ObservedQuery)

func (f cqlQueryObserverFunc) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	f(ctx, query)
}

func TestCQLAttemptTracing(t *testing.T) {
	for _, sampled := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			t.Run(map[bool]string{false: "unsampled", true: "sampled"}[sampled]+"/"+
				map[bool]string{false: "query", true: "batch"}[batch], func(t *testing.T) {
				recorder := tracetest.NewSpanRecorder()
				sampler := sdktrace.NeverSample()
				if sampled {
					sampler = sdktrace.AlwaysSample()
				}
				provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler), sdktrace.WithSpanProcessor(recorder))
				t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
				tracer := provider.Tracer("test")
				ctx, parent := tracer.Start(context.Background(), "parent")
				defer parent.End()
				start := time.Now()
				end := start.Add(3 * time.Millisecond)
				query := gocql.ObservedQuery{
					Start: start, End: end, Attempt: 2, Rows: 7,
					Statement: "SELECT 'private-literal' FROM private_table WHERE token=?",
					Values:    []any{"private-bound-value"},
					Err:       errors.New("private-error-detail"),
				}
				var observed atomic.Int32
				observer := cqlTraceObserver{
					tracer: tracer,
					next: cqlQueryObserverFunc(func(received context.Context, got gocql.ObservedQuery) {
						assert.Equal(t, ctx, received)
						assert.Equal(t, query, got)
						observed.Add(1)
					}),
				}
				if batch {
					observer.ObserveBatch(ctx, gocql.ObservedBatch{
						Start: start, End: end, Attempt: 2, Err: query.Err,
						Statements: []string{query.Statement, query.Statement},
						Values:     [][]any{query.Values, query.Values},
					})
					assert.Zero(t, observed.Load())
				} else {
					observer.ObserveQuery(ctx, query)
					assert.EqualValues(t, 1, observed.Load())
				}
				spans := recorder.Ended()
				if !sampled {
					require.Empty(t, spans)
					return
				}
				require.Len(t, spans, 1)
				span := spans[0]
				assert.Equal(t, parent.SpanContext().TraceID(), span.SpanContext().TraceID())
				assert.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID())
				assert.Equal(t, trace.SpanKindClient, span.SpanKind())
				assert.Equal(t, start, span.StartTime())
				assert.Equal(t, end, span.EndTime())
				assert.Equal(t, codes.Error, span.Status().Code)
				assert.NotContains(t, span.Status().Description, "private-")
				attrs := make(map[attribute.Key]attribute.Value)
				for _, item := range span.Attributes() {
					attrs[item.Key] = item.Value
					assert.NotContains(t, item.Value.Emit(), "private-")
				}
				assert.Equal(t, int64(2), attrs["db.operation.retry_count"].AsInt64())
				assert.Equal(t, int64(3*time.Millisecond), attrs["gitstore.db.attempt.duration_ns"].AsInt64())
				assert.Equal(t, "scylla", attrs["db.system.name"].AsString())
				if batch {
					assert.Equal(t, "scylla.batch", span.Name())
					assert.Equal(t, "BATCH", attrs["db.operation.name"].AsString())
					assert.Equal(t, int64(2), attrs["db.operation.batch.size"].AsInt64())
				} else {
					assert.Equal(t, "scylla.query", span.Name())
					assert.Equal(t, "SELECT", attrs["db.operation.name"].AsString())
					assert.Equal(t, int64(7), attrs["db.response.returned_rows"].AsInt64())
				}
				assert.Empty(t, span.Events())
			})
		}
	}
}

func TestCQLAttemptTracingWithoutParentStillCallsObserver(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	var observed atomic.Int32
	observer := cqlTraceObserver{
		tracer: provider.Tracer("test"),
		next: cqlQueryObserverFunc(func(context.Context, gocql.ObservedQuery) {
			observed.Add(1)
		}),
	}
	observer.ObserveQuery(context.Background(), gocql.ObservedQuery{})
	observer.ObserveBatch(context.Background(), gocql.ObservedBatch{})
	assert.EqualValues(t, 1, observed.Load())
	assert.Empty(t, recorder.Ended())
}

func TestCQLAttemptTracingDoesNotExtendCompletedOrRemoteParents(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "completed")
	parent.End()
	remote := trace.ContextWithRemoteSpanContext(context.Background(), parent.SpanContext())
	observer := cqlTraceObserver{tracer: tracer}
	for _, ctx := range []context.Context{ctx, remote} {
		observer.ObserveQuery(ctx, gocql.ObservedQuery{})
		observer.ObserveBatch(ctx, gocql.ObservedBatch{})
	}
	require.Len(t, recorder.Ended(), 1)
}

func TestCQLAttemptTracingConcurrentSuccessfulAttempts(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "parent")
	defer parent.End()
	var observed atomic.Int32
	observer := cqlTraceObserver{
		tracer: tracer,
		next: cqlQueryObserverFunc(func(context.Context, gocql.ObservedQuery) {
			observed.Add(1)
		}),
	}
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			start := time.Now()
			observer.ObserveQuery(ctx, gocql.ObservedQuery{
				Start: start, End: start.Add(time.Millisecond), Statement: "UPDATE private_table SET token=?",
			})
			observer.ObserveBatch(ctx, gocql.ObservedBatch{Start: start, End: start.Add(time.Millisecond)})
		})
	}
	workers.Wait()
	assert.EqualValues(t, 32, observed.Load())
	require.Len(t, recorder.Ended(), 64)
	for _, span := range recorder.Ended() {
		assert.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID())
		assert.Equal(t, codes.Unset, span.Status().Code)
	}
}

func TestCQLExchangeStreamTimingsHaveExactAttemptParent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "parent")
	defer parent.End()
	queryCtx, finish := startCQLExchange(ctx, tracer, attribute.String("db.collection.name", "files_by_uid"))
	observer := cqlTraceObserver{tracer: tracer}
	stream := observer.StreamContext(queryCtx)
	require.NotNil(t, stream)
	start := time.Now()
	stream.StreamStarted(gocql.ObservedStream{})
	stream.StreamFinished(gocql.ObservedStream{})
	observer.ObserveQuery(queryCtx, gocql.ObservedQuery{
		Start: start, End: time.Now(), Statement: "UPDATE private_table SET token=?",
		Values: []any{"private-bound-value"},
	})
	finish(errors.New("private-error-detail"))
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	attempt, exchange := spans[0], spans[1]
	require.Equal(t, "scylla.query", attempt.Name())
	require.Equal(t, "scylla.exchange", exchange.Name())
	require.Equal(t, exchange.SpanContext().SpanID(), attempt.Parent().SpanID())
	require.Equal(t, parent.SpanContext().SpanID(), exchange.Parent().SpanID())
	require.Equal(t, codes.Error, exchange.Status().Code)
	require.NotContains(t, exchange.Status().Description, "private-")
	attrs := make(map[attribute.Key]attribute.Value)
	for _, item := range exchange.Attributes() {
		attrs[item.Key] = item.Value
		require.NotContains(t, item.Value.Emit(), "private-")
	}
	require.EqualValues(t, 1, attrs["gitstore.db.stream.reserved"].AsInt64())
	require.Equal(t, "files_by_uid", attrs["db.collection.name"].AsString())
	require.EqualValues(t, 1, attrs["gitstore.db.stream.started"].AsInt64())
	require.EqualValues(t, 1, attrs["gitstore.db.stream.completed"].AsInt64())
	require.Zero(t, attrs["gitstore.db.stream.abandoned"].AsInt64())
	require.Zero(t, attrs["gitstore.db.stream.overflow"].AsInt64())
	starts := attrs["gitstore.db.stream.start_offset_ns"].AsInt64Slice()
	finishes := attrs["gitstore.db.stream.finish_offset_ns"].AsInt64Slice()
	durations := attrs["gitstore.db.stream.duration_ns"].AsInt64Slice()
	require.Len(t, starts, 1)
	require.Len(t, finishes, 1)
	require.Len(t, durations, 1)
	require.GreaterOrEqual(t, starts[0], int64(0))
	require.GreaterOrEqual(t, durations[0], int64(0))
	require.Equal(t, finishes[0]-starts[0], durations[0])
	require.LessOrEqual(t, finishes[0], attrs["gitstore.db.exchange.duration_ns"].AsInt64())
}

func TestCQLExchangeBoundsConcurrentStreamsAndLateCallbacks(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "parent")
	defer parent.End()
	queryCtx, finish := startCQLExchange(ctx, tracer)
	observer := cqlTraceObserver{tracer: tracer}
	var running sync.WaitGroup
	var admitted atomic.Int32
	for range 32 {
		running.Go(func() {
			stream := observer.StreamContext(queryCtx)
			if stream != nil {
				admitted.Add(1)
				stream.StreamStarted(gocql.ObservedStream{})
				stream.StreamFinished(gocql.ObservedStream{})
			}
		})
	}
	running.Wait()
	require.EqualValues(t, 4, admitted.Load())
	finish(nil)
	require.Len(t, recorder.Ended(), 1)
	overflowFound := false
	for _, item := range recorder.Ended()[0].Attributes() {
		if item.Key == "gitstore.db.stream.overflow" {
			overflowFound = true
			require.EqualValues(t, 28, item.Value.AsInt64())
		}
	}
	require.True(t, overflowFound)

	queryCtx, finish = startCQLExchange(ctx, tracer)
	pending := observer.StreamContext(queryCtx)
	abandoned := observer.StreamContext(queryCtx)
	require.NotNil(t, observer.StreamContext(queryCtx)) // Discarded before stream creation.
	pending.StreamStarted(gocql.ObservedStream{})
	abandoned.StreamStarted(gocql.ObservedStream{})
	abandoned.StreamAbandoned(gocql.ObservedStream{})
	finish(context.Canceled)
	before := recorder.Ended()[1].Attributes()
	pending.StreamFinished(gocql.ObservedStream{})
	require.Equal(t, before, recorder.Ended()[1].Attributes())
	attrs := make(map[attribute.Key]attribute.Value)
	for _, item := range before {
		attrs[item.Key] = item.Value
	}
	require.EqualValues(t, 3, attrs["gitstore.db.stream.reserved"].AsInt64())
	require.EqualValues(t, 2, attrs["gitstore.db.stream.started"].AsInt64())
	require.Zero(t, attrs["gitstore.db.stream.completed"].AsInt64())
	require.EqualValues(t, 1, attrs["gitstore.db.stream.abandoned"].AsInt64())
	require.Equal(t, int64(-1), attrs["gitstore.db.stream.duration_ns"].AsInt64Slice()[0])
}

func TestCQLExchangeIgnoresUnrecordedParents(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "parent")
	defer parent.End()
	for _, ctx := range []context.Context{ctx, context.Background()} {
		queryCtx, finish := startCQLExchange(ctx, tracer)
		require.Equal(t, ctx, queryCtx)
		require.Nil(t, (cqlTraceObserver{tracer: tracer}).StreamContext(queryCtx))
		finish(nil)
	}
	require.Empty(t, recorder.Ended())
}

func TestCQLExchangeIgnoresCompletedAndRemoteSampledParents(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "parent")
	require.True(t, parent.SpanContext().IsSampled())
	parent.End()
	remote := trace.ContextWithRemoteSpanContext(context.Background(), parent.SpanContext())
	for _, ctx := range []context.Context{ctx, remote} {
		queryCtx, finish := startCQLExchange(ctx, tracer)
		require.Equal(t, ctx, queryCtx)
		require.Nil(t, (cqlTraceObserver{tracer: tracer}).StreamContext(queryCtx))
		finish(nil)
	}
	require.Len(t, recorder.Ended(), 1)
}

func BenchmarkCQLExchangeDisabled(b *testing.B) {
	ctx := context.Background()
	tracer := trace.NewNoopTracerProvider().Tracer("test")
	b.ReportAllocs()
	for b.Loop() {
		_, finish := startCQLExchange(ctx, tracer)
		finish(nil)
	}
}
