// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
)

func TestHealth_JSONFieldsPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &countingReconciler{}
	mgr := manager.New()
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      r,
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     2,
	})

	go func() { _ = mgr.Start(ctx) }()

	handler := health.NewHandler(mgr)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := body["status"]; !ok {
		t.Error("missing top-level 'status' field")
	}

	kinds, ok := body["kinds"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'kinds' object, got %T", body["kinds"])
	}

	widgetRaw, ok := kinds["Widget"]
	if !ok {
		t.Fatal("expected 'Widget' in kinds")
	}
	widget := widgetRaw.(map[string]any)

	for _, field := range []string{"activeWorkers", "queueDepth", "poisonItems", "stalled"} {
		if _, ok := widget[field]; !ok {
			t.Errorf("missing field %q in Widget health", field)
		}
	}
}

func TestHealth_MetricsEndpointResponds(t *testing.T) {
	mgr := manager.New()
	handler := health.NewMetricsHandler(mgr)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, metric := range []string{
		"gitstore_controller_queue_depth",
		"gitstore_controller_active_workers",
		"gitstore_controller_poison_items_total",
	} {
		if !contains(body, metric) {
			t.Errorf("metric %q not found in /metrics output", metric)
		}
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
