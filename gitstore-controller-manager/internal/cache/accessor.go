// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cache

import "github.com/gitstore-dev/gitstore/controller-manager/internal/types"

// CacheAccessor is a read-only view of a per-kind informer cache.
// Reconcilers receive this interface so they cannot mutate the cache.
type CacheAccessor[T any] interface {
	Get(key types.WorkItemKey) (T, bool)
}

// readOnlyCache wraps *Cache[T] and exposes only Get, preventing type-assertion
// escapes to the mutable *Cache[T].
type readOnlyCache[T any] struct {
	c *Cache[T]
}

func (r readOnlyCache[T]) Get(key types.WorkItemKey) (T, bool) {
	return r.c.Get(key)
}

// AsReadOnly returns a CacheAccessor[T] backed by c. The returned value does
// not expose Set, Delete, or MarkSynced.
func AsReadOnly[T any](c *Cache[T]) CacheAccessor[T] {
	return readOnlyCache[T]{c: c}
}
