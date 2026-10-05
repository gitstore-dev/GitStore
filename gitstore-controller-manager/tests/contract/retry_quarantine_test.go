// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/api"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

type diskReconcileFunc func(context.Context, types.WorkItemKey) types.ReconcileResult

func (f diskReconcileFunc) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	return f(ctx, key)
}

func diskManagerStore(t *testing.T, rows int) *checkpoint.DiskStore {
	t.Helper()
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Widget")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	generation, err := store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	for start := 0; start < rows; start += checkpoint.DiskPageItems {
		var page []checkpoint.DiskItem
		for i := start; i < min(rows, start+checkpoint.DiskPageItems); i++ {
			page = append(page, checkpoint.DiskItem{
				Key:     types.WorkItemKey{Kind: "Widget", Namespace: "shop", Name: fmt.Sprintf("work-%05d", i)},
				Version: "1", Value: json.RawMessage(`{"ready":true}`),
			})
		}
		require.NoError(t, store.PutSnapshotPage(t.Context(), generation, page))
	}
	require.NoError(t, store.FinishSnapshot(t.Context(), generation, "snapshot"))
	return store
}

func TestDiskManagerCredentialOutagePreservesWorkUntilRecovery(t *testing.T) {
	store := diskManagerStore(t, 1)
	var ready atomic.Bool
	var calls atomic.Int32
	reconciler := diskReconcileFunc(func(context.Context, types.WorkItemKey) types.ReconcileResult {
		calls.Add(1)
		if !ready.Load() {
			return types.ResultTransient(fmt.Errorf("shared provider outage: %w", types.ErrCredentialsUnavailable))
		}
		return types.ResultOK()
	})
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Widget", Cache: newSyncedCache(), Disk: store, Reconciler: reconciler,
		WorkerCount: 1, MaxAttempts: 1, StallThreshold: time.Minute,
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 3*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, store.Counts().Pending)
	require.Zero(t, store.Counts().Poison)
	ready.Store(true)
	require.Eventually(t, func() bool { return store.Counts().Pending == 0 }, 3*time.Second, 10*time.Millisecond)
	require.Zero(t, store.Counts().Poison)
}

func TestDiskManagerBoundsDispatchAndPreservesNewerInflightWork(t *testing.T) {
	store := diskManagerStore(t, 5000)
	gate := newSyncedCache()
	release := make(chan struct{})
	started := make(chan types.WorkItemKey, 2)
	live := make(chan struct{}, 1)
	var active, peak, firstCalls atomic.Int32
	reconciler := diskReconcileFunc(func(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		if key.Name == "work-00000" {
			firstCalls.Add(1)
		}
		if key.Name == "live" {
			select {
			case live <- struct{}{}:
			default:
			}
		}
		select {
		case started <- key:
		default:
		}
		select {
		case <-ctx.Done():
			return types.ResultTransient(ctx.Err())
		case <-release:
			return types.ResultOK()
		}
	})
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Widget", Cache: gate, Disk: store, Reconciler: reconciler, WorkerCount: 2,
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("bounded workers did not start")
		}
	}
	stats := mgr.KindStats()["Widget"]
	require.EqualValues(t, 2, stats.ActiveWorkers)
	require.Equal(t, 4998, stats.QueueDepth)
	require.Empty(t, gate.List(), "the runtime gate must not retain the catalog")
	require.NoError(t, mgr.Enqueue(types.WorkItemKey{Kind: "Widget", Namespace: "shop", Name: "work-00000"}))
	require.NoError(t, mgr.Enqueue(types.WorkItemKey{Kind: "Widget", Namespace: "shop", Name: "live"}))
	close(release)
	select {
	case <-live:
	case <-time.After(3 * time.Second):
		t.Fatal("live work was starved behind the snapshot backlog")
	}
	require.Eventually(t, func() bool { return firstCalls.Load() >= 2 }, 3*time.Second, time.Millisecond,
		"old completion erased the updated in-flight key")
	require.LessOrEqual(t, peak.Load(), int32(2))
}

