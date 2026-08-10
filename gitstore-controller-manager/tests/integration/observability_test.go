// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Row 5 of contracts/runbook-signal-contract.md
// (gitstore_controller_checkpoint_last_write_timestamp_seconds and
// gitstore_controller_checkpoint_replay_backlog) is already covered by
// TestHealth_CheckpointLastWriteTimestamp_UpdatesOnSave and
// TestHealth_CheckpointReplayBacklog_TracksQueueDepth in
// tests/contract/health_test.go — cross-referenced here, not duplicated.
package integration_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/api"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestObservability_QueueDepth_ReflectsPendingItems and
// TestObservability_ActiveWorkers_ReflectsRunningReconciles cover
// contracts/runbook-signal-contract.md rows 1-2 (controller-lag.md).
//
// Queue depth includes both the manager queue and tasks waiting inside the
// worker pool, so it remains meaningful after the dispatch loop has submitted
// work to a saturated pool.
func TestObservability_QueueDepth_ReflectsPendingItems(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US5-QueueDepth"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	r := &blockingUntilReleased{started: started, release: release}
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

	go func() { _ = mgr.Start(ctx) }()

	const total = 4
	for i := range total {
		key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "pending" + strconv.Itoa(i)}
		if err := mgr.Enqueue(key); err != nil {
			t.Fatalf("Enqueue failed: %v", err)
		}
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler never started")
	}

	wantPending := total - 1 // one running worker; the rest wait in Pond
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mgr.KindStats()
		if testutil.ToFloat64(health.QueueDepth.WithLabelValues(kind)) == float64(wantPending) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("QueueDepth{%s} = %v, want %d waiting tasks while pool is saturated", kind, testutil.ToFloat64(health.QueueDepth.WithLabelValues(kind)), wantPending)
}

func TestObservability_ActiveWorkers_ReflectsRunningReconciles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US5-ActiveWorkers"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	r := &blockingUntilReleased{started: started, release: release}

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

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "w1"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler never started")
	}

	mgr.KindStats() // refresh the gauge
	if v := testutil.ToFloat64(health.ActiveWorkers.WithLabelValues(kind)); v != 1 {
		t.Errorf("ActiveWorkers{%s} while reconcile in flight = %v, want 1", kind, v)
	}
	close(release)
}

// blockingUntilReleased signals started on its first call and blocks until
// release is closed, then returns Success. Used to hold a worker "active"
// long enough to observe the ActiveWorkers gauge mid-reconcile.
type blockingUntilReleased struct {
	started chan struct{}
	release chan struct{}
	fired   bool
}

func (b *blockingUntilReleased) Reconcile(_ context.Context, _ types.WorkItemKey) types.ReconcileResult {
	if !b.fired {
		b.fired = true
		b.started <- struct{}{}
	}
	<-b.release
	return types.ResultOK()
}

// TestObservability_StalledWorkers_SetWhenNoRecentSuccess and
// TestObservability_ReconcileTotal_LabeledByOutcome cover
// contracts/runbook-signal-contract.md rows 3-4 (controller-lag.md).
func TestObservability_StalledWorkers_SetWhenNoRecentSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US5-Stalled"
	// No reconcile ever succeeds: the manager must still mark the kind stalled
	// after the threshold, and a /metrics scrape must refresh the gauge.
	r := newScriptedReconciler(types.ResultTransient(errors.New("persistent failure")))
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
		StallThreshold:  50 * time.Millisecond,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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

	if r.callCount() == 0 {
		t.Fatal("reconciler was never called")
	}

	time.Sleep(100 * time.Millisecond)
	rec := httptest.NewRecorder()
	health.NewMetricsHandler(mgr).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /metrics status = %d, want 200", rec.Code)
	}
	if v := testutil.ToFloat64(health.StalledWorkers.WithLabelValues(kind)); v != 1 {
		t.Errorf("StalledWorkers{%s} after scrape and no successful reconcile = %v, want 1", kind, v)
	}
}

func TestObservability_ReconcileTotal_LabeledByOutcome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US5-ReconcileTotalLabels"
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
		if testutil.ToFloat64(health.ReconcileTotal.WithLabelValues(kind, "success")) > before {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	after := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues(kind, "success"))
	if after != before+1 {
		t.Errorf("ReconcileTotal{%s,success} delta = %v, want 1 (labeled correctly by kind and outcome)", kind, after-before)
	}
}

