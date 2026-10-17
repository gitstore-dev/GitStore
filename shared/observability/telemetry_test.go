// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/proto"
)

func preserveGlobals(t *testing.T) {
	t.Helper()
	traces, logs, metrics := otel.GetTracerProvider(), otel.GetLoggerProvider(), otel.GetMeterProvider()
	propagation, handler := otel.GetTextMapPropagator(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(traces)
		otel.SetLoggerProvider(logs)
		otel.SetMeterProvider(metrics)
		otel.SetTextMapPropagator(propagation)
		otel.SetErrorHandler(handler)
	})
}

func discardLogger() *zap.Logger {
	return zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(io.Discard), zap.DebugLevel))
}

func TestConfigSecurityAndSampling(t *testing.T) {
	for _, uri := range []string{"", "http://localhost:4318", "https://collector/otlp", "http://[::1]:4318/base/"} {
		require.NoError(t, (OTLPConfig{URI: uri, TraceSampleRatio: 0.05}).Validate())
	}
	for _, uri := range []string{"http://user:private@host", "http://host?", "http://host#", "http://host?token=private",
		"grpc://host:4317", "http:///missing", "http://host:bad", strings.Repeat("x", 257)} {
		err := (OTLPConfig{URI: uri}).Validate()
		require.Error(t, err, uri)
		require.NotContains(t, err.Error(), "private")
	}
	for _, ratio := range []float64{-1, 1.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		require.Error(t, (OTLPConfig{TraceSampleRatio: ratio}).Validate())
	}
	for _, ratio := range []float64{0, 1, 0.5} {
		require.NoError(t, (OTLPConfig{TraceSampleRatio: ratio}).Validate())
	}
}

func TestDisabledAndInvalidTelemetry(t *testing.T) {
	preserveGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_SAMPLER", "invalid")
	core, entries := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	provider := otel.GetTracerProvider()
	resolved, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{}, ControllerService, "test", logger)
	require.NoError(t, err)
	require.Same(t, logger, resolved)
	require.Same(t, provider, otel.GetTracerProvider())
	resolved.Info("local logging survives")
	require.Equal(t, 1, entries.Len())
	require.NoError(t, shutdown(t.Context()))
	_, _, err = InitTelemetry(t.Context(), OTLPConfig{URI: "http://user:private@collector"}, APIService, "test", logger)
	require.Error(t, err)
	require.Same(t, provider, otel.GetTracerProvider())
	_, _, err = InitTelemetry(t.Context(), OTLPConfig{}, "untrusted-service", "test", logger)
	require.ErrorContains(t, err, "service identity")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = InitTelemetry(cancelled, OTLPConfig{URI: "http://localhost:4318"}, APIService, "test", logger)
	require.ErrorIs(t, err, context.Canceled)
	require.Same(t, provider, otel.GetTracerProvider())
}

type rejectedMetrics struct{ prometheus.Registerer }

func (rejectedMetrics) Register(prometheus.Collector) error {
	return errors.New("metrics registration rejected")
}

func TestInitializationFailureDoesNotInstallPartialProviders(t *testing.T) {
	preserveGlobals(t)
	previous := prometheus.DefaultRegisterer
	prometheus.DefaultRegisterer = rejectedMetrics{previous}
	defer func() { prometheus.DefaultRegisterer = previous }()
	traces, logs, metrics := otel.GetTracerProvider(), otel.GetLoggerProvider(), otel.GetMeterProvider()
	_, _, err := InitTelemetry(t.Context(), OTLPConfig{URI: "http://localhost:4318"}, ControllerService, "test", discardLogger())
	require.ErrorContains(t, err, "initialize telemetry metrics")
	require.Same(t, traces, otel.GetTracerProvider())
	require.Same(t, logs, otel.GetLoggerProvider())
	require.Same(t, metrics, otel.GetMeterProvider())
}

func TestResolvedZeroSamplingStillHonorsSampledParents(t *testing.T) {
	preserveGlobals(t)
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	var count atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		require.NoError(t, err)
		var batch tracepb.ExportTraceServiceRequest
		require.NoError(t, proto.Unmarshal(body, &batch))
		for _, resource := range batch.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				count.Add(int64(len(scope.Spans)))
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	_, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{URI: collector.URL, TraceSampleRatio: 0},
		ControllerService, "test", zap.NewNop())
	require.NoError(t, err)
	_, root := otel.Tracer("sampling-test").Start(t.Context(), "unsampled-root")
	require.False(t, root.IsRecording(), "resolved ratio must override SDK sampler environment")
	root.End()
	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	_, child := otel.Tracer("sampling-test").Start(trace.ContextWithRemoteSpanContext(t.Context(), remote), "sampled-child")
	require.True(t, child.IsRecording(), "sampling is parent-based, not an unconditional root ratio")
	child.End()
	require.NoError(t, shutdown(t.Context()))
	require.EqualValues(t, 1, count.Load())
}

