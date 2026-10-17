// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gin-gonic/gin"
	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type graphQLPhaseStore struct {
	datastore.Datastore
	failure       error
	snapshotReads atomic.Int64
	pointReads    atomic.Int64
}

func (s *graphQLPhaseStore) GetFileSnapshots(_ context.Context, ids []string) ([]*datastore.File, error) {
	s.snapshotReads.Add(1)
	if s.failure != nil {
		return nil, s.failure
	}
	files := make([]*datastore.File, len(ids))
	for i, id := range ids {
		files[i] = &datastore.File{UID: id, Name: "private-file", Namespace: "private-namespace"}
	}
	return files, nil
}

func (s *graphQLPhaseStore) GetFile(context.Context, string) (*datastore.File, error) {
	s.pointReads.Add(1)
	return nil, errors.New("unexpected point read")
}

type graphQLPhaseAuthN struct{ webSocketAuthProvider }

func (*graphQLPhaseAuthN) Name() string { return "phase-test" }

func (*graphQLPhaseAuthN) Authenticate(context.Context, auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	return &auth.Principal{Subject: "private-reader", AuthMethod: "test", ExpiresAt: time.Now().Add(time.Hour)},
		auth.Allow("test", "test"), nil
}

func TestGraphQLFileReadPhasesPreserveBoundedReadsAndTraceTree(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	for _, tc := range []struct {
		name    string
		count   int
		tracing bool
		sampled bool
		failure error
	}{
		{name: "hundred_files", count: 100, tracing: true, sampled: true},
		{name: "multiple_bounded_batches", count: datastore.FileSnapshotBatchLimit + 1, tracing: true, sampled: true},
		{name: "storage_failure", count: 100, tracing: true, sampled: true, failure: errors.New("private failure")},
		{name: "unsampled", count: 100, tracing: true},
		{name: "disabled", count: 100, sampled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder),
				sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
			defer provider.Shutdown(t.Context())
			otel.SetTracerProvider(provider)
			base, err := memdb.New()
			require.NoError(t, err)
			defer base.Close()
			store := &graphQLPhaseStore{Datastore: base, failure: tc.failure}
			instrumented := datastore.NewInstrumentedDatastoreWithRegistry(store, "memdb", zap.NewNop(), prometheus.NewRegistry())
			handler, err := NewGraphQLHandler(GraphQLHandlerDeps{
				Store: instrumented, Logger: zap.NewNop(), IDs: apiruntime.NewSequenceIDGenerator(), Tracing: tc.tracing,
				Registry: auth.NewProviderRegistry(auth.NewChainedAuthN(&graphQLPhaseAuthN{}), testutil.NewAllowAllAuthZ(), nil),
			})
			require.NoError(t, err)
			server := httptest.NewServer(handler)
			defer server.Close()
			ids := make([]string, tc.count)
			for i := range ids {
				ids[i] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("gid://GitStore/File/00000000-0000-0000-0000-%012d", i)))
			}
			body, err := json.Marshal(map[string]any{
				"query":     `query($ids:[ID!]!){nodes(ids:$ids){id ... on File {metadata{name namespace}}}}`,
				"variables": map[string]any{"ids": ids},
			})
			require.NoError(t, err)
			request, err := http.NewRequest(http.MethodPost, server.URL+"/graphql", strings.NewReader(string(body)))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer phase-test")
			flags := trace.TraceFlags(0)
			if tc.sampled {
				flags = trace.FlagsSampled
			}
			remote := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags, Remote: true,
			})
			observability.Inject(trace.ContextWithRemoteSpanContext(t.Context(), remote), request.Header)
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			var result struct {
				Data   struct{ Nodes []struct{ ID string } }
				Errors []json.RawMessage
			}
			require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode)
			expectedReads := (tc.count + datastore.FileSnapshotBatchLimit - 1) / datastore.FileSnapshotBatchLimit
			if tc.failure != nil {
				expectedReads = 1
				require.NotEmpty(t, result.Errors)
				require.Empty(t, result.Data.Nodes)
			} else {
				require.Empty(t, result.Errors)
				require.Len(t, result.Data.Nodes, len(ids))
				for i, node := range result.Data.Nodes {
					require.Equal(t, ids[i], node.ID)
				}
			}
			require.EqualValues(t, expectedReads, store.snapshotReads.Load())
			require.Zero(t, store.pointReads.Load())
			spans := recorder.Ended()
			if !tc.tracing || !tc.sampled {
				require.Empty(t, spans)
				return
			}
			byName := make(map[string][]sdktrace.ReadOnlySpan)
			for _, span := range spans {
				require.Equal(t, remote.TraceID(), span.SpanContext().TraceID())
				require.NotContains(t, span.Status().Description, "private")
				require.Empty(t, span.Events())
				if span.Name() != "datastore.GetFileSnapshots" {
					require.Empty(t, span.Attributes())
				}
				byName[span.Name()] = append(byName[span.Name()], span)
			}
			for _, name := range []string{"http.request", "graphql.execute", "graphql.file_node_authorization"} {
				require.Len(t, byName[name], 1, name)
			}
			require.Len(t, byName["datastore.GetFileSnapshots"], expectedReads)
			httpSpan, execution, authorization := byName["http.request"][0], byName["graphql.execute"][0], byName["graphql.file_node_authorization"][0]
			require.Equal(t, remote.SpanID(), httpSpan.Parent().SpanID())
			for _, pair := range [][2]sdktrace.ReadOnlySpan{{httpSpan, execution}, {execution, authorization}} {
				require.Equal(t, pair[0].SpanContext().SpanID(), pair[1].Parent().SpanID())
				require.False(t, pair[1].StartTime().Before(pair[0].StartTime()))
				require.False(t, pair[1].EndTime().After(pair[0].EndTime()))
			}
			for _, snapshot := range byName["datastore.GetFileSnapshots"] {
				require.Equal(t, authorization.SpanContext().SpanID(), snapshot.Parent().SpanID())
			}
			if tc.failure != nil {
				require.Empty(t, byName["graphql.node_resolution"])
				require.Equal(t, "Error", authorization.Status().Code.String())
				require.Equal(t, "Error", execution.Status().Code.String())
				require.Len(t, spans, expectedReads+3)
			} else {
				require.Len(t, byName["graphql.node_resolution"], 1)
				resolution := byName["graphql.node_resolution"][0]
				require.Equal(t, execution.SpanContext().SpanID(), resolution.Parent().SpanID(), "resolution must not inherit an already-ended authorization span")
				require.False(t, resolution.StartTime().Before(authorization.EndTime()))
				require.False(t, resolution.EndTime().After(execution.EndTime()))
				require.Len(t, spans, expectedReads+4, "no per-File phase spans")
			}
		})
	}
}

