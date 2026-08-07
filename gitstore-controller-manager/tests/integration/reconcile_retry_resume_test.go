// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration_test

import (
	"context"
	"errors"
	"sync"
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

// TestIntegration_Reconcile_SucceedsOnFirstAttempt covers FR-001: a resource
// reconciled successfully on the first attempt is reflected in the success
// metric and the reconciler is invoked exactly once.
func TestIntegration_Reconcile_SucceedsOnFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US1-FirstAttempt"
	r := newScriptedReconciler(types.ResultOK())
	mgr := manager.New()
	c := cache.New[string]()
	c.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            kind,
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

	before := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues(kind, "success"))

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "w1"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := r.callCount(); got != 1 {
		t.Fatalf("reconciler call count = %d, want 1", got)
	}
	after := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues(kind, "success"))
	if after-before != 1 {
		t.Errorf("ReconcileTotal{success} delta = %v, want 1", after-before)
	}
}

// TestIntegration_Reconcile_TransientFailureThenSucceeds covers FR-002: a
// reconciler that fails transiently N times then succeeds must reach success
// with exactly N+1 total calls. health.ReconcileTotal{transient_failure} is
// only incremented when the retry budget is exhausted (see
// internal/manager/manager.go's handleTransient) — an eventual success does
// not increment it, so only the success counter is asserted here.
func TestIntegration_Reconcile_TransientFailureThenSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US1-TransientThenSuccess"
	script := []types.ReconcileResult{
		types.ResultTransient(errors.New("transient 1")),
		types.ResultTransient(errors.New("transient 2")),
		types.ResultOK(),
	}
	r := newScriptedReconciler(script...)
	mgr := manager.New()
	c := cache.New[string]()
	c.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            kind,
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     5,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	beforeSuccess := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues(kind, "success"))

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "w1"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.callCount() >= int64(len(script)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := r.callCount(); got != int64(len(script)) {
		t.Fatalf("reconciler call count = %d, want %d", got, len(script))
	}
	afterSuccess := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues(kind, "success"))
	if afterSuccess-beforeSuccess != 1 {
		t.Errorf("ReconcileTotal{success} delta = %v, want 1", afterSuccess-beforeSuccess)
	}
}

