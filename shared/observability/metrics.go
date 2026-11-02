// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

const maxMetricSeries = 2048

func metricExemplar(exemplar *dto.Exemplar) *metricpb.Exemplar {
	if exemplar == nil || exemplar.Timestamp == nil {
		return nil
	}
	result := &metricpb.Exemplar{
		TimeUnixNano: uint64(exemplar.Timestamp.AsTime().UnixNano()),
		Value:        &metricpb.Exemplar_AsDouble{AsDouble: exemplar.GetValue()},
	}
	for _, label := range exemplar.Label {
		switch label.GetName() {
		case "trace_id":
			result.TraceId, _ = hex.DecodeString(label.GetValue())
		case "span_id":
			result.SpanId, _ = hex.DecodeString(label.GetValue())
		}
	}
	if len(result.TraceId) != 16 || len(result.SpanId) != 8 {
		return nil
	}
	return result
}

// Keep native instrumentation and the SDK reader on one cumulative export path.
// No request, subject, repository or resource identifiers become metric labels.
func metricAttributes(labels []*dto.LabelPair) ([]*commonpb.KeyValue, bool) {
	attrs := make([]*commonpb.KeyValue, 0, len(labels))
	for _, label := range labels {
		switch label.GetName() {
		case "kind", "result", "stage", "phase", "operation", "backend", "resource_kind",
			"projection", "finding_type", "code", "reason", "outcome", "path",
			"grpc_service", "grpc_method", "grpc_type", "grpc_code", "signal",
			"version", "goversion", "otel_scope_name", "otel_scope_version", "otel_scope_schema_url":
			if len(label.GetValue()) > 128 {
				return nil, false
			}
			attrs = append(attrs, metricStringAttribute(label.GetName(), label.GetValue()))
		default:
			return nil, false
		}
	}
	return attrs, true
}

func metricStringAttribute(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func nativeMetrics(gatherer prometheus.Gatherer, started, now uint64) ([]*metricpb.Metric, error) {
	families, err := gatherer.Gather()
	if err != nil {
		return nil, err
	}
	var result []*metricpb.Metric
	series := 0
	for _, family := range families {
		metric := &metricpb.Metric{Name: family.GetName(), Description: family.GetHelp()}
		var numbers []*metricpb.NumberDataPoint
		var histograms []*metricpb.HistogramDataPoint
		var summaries []*metricpb.SummaryDataPoint
		for _, sample := range family.Metric {
			attrs, safe := metricAttributes(sample.Label)
			if !safe || series >= maxMetricSeries {
				telemetryRecords.WithLabelValues("metrics", "dropped").Inc()
				continue
			}
			series++
			number := &metricpb.NumberDataPoint{Attributes: attrs, StartTimeUnixNano: started, TimeUnixNano: now}
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				number.Value = &metricpb.NumberDataPoint_AsDouble{AsDouble: sample.GetCounter().GetValue()}
				if exemplar := metricExemplar(sample.GetCounter().Exemplar); exemplar != nil {
					number.Exemplars = append(number.Exemplars, exemplar)
				}
				numbers = append(numbers, number)
			case dto.MetricType_GAUGE, dto.MetricType_UNTYPED:
				value := sample.GetGauge().GetValue()
				if family.GetType() == dto.MetricType_UNTYPED {
					value = sample.GetUntyped().GetValue()
				}
				number.Value = &metricpb.NumberDataPoint_AsDouble{AsDouble: value}
				numbers = append(numbers, number)
			case dto.MetricType_HISTOGRAM:
				h := sample.GetHistogram()
				point := &metricpb.HistogramDataPoint{Attributes: attrs, StartTimeUnixNano: started, TimeUnixNano: now, Count: h.GetSampleCount(), Sum: h.SampleSum}
				var previous uint64
				for _, bucket := range h.Bucket {
					if exemplar := metricExemplar(bucket.Exemplar); exemplar != nil {
						point.Exemplars = append(point.Exemplars, exemplar)
					}
					if math.IsInf(bucket.GetUpperBound(), 1) {
						continue
					}
					point.ExplicitBounds = append(point.ExplicitBounds, bucket.GetUpperBound())
					point.BucketCounts = append(point.BucketCounts, bucket.GetCumulativeCount()-previous)
					previous = bucket.GetCumulativeCount()
				}
				point.BucketCounts = append(point.BucketCounts, h.GetSampleCount()-previous)
				histograms = append(histograms, point)
			case dto.MetricType_SUMMARY:
				s := sample.GetSummary()
				point := &metricpb.SummaryDataPoint{Attributes: attrs, StartTimeUnixNano: started, TimeUnixNano: now, Count: s.GetSampleCount(), Sum: s.GetSampleSum()}
				for _, quantile := range s.Quantile {
					point.QuantileValues = append(point.QuantileValues, &metricpb.SummaryDataPoint_ValueAtQuantile{Quantile: quantile.GetQuantile(), Value: quantile.GetValue()})
				}
				summaries = append(summaries, point)
			}
		}
		switch family.GetType() {
		case dto.MetricType_COUNTER:
			metric.Data = &metricpb.Metric_Sum{Sum: &metricpb.Sum{DataPoints: numbers, AggregationTemporality: metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, IsMonotonic: true}}
		case dto.MetricType_GAUGE, dto.MetricType_UNTYPED:
			metric.Data = &metricpb.Metric_Gauge{Gauge: &metricpb.Gauge{DataPoints: numbers}}
		case dto.MetricType_HISTOGRAM:
			metric.Data = &metricpb.Metric_Histogram{Histogram: &metricpb.Histogram{DataPoints: histograms, AggregationTemporality: metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE}}
		case dto.MetricType_SUMMARY:
			metric.Data = &metricpb.Metric_Summary{Summary: &metricpb.Summary{DataPoints: summaries}}
		default:
			continue
		}
		if len(numbers)+len(histograms)+len(summaries) > 0 {
			result = append(result, metric)
		}
	}
	return result, nil
}

