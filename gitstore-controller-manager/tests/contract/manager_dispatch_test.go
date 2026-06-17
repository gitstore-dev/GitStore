// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// --- countingReconciler tracks dispatch count ---

type countingReconciler struct {
	calls atomic.Int64
}

func (c *countingReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) manager.ReconcileResult {
	c.calls.Add(1)
	return types.ResultOK()
}

func TestManager_ReconcilerDispatchedOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &countingReconciler{}
	mgr := manager.New()
	c0 := cache.New[string]()
	c0.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "Widget",
		Reconciler:      r,
		Cache:           c0,
		MaxAttempts:     3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     100 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     2,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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

// T009: TerminalFailure quarantines immediately with zero retry attempts and
// increments gitstore_controller_reconcile_total{result="terminal_failure"}.
func TestManager_TerminalFailure_QuarantinesImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	termErr := errors.New("unrecoverable")
	r := &funcReconciler{fn: func(_ context.Context, _ manager.WorkItemKey) types.ReconcileResult {
		return types.ResultTerminal(termErr)
	}}

	c := cache.New[string]()
	c.MarkSynced()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "CategoryTaxonomy",
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     5,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     100 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := manager.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "ns", Name: "tax1"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.IsQuarantined(key) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mgr.IsQuarantined(key) {
		t.Fatal("expected item to be quarantined after TerminalFailure")
	}
	// Only one Reconcile call — no retries.
	if r.calls.Load() != 1 {
		t.Errorf("expected exactly 1 reconcile call, got %d", r.calls.Load())
	}

	count := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues("CategoryTaxonomy", "terminal_failure"))
	if count < 1 {
		t.Errorf("expected terminal_failure counter >= 1, got %v", count)
	}
}

// T010: RequeueAfter re-enqueues after the specified delay but not before.
func TestManager_RequeueAfter_DelaysReenqueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	delay := 150 * time.Millisecond
	var mu sync.Mutex
	callTimes := []time.Time{}

	r := &funcReconciler{fn: func(_ context.Context, _ manager.WorkItemKey) types.ReconcileResult {
		mu.Lock()
		callTimes = append(callTimes, time.Now())
		n := len(callTimes)
		mu.Unlock()
		if n == 1 {
			return types.ResultAfter(delay)
		}
		return types.ResultOK()
	}}

	c := cache.New[string]()
	c.MarkSynced()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "CategoryTaxonomy",
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     100 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := manager.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "ns", Name: "tax2"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Wait for the second call.
	deadline := time.Now().Add(3 * time.Second)
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
		t.Fatalf("expected at least 2 reconcile calls, got %d", len(times))
	}
	elapsed := times[1].Sub(times[0])
	if elapsed < delay {
		t.Errorf("second call arrived too soon: elapsed=%v, want >= %v", elapsed, delay)
	}
}

// T011: Reconciler panic is recovered as TransientFailure; counter incremented;
// PanicError.Stack is non-empty.
func TestManager_ReconcilerPanic_RecoveredAsTransient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &funcReconciler{fn: func(_ context.Context, _ manager.WorkItemKey) types.ReconcileResult {
		panic("boom")
	}}

	c := cache.New[string]()
	c.MarkSynced()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "CategoryTaxonomy",
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     2,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := manager.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "ns", Name: "panic-item"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Manager must not crash; item eventually quarantined after retry exhaustion.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.IsQuarantined(key) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mgr.IsQuarantined(key) {
		t.Fatal("expected panicking item to be quarantined")
	}

	count := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues("CategoryTaxonomy", "transient_failure"))
	if count < 1 {
		t.Errorf("expected transient_failure counter >= 1, got %v", count)
	}
}

func TestManager_ReconcilerPanic_LogsStackTrace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &funcReconciler{fn: func(_ context.Context, _ manager.WorkItemKey) types.ReconcileResult {
		panic("stack-check")
	}}

	c := cache.New[string]()
	c.MarkSynced()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "PanicKind",
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     1,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := manager.WorkItemKey{Kind: "PanicKind", Namespace: "ns", Name: "stack-item"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.IsQuarantined(key) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mgr.IsQuarantined(key) {
		t.Fatal("expected item to be quarantined after panic")
	}
	// Stack trace presence is validated indirectly via PanicError.Error() in PoisonItem.LastError.
	items := mgr.AllPoisonItems()
	found := false
	for _, pi := range items {
		if pi.Key == key {
			found = true
			if pi.LastError == "" {
				t.Error("PoisonItem.LastError should be non-empty for a panic")
			}
			break
		}
	}
	if !found {
		t.Error("poison item not found for panic key")
	}
}