func stringAttribute(attrs []*commonpb.KeyValue, key string) string {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.GetStringValue()
		}
	}
	return ""
}

func TestTelemetryProcessIdentity(t *testing.T) {
	const probeEnv = "GITSTORE_TEST_TELEMETRY_IDENTITY_PROBE"
	type probe struct {
		Hostname  string
		Instances []string
	}
	if mode := os.Getenv(probeEnv); mode != "" {
		preserveGlobals(t)
		if mode == "entropy-failure" {
			cause := errors.New("identity entropy unavailable")
			reader := rand.Reader
			rand.Reader = iotest.ErrReader(cause)
			defer func() { rand.Reader = reader }()
			providers := []any{otel.GetTracerProvider(), otel.GetLoggerProvider(), otel.GetMeterProvider()}
			logger := discardLogger()
			resolved, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{}, APIService, "test", logger)
			require.NoError(t, err, "disabled telemetry must not require entropy")
			require.Same(t, logger, resolved)
			require.NoError(t, shutdown(t.Context()))
			for range 2 {
				resolved, shutdown, err = InitTelemetry(t.Context(), OTLPConfig{URI: "http://127.0.0.1:1"},
					APIService, "test", logger)
				require.ErrorIs(t, err, cause)
				require.ErrorContains(t, err, "telemetry process identity")
				require.Nil(t, resolved)
				require.Nil(t, shutdown)
				require.Same(t, providers[0], otel.GetTracerProvider())
				require.Same(t, providers[1], otel.GetLoggerProvider())
				require.Same(t, providers[2], otel.GetMeterProvider())
				rand.Reader = reader
			}
			return
		}
		require.Contains(t, []string{APIService, ControllerService}, mode)
		collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.Copy(io.Discard, r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/x-protobuf")
		}))
		defer collector.Close()
		hostname, err := os.Hostname()
		require.NoError(t, err)
		result := probe{Hostname: hostname}
		for range 2 {
			_, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{URI: collector.URL, TraceSampleRatio: 1},
				mode, "test", discardLogger())
			require.NoError(t, err)
			_, span := otel.Tracer("identity-probe").Start(t.Context(), "identity-probe")
			record, ok := span.(sdktrace.ReadOnlySpan)
			require.True(t, ok)
			instance, ok := record.Resource().Set().Value(attribute.Key("service.instance.id"))
			require.True(t, ok)
			result.Instances = append(result.Instances, instance.AsString())
			span.End()
			require.NoError(t, shutdown(t.Context()))
		}
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		fmt.Printf("IDENTITY_PROBE %s\n", encoded)
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	hostname, err := os.Hostname()
	require.NoError(t, err)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=operator-override")
	seen := make(map[string]struct{})
	for _, mode := range []string{APIService, APIService, ControllerService, ControllerService} {
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestTelemetryProcessIdentity$")
		cmd.Env = append(os.Environ(), probeEnv+"="+mode)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		var result probe
		found := false
		for _, line := range strings.Split(string(output), "\n") {
			if payload, ok := strings.CutPrefix(line, "IDENTITY_PROBE "); ok {
				require.False(t, found)
				require.NoError(t, json.Unmarshal([]byte(payload), &result))
				found = true
			}
		}
		require.True(t, found)
		require.Equal(t, hostname, result.Hostname, "all probes must reuse the same actual hostname")
		require.Len(t, result.Instances, 2)
		require.Equal(t, result.Instances[0], result.Instances[1], "identity must remain stable across provider reinitialization")
		instance := result.Instances[0]
		seen[instance] = struct{}{}
	}
	require.Len(t, seen, 4, "four separate processes on the same actual hostname must have distinct identities")
	for instance := range seen {
		require.Len(t, instance, 32)
		raw, err := hex.DecodeString(instance)
		require.NoError(t, err)
		require.Len(t, raw, 16)
		require.Equal(t, strings.ToLower(instance), instance)
		require.NotEqual(t, hostname, instance)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestTelemetryProcessIdentity$")
	cmd.Env = append(os.Environ(), probeEnv+"=entropy-failure")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestHTTPDeliveryIdentitiesBoundsAndContext(t *testing.T) {
	for _, service := range []string{APIService, ControllerService} {
		t.Run(service, func(t *testing.T) {
			preserveGlobals(t)
			t.Setenv("OTEL_SERVICE_NAME", "not-gitstore")
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=not-gitstore")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
			t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=private")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "Authorization=private")
			t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "Authorization=private")
			t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")
			t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1")
			t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
			t.Setenv("OTEL_METRICS_EXEMPLAR_FILTER", "trace_based")
			var mu sync.Mutex
			var spans tracepb.ExportTraceServiceRequest
			var logs logpb.ExportLogsServiceRequest
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				require.NoError(t, err)
				require.Empty(t, r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("Content-Encoding"), "SDK compression environment is not a GitStore setting")
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/otlp/v1/traces":
					var batch tracepb.ExportTraceServiceRequest
					require.NoError(t, proto.Unmarshal(body, &batch))
					spans.ResourceSpans = append(spans.ResourceSpans, batch.ResourceSpans...)
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
			logger, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{URI: collector.URL + "/otlp", TraceSampleRatio: 0.05}, service, "test-version", zap.New(core))
			require.NoError(t, err)
			t.Cleanup(func() { _ = shutdown(context.Background()) })
			remote := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true,
			})
			ctx, span := otel.Tracer("test").Start(trace.ContextWithRemoteSpanContext(t.Context(), remote), "bounded-span")
			require.True(t, span.IsRecording())
			registry := prometheus.NewRegistry()
			histogram := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "correlation_duration_seconds", Help: "test"})
			registry.MustRegister(histogram)
			Observe(ctx, histogram, 0.01)
			sdkHistogram, err := otel.Meter("correlation-test").Float64Histogram("correlation_otel_duration_seconds",
				metric.WithExplicitBucketBoundaries(0.01, 0.1, 1))
			require.NoError(t, err)
			sdkHistogram.Record(ctx, 0.01)
			span.SetAttributes(attribute.String("bounded", strings.Repeat("a", 512)))
			for i := range 50 {
				span.SetAttributes(attribute.String("field."+strconv.Itoa(i), "bounded"))
			}
			Logger(ctx, logger.With(zap.String("Authorization", "private"))).Info("linked log",
				zap.String("kind", "Product"), zap.String("operation", strings.Repeat("x", 512)),
				zap.String("body", "private"), zap.Error(errors.New("private")))
			logger.Debug("disabled debug")
			logger.Info(strings.Repeat("x", 4095) + "\u20ac" + strings.Repeat("y", 100))
			spanContext := span.SpanContext()
			span.End()
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
			response := httptest.NewRecorder()
			promhttp.HandlerFor(prometheus.Gatherers{registry, prometheus.DefaultGatherer},
				promhttp.HandlerOpts{EnableOpenMetrics: true}).ServeHTTP(response, request)
			require.Contains(t, response.Header().Get("Content-Type"), "application/openmetrics-text")
			require.Contains(t, response.Body.String(), `trace_id="`+spanContext.TraceID().String()+`"`)
			require.Contains(t, response.Body.String(), `span_id="`+spanContext.SpanID().String()+`"`)
			require.Contains(t, response.Body.String(), "# EOF")
			sdkExemplar := false
			for _, line := range strings.Split(response.Body.String(), "\n") {
				if strings.HasPrefix(line, "correlation_otel_duration_seconds_bucket{") &&
					strings.Contains(line, `span_id="`+spanContext.SpanID().String()+`"`) {
					require.Contains(t, line, `trace_id="`+spanContext.TraceID().String()+`"`)
					sdkExemplar = true
				}
			}
			require.True(t, sdkExemplar, "the shared SDK Prometheus reader must also retain context exemplars")
			require.NoError(t, shutdown(t.Context()))
			require.NotEmpty(t, local.FilterMessage("linked log").All(), "local logging is preserved")
			require.Empty(t, local.FilterMessage("disabled debug").All())
			mu.Lock()
			defer mu.Unlock()
			require.NotEmpty(t, spans.ResourceSpans)
			instance := stringAttribute(spans.ResourceSpans[0].Resource.Attributes, "service.instance.id")
			require.Len(t, instance, 32)
			hostname, err := os.Hostname()
			require.NoError(t, err)
			require.Contains(t, response.Body.String(), `service_instance_id="`+instance+`"`)
			require.Contains(t, response.Body.String(), `host_name="`+hostname+`"`)
			for _, resource := range spans.ResourceSpans {
				require.Equal(t, service, stringAttribute(resource.Resource.Attributes, "service.name"))
				require.Equal(t, instance, stringAttribute(resource.Resource.Attributes, "service.instance.id"))
				require.Equal(t, hostname, stringAttribute(resource.Resource.Attributes, "host.name"))
				for _, scope := range resource.ScopeSpans {
					for _, received := range scope.Spans {
						require.Equal(t, "bounded-span", received.Name)
						id, traceID := spanContext.SpanID(), spanContext.TraceID()
						require.Equal(t, id[:], received.SpanId)
						require.Equal(t, traceID[:], received.TraceId)
						require.Len(t, stringAttribute(received.Attributes, "bounded"), 256)
						require.LessOrEqual(t, len(received.Attributes), 32)
						require.Positive(t, received.DroppedAttributesCount)
					}
				}
			}
			linked := false
			require.NotEmpty(t, logs.ResourceLogs)
			for _, resource := range logs.ResourceLogs {
				require.Equal(t, service, stringAttribute(resource.Resource.Attributes, "service.name"))
				require.Equal(t, instance, stringAttribute(resource.Resource.Attributes, "service.instance.id"))
				require.Equal(t, hostname, stringAttribute(resource.Resource.Attributes, "host.name"))
				for _, scope := range resource.ScopeLogs {
					for _, record := range scope.LogRecords {
						require.LessOrEqual(t, len(record.Body.GetStringValue()), 4096)
						require.True(t, utf8.ValidString(record.Body.GetStringValue()))
						require.NotContains(t, record.String(), "private")
						require.NotEqual(t, "disabled debug", record.Body.GetStringValue())
						if record.Body.GetStringValue() == "linked log" {
							linked = true
							id, traceID := spanContext.SpanID(), spanContext.TraceID()
							require.Equal(t, id[:], record.SpanId)
							require.Equal(t, traceID[:], record.TraceId)
							require.Equal(t, "Product", stringAttribute(record.Attributes, "kind"))
							require.Len(t, stringAttribute(record.Attributes, "operation"), 256)
						}
					}
				}
			}
			require.True(t, linked)
		})
	}
}

