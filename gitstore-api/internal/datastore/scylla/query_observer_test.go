// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

type cqlQueryObserverFunc func(context.Context, gocql.ObservedQuery)

func (f cqlQueryObserverFunc) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	f(ctx, query)
}

func TestCQLServerTraceDisabledDoesNotAllocate(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, float64(0), testing.AllocsPerRun(100, func() {
		require.Nil(t, sampleCQLServerTrace(ctx))
	}))
}

func TestCQLServerTraceSamplingRequiresExplicitActiveProfiler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	shutdown, err := observability.StartCapacityProfiler(
		observability.ProfilingConfig{URI: "http://" + address}, zap.NewNop(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, shutdown(ctx))
	})
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	ctx, span := provider.Tracer("test").Start(t.Context(), "exchange")
	defer span.End()
	require.Nil(t, sampleCQLServerTrace(ctx))
	completed := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://" + address + "/debug/pprof/trace?seconds=1&sample_limit=2")
		if err == nil {
			var size int64
			size, err = io.Copy(io.Discard, io.LimitReader(response.Body, 16<<20))
			err = errors.Join(err, response.Body.Close())
			if err == nil && (response.StatusCode != http.StatusOK || size < 16) {
				err = fmt.Errorf("execution capture status=%d bytes=%d", response.StatusCode, size)
			}
		}
		completed <- err
	}()
	var first gocql.Tracer
	require.Eventually(t, func() bool {
		first = sampleCQLServerTrace(ctx)
		return first != nil
	}, time.Second, time.Millisecond)
	second := sampleCQLServerTrace(ctx)
	require.NotNil(t, second)
	require.Nil(t, sampleCQLServerTrace(ctx), "exhausted budget must not trace further requests")
	id := gocql.TimeUUID()
	first.Trace(id[:])
	second.Trace(id[:])
	select {
	case err := <-completed:
		require.NoError(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("bounded execution capture did not finish")
	}
	require.Nil(t, sampleCQLServerTrace(ctx), "completed capture must not sample future requests")
	span.End()
	require.Len(t, recorder.Ended(), 1)
	got := recorder.Ended()[0]
	require.Len(t, got.Events(), 2)
	require.Contains(t, got.Attributes(), attribute.Bool("gitstore.db.server_trace.requested", true))
}

func TestCQLServerTraceIDsAreBoundedAndDoNotExposeInvalidBytes(t *testing.T) {
	for _, invalid := range [][]byte{nil, []byte("private-detail"), make([]byte, 16)} {
		recorder := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
		_, span := provider.Tracer("test").Start(t.Context(), "exchange")
		tracer := &cqlServerTrace{span: span}
		tracer.Trace(invalid)
		span.End()
		require.NoError(t, provider.Shutdown(t.Context()))
		require.Len(t, recorder.Ended(), 1)
		got := recorder.Ended()[0]
		require.Empty(t, got.Events())
		require.Contains(t, got.Attributes(), attribute.Bool("gitstore.db.server_trace.invalid", true))
		for _, attr := range got.Attributes() {
			require.NotContains(t, attr.Value.String(), "private")
		}
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	_, span := provider.Tracer("test").Start(t.Context(), "exchange")
	tracer := &cqlServerTrace{span: span}
	id := gocql.TimeUUID()
	var callbacks sync.WaitGroup
	for range 128 {
		callbacks.Go(func() { tracer.Trace(id[:]) })
	}
	callbacks.Wait()
	span.End()
	got := recorder.Ended()[0]
	require.Len(t, got.Events(), 4)
	require.Contains(t, got.Attributes(), attribute.Bool("gitstore.db.server_trace.overflow", true))
	for _, event := range got.Events() {
		require.Equal(t, "scylla.server_trace", event.Name)
		require.Contains(t, event.Attributes, attribute.String("db.cassandra.trace_id", id.String()))
	}
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
					assert.NotContains(t, item.Value.String(), "private-")
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
		require.NotContains(t, item.Value.String(), "private-")
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

func TestCQLRequestGroupLinksExactRowsWithoutChangingCancellation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	type callerKey struct{}
	first, firstSpan := tracer.Start(context.WithValue(t.Context(), callerKey{}, "private-value"), "datastore.UpdateFile")
	second, secondSpan := tracer.Start(t.Context(), "datastore.UpdateFile")
	first, cancelFirst := context.WithCancel(first)
	defer cancelFirst()
	second, cancelSecond := context.WithCancel(second)
	defer cancelSecond()
	group, cancelGroup := fileRequestGroupContext([]context.Context{first, second})
	defer cancelGroup()
	require.Equal(t, "private-value", group.Value(callerKey{}))
	cancelFirst()
	require.NoError(t, group.Err(), "one canceled row must not cancel accepted work for its live peer")
	queryCtx, finish := startCQLExchange(group, tracer)
	observer := cqlTraceObserver{tracer: tracer}
	stream := observer.StreamContext(queryCtx)
	require.NotNil(t, stream)
	stream.StreamStarted(gocql.ObservedStream{})
	stream.StreamFinished(gocql.ObservedStream{})
	start := time.Now()
	observer.ObserveBatch(queryCtx, gocql.ObservedBatch{Start: start, End: time.Now()})
	finish(nil)
	cancelSecond()
	select {
	case <-group.Done():
	case <-time.After(time.Second):
		t.Fatal("all-caller cancellation must still reach accepted physical work")
	}
	firstSpan.End()
	secondSpan.End()
	var exchange sdktrace.ReadOnlySpan
	rows := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, span := range recorder.Ended() {
		if span.Name() == "scylla.exchange" {
			exchange = span
		} else if span.Name() == "datastore.UpdateFile" {
			rows[span.SpanContext().SpanID()] = span
		}
		for _, attr := range span.Attributes() {
			require.NotContains(t, attr.Value.String(), "private-")
		}
	}
	require.NotNil(t, exchange)
	require.Len(t, rows, 2)
	require.Len(t, exchange.Links(), 2)
	for _, link := range exchange.Links() {
		row := rows[link.SpanContext.SpanID()]
		require.NotNil(t, row)
		require.Equal(t, row.SpanContext(), link.SpanContext)
		require.Len(t, row.Links(), 1)
		require.Equal(t, exchange.SpanContext(), row.Links()[0].SpanContext)
	}
	for _, span := range recorder.Ended() {
		if span.Name() == "scylla.batch" {
			require.Equal(t, exchange.SpanContext().SpanID(), span.Parent().SpanID())
		}
	}
}

func TestCQLRequestGroupBoundsAndDeduplicatesLinks(t *testing.T) {
	for _, count := range []int{2, 16, 17} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			tracer := provider.Tracer("test")
			var contexts []context.Context
			var members []trace.Span
			for range count {
				ctx, span := tracer.Start(t.Context(), "row")
				contexts = append(contexts, ctx)
				members = append(members, span)
			}
			group := withCQLRequestGroup(contexts[0], contexts)
			_, finish := startCQLExchange(group, tracer)
			finish(nil)
			exchange := recorder.Ended()[0]
			require.Len(t, exchange.Links(), min(count, 16))
			values := map[attribute.Key]attribute.Value{}
			for _, attr := range exchange.Attributes() {
				values[attr.Key] = attr.Value
			}
			require.EqualValues(t, count, values["gitstore.db.group.member_count"].AsInt64())
			require.EqualValues(t, min(count, 16), values["gitstore.db.group.linked_member_count"].AsInt64())
			require.Equal(t, count > 16, values["gitstore.db.group.links_overflow"].AsBool())
			for _, member := range members {
				member.End()
			}
			repeated := []context.Context{contexts[0], contexts[0], contexts[1]}
			// Ended spans and remote sampled parents are not live row operations.
			require.Equal(t, contexts[0], withCQLRequestGroup(contexts[0], repeated))
			first, firstSpan := tracer.Start(t.Context(), "first")
			defer firstSpan.End()
			remote := trace.ContextWithRemoteSpanContext(t.Context(), firstSpan.SpanContext())
			_, finish = startCQLExchange(withCQLRequestGroup(first, []context.Context{first, first, remote}), tracer)
			finish(nil)
			exchanges := recorder.Ended()
			require.Len(t, exchanges[len(exchanges)-1].Links(), 1)
		})
	}
}