// TestIntegration_Restart_ResumesFromCheckpoint_NoLostOrDuplicateWork covers
// FR-003: a Runner + Manager pair that is torn down mid-run and reconstructed
// against the same checkpoint.Store must resume without losing pending work
// or redundantly re-dispatching already-completed work.
//
// The Manager is registered under Kind "Widget" — the same Kind widgetKey
// produces — so the Runner's Enqueue callback can feed the Manager directly
// with no key translation.
func TestIntegration_Restart_ResumesFromCheckpoint_NoLostOrDuplicateWork(t *testing.T) {
	store := checkpoint.NewMemoryStore()

	completed := widget{Namespace: "ns", Name: "completed", ResourceVersion: "1"}
	pending := widget{Namespace: "ns", Name: "pending", ResourceVersion: "1"}

	// --- First run: list both items, reconcile "completed" to success, then
	// tear down before "pending" is dispatched.
	lw1 := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{
			Items:           []widget{completed, pending},
			ResourceVersion: "10",
		},
	}
	runner1, cache1, _ := newRunner(t, lw1, store)

	reconciled1 := make(map[types.WorkItemKey]int)
	var reconciledMu1 sync.Mutex
	completedAck := make(chan struct{}, 1)
	mgr1 := manager.New()
	reconciler1 := &blockingSelectReconciler{
		allow: map[types.WorkItemKey]bool{widgetKey(completed): true},
		onCall: func(k types.WorkItemKey) {
			reconciledMu1.Lock()
			reconciled1[k]++
			reconciledMu1.Unlock()
		},
	}
	if err := mgr1.Register(manager.ReconcilerRegistration{
		Kind:       "Widget",
		Reconciler: reconciler1,
		Cache:      cache1,
		OnSuccess: func(key types.WorkItemKey) {
			runner1.MarkCompleted(key)
			if key == widgetKey(completed) {
				completedAck <- struct{}{}
			}
		},
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register (run 1) failed: %v", err)
	}
	// Wire the Runner's Enqueue callback straight into the Manager — no
	// polling/forwarding loop needed since both share the "Widget" Kind.
	runner1.Enqueue = mgr1.Enqueue

	ctx1, cancel1 := context.WithCancel(context.Background())
	runnerDone1 := make(chan error, 1)
	go func() { runnerDone1 <- runner1.Run(ctx1) }()
	mgrDone1 := make(chan error, 1)
	go func() { mgrDone1 <- mgr1.Start(ctx1) }()

	select {
	case <-completedAck:
	case <-time.After(3 * time.Second):
		t.Fatal("expected 'completed' item to reconcile successfully before simulated restart")
	}
	reconciledMu1.Lock()
	completedCalls := reconciled1[widgetKey(completed)]
	reconciledMu1.Unlock()
	if completedCalls == 0 {
		t.Fatal("expected 'completed' item to be reconciled before simulated restart")
	}
	// "pending" may or may not have been dispatched once before shutdown
	// (dispatch order across keys is not guaranteed with a single worker) —
	// its reconciler returns a long RequeueAfter for disallowed keys, so a
	// call here never reaches success. What matters is that it has not
	// completed (verified after resume below), simulating in-flight/pending
	// work at the moment of a restart.

	cancel1()
	<-runnerDone1
	<-mgrDone1

	// --- Second run: fresh Runner + Manager against the same Store. On
	// resume, the Runner restores its cache from the checkpoint snapshot but
	// enqueues only durable ReplayKeys. The successful item was removed from
	// that set by OnSuccess; the unfinished item remains pending.
	lw2 := &stubListWatcher[widget]{}
	runner2, cache2, _ := newRunner(t, lw2, store)

	reconciled2 := make(map[types.WorkItemKey]int)
	var reconciledMu2 sync.Mutex
	mgr2 := manager.New()
	reconciler2 := &blockingSelectReconciler{
		allowAll: true,
		onCall: func(k types.WorkItemKey) {
			reconciledMu2.Lock()
			reconciled2[k]++
			reconciledMu2.Unlock()
		},
	}
	if err := mgr2.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      reconciler2,
		Cache:           cache2,
		OnSuccess:       runner2.MarkCompleted,
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register (run 2) failed: %v", err)
	}
	runner2.Enqueue = mgr2.Enqueue

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	runnerDone2 := make(chan error, 1)
	go func() { runnerDone2 <- runner2.Run(ctx2) }()
	mgrDone2 := make(chan error, 1)
	go func() { mgrDone2 <- mgr2.Start(ctx2) }()

	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) {
		reconciledMu2.Lock()
		n := reconciled2[widgetKey(pending)]
		reconciledMu2.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Give any spurious redundant dispatch of "completed" a moment to surface.
	time.Sleep(100 * time.Millisecond)
	cancel2()
	<-runnerDone2
	<-mgrDone2

	reconciledMu2.Lock()
	defer reconciledMu2.Unlock()
	if lw2.watchCallCount() == 0 {
		t.Error("expected resume to skip List and go straight to Watch")
	}
	if reconciled2[widgetKey(pending)] != 1 {
		t.Errorf("'pending' item reconcile count after resume = %d, want exactly 1 (no lost work)", reconciled2[widgetKey(pending)])
	}
	if reconciled2[widgetKey(completed)] != 0 {
		t.Errorf("'completed' item reconcile count after resume = %d, want 0 (already-completed work must not replay)", reconciled2[widgetKey(completed)])
	}
}

// blockingSelectReconciler succeeds immediately for keys present in allow
// (or, if allowAll is set, for every key), invoking onCall for every attempt
// regardless of outcome. Keys absent from allow (with allowAll unset) are
// left pending forever by returning a long RequeueAfter, simulating an
// in-flight/incomplete reconcile at the moment of a simulated restart.
type blockingSelectReconciler struct {
	mu       sync.Mutex
	allow    map[types.WorkItemKey]bool
	allowAll bool
	onCall   func(types.WorkItemKey)
}

func (b *blockingSelectReconciler) Reconcile(_ context.Context, key types.WorkItemKey) types.ReconcileResult {
	if b.onCall != nil {
		b.onCall(key)
	}
	b.mu.Lock()
	ok := b.allowAll || b.allow[key]
	b.mu.Unlock()
	if ok {
		return types.ResultOK()
	}
	return types.ResultAfter(time.Hour)
}
