// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type enqueueCall struct {
	namespace string
	category  string
}

// T009: OnAdd enqueues the referenced category when CategoryRefName is
// non-empty, and enqueues nothing when it is empty.
func TestProductCategoryEnqueueHandler_OnAdd(t *testing.T) {
	var calls []enqueueCall
	h := NewProductCategoryEnqueueHandler(func(namespace, category string) {
		calls = append(calls, enqueueCall{namespace, category})
	})

	h.OnAdd(types.WorkItemKey{}, Product{Namespace: "acme", Name: "widget", CategoryRefName: "electronics"})
	if len(calls) != 1 || calls[0] != (enqueueCall{"acme", "electronics"}) {
		t.Fatalf("OnAdd with categoryRef: got %+v, want one call to acme/electronics", calls)
	}

	calls = nil
	h.OnAdd(types.WorkItemKey{}, Product{Namespace: "acme", Name: "widget", CategoryRefName: ""})
	if len(calls) != 0 {
		t.Fatalf("OnAdd with empty categoryRef: got %+v, want no calls", calls)
	}
}

// T018: OnDelete enqueues the deleted product's last-known category, and
// enqueues nothing when CategoryRefName was empty.
func TestProductCategoryEnqueueHandler_OnDelete(t *testing.T) {
	var calls []enqueueCall
	h := NewProductCategoryEnqueueHandler(func(namespace, category string) {
		calls = append(calls, enqueueCall{namespace, category})
	})

	h.OnDelete(types.WorkItemKey{}, Product{Namespace: "acme", Name: "widget", CategoryRefName: "electronics"})
	if len(calls) != 1 || calls[0] != (enqueueCall{"acme", "electronics"}) {
		t.Fatalf("OnDelete with categoryRef: got %+v, want one call to acme/electronics", calls)
	}

	calls = nil
	h.OnDelete(types.WorkItemKey{}, Product{Namespace: "acme", Name: "widget", CategoryRefName: ""})
	if len(calls) != 0 {
		t.Fatalf("OnDelete with empty categoryRef: got %+v, want no calls", calls)
	}
}

// T026: OnUpdate enqueues both old and current categories when they differ
// (both non-empty); enqueues nothing when they are equal; enqueues only
// the non-empty side when one of them is empty.
func TestProductCategoryEnqueueHandler_OnUpdate(t *testing.T) {
	var calls []enqueueCall
	h := NewProductCategoryEnqueueHandler(func(namespace, category string) {
		calls = append(calls, enqueueCall{namespace, category})
	})

	// Both non-empty and different: enqueue both.
	h.OnUpdate(types.WorkItemKey{},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: "electronics"},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: "computers"},
	)
	if len(calls) != 2 || calls[0] != (enqueueCall{"acme", "electronics"}) || calls[1] != (enqueueCall{"acme", "computers"}) {
		t.Fatalf("OnUpdate with differing categoryRef: got %+v, want calls to electronics then computers", calls)
	}

	// Unchanged: enqueue nothing.
	calls = nil
	h.OnUpdate(types.WorkItemKey{},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: "electronics"},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: "electronics"},
	)
	if len(calls) != 0 {
		t.Fatalf("OnUpdate with unchanged categoryRef: got %+v, want no calls", calls)
	}

	// Old empty, current non-empty: enqueue only current.
	calls = nil
	h.OnUpdate(types.WorkItemKey{},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: ""},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: "electronics"},
	)
	if len(calls) != 1 || calls[0] != (enqueueCall{"acme", "electronics"}) {
		t.Fatalf("OnUpdate from no-category to categoryRef: got %+v, want one call to electronics", calls)
	}

	// Old non-empty, current empty: enqueue only old.
	calls = nil
	h.OnUpdate(types.WorkItemKey{},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: "electronics"},
		Product{Namespace: "acme", Name: "widget", CategoryRefName: ""},
	)
	if len(calls) != 1 || calls[0] != (enqueueCall{"acme", "electronics"}) {
		t.Fatalf("OnUpdate from categoryRef to no-category: got %+v, want one call to electronics", calls)
	}
}
