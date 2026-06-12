// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package queue_test

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/queue"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func key(name string) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: name}
}

func TestQueue_BasicEnqueueDequeue(t *testing.T) {
	q := queue.New(10, 0)
	defer q.ShutDown()

	k := key("a")
	if err := q.Enqueue(k); err != nil {
		t.Fatal(err)
	}
	got, shutdown := q.Dequeue()
	if shutdown {
		t.Fatal("unexpected shutdown")
	}
	if got != k {
		t.Errorf("got %v, want %v", got, k)
	}
	q.Done(got)
}

func TestQueue_DeduplicatesSameKey(t *testing.T) {
	q := queue.New(10, 0)
	defer q.ShutDown()

	k := key("dup")
	for i := 0; i < 10; i++ {
		_ = q.Enqueue(k)
	}
	if q.Len() != 1 {
		t.Errorf("expected Len 1, got %d", q.Len())
	}
	got, _ := q.Dequeue()
	q.Done(got)
}

func TestQueue_DirtyReenqueue(t *testing.T) {
	q := queue.New(10, 0)
	defer q.ShutDown()

	k := key("dirty")
	_ = q.Enqueue(k)
	got, _ := q.Dequeue()

	// Enqueue again while processing.
	_ = q.Enqueue(k)

	q.Done(got)

	// Should be available once more.
	got2, shutdown := q.Dequeue()
	if shutdown {
		t.Fatal("unexpected shutdown")
	}
	if got2 != k {
		t.Errorf("expected re-enqueued key")
	}
	q.Done(got2)

	if q.Len() != 0 {
		t.Errorf("expected empty, got %d", q.Len())
	}
}

func TestQueue_ShutDownUnblocksDequeue(t *testing.T) {
	q := queue.New(10, 0)
	done := make(chan bool, 1)
	go func() {
		_, shutdown := q.Dequeue()
		done <- shutdown
	}()
	time.Sleep(5 * time.Millisecond)
	q.ShutDown()
	select {
	case v := <-done:
		if !v {
			t.Error("expected shutdown=true")
		}
	case <-time.After(time.Second):
		t.Fatal("Dequeue did not unblock")
	}
}

func TestQueue_EnqueueAfterShutdown(t *testing.T) {
	q := queue.New(10, 0)
	q.ShutDown()
	err := q.Enqueue(key("x"))
	if err == nil {
		t.Fatal("expected error after shutdown")
	}
}
