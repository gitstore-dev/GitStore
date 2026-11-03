// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func TestNativeMetricsPreserveCumulativeHistogramsAndRejectIdentifiers(t *testing.T) {
	registry := prometheus.NewRegistry()
	histogram := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "duration_seconds", Help: "test", Buckets: []float64{1, 2}})
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "requests_total", Help: "test"}, []string{"result"})
	private := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "private", Help: "test"}, []string{"repository"})
	registry.MustRegister(histogram, counter, private)
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	}))
	Observe(ctx, histogram, 0.5)
	histogram.Observe(3)
	counter.WithLabelValues("ok").Add(4)
	private.WithLabelValues("sensitive-user-resource").Set(1)
	metrics, err := nativeMetrics(registry, 1, 2)
	require.NoError(t, err)
	require.Len(t, metrics, 2)
	require.Equal(t, []uint64{1, 0, 1}, metrics[0].GetHistogram().DataPoints[0].BucketCounts)
	require.Equal(t, []float64{1, 2}, metrics[0].GetHistogram().DataPoints[0].ExplicitBounds)
	require.Equal(t, float64(3.5), metrics[0].GetHistogram().DataPoints[0].GetSum())
	require.Len(t, metrics[0].GetHistogram().DataPoints[0].Exemplars, 1)
	require.Len(t, metrics[0].GetHistogram().DataPoints[0].Exemplars[0].TraceId, 16)
	require.Equal(t, float64(4), metrics[1].GetSum().DataPoints[0].GetAsDouble())
}

func TestMetricExportFlushesOnShutdownAndReportsRejection(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/otlp/v1/metrics", r.URL.Path)
				require.Empty(t, r.Header.Get("Authorization"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				var batch collectorpb.ExportMetricsServiceRequest
				require.NoError(t, proto.Unmarshal(body, &batch))
				require.Equal(t, "gitstore-api", batch.ResourceMetrics[0].Resource.Attributes[0].Value.GetStringValue())
				require.NotEmpty(t, batch.ResourceMetrics[0].ScopeMetrics[0].Metrics)
				requests++
				w.WriteHeader(status)
			}))
			defer collector.Close()
			stop := startMetricExport(collector.Client(), collector.URL+"/otlp", &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{metricStringAttribute("service.name", APIService)},
			})
			err := stop(context.Background())
			if status == 200 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, stop(context.Background()), "shutdown must not repeat exports")
			require.Equal(t, 1, requests)
		})
	}
}
