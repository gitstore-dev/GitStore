// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"sync"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// productCategoryKey scopes a categoryName to its namespace, mirroring
// spec.categoryRef.name resolution scope.
type productCategoryKey struct {
	namespace    string
	categoryName string
}

// ProductCategoryIndex maps (namespace, categoryName) to the set of Product
// cache keys currently referencing that category (data-model.md's "Product
// category index"). It exists so a CategoryTaxonomy create/rename can
// re-enqueue affected Products with an O(1) lookup instead of a full
// Product-cache scan (research.md R3, PR-003/PR-004).
type ProductCategoryIndex struct {
	mu      sync.RWMutex
	byGroup map[productCategoryKey]map[types.WorkItemKey]struct{}
}

// NewProductCategoryIndex returns an empty index.
func NewProductCategoryIndex() *ProductCategoryIndex {
	return &ProductCategoryIndex{byGroup: make(map[productCategoryKey]map[types.WorkItemKey]struct{})}
}

// NewProductCategoryIndexHandler returns a cache.EventHandler[Product] that
// maintains idx from Product cache add/update/delete events: on add it
// records (namespace, categoryRefName); on update it removes the old
// membership and adds the new one when categoryRefName changed; on delete
// it removes the membership. A sibling of NewProductCategoryEnqueueHandler
// (the existing spec-042 count-fan-out handler) — both are registered on the
// same Product cache, independently.
func NewProductCategoryIndexHandler(idx *ProductCategoryIndex) cache.EventHandler[Product] {
	return cache.EventHandler[Product]{
		OnAdd: func(key types.WorkItemKey, p Product) {
			idx.Add(p.Namespace, p.CategoryRefName, key)
		},
		OnUpdate: func(key types.WorkItemKey, old, current Product) {
			if old.CategoryRefName == current.CategoryRefName {
				return
			}
			idx.Remove(old.Namespace, old.CategoryRefName, key)
			idx.Add(current.Namespace, current.CategoryRefName, key)
		},
		OnDelete: func(key types.WorkItemKey, p Product) {
			idx.Remove(p.Namespace, p.CategoryRefName, key)
		},
	}
}

// NewCategoryReenqueueHandler returns a cache.EventHandler[CategoryTaxonomy]
// that re-enqueues previously-unresolved Products when a CategoryTaxonomy
// newly matches their spec.categoryRef (FR-009): on OnAdd, and on an
// OnUpdate where the category's Name changed (a rename), it looks up idx
// for (namespace, name) — and, for a rename, also (namespace, oldName) —
// and calls enqueue for every matching Product key. This is always an O(1)
// map lookup plus O(matching Products) enqueues, never a full Product-cache
// scan (research.md R3, PR-003/PR-004).
func NewCategoryReenqueueHandler(idx *ProductCategoryIndex, enqueue func(types.WorkItemKey)) cache.EventHandler[CategoryTaxonomy] {
	reenqueue := func(namespace, categoryName string) {
		for _, key := range idx.Get(namespace, categoryName) {
			enqueue(key)
		}
	}
	return cache.EventHandler[CategoryTaxonomy]{
		OnAdd: func(_ types.WorkItemKey, c CategoryTaxonomy) {
			reenqueue(c.Namespace, c.Name)
		},
		OnUpdate: func(_ types.WorkItemKey, old, current CategoryTaxonomy) {
			if old.Name == current.Name {
				return
			}
			reenqueue(old.Namespace, old.Name)
			reenqueue(current.Namespace, current.Name)
		},
	}
}

// Add records that key references (namespace, categoryName). A no-op for an
// empty categoryName, since a Product with no categoryRef affects no group.
func (idx *ProductCategoryIndex) Add(namespace, categoryName string, key types.WorkItemKey) {
	if categoryName == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	group := productCategoryKey{namespace: namespace, categoryName: categoryName}
	set, ok := idx.byGroup[group]
	if !ok {
		set = make(map[types.WorkItemKey]struct{})
		idx.byGroup[group] = set
	}
	set[key] = struct{}{}
}

// Remove drops key's membership under (namespace, categoryName). A no-op for
// an empty categoryName or an absent group/key.
func (idx *ProductCategoryIndex) Remove(namespace, categoryName string, key types.WorkItemKey) {
	if categoryName == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	group := productCategoryKey{namespace: namespace, categoryName: categoryName}
	set, ok := idx.byGroup[group]
	if !ok {
		return
	}
	delete(set, key)
	if len(set) == 0 {
		delete(idx.byGroup, group)
	}
}

// Get returns every Product key currently referencing (namespace,
// categoryName), or nil if none.
func (idx *ProductCategoryIndex) Get(namespace, categoryName string) []types.WorkItemKey {
	if categoryName == "" {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	set, ok := idx.byGroup[productCategoryKey{namespace: namespace, categoryName: categoryName}]
	if !ok {
		return nil
	}
	keys := make([]types.WorkItemKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	return keys
}
