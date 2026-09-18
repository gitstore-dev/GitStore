// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/assert"
)

func TestProductCategoryIndex_AddGetRemove(t *testing.T) {
	idx := NewProductCategoryIndex()
	key := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}

	assert.Empty(t, idx.Get("acme", "laptops"))

	idx.Add("acme", "laptops", key)
	assert.ElementsMatch(t, []types.WorkItemKey{key}, idx.Get("acme", "laptops"))

	idx.Remove("acme", "laptops", key)
	assert.Empty(t, idx.Get("acme", "laptops"))
}

func TestProductCategoryIndex_EmptyCategoryNameIsNoOp(t *testing.T) {
	idx := NewProductCategoryIndex()
	key := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}

	idx.Add("acme", "", key)
	assert.Empty(t, idx.Get("acme", ""))
	idx.Remove("acme", "", key)
}

func TestProductCategoryIndex_ScopedByNamespace(t *testing.T) {
	idx := NewProductCategoryIndex()
	acmeKey := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
	otherKey := types.WorkItemKey{Kind: "Product", Namespace: "other", Name: "widget"}

	idx.Add("acme", "laptops", acmeKey)
	idx.Add("other", "laptops", otherKey)

	assert.ElementsMatch(t, []types.WorkItemKey{acmeKey}, idx.Get("acme", "laptops"))
	assert.ElementsMatch(t, []types.WorkItemKey{otherKey}, idx.Get("other", "laptops"))
}

func TestProductCategoryIndex_MultipleProductsSameCategory(t *testing.T) {
	idx := NewProductCategoryIndex()
	first := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget-1"}
	second := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget-2"}

	idx.Add("acme", "laptops", first)
	idx.Add("acme", "laptops", second)
	assert.ElementsMatch(t, []types.WorkItemKey{first, second}, idx.Get("acme", "laptops"))

	idx.Remove("acme", "laptops", first)
	assert.ElementsMatch(t, []types.WorkItemKey{second}, idx.Get("acme", "laptops"))
}

// TestProductCategoryIndex_CategoryRefChangeLeavesNoStaleMembership covers a
// Product's categoryRef changing from one category to another: the caller is
// expected to Remove the old (namespace, oldName) membership and Add the new
// (namespace, newName) membership (mirroring the OnUpdate handling
// registerProductWatch's Product-cache event handler performs). This test
// confirms the index itself leaves no stale entry under the old name once
// that sequence runs.
func TestProductCategoryIndex_CategoryRefChangeLeavesNoStaleMembership(t *testing.T) {
	idx := NewProductCategoryIndex()
	key := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}

	idx.Add("acme", "laptops", key)
	idx.Remove("acme", "laptops", key)
	idx.Add("acme", "phones", key)

	assert.Empty(t, idx.Get("acme", "laptops"))
	assert.ElementsMatch(t, []types.WorkItemKey{key}, idx.Get("acme", "phones"))
}