func TestGraphQLExecutionPhaseBoundaries(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	for _, tc := range []struct {
		name                                            string
		subscription, noOperation, noContext, noTracing bool
		cancel, panics, noResponse, failure             bool
	}{
		{name: "query"},
		{name: "errors", failure: true},
		{name: "cancelled", cancel: true},
		{name: "panic", panics: true},
		{name: "empty_response", noResponse: true},
		{name: "subscription", subscription: true},
		{name: "no_operation", noOperation: true},
		{name: "no_operation_context", noContext: true},
		{name: "no_recording_parent", noTracing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(t.Context())
			otel.SetTracerProvider(provider)
			ctx, parent := provider.Tracer("test").Start(t.Context(), "request")
			defer parent.End()
			if tc.noTracing {
				ctx = t.Context()
			}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			op := &ast.OperationDefinition{Operation: ast.Query}
			if tc.subscription {
				op.Operation = ast.Subscription
			} else if tc.noOperation {
				op = nil
			}
			if !tc.noContext {
				ctx = graphql.WithOperationContext(ctx, &graphql.OperationContext{Operation: op})
			}
			expected := &graphql.Response{Data: json.RawMessage(`{"private":"data"}`)}
			if tc.failure {
				expected.Errors = gqlerror.List{gqlerror.Errorf("private error details")}
			} else if tc.noResponse {
				expected = nil
			}
			called := false
			run := func() {
				got := graphQLExecutionPhase(ctx, func(nextCtx context.Context) *graphql.Response {
					called = true
					if tc.cancel {
						cancel()
						require.ErrorIs(t, nextCtx.Err(), context.Canceled)
					}
					if tc.panics {
						panic("private panic details")
					}
					return expected
				})
				if expected == nil {
					require.Nil(t, got)
				} else {
					require.Same(t, expected, got)
				}
			}
			if tc.panics {
				require.Panics(t, run)
			} else {
				run()
			}
			require.True(t, called)
			spans := recorder.Ended()
			if tc.subscription || tc.noOperation || tc.noContext || tc.noTracing {
				require.Empty(t, spans)
				return
			}
			require.Len(t, spans, 1)
			span := spans[0]
			require.Equal(t, "graphql.execute", span.Name())
			require.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID())
			require.Empty(t, span.Attributes())
			require.Empty(t, span.Events())
			require.NotContains(t, span.Status().Description, "private")
			if tc.cancel || tc.panics || tc.failure || tc.noResponse {
				require.Equal(t, "Error", span.Status().Code.String())
			} else {
				require.Equal(t, "Unset", span.Status().Code.String())
			}
		})
	}
}

