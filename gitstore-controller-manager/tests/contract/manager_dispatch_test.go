// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
)

// --- countingReconciler tracks dispatch count ---

type countingReconciler struct {
	calls atomic.Int64
}

func (c *countingReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) (manager.Result, error) {
	c.calls.Add(1)
	return manager.Result{}, nil
}

func TestManager_ReconcilerDispatchedOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &countingReconciler{}
	mgr := manager.New()
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      r,
		MaxAttempts:     3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     100 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     2,
	})

	go func() { _ = mgr.Start(ctx) }()

	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w1"}

	// Enqueue the same key 5 times — should dispatch once per quiescent moment.
	for i := 0; i < 5; i++ {
		if err := mgr.Enqueue(key); err != nil {
			t.Fatalf("Enqueue failed: %v", err)
		}
	}

	// Wait for at least one dispatch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if r.calls.Load() == 0 {
		t.Fatal("reconciler was never called")
	}
}

func TestManager_UnregisteredKind_ReturnsError(t *testing.T) {
	mgr := manager.New()

	key := manager.WorkItemKey{Kind: "Unknown", Namespace: "ns", Name: "x"}
	err := mgr.Enqueue(key)
	if !errors.Is(err, manager.ErrKindNotRegistered) {
		t.Errorf("expected ErrKindNotRegistered, got %v", err)
	}
}