func TestNativeExemplarsFollowParentSamplingAndPreserveSeries(t *testing.T) {
	for _, ratio := range []float64{0, 0.05} {
		for _, sampled := range []bool{false, true} {
			t.Run(strconv.FormatFloat(ratio, 'f', -1, 64)+"/"+strconv.FormatBool(sampled), func(t *testing.T) {
				provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))))
				defer provider.Shutdown(t.Context())
				flags := trace.TraceFlags(0)
				if sampled {
					flags = trace.FlagsSampled
				}
				remote := trace.NewSpanContext(trace.SpanContextConfig{
					TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags, Remote: true,
				})
				ctx, span := provider.Tracer("parent-test").Start(trace.ContextWithRemoteSpanContext(t.Context(), remote), "child")
				require.Equal(t, sampled, span.IsRecording())
				require.Equal(t, sampled, Sampled(ctx))
				histogram := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "sample_duration_seconds", Help: "test"}, []string{"kind"})
				registry := prometheus.NewRegistry()
				registry.MustRegister(histogram)
				Observe(ctx, histogram.WithLabelValues("Namespace"), 0.01)
				span.End()
				families, err := registry.Gather()
				require.NoError(t, err)
				require.Len(t, families, 1)
				series := families[0].Metric[0]
				require.Len(t, series.Label, 1)
				require.Equal(t, "kind", series.Label[0].GetName())
				require.EqualValues(t, 1, series.Histogram.GetSampleCount())
				exemplars := 0
				for _, bucket := range series.Histogram.Bucket {
					if exemplar := bucket.Exemplar; exemplar != nil {
						exemplars++
						require.Len(t, exemplar.Label, 2)
						values := map[string]string{}
						for _, label := range exemplar.Label {
							values[label.GetName()] = label.GetValue()
						}
						require.Equal(t, map[string]string{
							"trace_id": span.SpanContext().TraceID().String(), "span_id": span.SpanContext().SpanID().String(),
						}, values)
					}
				}
				require.Equal(t, sampled, exemplars == 1)
				request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
				request.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
				response := httptest.NewRecorder()
				handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
				handler.ServeHTTP(response, request)
				require.Equal(t, sampled, strings.Contains(response.Body.String(), `trace_id="`))
				if sampled {
					require.Contains(t, response.Body.String(), `span_id="`+span.SpanContext().SpanID().String()+`"`)
				}
				legacy := httptest.NewRecorder()
				handler.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				require.Contains(t, legacy.Header().Get("Content-Type"), "text/plain")
				require.NotContains(t, legacy.Body.String(), "trace_id")
				require.Contains(t, legacy.Body.String(), `sample_duration_seconds_count{kind="Namespace"} 1`)
			})
		}
	}
	for _, sc := range []trace.SpanContext{
		{},
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, TraceFlags: trace.FlagsSampled}),
		trace.NewSpanContext(trace.SpanContextConfig{SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled}),
	} {
		ctx := trace.ContextWithSpanContext(t.Context(), sc)
		require.False(t, Sampled(ctx))
		histogram := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "invalid_context_duration_seconds", Help: "test"})
		registry := prometheus.NewRegistry()
		registry.MustRegister(histogram)
		Observe(ctx, histogram, 0.01)
		families, err := registry.Gather()
		require.NoError(t, err)
		require.EqualValues(t, 1, families[0].Metric[0].Histogram.GetSampleCount())
		for _, bucket := range families[0].Metric[0].Histogram.Bucket {
			require.Nil(t, bucket.Exemplar)
		}
	}
	var observed float64
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
	}))
	Observe(ctx, prometheus.ObserverFunc(func(value float64) { observed += value }), 2)
	require.Equal(t, float64(2), observed, "non-exemplar observers must retain observations")
}

