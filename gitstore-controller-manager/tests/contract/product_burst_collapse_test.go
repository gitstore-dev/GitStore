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

// blockingOnceReconciler blocks its first Reconcile call on release, then
// completes instantly for every subsequent call — used to hold a work item
// "in flight" while a burst of Enqueue calls for the same key arrives, so
// the queue's dedup behavior can be observed before any of them are
// dispatched.
type blockingOnceReconciler struct {
	calls   atomic.Int64
	release chan struct{}
}

func (r *blockingOnceReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) manager.ReconcileResult {
	if r.calls.Add(1) == 1 {
		<-r.release
	}
	return types.ResultOK()
}

// T037 (spec 042, SC-006): a burst of many enqueues for the same
// CategoryTaxonomy key — as would result from a rapid succession of
// Product changes all affecting one category — collapses to at most one
// pending item for that key, rather than growing the queue proportionally
// to the number of Product changes (research.md R5).
func TestManager_BurstOfSameKeyEnqueues_CollapsesToOnePendingItem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &blockingOnceReconciler{release: make(chan struct{})}
	c := cache.New[string]()
	c.MarkSynced()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "CategoryTaxonomy",
		Reconciler:      r,
		Cache:           c,
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

	key := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics"}

	// First enqueue is picked up by the single worker and blocks inside
	// Reconcile (simulating a category reconcile in flight).
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue (initial) failed: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if r.calls.Load() != 1 {
		t.Fatalf("expected reconciler to be dispatched once and blocked, got %d calls", r.calls.Load())
	}

	// Burst: many more Product-driven enqueues for the same key while the
	// first is still in flight — simulating a bulk import touching one
	// category repeatedly.
	const burstSize = 50
	for i := 0; i < burstSize; i++ {
		if err := mgr.Enqueue(key); err != nil {
			t.Fatalf("Enqueue (burst item %d) failed: %v", i, err)
		}
	}

	stats := mgr.KindStats()["CategoryTaxonomy"]
	// One worker is active (the blocked first call) and at most one more
	// item is pending for this single-key burst — never burstSize.
	if stats.QueueDepth > 1 {
		t.Errorf("QueueDepth = %d after a %d-item same-key burst, want <= 1 (dedup must collapse repeat enqueues)", stats.QueueDepth, burstSize)
	}

	close(r.release)

	// After release, the blocked call finishes and the single collapsed
	// pending item (if any) is dispatched — total calls must be small
	// (2: the initial + the one collapsed burst re-enqueue), never
	// anywhere near burstSize+1.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.calls.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // settle
	if got := r.calls.Load(); got > 3 {
		t.Errorf("reconciler called %d times after a %d-item same-key burst, want a small constant (dedup collapse), not proportional to burst size", got, burstSize)
	}
}
