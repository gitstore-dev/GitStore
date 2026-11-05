// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

const maxMetricSeries = 2048

type metricHTTPState struct{ gatherer prometheus.Gatherer }

var metricHTTP atomic.Pointer[metricHTTPState]
var emptyMetrics = prometheus.NewRegistry()

// MetricsHandler serves SDK collection, including SDK-native runtime signals.
// GitStore instruments never record into Prometheus collectors.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meter := otel.Meter("gitstore/metrics-http")
		requests := MustInstrument(meter.Int64Counter("promhttp_metric_handler_requests_total", metric.WithDescription("Metric scrape requests by bounded HTTP status.")))
		inflight := MustInstrument(meter.Int64UpDownCounter("promhttp_metric_handler_requests_in_flight", metric.WithDescription("Active metric scrape requests.")))
		inflight.Add(r.Context(), 1)
		defer inflight.Add(r.Context(), -1)
		for _, code := range []string{"200", "500", "503"} {
			requests.Add(r.Context(), 0, metric.WithAttributes(attribute.String("code", code)))
		}
		gatherer := prometheus.Gatherer(emptyMetrics)
		if state := metricHTTP.Load(); state != nil {
			gatherer = state.gatherer
		}
		response := &metricResponseWriter{ResponseWriter: w}
		promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{EnableOpenMetrics: true}).ServeHTTP(response, r)
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		code := "other"
		if status >= 100 && status <= 599 {
			code = strconv.Itoa(status)
		}
		requests.Add(r.Context(), 1, metric.WithAttributes(attribute.String("code", code)))
	})
}

type metricResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *metricResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *metricResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (w *metricResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// MustInstrument validates constant declarations without wrapping recording or
// retaining a parallel measurement store.
func MustInstrument[T any](instrument T, err error) T {
	if err != nil {
		panic(err)
	}
	return instrument
}

func recordTelemetry(signal, stage string, count float64) {
	counter := MustInstrument(otel.Meter("gitstore/telemetry").Float64Counter(
		"gitstore_telemetry_records_total", metric.WithDescription(
			"Telemetry records admitted, exported or failed; admitted minus exported includes live queued work and loss.")))
	counter.Add(context.Background(), count, metric.WithAttributes(
		attribute.String("signal", signal), attribute.String("stage", stage)))
}

type telemetryMetricExporter struct{ sdkmetric.Exporter }

func (e telemetryMetricExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	err := e.Exporter.Export(ctx, data)
	stage := "exported"
	if err != nil {
		stage = "failed"
	}
	recordTelemetry("metrics", stage, 1)
	return err
}

func newMetricProvider(ctx context.Context, endpoint string, client *http.Client, res *resource.Resource) (*sdkmetric.MeterProvider, *prometheus.Registry, error) {
	registry := prometheus.NewRegistry()
	reader, err := otelprom.New(otelprom.WithRegisterer(registry), otelprom.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithoutSuffixes), otelprom.WithoutScopeInfo())
	if err != nil {
		return nil, nil, err
	}
	opts := []sdkmetric.Option{sdkmetric.WithResource(res), sdkmetric.WithReader(reader), sdkmetric.WithCardinalityLimit(maxMetricSeries)}
	if endpoint != "" {
		exporter, err := otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/metrics"),
			otlpmetrichttp.WithHTTPClient(client), otlpmetrichttp.WithHeaders(map[string]string{}),
			otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
			otlpmetrichttp.WithTimeout(2*time.Second),
			otlpmetrichttp.WithMaxRequestSize(2<<20), otlpmetrichttp.WithMaxResponseSize(64<<10),
			otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}),
			otlpmetrichttp.WithTemporalitySelector(func(sdkmetric.InstrumentKind) metricdata.Temporality { return metricdata.CumulativeTemporality }),
		)
		if err != nil {
			_ = reader.Shutdown(ctx)
			return nil, nil, err
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(telemetryMetricExporter{exporter},
			sdkmetric.WithInterval(10*time.Second), sdkmetric.WithTimeout(2*time.Second))))
	}
	provider := sdkmetric.NewMeterProvider(opts...)
	if err := registerRuntimeMetrics(provider); err != nil {
		_ = provider.Shutdown(ctx)
		return nil, nil, err
	}
	return provider, registry, nil
}