func TestGraphQLMalformedBodiesPreserveClientErrorResponses(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(t.Context())
	otel.SetTracerProvider(provider)
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Hour), true)
	for i, body := range []string{"{", "", `{"query":42}`} {
		t.Run(fmt.Sprintf("malformed_body_%d", i), func(t *testing.T) {
			before := len(recorder.Ended())
			request, err := http.NewRequest(http.MethodPost, env.server.URL+"/graphql", strings.NewReader(body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			remote := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{byte(i + 1)}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true,
			})
			observability.Inject(trace.ContextWithRemoteSpanContext(t.Context(), remote), request.Header)
			response, err := env.server.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			var result struct{ Errors []json.RawMessage }
			require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
			require.Equal(t, http.StatusBadRequest, response.StatusCode)
			require.NotEmpty(t, result.Errors)
			spans := recorder.Ended()[before:]
			require.Len(t, spans, 1, "body decoding errors must not invent a GraphQL execution")
			require.Equal(t, "http.request", spans[0].Name())
			require.Equal(t, remote.TraceID(), spans[0].SpanContext().TraceID())
		})
	}
}

func TestGraphQLCompletionSamplingCancellationPanicAndWatchBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, method, result                  string
		sampled, cancel, panics, nonRecording bool
	}{
		{name: "sampled", method: http.MethodPost, sampled: true, result: "completed"},
		{name: "unsampled", method: http.MethodPost, result: "completed"},
		{name: "cancelled", method: http.MethodPost, sampled: true, cancel: true, result: "cancelled"},
		{name: "panic", method: http.MethodPost, sampled: true, panics: true, result: "panic"},
		{name: "watch", method: http.MethodGet, sampled: true},
		{name: "nonrecording", method: http.MethodPost, sampled: true, nonRecording: true, result: "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sampler := sdktrace.NeverSample()
			if tc.sampled {
				sampler = sdktrace.AlwaysSample()
			}
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler))
			defer provider.Shutdown(t.Context())
			ctx, span := provider.Tracer("completion-test").Start(t.Context(), "request")
			defer span.End()
			if tc.nonRecording {
				ctx = trace.ContextWithSpanContext(ctx, span.SpanContext())
			}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			core, local := observer.New(zap.InfoLevel)
			registry := prometheus.NewRegistry()
			registry.MustRegister(graphQLRequestDuration)
			count := func() uint64 {
				families, err := registry.Gather()
				require.NoError(t, err)
				var result uint64
				for _, family := range families {
					for _, series := range family.Metric {
						result += series.Histogram.GetSampleCount()
					}
				}
				return result
			}
			before := count()
			handler := graphQLHTTPCompletion(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.cancel {
					cancel()
					require.ErrorIs(t, r.Context().Err(), context.Canceled)
				}
				if tc.panics {
					panic("private")
				}
				w.WriteHeader(http.StatusNoContent)
			}), zap.New(core))
			run := func() {
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, "/graphql", nil).WithContext(ctx))
			}
			if tc.panics {
				require.Panics(t, run)
			} else {
				run()
			}
			if tc.method == http.MethodPost {
				require.Equal(t, before+1, count(), "one observation even on panic/cancellation")
			} else {
				require.Equal(t, before, count(), "watch lifetime must not be treated as a finite request")
			}
			if tc.sampled && !tc.nonRecording && tc.method == http.MethodPost {
				require.Equal(t, 1, local.Len())
				require.Equal(t, tc.result, local.All()[0].ContextMap()["result"])
			} else {
				require.Zero(t, local.Len())
			}
		})
	}
}

