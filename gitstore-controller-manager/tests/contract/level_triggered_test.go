// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// stateReadingReconciler reads from the cache during reconciliation.
type stateReadingReconciler struct {
	cache    *cache.Cache[string]
	lastSeen atomic.Value // stores string
}

func (s *stateReadingReconciler) Reconcile(_ context.Context, key types.WorkItemKey) (types.Result, error) {
	val, _ := s.cache.Get(key)
	s.lastSeen.Store(val)
	return types.Result{}, nil
}

func TestManager_LevelTriggered_ReadsCurrentCacheState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := cache.New[string]()
	c.MarkSynced()

	key := types.WorkItemKey{Kind: "Product", Namespace: "ns", Name: "widget"}
	c.Set(key, "state-v1")

	r := &stateReadingReconciler{cache: c}
	mgr := manager.New()
	mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Product",
		Reconciler:      r,
		MaxAttempts:     1,
		InitialInterval: 1 * time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	})

	go func() { _ = mgr.Start(ctx) }()

	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := r.lastSeen.Load().(string); v == "state-v1" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if v, _ := r.lastSeen.Load().(string); v != "state-v1" {
		t.Errorf("reconciler saw %q, want %q", v, "state-v1")
	}

	// Update cache, enqueue again, assert reconciler sees new state.
	c.Set(key, "state-v2")
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("second Enqueue failed: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := r.lastSeen.Load().(string); v == "state-v2" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if v, _ := r.lastSeen.Load().(string); v != "state-v2" {
		t.Errorf("reconciler saw %q after update, want %q", v, "state-v2")
	}
}
