// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func telemetryValue(t *testing.T, signal, stage string) float64 {
	t.Helper()
	state := metricHTTP.Load()
	require.NotNil(t, state)
	families, err := state.gatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "gitstore_telemetry_records_total" {
			continue
		}
		for _, point := range family.Metric {
			labels := map[string]string{}
			for _, label := range point.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["signal"] == signal && labels["stage"] == stage {
				return point.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func flushTelemetrySignals(ctx context.Context) {
	type flusher interface{ ForceFlush(context.Context) error }
	if provider, ok := otel.GetTracerProvider().(flusher); ok {
		_ = provider.ForceFlush(ctx)
	}
	if provider, ok := otel.GetLoggerProvider().(flusher); ok {
		_ = provider.ForceFlush(ctx)
	}
}

func TestSDKMetricExportPreservesCumulativeSignalsScopesAndBoundedAttributes(t *testing.T) {
	batches := make(chan *collectorpb.ExportMetricsServiceRequest, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/otlp/v1/metrics", r.URL.Path)
		require.Empty(t, r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		batch := &collectorpb.ExportMetricsServiceRequest{}
		require.NoError(t, proto.Unmarshal(body, batch))
		batches <- batch
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	res := resource.NewSchemaless(attribute.String("service.name", APIService), attribute.String("service.instance.id", "process-one"))
	provider, registry, err := newMetricProvider(t.Context(), collector.URL+"/otlp", collector.Client(), res)
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	meter := provider.Meter("gitstore/sdk-contract", metric.WithInstrumentationVersion("v1"))
	counter := MustInstrument(meter.Float64Counter("requests_total"))
	reloads := MustInstrument(meter.Float64Counter("gitstore_authz_registry_reloads_total"))
	reloads.Add(t.Context(), 2, metric.WithAttributes(attribute.String("outcome", "accepted")))
	reloads.Add(t.Context(), 1, metric.WithAttributes(attribute.String("outcome", "rejected")))
	histogram := MustInstrument(meter.Float64Histogram("duration_seconds", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(1, 2)))
	gauge := MustInstrument(meter.Float64Gauge("current_workers"))
	attrs := metric.WithAttributes(attribute.String("endpoint", "admin"), attribute.String("reason", "http"), attribute.String("provider", "file"), attribute.String("consumer", "controller-manager"))
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled}))
	counter.Add(ctx, 4, attrs)
	histogram.Record(ctx, .5, attrs)
	histogram.Record(t.Context(), 3, attrs)
	gauge.Record(t.Context(), 7)
	for range 2 {
		_, err := registry.Gather()
		require.NoError(t, err)
		require.NoError(t, provider.ForceFlush(t.Context()))
		batch := <-batches
		require.Len(t, batch.ResourceMetrics, 1)
		require.Equal(t, "process-one", stringAttribute(batch.ResourceMetrics[0].Resource.Attributes, "service.instance.id"))
		found := map[string]*metricpb.Metric{}
		for _, scope := range batch.ResourceMetrics[0].ScopeMetrics {
			if scope.Scope.Name != "gitstore/sdk-contract" {
				continue
			}
			require.Equal(t, "v1", scope.Scope.Version)
			for _, signal := range scope.Metrics {
				found[signal.Name] = signal
			}
		}
		require.Len(t, found, 4)
		reloaded := found["gitstore_authz_registry_reloads_total"].GetSum()
		require.True(t, reloaded.IsMonotonic)
		require.Equal(t, metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, reloaded.AggregationTemporality)
		require.Len(t, reloaded.DataPoints, 2)
		for _, point := range reloaded.DataPoints {
			require.Len(t, point.Attributes, 1)
			require.Equal(t, "outcome", point.Attributes[0].Key)
			outcome := point.Attributes[0].Value.GetStringValue()
			expected := map[string]float64{"accepted": 2, "rejected": 1}
			require.Contains(t, expected, outcome)
			require.Equal(t, expected[outcome], point.GetAsDouble())
		}
		require.Equal(t, float64(4), found["requests_total"].GetSum().DataPoints[0].GetAsDouble())
		require.True(t, found["requests_total"].GetSum().IsMonotonic)
		require.Equal(t, metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, found["requests_total"].GetSum().AggregationTemporality)
		h := found["duration_seconds"].GetHistogram().DataPoints[0]
		require.EqualValues(t, 2, h.Count)
		require.Equal(t, 3.5, h.GetSum())
		require.Equal(t, []float64{1, 2}, h.ExplicitBounds)
		require.Equal(t, []uint64{1, 0, 1}, h.BucketCounts)
		require.Equal(t, "admin", stringAttribute(h.Attributes, "endpoint"))
		require.Equal(t, "file", stringAttribute(h.Attributes, "provider"))
		require.Equal(t, "controller-manager", stringAttribute(h.Attributes, "consumer"))
		require.Equal(t, float64(7), found["current_workers"].GetGauge().DataPoints[0].GetAsDouble(), "independent collections retain the latest gauge without double recording")
	}
}

func TestSDKRuntimeAndLibraryCollectionExcludesTheDefaultRegistry(t *testing.T) {
	private := prometheus.NewGauge(prometheus.GaugeOpts{Name: "private_default_registry_probe", Help: "must not be bridged"})
	prometheus.MustRegister(private)
	defer prometheus.Unregister(private)
	private.Set(1)
	provider, registry, err := newMetricProvider(t.Context(), "", nil, resource.Empty())
	require.NoError(t, err)
	defer provider.Shutdown(t.Context())
	counter := MustInstrument(provider.Meter("gitstore/test").Float64Counter("gitstore_sdk_owned_total"))
	counter.Add(t.Context(), 3)
	for range 2 {
		families, err := registry.Gather()
		require.NoError(t, err)
		counts := map[string]int{}
		for _, family := range families {
			counts[family.GetName()]++
			require.NotEqual(t, "private_default_registry_probe", family.GetName())
		}
		require.Equal(t, 1, counts["gitstore_sdk_owned_total"])
		require.Equal(t, 1, counts["go_goroutines"])
		require.Equal(t, 1, counts["process_cpu_seconds_total"])
	}
}

func TestSDKRuntimePreservesLegacySignalNames(t *testing.T) {
	legacy := prometheus.NewRegistry()
	legacy.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	old, err := legacy.Gather()
	require.NoError(t, err)
	provider, registry, err := newMetricProvider(t.Context(), "", nil, resource.Empty())
	require.NoError(t, err)
	defer provider.Shutdown(t.Context())
	current, err := registry.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, family := range current {
		names[family.GetName()] = true
	}
	for _, family := range old {
		require.True(t, names[family.GetName()], "missing runtime signal %s", family.GetName())
	}
	require.True(t, names["go_gc_duration_seconds_count"])
	require.True(t, names["go_gc_duration_seconds_sum"])
}

func TestSDKGCQuantileGaugesPreserveCanonicalBackendLabels(t *testing.T) {
	provider, registry, err := newMetricProvider(t.Context(), "", nil, resource.Empty())
	require.NoError(t, err)
	defer provider.Shutdown(t.Context())
	families, err := registry.Gather()
	require.NoError(t, err)
	var quantiles []string
	for _, family := range families {
		if family.GetName() != "go_gc_duration_seconds" {
			continue
		}
		for _, point := range family.Metric {
			require.NotNil(t, point.Gauge)
			for _, label := range point.Label {
				if label.GetName() == "quantile" {
					quantiles = append(quantiles, label.GetValue())
				}
			}
		}
	}
	require.ElementsMatch(t, []string{"0.0", "0.25", "0.5", "0.75", "1.0"}, quantiles,
		"gauge labels must preserve existing PromQL selectors after summary normalization no longer applies")
}

func TestSDKMetricsRejectCollectorFailuresAndBoundResponses(t *testing.T) {
	for _, body := range []string{"reject", "partial", strings.Repeat("x", 65537)} {
		t.Run(body[:6], func(t *testing.T) {
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if body == "reject" {
					w.WriteHeader(http.StatusBadRequest)
				}
				if body == "partial" {
					w.Header().Set("Content-Type", "application/x-protobuf")
					reply, _ := proto.Marshal(&collectorpb.ExportMetricsServiceResponse{PartialSuccess: &collectorpb.ExportMetricsPartialSuccess{RejectedDataPoints: 1, ErrorMessage: "private collector detail"}})
					_, _ = w.Write(reply)
					return
				}
				_, _ = io.WriteString(w, body)
			}))
			defer collector.Close()
			provider, _, err := newMetricProvider(t.Context(), collector.URL, collector.Client(), resource.Empty())
			require.NoError(t, err)
			counter := MustInstrument(provider.Meter("gitstore/test").Int64Counter("failure_probe"))
			counter.Add(t.Context(), 1)
			start := time.Now()
			require.Error(t, provider.ForceFlush(t.Context()))
			require.Less(t, time.Since(start), 3*time.Second)
			_ = provider.Shutdown(t.Context())
		})
	}
}