func TestLogBodiesAndExportFailuresAreAccounted(t *testing.T) {
	record := sdklog.Record{}
	record.SetBody(attribute.StringValue(strings.Repeat("x", 4095) + "\u20ac" + strings.Repeat("y", 100)))
	before := testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "truncated"))
	require.NoError(t, (telemetryObserver{signal: "logs"}).OnEmit(t.Context(), &record))
	require.Equal(t, 4095, len(record.Body().AsString()))
	require.True(t, utf8.ValidString(record.Body().AsString()))
	require.Equal(t, before+1, testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "truncated")))

	preserveGlobals(t)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "private server detail", http.StatusBadRequest)
	}))
	defer collector.Close()
	core, local := observer.New(zap.DebugLevel)
	logger, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{URI: collector.URL, TraceSampleRatio: 1}, APIService, "test", zap.New(core))
	require.NoError(t, err)
	spansBefore := testutil.ToFloat64(telemetryRecords.WithLabelValues("spans", "failed"))
	logsBefore := testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "failed"))
	_, span := otel.Tracer("test").Start(t.Context(), "failed-export")
	span.End()
	logger.Info("local record survives export failure")
	require.Error(t, shutdown(t.Context()))
	require.Greater(t, testutil.ToFloat64(telemetryRecords.WithLabelValues("spans", "failed")), spansBefore)
	require.Greater(t, testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "failed")), logsBefore)
	require.Equal(t, 1, local.FilterMessage("local record survives export failure").Len())
	for _, entry := range local.All() {
		require.NotContains(t, entry.ContextMap(), "private server detail")
	}
}

