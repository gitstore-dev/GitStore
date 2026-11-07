// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package observabilitytest constructs isolated SDK metrics pipelines for tests.
package observabilitytest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/otlptranslator"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// HTTPGatherer reads the real metrics route so contract assertions cover the
// configured SDK reader and scrape naming, not an independent test registry.
func HTTPGatherer(handler http.Handler) prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if response.Code != http.StatusOK {
			return nil, errors.New("SDK metrics route failed")
		}
		parser := expfmt.NewTextParser(model.UTF8Validation)
		families, err := parser.TextToMetricFamilies(strings.NewReader(response.Body.String()))
		if err != nil {
			return nil, err
		}
		result := make([]*dto.MetricFamily, 0, len(families))
		for _, family := range families {
			result = append(result, family)
		}
		return result, nil
	})
}

// Meter returns a meter backed by SDK collection and its supported Prometheus
// exporter. The test owns shutdown; application instrumentation never registers
// individual Prometheus collectors.
func Meter(t testing.TB, registry *prometheus.Registry, scope string) metric.Meter {
	t.Helper()
	return Provider(t, registry).Meter(scope)
}

// Value reads the SDK-derived scrape output for a single bounded series.
func Value(t testing.TB, registry prometheus.Gatherer, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, sample := range family.Metric {
			match := len(sample.Label) == len(labels)
			for _, label := range sample.Label {
				match = match && labels[label.GetName()] == label.GetValue()
			}
			if match {
				if sample.Counter != nil {
					return sample.Counter.GetValue()
				}
				if sample.Gauge != nil {
					return sample.Gauge.GetValue()
				}
			}
		}
	}
	return 0
}

// Provider constructs an isolated SDK provider whose reader the test can scrape.
func Provider(t testing.TB, registry *prometheus.Registry) *sdkmetric.MeterProvider {
	t.Helper()
	provider, err := newProvider(registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider
}

func newProvider(registry *prometheus.Registry) (*sdkmetric.MeterProvider, error) {
	reader, err := otelprom.New(otelprom.WithRegisterer(registry), otelprom.WithoutScopeInfo(), otelprom.WithoutTargetInfo(), otelprom.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithoutSuffixes))
	if err != nil {
		return nil, err
	}
	return sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithCardinalityLimit(2048)), nil
}
