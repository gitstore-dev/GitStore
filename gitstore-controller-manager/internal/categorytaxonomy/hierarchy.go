// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// computeHierarchy walks self up to its root via ParentRefName using c
// (research.md R2), builds Path in root-to-self order, and sets Depth to
// len(Path)-1. A missing ancestor (deleted, or simply not yet cached) stops
// the walk early and promotes self to root of whatever chain remains — this
// also naturally bounds a walk through a cycle, since a visited set is
// tracked defensively (a cycle participant's Depth/Path is not meant to be
// trusted by the caller; see the reconciler's cycle-participant handling in
// Reconcile, which skips calling this function entirely per FR-008).
// ChildCount is a linear scan of c for entries whose ParentRefName equals
// self's name; ProductCount is passed in as the caller's already-computed,
// client-side-filtered product count (research.md R4).
func computeHierarchy(c cache.CacheAccessor[CategoryTaxonomy], self CategoryTaxonomy, productCount int64) ResolvedCategoryTaxonomy {
	path := []string{self.Name}
	visited := map[string]struct{}{self.Name: {}}

	cur := self
	for cur.ParentRefName != "" {
		if _, seen := visited[cur.ParentRefName]; seen {
			break
		}
		parent, ok := c.Get(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: self.Namespace, Name: cur.ParentRefName})
		if !ok {
			break
		}
		path = append(path, parent.Name)
		visited[parent.Name] = struct{}{}
		cur = parent
	}
	// path was built self-to-root; reverse to root-to-self.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	var childCount int64
	for _, item := range c.List() {
		if item.Namespace == self.Namespace && item.ParentRefName == self.Name {
			childCount++
		}
	}

	return ResolvedCategoryTaxonomy{
		Depth:        int8(len(path) - 1),
		Path:         path,
		ChildCount:   childCount,
		ProductCount: productCount,
	}
}

// detectCycles reimplements gitstore-api's admission-time three-color DFS
// (internal/admission/catalog.DetectCycles) against a name -> parentRef.name
// adjacency map, rather than importing that package — internal/ packages are
// not importable across Go modules, and this ~20-line algorithm is small
// enough that duplicating it is lighter-weight than a new shared module
// (research.md R3). An empty-string parent means "no parent" (root),
// matching CategoryTaxonomy.ParentRefName's convention.
func detectCycles(parentMap map[string]string) map[string]bool {
	inCycle := make(map[string]bool)
	color := make(map[string]int, len(parentMap))
	var grayStack []string
	var visit func(name string)
	visit = func(name string) {
		if color[name] == 2 {
			return
		}
		if color[name] == 1 {
			for i, n := range grayStack {
				if n == name {
					for _, m := range grayStack[i:] {
						inCycle[m] = true
					}
					break
				}
			}
			return
		}
		parent, inMap := parentMap[name]
		if !inMap || parent == "" {
			color[name] = 2
			return
		}
		color[name] = 1
		grayStack = append(grayStack, name)
		visit(parent)
		grayStack = grayStack[:len(grayStack)-1]
		color[name] = 2
	}
	for name := range parentMap {
		visit(name)
	}
	return inCycle
}
