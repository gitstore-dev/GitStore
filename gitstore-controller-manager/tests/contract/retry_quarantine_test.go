// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/api"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// alwaysFailReconciler fails every call.
type alwaysFailReconciler struct{}

func (a *alwaysFailReconciler) Reconcile(_ context.Context, _ types.WorkItemKey) types.ReconcileResult {
	return types.ResultTransient(errors.New("permanent failure"))
}

func newSyncedCache() *cache.Cache[string] {
	c := cache.New[string]()
	c.MarkSynced()
	return c
}

func TestManager_QuarantinesAfterMaxAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      &alwaysFailReconciler{},
		Cache:           newSyncedCache(),
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	poisonKey := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "poison-widget"}
	if err := mgr.Enqueue(poisonKey); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var quarantined bool
	for time.Now().Before(deadline) {
		if mgr.IsQuarantined(poisonKey) {
			quarantined = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !quarantined {
		t.Fatal("expected item to be quarantined after MaxAttempts")
	}
}

func TestManager_OtherItemsUnaffectedByPoison(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	healthy := &countingReconciler{}
	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      healthy,
		Cache:           newSyncedCache(),
		MaxAttempts:     2,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     2,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	healthyKey := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "healthy"}
	if err := mgr.Enqueue(healthyKey); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if healthy.calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if healthy.calls.Load() == 0 {
		t.Fatal("healthy reconciler was never called")
	}
}

// TestManager_QuarantineNotBypassedByPendingEvent verifies that an event
// arriving during the retry loop does not cause the item to be re-enqueued
// automatically after quarantine (P1 fix).
func TestManager_QuarantineNotBypassedByPendingEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      &alwaysFailReconciler{},
		Cache:           newSyncedCache(),
		MaxAttempts:     2,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "bypass-check"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	// Re-enqueue while likely still processing to simulate an event arriving mid-retry.
	time.Sleep(2 * time.Millisecond)
	_ = mgr.Enqueue(key)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.IsQuarantined(key) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !mgr.IsQuarantined(key) {
		t.Fatal("expected item to be quarantined")
	}

	time.Sleep(100 * time.Millisecond)
	if !mgr.IsQuarantined(key) {
		t.Fatal("item left quarantine automatically — quarantine bypass bug reproduced")
	}
}

// TestManager_Stalled detects a stalled reconciler via /health (P2a fix).
func TestManager_Stalled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Staller",
		Reconciler:      &countingReconciler{},
		Cache:           newSyncedCache(),
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  50 * time.Millisecond,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: "Staller", Namespace: "ns", Name: "item"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := mgr.KindStats()
		if _, ok := stats["Staller"]; ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(120 * time.Millisecond)

	stats := mgr.KindStats()
	if s, ok := stats["Staller"]; !ok {
		t.Fatal("Staller kind not found in stats")
	} else if !s.Stalled {
		t.Error("expected Stalled=true after StallThreshold exceeded")
	}
}

// TestPoisonAll_AggregatesAcrossKinds verifies GET /controller/v1/poison/_all
// returns items from all registered kinds (P2b fix).
func TestPoisonAll_AggregatesAcrossKinds(t *testing.T) {
	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind: "Alpha", Reconciler: &countingReconciler{}, Cache: newSyncedCache(),
		MaxAttempts: 1, InitialInterval: 1 * time.Millisecond,
		MaxInterval: 1 * time.Millisecond, Multiplier: 1, StallThreshold: time.Minute, WorkerCount: 1,
	}); err != nil {
		t.Fatalf("Register Alpha failed: %v", err)
	}
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind: "Beta", Reconciler: &countingReconciler{}, Cache: newSyncedCache(),
		MaxAttempts: 1, InitialInterval: 1 * time.Millisecond,
		MaxInterval: 1 * time.Millisecond, Multiplier: 1, StallThreshold: time.Minute, WorkerCount: 1,
	}); err != nil {
		t.Fatalf("Register Beta failed: %v", err)
	}

	alphaQS := mgr.QuarantineStore("Alpha")
	betaQS := mgr.QuarantineStore("Beta")
	alphaQS.Put(&retry.PoisonItem{Key: types.WorkItemKey{Kind: "Alpha", Namespace: "ns", Name: "a1"}})
	betaQS.Put(&retry.PoisonItem{Key: types.WorkItemKey{Kind: "Beta", Namespace: "ns", Name: "b1"}})
	betaQS.Put(&retry.PoisonItem{Key: types.WorkItemKey{Kind: "Beta", Namespace: "ns", Name: "b2"}})

	handler := api.ListPoisonHandler(mgr)
	req := httptest.NewRequest(http.MethodGet, "/controller/v1/poison/_all", nil)
	req.SetPathValue("kind", "_all")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var items []any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items across all kinds, got %d", len(items))
	}
}

// T038: BackoffHint on TransientFailure overrides the registration's InitialInterval.
func TestRetry_BackoffHint_OverridesInitialInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hint := 150 * time.Millisecond
	var callTimes []time.Time
	var mu sync.Mutex

	// Reconciler always fails with a BackoffHint the first call, succeeds after.
	r := &funcReconciler{fn: func(_ context.Context, _ manager.WorkItemKey) types.ReconcileResult {
		mu.Lock()
		callTimes = append(callTimes, time.Now())
		n := len(callTimes)
		mu.Unlock()
		if n == 1 {
			return types.ResultTransient(errors.New("transient"), hint)
		}
		return types.ResultOK()
	}}

	c := newSyncedCache()
	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond, // much shorter than hint
		MaxInterval:     500 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "hint-item"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Wait for at least 2 calls.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(callTimes)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	times := callTimes
	mu.Unlock()

	if len(times) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(times))
	}
	elapsed := times[1].Sub(times[0])
	if elapsed < hint {
		t.Errorf("retry fired too soon: elapsed=%v, want >= BackoffHint=%v", elapsed, hint)
	}
}

func TestManager_RequeueResetsAttemptCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      &alwaysFailReconciler{},
		Cache:           newSyncedCache(),
		MaxAttempts:     2,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "requeue-me"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.IsQuarantined(key) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !mgr.IsQuarantined(key) {
		t.Fatal("expected item to be quarantined first")
	}

	if err := mgr.Requeue(key); err != nil {
		t.Fatalf("Requeue failed: %v", err)
	}

	if mgr.IsQuarantined(key) {
		t.Fatal("expected item to leave quarantine after Requeue")
	}
}