func TestSDKMetricCardinalityOverflowRetainsTotals(t *testing.T) {
	provider, registry, err := newMetricProvider(t.Context(), "", nil, resource.Empty())
	require.NoError(t, err)
	defer provider.Shutdown(t.Context())
	counter := MustInstrument(provider.Meter("gitstore/test").Int64Counter("cardinality_probe_total"))
	for i := range maxMetricSeries + 100 {
		counter.Add(t.Context(), 1, metric.WithAttributes(attribute.String("reason", strconv.Itoa(i))))
	}
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "cardinality_probe_total" {
			continue
		}
		require.LessOrEqual(t, len(family.Metric), maxMetricSeries)
		var total float64
		for _, point := range family.Metric {
			total += point.GetCounter().GetValue()
		}
		require.EqualValues(t, maxMetricSeries+100, total)
		return
	}
	t.Fatal("missing counter")
}

func TestSDKMetricTimeoutKeepsRecordingAndNextCumulativeSnapshot(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseCollector := sync.OnceFunc(func() { close(release) })
	batches := make(chan *collectorpb.ExportMetricsServiceRequest, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		batch := &collectorpb.ExportMetricsServiceRequest{}
		_ = proto.Unmarshal(body, batch)
		batches <- batch
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	defer releaseCollector()
	provider, _, err := newMetricProvider(t.Context(), collector.URL, collector.Client(), resource.Empty())
	require.NoError(t, err)
	defer provider.Shutdown(t.Context())
	counter := MustInstrument(provider.Meter("gitstore/test").Int64Counter("timeout_probe_total"))
	counter.Add(t.Context(), 1)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	flushed := make(chan error, 1)
	go func() { flushed <- provider.ForceFlush(ctx) }()
	select {
	case <-started:
	case err := <-flushed:
		t.Fatalf("export did not reach collector: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("export did not start")
	}
	begin := time.Now()
	for range 10000 {
		counter.Add(t.Context(), 1)
	}
	require.Less(t, time.Since(begin), time.Second, "recording must not wait for exporter HTTP")
	err = <-flushed
	releaseCollector()
	require.Error(t, err)
	<-batches
	require.NoError(t, provider.ForceFlush(t.Context()))
	batch := <-batches
	for _, resource := range batch.ResourceMetrics {
		for _, scope := range resource.ScopeMetrics {
			for _, signal := range scope.Metrics {
				if signal.Name == "timeout_probe_total" {
					require.EqualValues(t, 10001, signal.GetSum().DataPoints[0].GetAsInt())
					return
				}
			}
		}
	}
	t.Fatal("missing recovered cumulative counter")
}
