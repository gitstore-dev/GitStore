// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/config"
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

// InitTelemetry enables bounded OTLP export only when an endpoint is configured.
// Prometheus remains the native metrics surface; baggage and message bodies are
// deliberately excluded from trace propagation.
func InitTelemetry(ctx context.Context, cfg config.OTLPConfig, logger *zap.Logger) (*zap.Logger, func(context.Context) error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		return logger, func(context.Context) error { return nil }, nil
	}
	ratio := cfg.TraceSampleRatio
	const service = "gitstore-api"
	instance, err := os.Hostname()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve telemetry process identity: %w", err)
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", service),
		attribute.String("service.instance.id", instance),
		attribute.String("service.version", version),
	)
	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/traces"),
		otlptracehttp.WithTimeout(2*time.Second),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize trace exporter: %w", err)
	}
	logExporter, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/logs"),
		otlploghttp.WithTimeout(2*time.Second),
	)
	if err != nil {
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
	metricReader, err := otelprom.New()
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return nil, nil, errors.Join(fmt.Errorf("initialize telemetry metrics: %w", err),
			traces.Shutdown(cleanupCtx), logs.Shutdown(cleanupCtx))
	}
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(metricReader), sdkmetric.WithCardinalityLimit(2048))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Error("telemetry export failed", zap.Error(err))
	}))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(traces)
	otel.SetLoggerProvider(logs)
	otel.SetMeterProvider(metrics)
	enhanced := logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, otelzap.NewCore(service, otelzap.WithLoggerProvider(logs)))
	}))
	enhanced.Info("local telemetry enabled", zap.Float64("trace_sampling_ratio", ratio))
	return enhanced, func(ctx context.Context) error {
		return errors.Join(traces.Shutdown(ctx), logs.Shutdown(ctx), metrics.Shutdown(ctx))
	}, nil
}
