// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var telemetryRecords = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gitstore_telemetry_records_total",
	Help: "Telemetry records admitted, exported or failed; admitted minus exported includes live queued work and loss.",
}, []string{"signal", "stage"})

func init() { prometheus.MustRegister(telemetryRecords) }

var telemetryInstanceID = sync.OnceValues(func() (string, error) {
	var id [16]byte
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		return "", fmt.Errorf("generate telemetry process identity: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
})

type telemetryObserver struct {
	signal string
}

func (o telemetryObserver) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (o telemetryObserver) OnEnd(sdktrace.ReadOnlySpan) {
	telemetryRecords.WithLabelValues(o.signal, "admitted").Inc()
}
func (o telemetryObserver) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (o telemetryObserver) OnEmit(_ context.Context, record *sdklog.Record) error {
	telemetryRecords.WithLabelValues(o.signal, "admitted").Inc()
	body := record.Body()
	if body.Type() == attribute.STRING && len(body.AsString()) > 4096 {
		value, n := body.AsString(), 4096
		for n > 0 && !utf8.RuneStart(value[n]) {
			n--
		}
		record.SetBody(attribute.StringValue(value[:n]))
		telemetryRecords.WithLabelValues(o.signal, "truncated").Inc()
	}
	return nil
}
func (telemetryObserver) Shutdown(context.Context) error   { return nil }
func (telemetryObserver) ForceFlush(context.Context) error { return nil }

type telemetryTraceExporter struct{ sdktrace.SpanExporter }

func (e telemetryTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	stage := "exported"
	if err != nil {
		stage = "failed"
	}
	telemetryRecords.WithLabelValues("spans", stage).Add(float64(len(spans)))
	return err
}

type telemetryLogExporter struct{ sdklog.Exporter }

func (e telemetryLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	err := e.Exporter.Export(ctx, records)
	stage := "exported"
	if err != nil {
		stage = "failed"
	}
	telemetryRecords.WithLabelValues("logs", stage).Add(float64(len(records)))
	return err
}

type metricRegisterer struct {
	prometheus.Registerer
	collector prometheus.Collector
}

func (r *metricRegisterer) Register(c prometheus.Collector) error {
	if err := r.Registerer.Register(c); err != nil {
		return err
	}
	r.collector = c
	return nil
}

// InitTelemetry enables bounded OTLP export only when an endpoint is configured.
// Prometheus remains the native metrics surface; baggage and message bodies are
// deliberately excluded from trace propagation.
func InitTelemetry(ctx context.Context, cfg OTLPConfig, service, version string, logger *zap.Logger) (*zap.Logger, func(context.Context) error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	if logger == nil {
		return nil, nil, errors.New("telemetry logger is required")
	}
	if service != APIService && service != ControllerService {
		return nil, nil, errors.New("unsupported telemetry service identity")
	}
	endpoint := cfg.URI
	if endpoint == "" {
		return logger, func(context.Context) error { return nil }, nil
	}
	ratio := cfg.TraceSampleRatio
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve telemetry hostname: %w", err)
	}
	instance, err := telemetryInstanceID()
	if err != nil {
		return nil, nil, err
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", service),
		attribute.String("service.instance.id", instance),
		attribute.String("service.version", version),
		attribute.String("host.name", hostname),
	)
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 2 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{}),
		otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithTimeout(2*time.Second),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, fmt.Errorf("initialize trace exporter: %w", err)
	}
	logExporter, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/logs"),
		otlploghttp.WithHeaders(map[string]string{}),
		otlploghttp.WithHTTPClient(client),
		otlploghttp.WithCompression(otlploghttp.NoCompression),
		otlploghttp.WithTimeout(2*time.Second),
	)
	if err != nil {
		transport.CloseIdleConnections()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return nil, nil, errors.Join(fmt.Errorf("initialize log exporter: %w", err), traceExporter.Shutdown(cleanupCtx))
	}
	traces := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(telemetryObserver{signal: "spans"}),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit: 256, AttributeCountLimit: 32,
			EventCountLimit: 32, LinkCountLimit: 8,
			AttributePerEventCountLimit: 8, AttributePerLinkCountLimit: 8,
		}),
		sdktrace.WithBatcher(telemetryTraceExporter{traceExporter},
			sdktrace.WithMaxQueueSize(2048), sdktrace.WithMaxExportBatchSize(256),
			sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(3*time.Second)),
	)
	logs := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithAttributeCountLimit(32),
		sdklog.WithAttributeValueLengthLimit(256),
		sdklog.WithProcessor(telemetryObserver{signal: "logs"}),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(telemetryLogExporter{logExporter},
			sdklog.WithMaxQueueSize(2048), sdklog.WithExportMaxBatchSize(256),
			sdklog.WithExportInterval(time.Second), sdklog.WithExportTimeout(3*time.Second))),
	)
	registerer := &metricRegisterer{Registerer: prometheus.DefaultRegisterer}
	metricReader, err := otelprom.New(otelprom.WithRegisterer(registerer))
	if err != nil {
		transport.CloseIdleConnections()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return nil, nil, errors.Join(fmt.Errorf("initialize telemetry metrics: %w", err),
			traces.Shutdown(cleanupCtx), logs.Shutdown(cleanupCtx))
	}
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(metricReader), sdkmetric.WithCardinalityLimit(2048))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		// Exporter errors can contain endpoints or server responses.
		logger.Error("telemetry export failed")
	}))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(traces)
	otel.SetLoggerProvider(logs)
	otel.SetMeterProvider(metrics)
	enhanced := logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, exportCore{
			Core:  otelzap.NewCore(service, otelzap.WithLoggerProvider(logs)),
			level: zap.LevelEnablerFunc(core.Enabled),
		})
	}))
	enhanced.Info("local telemetry enabled", zap.Float64("trace_sampling_ratio", ratio))
	return enhanced, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err := errors.Join(traces.Shutdown(ctx), logs.Shutdown(ctx), metrics.Shutdown(ctx))
		transport.CloseIdleConnections()
		if registerer.collector != nil {
			registerer.Unregister(registerer.collector)
		}
		return err
	}, nil
}

// Export only operational fields. Local Zap logs retain their original fields
// and redaction; OTLP must not acquire resource bodies, errors or credentials.
type exportCore struct {
	zapcore.Core
	level zapcore.LevelEnabler
}

func (c exportCore) Enabled(level zapcore.Level) bool {
	return c.level.Enabled(level) && c.Core.Enabled(level)
}

func exportFields(fields []zapcore.Field) []zapcore.Field {
	result := make([]zapcore.Field, 0, min(len(fields), 32))
	for _, field := range fields {
		if len(result) == 32 {
			break
		}
		if _, ok := field.Interface.(context.Context); ok {
			result = append(result, field)
			continue
		}
		switch field.Key {
		case "kind", "result", "stage", "phase", "operation", "method", "code",
			"duration", "elapsed", "attempt", "attempts", "count", "trace_sampling_ratio",
			"status", "authentication_ms", "resolution_ms", "authorization_ms",
			"push_context_ms", "handler_ms", "total_ms":
			if field.Interface == nil {
				result = append(result, field)
			}
		}
	}
	return result
}

func (c exportCore) With(fields []zapcore.Field) zapcore.Core {
	return exportCore{Core: c.Core.With(exportFields(fields)), level: c.level}
}

func (c exportCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c exportCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	return c.Core.Write(entry, exportFields(fields))
}