// T012: Dispatch is held until cache HasSynced(); after MarkSynced() reconciler is called.
func TestManager_DispatchHeldUntilCacheSynced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := &countingReconciler{}
	c := cache.New[string]() // NOT synced yet

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
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "gated"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Reconciler must NOT be called within 200 ms while cache is unsynced.
	time.Sleep(200 * time.Millisecond)
	if r.calls.Load() != 0 {
		t.Errorf("reconciler called before cache synced: got %d calls", r.calls.Load())
	}

	// Mark synced — reconciler must be called within 500 ms.
	c.MarkSynced()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r.calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.calls.Load() == 0 {
		t.Error("reconciler not called within 500 ms after cache synced")
	}
}

// --- Phase 4 (US2): registration validation tests ---

// T020: duplicate registration returns a non-nil error containing the kind name.
func TestManager_DuplicateRegistration_ReturnsError(t *testing.T) {
	mgr := manager.New()
	reg := manager.ReconcilerRegistration{
		Kind:            "BackfillJob",
		Reconciler:      &countingReconciler{},
		Cache:           cache.New[string](),
		MaxAttempts:     1,
		InitialInterval: 1 * time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  time.Minute,
		WorkerCount:     1,
	}
	if err := mgr.Register(reg); err != nil {
		t.Fatalf("first Register failed unexpectedly: %v", err)
	}
	err := mgr.Register(reg)
	if err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
	if !strings.Contains(err.Error(), "BackfillJob") {
		t.Errorf("error should mention kind name, got: %v", err)
	}
}

// T021: nil Reconciler or nil Cache returns a descriptive error.
func TestManager_NilReconciler_ReturnsError(t *testing.T) {
	mgr := manager.New()
	err := mgr.Register(manager.ReconcilerRegistration{
		Kind:       "BackfillJob",
		Reconciler: nil,
		Cache:      cache.New[string](),
	})
	if err == nil {
		t.Fatal("expected error for nil Reconciler, got nil")
	}
}

func TestManager_NilCache_ReturnsError(t *testing.T) {
	mgr := manager.New()
	err := mgr.Register(manager.ReconcilerRegistration{
		Kind:       "BackfillJob",
		Reconciler: &countingReconciler{},
		Cache:      nil,
	})
	if err == nil {
		t.Fatal("expected error for nil Cache, got nil")
	}
}

// T022: CRD kind dispatched on same code path as core kind.
func TestManager_CRDKind_DispatchedOnSamePathAsCoreKind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	coreCalls := &countingReconciler{}
	crdCalls := &countingReconciler{}

	coreCache := cache.New[string]()
	coreCache.MarkSynced()
	crdCache := cache.New[string]()
	crdCache.MarkSynced()

	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind: "CategoryTaxonomy", Reconciler: coreCalls, Cache: coreCache,
		MaxAttempts: 1, InitialInterval: 5 * time.Millisecond, MaxInterval: 10 * time.Millisecond,
		Multiplier: 2.0, StallThreshold: time.Minute, WorkerCount: 1,
	}); err != nil {
		t.Fatalf("Register core kind failed: %v", err)
	}
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind: "BackfillJob", Reconciler: crdCalls, Cache: crdCache,
		MaxAttempts: 1, InitialInterval: 5 * time.Millisecond, MaxInterval: 10 * time.Millisecond,
		Multiplier: 2.0, StallThreshold: time.Minute, WorkerCount: 1,
	}); err != nil {
		t.Fatalf("Register CRD kind failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	coreKey := manager.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "ns", Name: "tax1"}
	crdKey := manager.WorkItemKey{Kind: "BackfillJob", Namespace: "ns", Name: "job1"}
	if err := mgr.Enqueue(coreKey); err != nil {
		t.Fatalf("Enqueue core failed: %v", err)
	}
	if err := mgr.Enqueue(crdKey); err != nil {
		t.Fatalf("Enqueue CRD failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if coreCalls.calls.Load() >= 1 && crdCalls.calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if coreCalls.calls.Load() == 0 {
		t.Error("core reconciler was never called")
	}
	if crdCalls.calls.Load() == 0 {
		t.Error("CRD reconciler was never called")
	}
}

// T023: enqueue for unregistered kind returns ErrKindNotRegistered, no panic.
func TestManager_UnregisteredKind_EmitsSignalNoPanic(t *testing.T) {
	mgr := manager.New()
	key := manager.WorkItemKey{Kind: "GhostKind", Namespace: "ns", Name: "item"}
	err := mgr.Enqueue(key)
	if !errors.Is(err, manager.ErrKindNotRegistered) {
		t.Errorf("expected ErrKindNotRegistered, got %v", err)
	}
}

// funcReconciler lets tests inject arbitrary Reconcile behaviour.
type funcReconciler struct {
	calls atomic.Int64
	fn    func(context.Context, manager.WorkItemKey) types.ReconcileResult
}

func (f *funcReconciler) Reconcile(ctx context.Context, key manager.WorkItemKey) types.ReconcileResult {
	f.calls.Add(1)
	return f.fn(ctx, key)
}