// TestObservability_WatchExpired_LogsRelistTrigger covers
// contracts/runbook-signal-contract.md row 6 (controller-replay-window-exceeded.md).
func TestObservability_WatchExpired_LogsRelistTrigger(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	item := widget{Namespace: "ns", Name: "a", ResourceVersion: "1"}
	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{Items: []widget{item}, ResourceVersion: "100"},
	}
	expiredWatcher := newStubWatcher[widget](nil, listwatch.ErrWatchExpired)
	expiredWatcher.closeNow()
	finalWatcher := newStubWatcher[widget](nil, nil)
	finalWatcher.closeNow()
	lw.watchers = []*stubWatcher[widget]{expiredWatcher, finalWatcher}

	r, _, _ := newRunner(t, lw, store)
	core, logs := observer.New(zap.WarnLevel)
	r.Log = zap.New(core)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(800 * time.Millisecond)
	for {
		found := false
		for _, entry := range logs.All() {
			if entry.Message == "watch cursor expired; re-listing" {
				found = true
				break
			}
		}
		if found {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected a 'watch cursor expired; re-listing' log line after ErrWatchExpired")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// TestObservability_PoisonItemsTotal_IncrementsOnQuarantine and
// TestObservability_RequeuePoisonAPI_ClearsQuarantineAndReenqueues cover
// contracts/runbook-signal-contract.md rows 7 and 9 (controller-poisoned-item.md).
func TestObservability_PoisonItemsTotal_IncrementsOnQuarantine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US5-PoisonItemsTotal"
	r := newScriptedReconciler(types.ResultTerminal(errors.New("bad data")))
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

	before := testutil.ToFloat64(health.PoisonItemsTotal.WithLabelValues(kind))
	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "w1"}
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
	mgr.KindStats()
	after := testutil.ToFloat64(health.PoisonItemsTotal.WithLabelValues(kind))
	if after != before+1 {
		t.Errorf("PoisonItemsTotal{%s} delta = %v, want 1", kind, after-before)
	}
}

func TestObservability_RequeuePoisonAPI_ClearsQuarantineAndReenqueues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US5-RequeueAPI"
	r := newScriptedReconciler(types.ResultTerminal(errors.New("bad data")), types.ResultOK())
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

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "w1"}
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
		t.Fatal("expected item to be quarantined before requeue")
	}

	// api.RequeuePoisonHandler wraps Manager.Requeue — exercised directly
	// here (poison_item_test.go already covers the HTTP-layer wiring).
	if err := mgr.Requeue(key); err != nil {
		t.Fatalf("Requeue failed: %v", err)
	}
	if mgr.IsQuarantined(key) {
		t.Fatal("expected Requeue to clear quarantine immediately")
	}
	// Use the shared api.Requeuer type to confirm the Manager still
	// satisfies the interface the poison HTTP handlers depend on.
	var _ api.Requeuer = mgr

	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) {
		if r.callCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.callCount(); got < 2 {
		t.Fatalf("reconciler call count after requeue = %d, want at least 2", got)
	}
}

// TestObservability_QuarantineLog_IncludesLastError covers
// contracts/runbook-signal-contract.md row 10 (controller-poisoned-item.md).
func TestObservability_QuarantineLog_IncludesLastError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US5-QuarantineLog"
	wantErr := errors.New("distinctive bad data error")
	r := newScriptedReconciler(types.ResultTerminal(wantErr))
	core, logs := observer.New(zap.ErrorLevel)
	mgr := manager.New().WithLogger(zap.New(core))
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

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "w1"}
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
		t.Fatal("expected item to be quarantined")
	}

	found := false
	for _, entry := range logs.All() {
		if entry.Message != "terminal reconcile failure — quarantining immediately" {
			continue
		}
		for _, f := range entry.Context {
			if f.Key == "error" && f.Interface != nil {
				if errStr, ok := f.Interface.(error); ok && errStr.Error() == wantErr.Error() {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected quarantine log line to include the reconciler's error text")
	}
}
