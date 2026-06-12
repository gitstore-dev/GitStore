// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package cache provides a generic in-memory informer cache for level-triggered reconciliation.
// Reconcilers MUST read resource state from the cache at dispatch time — never from the
// original event payload.
package cache

import (
	"sync"

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
	mu       sync.RWMutex
	store    map[types.WorkItemKey]T
	handlers []EventHandler[T]
	synced   bool
}

// New creates an empty Cache.
func New[T any]() *Cache[T] {
	return &Cache[T]{store: make(map[types.WorkItemKey]T)}
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
func (c *Cache[T]) MarkSynced() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.synced = true
}