// Restore SDK scope metadata before the collector adds its reserved scope labels.
func metricScopes(metrics []*metricpb.Metric) []*metricpb.ScopeMetrics {
	type group struct {
		scope   *metricpb.ScopeMetrics
		metrics map[*metricpb.Metric]*metricpb.Metric
	}
	groups := make(map[[3]string]*group)
	var scopes []*metricpb.ScopeMetrics
	targetFor := func(source *metricpb.Metric, attributes []*commonpb.KeyValue) (*metricpb.Metric, []*commonpb.KeyValue) {
		key := [3]string{"gitstore/native", "", ""}
		remaining := attributes[:0]
		for _, attribute := range attributes {
			switch attribute.Key {
			case "otel_scope_name":
				key[0] = attribute.Value.GetStringValue()
			case "otel_scope_version":
				key[1] = attribute.Value.GetStringValue()
			case "otel_scope_schema_url":
				key[2] = attribute.Value.GetStringValue()
			default:
				remaining = append(remaining, attribute)
			}
		}
		current := groups[key]
		if current == nil {
			current = &group{
				scope: &metricpb.ScopeMetrics{
					Scope:     &commonpb.InstrumentationScope{Name: key[0], Version: key[1]},
					SchemaUrl: key[2],
				},
				metrics: make(map[*metricpb.Metric]*metricpb.Metric),
			}
			groups[key] = current
			scopes = append(scopes, current.scope)
		}
		target := current.metrics[source]
		if target == nil {
			target = &metricpb.Metric{Name: source.Name, Description: source.Description, Unit: source.Unit}
			switch data := source.Data.(type) {
			case *metricpb.Metric_Sum:
				target.Data = &metricpb.Metric_Sum{Sum: &metricpb.Sum{AggregationTemporality: data.Sum.AggregationTemporality, IsMonotonic: data.Sum.IsMonotonic}}
			case *metricpb.Metric_Gauge:
				target.Data = &metricpb.Metric_Gauge{Gauge: &metricpb.Gauge{}}
			case *metricpb.Metric_Histogram:
				target.Data = &metricpb.Metric_Histogram{Histogram: &metricpb.Histogram{AggregationTemporality: data.Histogram.AggregationTemporality}}
			case *metricpb.Metric_Summary:
				target.Data = &metricpb.Metric_Summary{Summary: &metricpb.Summary{}}
			}
			current.metrics[source] = target
			current.scope.Metrics = append(current.scope.Metrics, target)
		}
		return target, remaining
	}
	for _, metric := range metrics {
		for _, point := range metric.GetSum().GetDataPoints() {
			target, attributes := targetFor(metric, point.Attributes)
			point.Attributes = attributes
			target.GetSum().DataPoints = append(target.GetSum().DataPoints, point)
		}
		for _, point := range metric.GetGauge().GetDataPoints() {
			target, attributes := targetFor(metric, point.Attributes)
			point.Attributes = attributes
			target.GetGauge().DataPoints = append(target.GetGauge().DataPoints, point)
		}
		for _, point := range metric.GetHistogram().GetDataPoints() {
			target, attributes := targetFor(metric, point.Attributes)
			point.Attributes = attributes
			target.GetHistogram().DataPoints = append(target.GetHistogram().DataPoints, point)
		}
		for _, point := range metric.GetSummary().GetDataPoints() {
			target, attributes := targetFor(metric, point.Attributes)
			point.Attributes = attributes
			target.GetSummary().DataPoints = append(target.GetSummary().DataPoints, point)
		}
	}
	return scopes
}

func startMetricExport(client *http.Client, endpoint string, resource *resourcepb.Resource) func(context.Context) error {
	started := uint64(time.Now().UnixNano())
	export := func(ctx context.Context) (err error) {
		defer func() {
			stage := "exported"
			if err != nil {
				stage = "failed"
			}
			telemetryRecords.WithLabelValues("metrics", stage).Inc()
		}()
		metrics, err := nativeMetrics(prometheus.DefaultGatherer, started, uint64(time.Now().UnixNano()))
		if err != nil {
			return err
		}
		message := &collectorpb.ExportMetricsServiceRequest{ResourceMetrics: []*metricpb.ResourceMetrics{{
			Resource: resource, ScopeMetrics: metricScopes(metrics),
		}}}
		if proto.Size(message) > 2<<20 {
			telemetryRecords.WithLabelValues("metrics", "dropped").Inc()
			return errors.New("metrics export exceeds size limit")
		}
		body, err := proto.Marshal(message)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/metrics", bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/x-protobuf")
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode/100 != 2 {
			return errors.New("collector rejected metrics")
		}
		reply, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil {
			return err
		}
		var result collectorpb.ExportMetricsServiceResponse
		if len(reply) > 65536 || proto.Unmarshal(reply, &result) != nil ||
			result.GetPartialSuccess().GetRejectedDataPoints() > 0 {
			return errors.New("collector did not accept the complete metrics snapshot")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = export(ctx)
			}
		}
	}()
	var once sync.Once
	return func(ctx context.Context) (err error) {
		once.Do(func() {
			cancel()
			<-done
			err = export(ctx)
		})
		return err
	}
}
