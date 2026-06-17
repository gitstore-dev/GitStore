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
	"github.com/gitstore-dev/gitstore/controller-manager/internal/queue"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// --- Reconciler interface contract ---

type stubReconciler struct {
	calls atomic.Int64
}

func (s *stubReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) manager.ReconcileResult {
	s.calls.Add(1)
	return types.ResultOK()
}

// T008: all four ReconcileResult variants satisfy the interface and carry correct fields.
func TestReconcileResult_AllFourVariants(t *testing.T) {
	ok := types.ResultOK()
	if _, is := ok.(types.Success); !is {
		t.Errorf("ResultOK() must return Success, got %T", ok)
	}

	d := 100 * time.Millisecond
	after := types.ResultAfter(d)
	ra, is := after.(types.RequeueAfter)
	if !is {
		t.Errorf("ResultAfter() must return RequeueAfter, got %T", after)
	}
	if ra.After != d {
		t.Errorf("RequeueAfter.After = %v, want %v", ra.After, d)
	}

	sentinelErr := errors.New("transient error")
	transient := types.ResultTransient(sentinelErr)
	tf, is := transient.(types.TransientFailure)
	if !is {
		t.Errorf("ResultTransient() must return TransientFailure, got %T", transient)
	}
	if tf.Err != sentinelErr {
		t.Errorf("TransientFailure.Err = %v, want %v", tf.Err, sentinelErr)
	}
	if tf.BackoffHint != 0 {
		t.Errorf("BackoffHint should be zero without hint arg, got %v", tf.BackoffHint)
	}

	hint := 200 * time.Millisecond
	transientHint := types.ResultTransient(sentinelErr, hint)
	tfh, _ := transientHint.(types.TransientFailure)
	if tfh.BackoffHint != hint {
		t.Errorf("BackoffHint = %v, want %v", tfh.BackoffHint, hint)
	}

	termErr := errors.New("terminal error")
	terminal := types.ResultTerminal(termErr)
	term, is := terminal.(types.TerminalFailure)
	if !is {
		t.Errorf("ResultTerminal() must return TerminalFailure, got %T", terminal)
	}
	if term.Err != termErr {
		t.Errorf("TerminalFailure.Err = %v, want %v", term.Err, termErr)
	}

	// Verify all four satisfy the ReconcileResult interface.
	var _ types.ReconcileResult = ok
	var _ types.ReconcileResult = after
	var _ types.ReconcileResult = transient
	var _ types.ReconcileResult = terminal
}

func TestReconcilerInterface_IsCallable(t *testing.T) {
	var r manager.Reconciler = &stubReconciler{}
	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w1"}
	result := r.Reconcile(context.Background(), key)
	if _, ok := result.(types.Success); !ok {
		t.Errorf("expected Success result, got %T", result)
	}
}

// --- Queue interface contract ---

func TestQueue_DeduplicatesEnqueue(t *testing.T) {
	q := queue.New(100, 0)
	defer q.ShutDown()

	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w1"}

	// Enqueue the same key 5 times before any Dequeue.
	for i := 0; i < 5; i++ {
		if err := q.Enqueue(key); err != nil {
			t.Fatalf("Enqueue failed: %v", err)
		}
	}

	// Should dequeue exactly once.
	got, shutdown := q.Dequeue()
	if shutdown {
		t.Fatal("unexpected shutdown")
	}
	if got != key {
		t.Errorf("got %+v, want %+v", got, key)
	}
	q.Done(got)

	// No second item should be waiting.
	if q.Len() != 0 {
		t.Errorf("expected empty queue after Done, got Len=%d", q.Len())
	}
}

func TestQueue_DirtyReenqueuesAfterDone(t *testing.T) {
	q := queue.New(100, 0)
	defer q.ShutDown()

	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w2"}

	if err := q.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	got, _ := q.Dequeue()

	// Enqueue while processing.
	if err := q.Enqueue(key); err != nil {
		t.Fatalf("Enqueue while processing failed: %v", err)
	}

	q.Done(got) // should re-enqueue because dirty

	// Must be available again exactly once.
	gotAgain, shutdown := q.Dequeue()
	if shutdown {
		t.Fatal("unexpected shutdown")
	}
	if gotAgain != key {
		t.Errorf("expected re-enqueued key, got %+v", gotAgain)
	}
	q.Done(gotAgain)

	if q.Len() != 0 {
		t.Errorf("expected empty queue, got Len=%d", q.Len())
	}
}

func TestQueue_ShutDown_UnblocksDequeue(t *testing.T) {
	q := queue.New(100, 0)

	done := make(chan struct{})
	go func() {
		_, shutdown := q.Dequeue()
		if !shutdown {
			t.Errorf("expected shutdown signal")
		}
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	q.ShutDown()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Dequeue did not unblock after ShutDown")
	}
}

func TestQueue_EnqueueAfterShutDown_ReturnsError(t *testing.T) {
	q := queue.New(100, 0)
	q.ShutDown()

	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w3"}
	err := q.Enqueue(key)
	if err == nil {
		t.Fatal("expected error enqueuing after shutdown, got nil")
	}
}
