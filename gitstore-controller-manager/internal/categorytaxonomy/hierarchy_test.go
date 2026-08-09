// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"slices"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func key(ns, name string) types.WorkItemKey {
	return types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: ns, Name: name}
}

func seedCache(t *testing.T, items ...CategoryTaxonomy) cache.CacheAccessor[CategoryTaxonomy] {
	t.Helper()
	c := cache.New[CategoryTaxonomy]()
	for _, item := range items {
		c.Set(key(item.Namespace, item.Name), item)
	}
	return cache.AsReadOnly(c)
}

func TestComputeHierarchy_RootCategory(t *testing.T) {
	root := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}
	c := seedCache(t, root)

	h := computeHierarchy(c, root, 0)

	if h.Depth != 0 {
		t.Errorf("Depth = %d, want 0", h.Depth)
	}
	if !slices.Equal(h.Path, []string{"electronics"}) {
		t.Errorf("Path = %v, want [electronics]", h.Path)
	}
}

func TestComputeHierarchy_ThreeLevelChain(t *testing.T) {
	root := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}
	mid := CategoryTaxonomy{Namespace: "acme", Name: "computers", ParentRefName: "electronics"}
	leaf := CategoryTaxonomy{Namespace: "acme", Name: "laptops", ParentRefName: "computers"}
	c := seedCache(t, root, mid, leaf)

	h := computeHierarchy(c, leaf, 0)

	if h.Depth != 2 {
		t.Errorf("Depth = %d, want 2", h.Depth)
	}
	want := []string{"electronics", "computers", "laptops"}
	if !slices.Equal(h.Path, want) {
		t.Errorf("Path = %v, want %v", h.Path, want)
	}
}

func TestComputeHierarchy_ChildAndProductCounts(t *testing.T) {
	parent := CategoryTaxonomy{Namespace: "acme", Name: "computers"}
	child1 := CategoryTaxonomy{Namespace: "acme", Name: "laptops", ParentRefName: "computers"}
	child2 := CategoryTaxonomy{Namespace: "acme", Name: "desktops", ParentRefName: "computers"}
	other := CategoryTaxonomy{Namespace: "acme", Name: "furniture"}
	c := seedCache(t, parent, child1, child2, other)

	h := computeHierarchy(c, parent, 3)

	if h.ChildCount != 2 {
		t.Errorf("ChildCount = %d, want 2", h.ChildCount)
	}
	if h.ProductCount != 3 {
		t.Errorf("ProductCount = %d, want 3", h.ProductCount)
	}
}

func TestComputeHierarchy_ChildlessProductlessCategoryReportsZeroNotOmitted(t *testing.T) {
	leaf := CategoryTaxonomy{Namespace: "acme", Name: "gadgets"}
	c := seedCache(t, leaf)

	h := computeHierarchy(c, leaf, 0)

	if h.ChildCount != 0 {
		t.Errorf("ChildCount = %d, want 0 (not omitted)", h.ChildCount)
	}
	if h.ProductCount != 0 {
		t.Errorf("ProductCount = %d, want 0 (not omitted)", h.ProductCount)
	}
}

func TestComputeHierarchy_DeletedParent_PromotesOrphanToRoot(t *testing.T) {
	// "laptops" referenced "computers" as its parent, but "computers" is no
	// longer in the cache (deleted) — laptops must be treated as a root.
	orphan := CategoryTaxonomy{Namespace: "acme", Name: "laptops", ParentRefName: "computers"}
	c := seedCache(t, orphan)

	h := computeHierarchy(c, orphan, 0)

	if h.Depth != 0 {
		t.Errorf("Depth = %d, want 0 (promoted to root)", h.Depth)
	}
	if !slices.Equal(h.Path, []string{"laptops"}) {
		t.Errorf("Path = %v, want [laptops]", h.Path)
	}
}

func TestComputeHierarchy_DeletedIntermediateAncestor_RecomputesRelativeToNextAvailable(t *testing.T) {
	// "electronics" (root) -> "computers" (deleted, absent from cache) -> "laptops"
	root := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}
	leaf := CategoryTaxonomy{Namespace: "acme", Name: "laptops", ParentRefName: "computers"}
	c := seedCache(t, root, leaf)

	h := computeHierarchy(c, leaf, 0)

	if h.Depth != 0 {
		t.Errorf("Depth = %d, want 0 (promoted to root since its parent is missing)", h.Depth)
	}
	if !slices.Equal(h.Path, []string{"laptops"}) {
		t.Errorf("Path = %v, want [laptops]", h.Path)
	}
}

// ── Cycle detection (US2, T023) ──────────────────────────────────────────────

func TestDetectCycles_SelfReference(t *testing.T) {
	parentMap := map[string]string{"a": "a"}
	inCycle := detectCycles(parentMap)
	if !inCycle["a"] {
		t.Error("expected 'a' to be detected as a cycle participant")
	}
}

func TestDetectCycles_TwoNodeCycle(t *testing.T) {
	parentMap := map[string]string{"a": "b", "b": "a"}
	inCycle := detectCycles(parentMap)
	if !inCycle["a"] || !inCycle["b"] {
		t.Errorf("expected both 'a' and 'b' to be cycle participants, got %v", inCycle)
	}
}

func TestDetectCycles_AcyclicChainNotFlagged(t *testing.T) {
	parentMap := map[string]string{"root": "", "mid": "root", "leaf": "mid"}
	inCycle := detectCycles(parentMap)
	if inCycle["root"] || inCycle["mid"] || inCycle["leaf"] {
		t.Errorf("expected no cycle participants in an acyclic chain, got %v", inCycle)
	}
}