func TestMetricsCardinalityAndShutdownCancellation(t *testing.T) {
	preserveGlobals(t)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	_, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{URI: collector.URL}, ControllerService, "test", zap.NewNop())
	require.NoError(t, err)
	counter, err := otel.Meter("bounded-test").Int64Counter("observability_cardinality_test")
	require.NoError(t, err)
	for i := range 2200 {
		counter.Add(t.Context(), 1, metric.WithAttributes(attribute.String("test_index", strconv.Itoa(i))))
	}
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	found := false
	for _, family := range families {
		if family.GetName() == "observability_cardinality_test_total" {
			found = true
			require.LessOrEqual(t, len(family.Metric), 2049)
		}
	}
	require.True(t, found)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_ = shutdown(ctx)
	require.Less(t, time.Since(start), time.Second)
}

func TestLogQueueRemainsBoundedUnderSlowExport(t *testing.T) {
	preserveGlobals(t)
	release := make(chan struct{})
	var once sync.Once
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer func() {
		once.Do(func() { close(release) })
		collector.Close()
	}()
	logger, shutdown, err := InitTelemetry(t.Context(), OTLPConfig{URI: collector.URL, TraceSampleRatio: 1}, ControllerService, "test", discardLogger())
	require.NoError(t, err)
	before := testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "admitted"))
	exported := testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "exported"))
	spansBefore := testutil.ToFloat64(telemetryRecords.WithLabelValues("spans", "admitted"))
	spansExported := testutil.ToFloat64(telemetryRecords.WithLabelValues("spans", "exported"))
	const count = 10000
	for range count {
		logger.Info("bounded queue")
		_, span := otel.Tracer("bounded-queue").Start(t.Context(), "bounded-span")
		span.End()
	}
	require.Equal(t, float64(count), testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "admitted"))-before)
	require.Equal(t, float64(count), testutil.ToFloat64(telemetryRecords.WithLabelValues("spans", "admitted"))-spansBefore)
	once.Do(func() { close(release) })
	require.NoError(t, shutdown(t.Context()))
	delivered := testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "exported")) - exported
	require.Positive(t, delivered)
	require.LessOrEqual(t, delivered, float64(4096), "one bounded queue and in-flight batches, not an unbounded backlog")
	deliveredSpans := testutil.ToFloat64(telemetryRecords.WithLabelValues("spans", "exported")) - spansExported
	require.Positive(t, deliveredSpans)
	require.LessOrEqual(t, deliveredSpans, float64(4096))
}