func TestGraphQLHTTPCompletionExportsNativeContextAndExemplars(t *testing.T) {
	previousTraces, previousLogs, previousMetrics := otel.GetTracerProvider(), otel.GetLoggerProvider(), otel.GetMeterProvider()
	previousPropagation, previousErrors := otel.GetTextMapPropagator(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTraces)
		otel.SetLoggerProvider(previousLogs)
		otel.SetMeterProvider(previousMetrics)
		otel.SetTextMapPropagator(previousPropagation)
		otel.SetErrorHandler(previousErrors)
	})
	var mu sync.Mutex
	var traces tracepb.ExportTraceServiceRequest
	var logs logpb.ExportLogsServiceRequest
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/otlp/v1/traces":
			var batch tracepb.ExportTraceServiceRequest
			require.NoError(t, proto.Unmarshal(body, &batch))
			traces.ResourceSpans = append(traces.ResourceSpans, batch.ResourceSpans...)
		case "/otlp/v1/logs":
			var batch logpb.ExportLogsServiceRequest
			require.NoError(t, proto.Unmarshal(body, &batch))
			logs.ResourceLogs = append(logs.ResourceLogs, batch.ResourceLogs...)
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	core, local := observer.New(zap.InfoLevel)
	logger, shutdown, err := InitTelemetry(t.Context(), config.OTLPConfig{URI: collector.URL + "/otlp", TraceSampleRatio: 0}, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	env := newWebSocketTestEnvironmentWithLogger(t, time.Now().Add(time.Hour), logger, true)
	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	request, err := http.NewRequest(http.MethodPost, env.server.URL+"/graphql",
		strings.NewReader(`{"query":"query { __typename }"}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	observability.Inject(trace.ContextWithRemoteSpanContext(t.Context(), remote), request.Header)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, shutdown(t.Context()))
	require.Equal(t, 1, local.FilterMessage("graphql HTTP request completed").Len())
	mu.Lock()
	defer mu.Unlock()
	var serverID trace.SpanID
	foundSpan := false
	for _, resource := range traces.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				if span.Name == "http.request" {
					foundSpan = true
					copy(serverID[:], span.SpanId)
					parentID, traceID := remote.SpanID(), remote.TraceID()
					require.Equal(t, parentID[:], span.ParentSpanId)
					require.Equal(t, traceID[:], span.TraceId)
				}
			}
		}
	}
	require.True(t, foundSpan)
	foundLog := false
	for _, resource := range logs.ResourceLogs {
		for _, scope := range resource.ScopeLogs {
			for _, record := range scope.LogRecords {
				switch record.Body.GetStringValue() {
				case "graphql HTTP request completed":
					foundLog = true
					traceID := remote.TraceID()
					require.Equal(t, serverID[:], record.SpanId)
					require.Equal(t, traceID[:], record.TraceId)
					require.LessOrEqual(t, len(record.Attributes), 2)
				case "local telemetry enabled":
					require.Empty(t, record.TraceId, "startup logs legitimately have no request context")
					require.Empty(t, record.SpanId)
				}
			}
		}
	}
	require.True(t, foundLog)
	handler := health.NewHandler(health.HandlerDeps{InstanceID: "correlation-test"})
	router := gin.New()
	router.GET("/metrics", handler.Metrics)
	scrape := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	scrape.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	metrics := httptest.NewRecorder()
	router.ServeHTTP(metrics, scrape)
	require.Contains(t, metrics.Header().Get("Content-Type"), "application/openmetrics-text")
	foundExemplar := false
	for _, line := range strings.Split(metrics.Body.String(), "\n") {
		if strings.HasPrefix(line, "gitstore_api_graphql_http_request_duration_seconds_bucket{") &&
			strings.Contains(line, `span_id="`+serverID.String()+`"`) {
			require.Contains(t, line, `trace_id="`+remote.TraceID().String()+`"`)
			require.Contains(t, line, `result="completed"`)
			foundExemplar = true
		}
	}
	require.True(t, foundExemplar)
}

func TestTelemetryDisabledUsesResolvedConfiguration(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_SAMPLER", "invalid")
	logger := zap.NewNop()
	provider := otel.GetTracerProvider()
	resolved, shutdown, err := InitTelemetry(context.Background(), config.OTLPConfig{}, logger)
	require.NoError(t, err)
	require.Same(t, logger, resolved)
	require.Same(t, provider, otel.GetTracerProvider())
	require.NoError(t, shutdown(context.Background()))
}

func TestTelemetryExportsUsingResolvedConfiguration(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0")
	var traces, logs atomic.Int64
	assertAPIIdentity := func(attributes []*commonpb.KeyValue) {
		found := false
		for _, attr := range attributes {
			if attr.Key == "service.name" {
				found = true
				assert.Equal(t, "gitstore-api", attr.Value.GetStringValue())
			}
		}
		assert.True(t, found, "API startup adapter must set its fixed service identity")
	}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read export", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/otlp/v1/traces":
			var batch tracepb.ExportTraceServiceRequest
			require.NoError(t, proto.Unmarshal(body, &batch))
			require.NotEmpty(t, batch.ResourceSpans)
			for _, resource := range batch.ResourceSpans {
				assertAPIIdentity(resource.Resource.Attributes)
			}
			traces.Add(1)
		case "/otlp/v1/logs":
			var batch logpb.ExportLogsServiceRequest
			require.NoError(t, proto.Unmarshal(body, &batch))
			require.NotEmpty(t, batch.ResourceLogs)
			for _, resource := range batch.ResourceLogs {
				assertAPIIdentity(resource.Resource.Attributes)
			}
			logs.Add(1)
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	previousTraces := otel.GetTracerProvider()
	previousLogs := otel.GetLoggerProvider()
	previousMetrics := otel.GetMeterProvider()
	previousPropagation := otel.GetTextMapPropagator()
	previousErrors := otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTraces)
		otel.SetLoggerProvider(previousLogs)
		otel.SetMeterProvider(previousMetrics)
		otel.SetTextMapPropagator(previousPropagation)
		otel.SetErrorHandler(previousErrors)
	})
	logger, shutdown, err := InitTelemetry(context.Background(),
		config.OTLPConfig{URI: collector.URL + "/otlp", TraceSampleRatio: 1},
		zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
			zapcore.AddSync(io.Discard), zap.InfoLevel)))
	require.NoError(t, err)
	shutdownDone := false
	t.Cleanup(func() {
		if shutdownDone {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NoError(t, shutdown(ctx))
	})
	_, span := otel.Tracer("config-test").Start(context.Background(), "resolved-config")
	require.True(t, span.IsRecording(), "GitStore sampling configuration must override SDK environment defaults")
	span.End()
	logger.Info("resolved configuration log")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
	shutdownDone = true
	assert.Positive(t, traces.Load())
	assert.Positive(t, logs.Load())
}

func TestProfilerUsesResolvedConfiguration(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	t.Setenv("GITSTORE_CAPACITY_PROFILE_ADDR", listener.Addr().String())
	shutdown, err := observability.StartCapacityProfiler(config.ProfilingConfig{}, zap.NewNop())
	require.NoError(t, err, "the removed environment path must not open a listener")
	require.NoError(t, shutdown(context.Background()))
	_, err = observability.StartCapacityProfiler(config.ProfilingConfig{URI: "http://" + listener.Addr().String()}, zap.NewNop())
	require.ErrorContains(t, err, "listen for capacity profiles")
}

type catalogMetricsStub struct {
	catalogv1.UnimplementedCatalogServiceServer
	code codes.Code
}

func (s *catalogMetricsStub) AdmitResources(context.Context, *catalogv1.AdmitResourcesRequest) (*catalogv1.AdmitResourcesResponse, error) {
	if s.code != codes.OK {
		return nil, status.Error(s.code, "fixture denial")
	}
	return &catalogv1.AdmitResourcesResponse{}, nil
}

func TestCatalogGRPCMetricsMeasureSuccessAndDenialWithoutResourceLabels(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{codes.OK, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			registry := prometheus.NewRegistry()
			server, err := newCatalogGRPCServer(&catalogMetricsStub{code: code}, registry)
			require.NoError(t, err)
			listener := bufconn.Listen(1 << 20)
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				server.Stop()
				require.NoError(t, <-done)
				require.NoError(t, listener.Close())
			})
			conn, err := grpc.NewClient("passthrough:///catalog",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = catalogv1.NewCatalogServiceClient(conn).AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: "private-fixture-resource"})
			require.Equal(t, code, status.Code(err))
			families, err := registry.Gather()
			require.NoError(t, err)
			sampled := false
			for _, family := range families {
				for _, metric := range family.Metric {
					method := ""
					for _, label := range metric.Label {
						require.Contains(t, []string{"grpc_type", "grpc_service", "grpc_method", "grpc_code", "le"}, label.GetName())
						require.NotContains(t, label.GetValue(), "private-fixture-resource")
						if label.GetName() == "grpc_method" {
							method = label.GetValue()
						}
					}
					if family.GetName() == "gitstore_catalog_grpc_server_handling_seconds" && method == "AdmitResources" {
						require.EqualValues(t, 1, metric.Histogram.GetSampleCount())
						require.Positive(t, metric.Histogram.GetSampleSum())
						sampled = true
					}
				}
			}
			require.True(t, sampled, "the actual registered RPC server must measure both success and denial")
			_, err = newCatalogGRPCServer(&catalogMetricsStub{}, registry)
			require.ErrorContains(t, err, "register catalog gRPC metrics")
		})
	}
}

type bootstrapGitProvisioner struct {
	repositoryIDs []string
	actors        []*auth.Principal
}

func (p *bootstrapGitProvisioner) CreateRepository(ctx context.Context, repositoryID, _ string) (string, error) {
	p.repositoryIDs = append(p.repositoryIDs, repositoryID)
	p.actors = append(p.actors, auth.PrincipalFromContext(ctx))
	return "/repos/" + repositoryID, nil
}

func TestEnsureBootstrapResourcesCreatesExactlyTwoNamespacesAndRepositoriesIdempotently(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	git := &bootstrapGitProvisioner{}
	ids := apiruntime.NewSequenceIDGenerator(
		"00000000-0000-7000-8000-000000000001",
		"00000000-0000-7000-8000-000000000002",
		"00000000-0000-7000-8000-000000000003",
		"00000000-0000-7000-8000-000000000004",
	)
	clock := apiruntime.NewFixedClock(time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC))

	require.NoError(t, ensureBootstrapResources(context.Background(), store, git, clock, ids, zap.NewNop()))
	require.NoError(t, ensureBootstrapResources(context.Background(), store, git, clock, ids, zap.NewNop()))

	namespaces, err := store.ListNamespaces(context.Background(), datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, namespaces.Items, 2)
	assert.Len(t, git.repositoryIDs, 2)
	for _, actor := range git.actors {
		require.NotNil(t, actor, "bootstrap must explicitly identify its internal backing actor")
		require.Equal(t, "system:api", actor.Subject)
		require.Equal(t, "internal", actor.AuthMethod)
	}

	for _, identifier := range []string{"gitstore-system", "default"} {
		namespace, getErr := store.GetNamespaceByName(context.Background(), identifier)
		require.NoError(t, getErr)
		assert.Equal(t, bootstrapActor, namespace.CreationActor)
		assert.Equal(t, datastore.NamespaceTierOrganization, namespace.Tier)
		assert.Equal(t, datastore.NamespaceInitialResourceVersion, namespace.ResourceVersion)

		mapping, lookupErr := store.LookupRepository(context.Background(), namespace.Name, bootstrapRepositoryName)
		require.NoError(t, lookupErr)
		repository, repoErr := store.GetRepository(context.Background(), mapping.RepositoryID)
		require.NoError(t, repoErr)
		assert.Equal(t, bootstrapRepositoryName, repository.Name)
		assert.Equal(t, namespace.Name, repository.Namespace)
	}
}
