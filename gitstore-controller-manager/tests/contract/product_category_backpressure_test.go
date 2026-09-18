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

// slowReconciler simulates a burst of previously-unresolved Products
// re-enqueued by a CategoryTaxonomy create/rename (FR-009): each reconcile
// blocks for delay before succeeding, standing in for real status-write
// latency at scale.
type slowReconciler struct {
	delay time.Duration
	calls atomic.Int64
}

func (r *slowReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) manager.ReconcileResult {
	r.calls.Add(1)
	time.Sleep(r.delay)
	return types.ResultOK()
}

// fastReconciler stands in for an unrelated kind (Namespace/Repository/
// CategoryTaxonomy) that must keep reconciling promptly regardless of a
// Product burst on another kind's queue.
type fastReconciler struct {
	dispatched chan time.Time
}

func (r *fastReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) manager.ReconcileResult {
	r.dispatched <- time.Now()
	return types.ResultOK()
}

// TestBackpressure_ProductBurstDoesNotStarveUnrelatedKind covers T036/PR-004:
// gitstore-controller-manager gives every registered kind its own queue and
// worker pool (internal/manager.Manager — "one controller per registered
// kind"), so a burst of Product re-enqueues from a CategoryTaxonomy
// create/rename cannot consume the worker capacity another kind's
// reconciliation needs. This proves that structural isolation holds for the
// spec-062 burst scenario specifically, using the existing manager/health
// primitives — not a new capacity profile (plan.md's Capacity Profile: N/A
// justification).
func TestBackpressure_ProductBurstDoesNotStarveUnrelatedKind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := manager.New()

	// Product: a single worker, each reconcile takes 50ms. A 200-item burst
	// would take ~10s to drain serially — long enough that if the Namespace
	// queue below shared that capacity, its own dispatch would be delayed by
	// seconds, not milliseconds.
	product := &slowReconciler{delay: 50 * time.Millisecond}
	productCache := cache.New[string]()
	productCache.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Product",
		Reconciler:      product,
		Cache:           productCache,
		MaxAttempts:     3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     100 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("register Product: %v", err)
	}

	// Namespace: the unrelated kind whose responsiveness must survive the
	// Product burst untouched.
	namespace := &fastReconciler{dispatched: make(chan time.Time, 1)}
	namespaceCache := cache.New[string]()
	namespaceCache.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Namespace",
		Reconciler:      namespace,
		Cache:           namespaceCache,
		MaxAttempts:     3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     100 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("register Namespace: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	// Simulate the CategoryTaxonomy re-enqueue handler firing for 200
	// previously-unresolved Products in one burst (FR-009).
	const burstSize = 200
	for i := range burstSize {
		key := manager.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget-" + time.Now().Format("150405.000000000") + "-" + string(rune('a'+i%26))}
		if err := mgr.Enqueue(key); err != nil {
			t.Fatalf("enqueue Product burst item %d: %v", i, err)
		}
	}

	// Immediately after the burst, enqueue a single Namespace key and time
	// how long it takes to dispatch. Its own dedicated worker must pick it
	// up promptly, not wait behind the Product queue.
	enqueuedAt := time.Now()
	nsKey := manager.WorkItemKey{Kind: "Namespace", Namespace: "", Name: "gitstore-test"}
	if err := mgr.Enqueue(nsKey); err != nil {
		t.Fatalf("enqueue Namespace: %v", err)
	}

	select {
	case dispatchedAt := <-namespace.dispatched:
		latency := dispatchedAt.Sub(enqueuedAt)
		// Generous bound: well under the burst's ~10s serial drain time, but
		// tolerant of scheduler jitter under `go test` parallelism.
		if latency > 2*time.Second {
			t.Errorf("Namespace dispatch latency = %s, want well under the ~%s the Product burst would take serially (starvation)", latency, time.Duration(burstSize)*product.delay)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Namespace reconciler was never dispatched — starved by the Product burst")
	}

	// Sanity: the Product burst is actually still being worked (not a
	// vacuous pass because nothing was queued).
	if product.calls.Load() == 0 {
		t.Fatal("Product reconciler was never called — burst setup is broken")
	}
}