func TestDiskDeferredWorkDoesNotReportAnIdleControllerAsStalled(t *testing.T) {
	store := diskManagerStore(t, 1)
	started := make(chan struct{}, 1)
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Widget", Cache: newSyncedCache(), Disk: store, WorkerCount: 1, StallThreshold: 20 * time.Millisecond,
		Reconciler: diskReconcileFunc(func(context.Context, types.WorkItemKey) types.ReconcileResult {
			started <- struct{}{}
			return types.ResultAfter(time.Hour)
		}),
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("deferred work did not start")
	}
	after := time.Now().Add(50 * time.Millisecond)
	require.Eventually(t, func() bool {
		stat := mgr.KindStats()["Widget"]
		return time.Now().After(after) && stat.QueueDepth == 1 && stat.ActiveWorkers == 0 && !stat.Stalled
	}, time.Second, time.Millisecond)
}

func TestDiskPoisonAPIIsPaginatedAndReportsStorageFailure(t *testing.T) {
	store := diskManagerStore(t, 3)
	work, err := store.Pending(t.Context(), "", 3)
	require.NoError(t, err)
	for _, item := range work {
		ok, err := store.DeferWork(t.Context(), item.Key, item.Token, 0, "test failure", 2)
		require.NoError(t, err)
		require.True(t, ok)
	}
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Widget", Cache: newSyncedCache(), Disk: store, Reconciler: &alwaysFailReconciler{},
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /controller/v1/poison/{kind}", api.ListPoisonHandler(mgr))
	cursor, count := "", 0
	for range 4 {
		request := httptest.NewRequest(http.MethodGet, "/controller/v1/poison/Widget?limit=1&after="+cursor, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		var page []*retry.PoisonItem
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		require.LessOrEqual(t, len(page), 1)
		count += len(page)
		cursor = response.Header().Get("X-Next-Cursor")
		if cursor == "" {
			break
		}
	}
	require.Equal(t, 3, count)
	require.NoError(t, store.Close())
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/controller/v1/poison/Widget", nil))
	require.Equal(t, http.StatusServiceUnavailable, response.Code, "disk failure must not look like an empty poison list")
}

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

// TestManager_IdleKindIsNotStalled verifies that a quiet registered kind does
// not degrade health solely because its last successful reconcile is old.
func TestManager_IdleKindIsNotStalled(t *testing.T) {
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
	} else if s.Stalled {
		t.Error("expected Stalled=false after the successful work item became idle")
	}
}

// TestPoisonAll_AggregatesAcrossKinds verifies GET /controller/v1/poison/_all
// returns items from all registered kinds (P2b fix).
func TestPoisonAll_AggregatesAcrossKinds(t *testing.T) {
	mgr := manager.New()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind: "Alpha", Reconciler: &alwaysFailReconciler{}, Cache: newSyncedCache(),
		MaxAttempts: 1, InitialInterval: 1 * time.Millisecond,
		MaxInterval: 1 * time.Millisecond, Multiplier: 1, StallThreshold: time.Minute, WorkerCount: 1,
	}); err != nil {
		t.Fatalf("Register Alpha failed: %v", err)
	}
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind: "Beta", Reconciler: &alwaysFailReconciler{}, Cache: newSyncedCache(),
		MaxAttempts: 1, InitialInterval: 1 * time.Millisecond,
		MaxInterval: 1 * time.Millisecond, Multiplier: 1, StallThreshold: time.Minute, WorkerCount: 1,
	}); err != nil {
		t.Fatalf("Register Beta failed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	for _, key := range []types.WorkItemKey{
		{Kind: "Alpha", Namespace: "ns", Name: "a1"},
		{Kind: "Beta", Namespace: "ns", Name: "b1"},
		{Kind: "Beta", Namespace: "ns", Name: "b2"},
	} {
		require.NoError(t, mgr.Enqueue(key))
	}
	require.Eventually(t, func() bool {
		items, _, err := mgr.ListPoisonPage(ctx, "_all", "", 256)
		return err == nil && len(items) == 3
	}, time.Second, time.Millisecond)

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
