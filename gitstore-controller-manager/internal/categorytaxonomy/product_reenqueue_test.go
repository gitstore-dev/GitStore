// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/assert"
)

func TestCategoryReenqueueHandler_PathCascadeAndCounts(t *testing.T) {
	index := NewProductCategoryIndex()
	key := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
	index.Add("acme", "leaf", key)
	index.Add("other", "leaf", types.WorkItemKey{Kind: "Product", Namespace: "other", Name: "foreign"})
	var enqueued []types.WorkItemKey
	handler := NewCategoryReenqueueHandler(index, func(k types.WorkItemKey) { enqueued = append(enqueued, k) })
	before := CategoryTaxonomy{Namespace: "acme", Name: "leaf", UID: "leaf-id", Generation: 1,
		Status: status.ResourceStatus{Resolved: json.RawMessage(`{"path":["root","leaf"],"productCount":1}`)}}
	current := before
	current.Status.Resolved = json.RawMessage(`{"path":["anchor","root","leaf"],"productCount":1}`)
	handler.OnUpdate(types.WorkItemKey{}, before, current)
	assert.Equal(t, []types.WorkItemKey{key}, enqueued, "a same-generation path cascade must wake only referencing Products")
	enqueued = nil
	before = current
	current.Status.Resolved = json.RawMessage(`{"path":["anchor","root","leaf"],"productCount":2}`)
	handler.OnUpdate(types.WorkItemKey{}, before, current)
	assert.Empty(t, enqueued, "count-only updates must not fan out over Products")
	current.Status.Resolved = json.RawMessage(`{"path":42}`)
	handler.OnUpdate(types.WorkItemKey{}, before, current)
	assert.Equal(t, []types.WorkItemKey{key}, enqueued, "invalid paths must reach reconciliation errors")
	enqueued = nil
	handler.OnDelete(types.WorkItemKey{}, before)
	assert.Equal(t, []types.WorkItemKey{key}, enqueued)
}

// TestCategoryReenqueueHandler_OnAddEnqueuesMatchingProducts covers T026's
// OnAdd case: a newly-created CategoryTaxonomy re-enqueues every Product key
// already indexed under (namespace, name).
func TestCategoryReenqueueHandler_OnAddEnqueuesMatchingProducts(t *testing.T) {
	idx := NewProductCategoryIndex()
	key := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
	idx.Add("acme", "laptops", key)

	var enqueued []types.WorkItemKey
	h := NewCategoryReenqueueHandler(idx, func(k types.WorkItemKey) { enqueued = append(enqueued, k) })

	h.OnAdd(types.WorkItemKey{}, CategoryTaxonomy{Namespace: "acme", Name: "laptops"})
	assert.ElementsMatch(t, []types.WorkItemKey{key}, enqueued)
}

// TestCategoryReenqueueHandler_OnAddNoMatchEnqueuesNothing covers the
// not-yet-referenced case: no index entry, no enqueue.
func TestCategoryReenqueueHandler_OnAddNoMatchEnqueuesNothing(t *testing.T) {
	idx := NewProductCategoryIndex()
	var enqueued []types.WorkItemKey
	h := NewCategoryReenqueueHandler(idx, func(k types.WorkItemKey) { enqueued = append(enqueued, k) })

	h.OnAdd(types.WorkItemKey{}, CategoryTaxonomy{Namespace: "acme", Name: "laptops"})
	assert.Empty(t, enqueued)
}

// TestCategoryReenqueueHandler_RenameEnqueuesBothOldAndNewName covers T026's
// rename case: an OnUpdate where Name changed re-enqueues Products indexed
// under the old name and Products indexed under the new name.
func TestCategoryReenqueueHandler_RenameEnqueuesBothOldAndNewName(t *testing.T) {
	idx := NewProductCategoryIndex()
	oldNameKey := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "stuck-on-old-name"}
	newNameKey := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "already-referencing-new-name"}
	idx.Add("acme", "gadgets", oldNameKey)
	idx.Add("acme", "electronics", newNameKey)

	var enqueued []types.WorkItemKey
	h := NewCategoryReenqueueHandler(idx, func(k types.WorkItemKey) { enqueued = append(enqueued, k) })

	h.OnUpdate(types.WorkItemKey{},
		CategoryTaxonomy{Namespace: "acme", Name: "gadgets"},
		CategoryTaxonomy{Namespace: "acme", Name: "electronics"},
	)
	assert.ElementsMatch(t, []types.WorkItemKey{oldNameKey, newNameKey}, enqueued)
}

// TestCategoryReenqueueHandler_UnchangedNameEnqueuesNothing covers a
// non-rename update: no re-enqueue since nothing newly matches.
func TestCategoryReenqueueHandler_UnchangedNameEnqueuesNothing(t *testing.T) {
	idx := NewProductCategoryIndex()
	key := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
	idx.Add("acme", "laptops", key)

	var enqueued []types.WorkItemKey
	h := NewCategoryReenqueueHandler(idx, func(k types.WorkItemKey) { enqueued = append(enqueued, k) })

	h.OnUpdate(types.WorkItemKey{},
		CategoryTaxonomy{Namespace: "acme", Name: "laptops", ParentRefName: "electronics"},
		CategoryTaxonomy{Namespace: "acme", Name: "laptops", ParentRefName: ""},
	)
	assert.Empty(t, enqueued)
}

// TestCategoryReenqueueHandler_NeverScansProductCache proves PR-003/PR-004:
// the handler only ever consults idx (an O(1) map lookup), never any
// Product-cache List() method — there is no Product cache reference passed
// to NewCategoryReenqueueHandler at all, so a full-scan fallback is
// structurally impossible, not just avoided by convention.
func TestCategoryReenqueueHandler_NeverScansProductCache(t *testing.T) {
	idx := NewProductCategoryIndex()
	for i := 0; i < 10_000; i++ {
		idx.Add("acme", "unrelated-category", types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "product-" + string(rune('a'+i%26))})
	}
	var enqueued []types.WorkItemKey
	h := NewCategoryReenqueueHandler(idx, func(k types.WorkItemKey) { enqueued = append(enqueued, k) })

	h.OnAdd(types.WorkItemKey{}, CategoryTaxonomy{Namespace: "acme", Name: "laptops"})
	assert.Empty(t, enqueued, "an unrelated 10k-entry index must not surface in an unrelated category's re-enqueue")
}
