// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secret

import (
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/secretmaterial"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestObserverUsesOnlyFixedCategories(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	sink := NewObserver(zap.New(core))
	observation := secretmaterial.Observation{
		Consumer: "MUST-NOT-LEAK", Purpose: "MUST-NOT-LEAK",
		Provider: secretmaterial.ProviderFile,
	}
	sink.Inflight(observation, 1)
	for _, reason := range []string{
		"success", "InvalidRef", "NotFound", "MissingKey", "Forbidden",
		"ProviderUnavailable", "UnsupportedType", "ValueTooLarge", "canceled",
		"deadline_exceeded", "MUST-NOT-LEAK",
	} {
		sink.Observe(observation, reason, time.Millisecond)
	}
	sink.Inflight(observation, -1)
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "gitstore_secret_resolution_") {
			continue
		}
		found++
		if strings.Contains(family.String(), "MUST-NOT-LEAK") {
			t.Fatal("unbounded observation leaked into metrics")
		}
		if family.GetName() == "gitstore_secret_resolution_inflight" {
			for _, metric := range family.Metric {
				if metric.GetGauge().GetValue() != 0 {
					t.Fatal("inflight metric did not balance")
				}
			}
		}
	}
	if found != 3 {
		t.Fatalf("missing process metrics: %d families", found)
	}
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, "MUST-NOT-LEAK") {
			t.Fatal("unsafe observation message")
		}
		for _, field := range entry.Context {
			if strings.Contains(field.String, "MUST-NOT-LEAK") {
				t.Fatal("unsafe observation label")
			}
		}
	}
}
