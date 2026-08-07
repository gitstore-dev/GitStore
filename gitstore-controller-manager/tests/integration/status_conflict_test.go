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
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// TestIntegration_StatusConflict_StaleWriteRejected covers FR-004 scenario 1:
// of two status writes for the same key, the one carrying a resourceVersion
// that is already stale by the time it is submitted must be rejected with
// types.ErrConflict, and only the newer write is recorded as applied.
func TestIntegration_StatusConflict_StaleWriteRejected(t *testing.T) {
	client := newFakeStatusClient()
	key := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w1"}
	client.setResourceVersion(key, "rv-2")

	newGen := int64(2)
	newRev := "main@sha1:new"
	newPatch := &status.StatusPatch{ResourceVersion: "rv-2", ObservedGeneration: &newGen, LastAppliedRevision: &newRev}
	if err := client.Apply(context.Background(), key, newPatch); err != nil {
		t.Fatalf("newer write: unexpected error: %v", err)
	}

	staleGen := int64(1)
	staleRev := "main@sha1:stale"
	stalePatch := &status.StatusPatch{ResourceVersion: "rv-1", ObservedGeneration: &staleGen, LastAppliedRevision: &staleRev}
	err := client.Apply(context.Background(), key, stalePatch)
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("stale write: got err=%v, want types.ErrConflict", err)
	}

	applied := client.appliedPatches(key)
	if len(applied) != 1 {
		t.Fatalf("applied patch count = %d, want 1", len(applied))
	}
	if applied[0] != newPatch {
		t.Errorf("applied patch = %+v, want the newer write %+v", applied[0], newPatch)
	}
}

// statusConflictReconciler simulates a controller that writes status via a
// StatusClient: it applies a patch, and if Apply returns types.ErrConflict,
// treats the conflict as a retryable transient failure rather than a fatal
// one. On its next invocation, it succeeds.
type statusConflictReconciler struct {
	client *fakeStatusClient
	key    types.WorkItemKey
	mu     sync.Mutex
	first  bool
}

func (r *statusConflictReconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	r.mu.Lock()
	resourceVersion := r.client.currentResourceVersion(key)
	if r.first {
		resourceVersion = "rv-stale"
		r.first = false
	}
	r.mu.Unlock()
	gen := int64(1)
	rev := "main@sha1:abc"
	patch := &status.StatusPatch{ResourceVersion: resourceVersion, ObservedGeneration: &gen, LastAppliedRevision: &rev}
	if err := r.client.Apply(ctx, key, patch); err != nil {
		if errors.Is(err, types.ErrConflict) {
			// Re-fetch current state and retry — a conflict is not fatal.
			return types.ResultTransient(err)
		}
		return types.ResultTerminal(err)
	}
	return types.ResultOK()
}

// TestIntegration_StatusConflict_ControllerRetriesAfterConflict covers FR-004
// scenario 2: a reconciler whose status write is rejected with
// types.ErrConflict must be retried by the Manager (not quarantined as a
// terminal failure) and must reach success once the conflict clears.
func TestIntegration_StatusConflict_ControllerRetriesAfterConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US2-ConflictRetry"
	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "w1"}

	client := newFakeStatusClient()
	client.setResourceVersion(key, "rv-current")

	r := &statusConflictReconciler{client: client, key: key, first: true}
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

	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if client.callCount(key) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Allow the second (successful) call to fully settle in the manager.
	time.Sleep(50 * time.Millisecond)

	if got := client.callCount(key); got != 2 {
		t.Fatalf("StatusClient.Apply call count = %d, want exactly 2 (conflict then success)", got)
	}
	if mgr.IsQuarantined(key) {
		t.Error("item was quarantined — a conflict must be retried, not treated as terminal")
	}
	if len(client.appliedPatches(key)) != 1 {
		t.Errorf("applied patch count = %d, want 1 (only the successful call)", len(client.appliedPatches(key)))
	}
}
