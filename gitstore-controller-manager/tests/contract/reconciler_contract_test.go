// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/queue"
)

// --- Reconciler interface contract ---

type stubReconciler struct {
	calls atomic.Int64
}

func (s *stubReconciler) Reconcile(_ context.Context, _ manager.WorkItemKey) (manager.Result, error) {
	s.calls.Add(1)
	return manager.Result{}, nil
}

func TestReconcilerInterface_IsCallable(t *testing.T) {
	var r manager.Reconciler = &stubReconciler{}
	key := manager.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w1"}
	result, err := r.Reconcile(context.Background(), key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected zero Result, got %+v", result)
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
