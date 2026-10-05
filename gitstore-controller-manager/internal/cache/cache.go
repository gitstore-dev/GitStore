// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package cache provides a generic in-memory informer cache for level-triggered reconciliation.
// Reconcilers MUST read resource state from the cache at dispatch time — never from the
// original event payload.
package cache

import (
	"context"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// EventHandler holds callbacks invoked after cache mutations.
type EventHandler[T any] struct {
	OnAdd    func(key types.WorkItemKey, obj T)
	OnUpdate func(key types.WorkItemKey, oldObj, newObj T)
	OnDelete func(key types.WorkItemKey, obj T)
}

// Cache is a generic, thread-safe informer cache.
type Cache[T any] struct {
	mu         sync.RWMutex
	store      map[types.WorkItemKey]T
	handlers   []EventHandler[T]
	synced     bool
	syncedCh   chan struct{}
	recoveryMu sync.Mutex
	recovery   RecoveryState
	recovered  chan struct{}
	drained    chan struct{}
	dispatches int
}

// RecoveryState separates initial cache population from later watch recovery.
type RecoveryState struct {
	Recovering   bool      `json:"recovering"`
	LastProgress time.Time `json:"lastProgress"`
	CompletedAt  time.Time `json:"completedAt"`
	Pages        int64     `json:"pages"`
	Rows         int64     `json:"rows"`
}

func (c *Cache[T]) RecoveryState() RecoveryState {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	return c.recovery
}

// BeginRecovery closes admission before waiting for existing dispatches.
// Cancellation leaves admission closed; only a validated watch may reopen it.
func (c *Cache[T]) BeginRecovery(ctx context.Context) error {
	c.recoveryMu.Lock()
	if !c.recovery.Recovering {
		c.recovery = RecoveryState{Recovering: true, LastProgress: time.Now()}
		c.recovered = make(chan struct{})
	}
	if c.dispatches == 0 {
		c.recoveryMu.Unlock()
		return ctx.Err()
	}
	if c.drained == nil {
		c.drained = make(chan struct{})
	}
	drained := c.drained
	c.recoveryMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-drained:
		return ctx.Err()
	}
}

// ObserveListProgress accepts high-water progress, not repeated retry pages.
func (c *Cache[T]) ObserveListProgress(pages, rows int64) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.recovery.Recovering && pages > c.recovery.Pages {
		c.recovery.Pages = pages
		c.recovery.Rows = rows
		c.recovery.LastProgress = time.Now()
	}
}

func (c *Cache[T]) EndRecovery() {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.recovery.Recovering {
		c.recovery.Recovering = false
		c.recovery.CompletedAt = time.Now()
		close(c.recovered)
	}
}

func (c *Cache[T]) WaitForRecovery(ctx context.Context) error {
	c.recoveryMu.Lock()
	recovering, recovered := c.recovery.Recovering, c.recovered
	c.recoveryMu.Unlock()
	if !recovering {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-recovered:
		return ctx.Err()
	}
}

// AcquireDispatch covers reconciliation and its completion callback, so an
// old dispatch cannot acknowledge work introduced by the replacement snapshot.
func (c *Cache[T]) AcquireDispatch(ctx context.Context) (func(), error) {
	for {
		if err := c.WaitForRecovery(ctx); err != nil {
			return nil, err
		}
		c.recoveryMu.Lock()
		if c.recovery.Recovering {
			c.recoveryMu.Unlock()
			continue
		}
		c.dispatches++
		c.recoveryMu.Unlock()
		return func() {
			c.recoveryMu.Lock()
			defer c.recoveryMu.Unlock()
			c.dispatches--
			if c.dispatches == 0 && c.drained != nil {
				close(c.drained)
				c.drained = nil
			}
		}, nil
	}
}

// Replace publishes the complete replacement before notifying dependent caches.
// The caller transfers ownership of items and must not mutate it afterwards.
func (c *Cache[T]) Replace(items map[types.WorkItemKey]T) {
	c.mu.Lock()
	old, handlers := c.store, c.handlers
	c.store = items
	c.mu.Unlock()
	for key, item := range items {
		previous, existed := old[key]
		for _, h := range handlers {
			if existed && h.OnUpdate != nil {
				h.OnUpdate(key, previous, item)
			} else if !existed && h.OnAdd != nil {
				h.OnAdd(key, item)
			}
		}
	}
	for key, item := range old {
		if _, exists := items[key]; !exists {
			for _, h := range handlers {
				if h.OnDelete != nil {
					h.OnDelete(key, item)
				}
			}
		}
	}
}

// New creates an empty Cache.
func New[T any]() *Cache[T] {
	return &Cache[T]{
		store:    make(map[types.WorkItemKey]T),
		syncedCh: make(chan struct{}),
	}
}

// AddEventHandler registers a callback set. Callbacks fire synchronously after mutations.
func (c *Cache[T]) AddEventHandler(h EventHandler[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers = append(c.handlers, h)
}

// Set stores obj for key, calling OnAdd or OnUpdate handlers.
func (c *Cache[T]) Set(key types.WorkItemKey, obj T) {
	c.mu.Lock()
	old, existed := c.store[key]
	c.store[key] = obj
	handlers := c.handlers
	c.mu.Unlock()

	for _, h := range handlers {
		if existed {
			if h.OnUpdate != nil {
				h.OnUpdate(key, old, obj)
			}
		} else {
			if h.OnAdd != nil {
				h.OnAdd(key, obj)
			}
		}
	}
}

// Get returns the stored object and whether it was found.
func (c *Cache[T]) Get(key types.WorkItemKey) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.store[key]
	return v, ok
}

// Delete removes key from the cache, calling OnDelete handlers.
func (c *Cache[T]) Delete(key types.WorkItemKey) {
	c.mu.Lock()
	old, existed := c.store[key]
	if existed {
		delete(c.store, key)
	}
	handlers := c.handlers
	c.mu.Unlock()

	if existed {
		for _, h := range handlers {
			if h.OnDelete != nil {
				h.OnDelete(key, old)
			}
		}
	}
}

// List returns all stored objects as a slice of values.
func (c *Cache[T]) List() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]T, 0, len(c.store))
	for _, v := range c.store {
		out = append(out, v)
	}
	return out
}

// HasSynced returns true after MarkSynced has been called.
func (c *Cache[T]) HasSynced() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.synced
}

// MarkSynced signals that the cache has completed its initial population.
// Calling it more than once is a no-op.
func (c *Cache[T]) MarkSynced() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.synced {
		c.synced = true
		close(c.syncedCh)
	}
}

// SyncedCh returns a channel that is closed once MarkSynced has been called.
// Callers can select on it instead of polling HasSynced.
func (c *Cache[T]) SyncedCh() <-chan struct{} {
	return c.syncedCh
}
