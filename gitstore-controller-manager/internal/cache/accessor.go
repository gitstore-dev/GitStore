// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cache

import "github.com/gitstore-dev/gitstore/controller-manager/internal/types"

// CacheAccessor is a read-only view of a per-kind informer cache.
// Reconcilers receive this interface so they cannot mutate the cache.
type CacheAccessor[T any] interface {
	Get(key types.WorkItemKey) (T, bool)
	// List returns all cached objects. Reconcilers that need to enumerate a
	// kind's full population (e.g. hierarchy/cycle computation over a
	// parent-reference adjacency, spec 039) use this instead of Get.
	List() []T
}

// readOnlyCache wraps *Cache[T] and exposes only Get/List, preventing
// type-assertion escapes to the mutable *Cache[T].
type readOnlyCache[T any] struct {
	c *Cache[T]
}

func (r readOnlyCache[T]) Get(key types.WorkItemKey) (T, bool) {
	return r.c.Get(key)
}

func (r readOnlyCache[T]) List() []T {
	return r.c.List()
}

// AsReadOnly returns a CacheAccessor[T] backed by c. The returned value does
// not expose Set, Delete, or MarkSynced.
func AsReadOnly[T any](c *Cache[T]) CacheAccessor[T] {
	return readOnlyCache[T]{c: c}
}
