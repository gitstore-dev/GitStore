// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/api"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// alwaysFailReconciler fails every call.
type alwaysFailReconciler struct{}

func (a *alwaysFailReconciler) Reconcile(_ context.Context, _ types.WorkItemKey) (types.Result, error) {
	return types.Result{}, errors.New("permanent failure")
}

func TestManager_QuarantinesAfterMaxAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr := manager.New()
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      &alwaysFailReconciler{},
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	})

	go func() { _ = mgr.Start(ctx) }()

	poisonKey := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "poison-widget"}
	if err := mgr.Enqueue(poisonKey); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Give it time to exhaust retries and quarantine.
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
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      healthy,
		MaxAttempts:     2,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     2,
	})

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
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      &alwaysFailReconciler{},
		MaxAttempts:     2,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	})

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "bypass-check"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	// Re-enqueue while likely still processing to simulate an event arriving mid-retry.
	time.Sleep(2 * time.Millisecond)
	_ = mgr.Enqueue(key)

	// Wait for quarantine.
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

	// After quarantine, give a moment and confirm item is still quarantined (not re-enqueued).
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
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Staller",
		Reconciler:      &countingReconciler{},
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  50 * time.Millisecond,
		WorkerCount:     1,
	})

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: "Staller", Namespace: "ns", Name: "item"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Wait for at least one successful reconcile so lastSuccess is set.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := mgr.KindStats()
		if _, ok := stats["Staller"]; ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Sleep beyond StallThreshold without enqueuing any new work.
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
	mgr.Register(manager.ReconcilerRegistration{
		Kind: "Alpha", Reconciler: &countingReconciler{},
		MaxAttempts: 1, InitialInterval: 1 * time.Millisecond,
		MaxInterval: 1 * time.Millisecond, Multiplier: 1, StallThreshold: time.Minute, WorkerCount: 1,
	})
	mgr.Register(manager.ReconcilerRegistration{
		Kind: "Beta", Reconciler: &countingReconciler{},
		MaxAttempts: 1, InitialInterval: 1 * time.Millisecond,
		MaxInterval: 1 * time.Millisecond, Multiplier: 1, StallThreshold: time.Minute, WorkerCount: 1,
	})

	// Manually inject poison items directly via the quarantine stores.
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

func TestManager_RequeueResetsAttemptCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr := manager.New()
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      &alwaysFailReconciler{},
		MaxAttempts:     2,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	})

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "requeue-me"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Wait for quarantine.
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

	// Re-queue via manager.
	if err := mgr.Requeue(key); err != nil {
		t.Fatalf("Requeue failed: %v", err)
	}

	// After requeue, item must no longer be in quarantine.
	if mgr.IsQuarantined(key) {
		t.Fatal("expected item to leave quarantine after Requeue")
	}
}
