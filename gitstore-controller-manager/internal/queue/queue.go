// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package queue provides a rate-limited, deduplicated work queue.
// It mirrors the k8s client-go workqueue dirty/processing/queue three-set pattern
// without importing k8s machinery.
package queue

import (
	"context"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"golang.org/x/time/rate"
)

// Queue is a rate-limited, deduplicated FIFO work queue.
// A given WorkItemKey will never be dispatched concurrently;
// if the same key is added multiple times before its first Dequeue,
// only one entry is queued.
type Queue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	queue      []types.WorkItemKey
	dirty      map[types.WorkItemKey]struct{}
	processing map[types.WorkItemKey]struct{}
	shutdown   bool
	limiter    *rate.Limiter
	// stopCtx is cancelled by ShutDown so rate-limiter waits can unblock.
	stopCtx    context.Context
	stopCancel context.CancelFunc
}

// New creates a Queue with the given capacity and optional rate limit.
// ratePerSec <= 0 means unlimited.
func New(burst int, ratePerSec float64) *Queue {
	var lim *rate.Limiter
	if ratePerSec > 0 {
		lim = rate.NewLimiter(rate.Limit(ratePerSec), burst)
	} else {
		lim = rate.NewLimiter(rate.Inf, burst)
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		dirty:      make(map[types.WorkItemKey]struct{}),
		processing: make(map[types.WorkItemKey]struct{}),
		limiter:    lim,
		stopCtx:    ctx,
		stopCancel: cancel,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Enqueue adds key to the queue. If the key is already dirty or processing,
// it is recorded as dirty and will be re-queued after Done is called.
// Returns types.ErrQueueShutdown if the queue is shutting down.
func (q *Queue) Enqueue(key types.WorkItemKey) error {
	if err := q.limiter.Wait(q.stopCtx); err != nil {
		return types.ErrQueueShutdown
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.shutdown {
		return types.ErrQueueShutdown
	}

	if _, exists := q.dirty[key]; exists {
		return nil // already pending
	}

	q.dirty[key] = struct{}{}

	if _, processing := q.processing[key]; processing {
		// Will be re-queued after Done.
		return nil
	}

	q.queue = append(q.queue, key)
	q.cond.Signal()
	return nil
}

// Dequeue blocks until a key is available or the queue shuts down.
// Returns (zero, true) when shutdown.
func (q *Queue) Dequeue() (types.WorkItemKey, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.queue) == 0 && !q.shutdown {
		q.cond.Wait()
	}
	if len(q.queue) == 0 {
		return types.WorkItemKey{}, true
	}

	key := q.queue[0]
	q.queue = q.queue[1:]
	delete(q.dirty, key)
	q.processing[key] = struct{}{}
	return key, false
}

// Done marks processing of key as complete. If the key was re-enqueued while
// processing, it is pushed back onto the queue exactly once.
func (q *Queue) Done(key types.WorkItemKey) {
	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.processing, key)

	if _, dirty := q.dirty[key]; dirty {
		q.queue = append(q.queue, key)
		q.cond.Signal()
	}
}

// Len returns the number of keys waiting in the queue (not including in-flight items).
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queue)
}

// ShutDown closes the queue and unblocks all Dequeue and Enqueue callers.
func (q *Queue) ShutDown() {
	q.stopCancel()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shutdown = true
	q.cond.Broadcast()
}

// ShuttingDown reports whether ShutDown has been called.
func (q *Queue) ShuttingDown() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.shutdown
}

// Forget removes key from the dirty set so that a subsequent Done call will
// not re-enqueue it. Call this before quarantining an item to prevent the
// deferred Done from bypassing the quarantine guarantee.
func (q *Queue) Forget(key types.WorkItemKey) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.dirty, key)
}

// RateLimitedEnqueue waits up to timeout for the rate limiter then enqueues key.
func RateLimitedEnqueue(q *Queue, key types.WorkItemKey, timeout time.Duration) error {
	ctx, cancel := contextWithTimeout(timeout)
	defer cancel()
	if err := q.limiter.Wait(ctx); err != nil {
		return err
	}
	return q.Enqueue(key)
}