func TestCQLRequestGroupDisabledDoesNotAllocate(t *testing.T) {
	ctx := t.Context()
	contexts := []context.Context{ctx, ctx}
	var result context.Context
	require.Zero(t, testing.AllocsPerRun(100, func() {
		result = withCQLRequestGroup(ctx, contexts)
	}))
	require.Equal(t, ctx, result)
	require.Nil(t, result.Value(cqlRequestGroupKey{}))
}

func TestCQLRequestGroupDoesNotLinkUnrecordedExchange(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	ctx, row := provider.Tracer("test").Start(t.Context(), "row")
	group := withCQLRequestGroup(ctx, []context.Context{ctx})
	_, finish := startCQLExchange(group, noop.NewTracerProvider().Tracer("test"))
	finish(nil)
	row.End()
	require.Len(t, recorder.Ended(), 1)
	require.Empty(t, recorder.Ended()[0].Links(), "an unrecorded physical exchange must not create a dangling or self link")
}

func TestFileProjectionReaderExchangeLinksEveryAcceptedRow(t *testing.T) {
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	otel.SetTracerProvider(provider)
	tracer := provider.Tracer("test")
	var requests []*fileProjectionRequest
	var rowSpans []trace.Span
	for range 2 {
		ctx, span := tracer.Start(t.Context(), "datastore.UpdateFile")
		rowSpans = append(rowSpans, span)
		requests = append(requests, &fileProjectionRequest{ctx: ctx, reply: make(chan fileProjectionResult, 1)})
	}
	reader := &fileProjectionReader{read: func(ctx context.Context, keys []fileProjectionKey) ([]*ProjectionRecord, error) {
		require.Len(t, keys, 2)
		observer := cqlTraceObserver{tracer: tracer}
		stream := observer.StreamContext(ctx)
		require.NotNil(t, stream)
		stream.StreamStarted(gocql.ObservedStream{})
		stream.StreamFinished(gocql.ObservedStream{})
		start := time.Now()
		observer.ObserveQuery(ctx, gocql.ObservedQuery{Start: start, End: time.Now(), Statement: "SELECT private-columns"})
		return []*ProjectionRecord{{}, {}}, nil
	}}
	reader.execute(requests)
	for _, request := range requests {
		require.NoError(t, (<-request.reply).err)
	}
	for _, row := range rowSpans {
		row.End()
	}
	spans := recorder.Ended()
	require.Len(t, spans, 4)
	require.Equal(t, "scylla.query", spans[0].Name())
	require.Equal(t, "scylla.exchange", spans[1].Name())
	require.Equal(t, spans[1].SpanContext().SpanID(), spans[0].Parent().SpanID())
	require.Len(t, spans[1].Links(), 2)
	for _, row := range spans[2:] {
		require.Len(t, row.Links(), 1)
		require.Equal(t, spans[1].SpanContext(), row.Links()[0].SpanContext)
	}
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
	tracer := noop.NewTracerProvider().Tracer("test")
	b.ReportAllocs()
	for b.Loop() {
		_, finish := startCQLExchange(ctx, tracer)
		finish(nil)
	}
}