func TestTransportParentPropagationAndCancellation(t *testing.T) {
	preserveGlobals(t)
	recorder := &spanCapture{}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(t.Context())
	otel.SetTracerProvider(provider)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("Traceparent"))
		require.Empty(t, r.Header.Get("Baggage"))
		http.Error(w, "private", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	ctx, parent := otel.Tracer("test").Start(t.Context(), "parent")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/private?token=private", nil)
	require.NoError(t, err)
	response, err := (&http.Client{Transport: Transport(nil)}).Do(request)
	require.NoError(t, err)
	response.Body.Close()
	parent.End()
	require.Len(t, recorder.spans, 2)
	child := recorder.spans[0]
	require.Equal(t, parent.SpanContext().SpanID(), child.Parent().SpanID())
	require.Equal(t, trace.SpanKindClient, child.SpanKind())
	for _, attr := range child.Attributes() {
		require.NotContains(t, attr.Value.AsString(), "private")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	request = request.WithContext(cancelled)
	_, err = (&http.Client{Transport: Transport(nil)}).Do(request)
	require.ErrorIs(t, err, context.Canceled)
}

type spanCapture struct{ spans []sdktrace.ReadOnlySpan }

func (*spanCapture) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (s *spanCapture) OnEnd(span sdktrace.ReadOnlySpan)              { s.spans = append(s.spans, span) }
func (*spanCapture) Shutdown(context.Context) error                  { return nil }
func (*spanCapture) ForceFlush(context.Context) error                { return nil }

func TestHTTPHandlerPreservesWriterAndEndsFiniteSpansOnCancellationAndPanic(t *testing.T) {
	preserveGlobals(t)
	for _, mode := range []string{"completed", "cancelled", "panic", "watch"} {
		t.Run(mode, func(t *testing.T) {
			capture := &spanCapture{}
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(capture))
			defer provider.Shutdown(t.Context())
			otel.SetTracerProvider(provider)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			response := httptest.NewRecorder()
			handler := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Same(t, response, w)
				if mode == "cancelled" {
					cancel()
				}
				if mode == "panic" {
					panic("private")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			method := http.MethodPost
			if mode == "watch" {
				method = http.MethodGet
			}
			run := func() {
				handler.ServeHTTP(response, httptest.NewRequest(method, "/graphql", nil).WithContext(ctx))
			}
			if mode == "panic" {
				require.Panics(t, run)
			} else {
				run()
			}
			if mode == "watch" {
				require.Empty(t, capture.spans)
				return
			}
			require.Len(t, capture.spans, 1)
			if mode == "cancelled" || mode == "panic" {
				require.Equal(t, codes.Error, capture.spans[0].Status().Code)
				require.Equal(t, mode, capture.spans[0].Status().Description)
			}
			require.Empty(t, capture.spans[0].Events())
		})
	}
}

var _ zapcore.Core = exportCore{}
