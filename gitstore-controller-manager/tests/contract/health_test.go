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

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHealth_JSONFieldsPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &countingReconciler{}
	c := cache.New[string]()
	c.MarkSynced()
	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     2,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	handler := health.NewHandler(mgr, "0.0.1-alpha.0")
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

	if version, ok := body["version"]; !ok || version == "" {
		t.Error("missing top-level 'version' field")
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

// T034: KindStats() lists all registered kinds with Registered=true.
func TestHealth_RegisteredKindsListed(t *testing.T) {
	mgr := manager.New()

	c1 := cache.New[string]()
	c1.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "CategoryTaxonomy",
		Reconciler:      &countingReconciler{},
		Cache:           c1,
		MaxAttempts:     1,
		InitialInterval: 1 * time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register CategoryTaxonomy failed: %v", err)
	}

	c2 := cache.New[string]()
	c2.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Collection",
		Reconciler:      &countingReconciler{},
		Cache:           c2,
		MaxAttempts:     1,
		InitialInterval: 1 * time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register Collection failed: %v", err)
	}

	stats := mgr.KindStats()
	for _, kind := range []string{"CategoryTaxonomy", "Collection"} {
		s, ok := stats[kind]
		if !ok {
			t.Errorf("kind %q missing from KindStats()", kind)
			continue
		}
		if !s.Registered {
			t.Errorf("kind %q: Registered=false, want true", kind)
		}
	}
}

// T035: duplicate registration returns an error; no dispatch goroutines started.
func TestHealth_DuplicateKind_FatalBeforeStart(t *testing.T) {
	mgr := manager.New()
	c := cache.New[string]()
	c.MarkSynced()
	r := &countingReconciler{}
	reg := manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     1,
		InitialInterval: 1 * time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}
	if err := mgr.Register(reg); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}
	if err := mgr.Register(reg); err == nil {
		t.Fatal("expected error on duplicate Register, got nil")
	}
	// No Start() called after error — reconciler call count must remain zero.
	if r.calls.Load() != 0 {
		t.Errorf("reconciler should not have been called, got %d calls", r.calls.Load())
	}
}

func TestHealth_MetricsEndpointResponds(t *testing.T) {
	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:       "MetricsWidget",
		Reconciler: &countingReconciler{},
		Cache:      newSyncedCache(),
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
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

// T036: checkpoint metrics update on successful and failed flush.
func TestHealth_CheckpointLastWriteTimestamp_UpdatesOnSave(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{ResourceVersion: "0"}}
	sw := newStubWatcher([]listwatch.WatchEvent[widget]{
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "a"}, ResourceVersion: "1"},
	}, nil)
	lw.watchers = []*stubWatcher[widget]{sw}

	r, _, _ := newRunner(t, lw, store)
	r.FlushIntervalEvents = 1
	r.Kind = "HealthWidgetA"

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	before := testutil.ToFloat64(health.CheckpointLastWriteTimestamp.WithLabelValues("HealthWidgetA"))

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(400 * time.Millisecond)
	for {
		if testutil.ToFloat64(health.CheckpointLastWriteTimestamp.WithLabelValues("HealthWidgetA")) > before {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected CheckpointLastWriteTimestamp to update after a successful flush")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestHealth_CheckpointWriteFailuresTotal_IncrementsOnFailure(t *testing.T) {
	store := &failingStore{inner: checkpoint.NewMemoryStore(), failCount: 3}
	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{ResourceVersion: "0"}}
	sw := newStubWatcher([]listwatch.WatchEvent[widget]{
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "a"}, ResourceVersion: "1"},
	}, nil)
	lw.watchers = []*stubWatcher[widget]{sw}

	r, _, _ := newRunner(t, lw, store)
	r.FlushIntervalEvents = 1
	r.Kind = "HealthWidgetB"

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	before := testutil.ToFloat64(health.CheckpointWriteFailuresTotal.WithLabelValues("HealthWidgetB"))

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(800 * time.Millisecond)
	for {
		if testutil.ToFloat64(health.CheckpointWriteFailuresTotal.WithLabelValues("HealthWidgetB"))-before >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected CheckpointWriteFailuresTotal to increment at least 3 times")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// T037: replay backlog metric tracks the manager's existing queue depth.
func TestHealth_CheckpointReplayBacklog_TracksQueueDepth(t *testing.T) {
	mgr := manager.New()
	c := cache.New[string]()
	c.MarkSynced()
	// Slow reconciler so enqueued items pile up in the queue instead of
	// draining immediately, giving KindStats() a non-zero depth to report.
	block := make(chan struct{})
	slow := &blockingReconciler{unblock: block}
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "HealthWidgetC",
		Reconciler:      slow,
		Cache:           c,
		MaxAttempts:     1,
		InitialInterval: time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	for i := range 5 {
		key := types.WorkItemKey{Kind: "HealthWidgetC", Namespace: "ns", Name: string(rune('a' + i))}
		if err := mgr.Enqueue(key); err != nil {
			t.Fatalf("Enqueue failed: %v", err)
		}
	}

	deadline := time.After(1 * time.Second)
	var stats map[string]health.KindStat
	for {
		stats = mgr.KindStats()
		if stats["HealthWidgetC"].QueueDepth > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected non-zero queue depth before checking replay backlog metric")
		case <-time.After(10 * time.Millisecond):
		}
	}

	got := testutil.ToFloat64(health.CheckpointReplayBacklog.WithLabelValues("HealthWidgetC"))
	want := float64(stats["HealthWidgetC"].QueueDepth)
	if got != want {
		t.Errorf("CheckpointReplayBacklog = %v, want %v (KindStats().QueueDepth)", got, want)
	}
	close(block)
	cancel()
}

type blockingReconciler struct{ unblock <-chan struct{} }

func (b *blockingReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) manager.ReconcileResult {
	<-b.unblock
	return types.ResultOK()
}
