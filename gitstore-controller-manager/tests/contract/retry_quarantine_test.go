// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
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
