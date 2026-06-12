// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func TestQuarantineStore_PutGetDelete(t *testing.T) {
	qs := retry.NewQuarantineStore()

	key := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "bad"}
	item := &retry.PoisonItem{Key: key, Attempts: 5}

	qs.Put(item)

	got, ok := qs.Get(key)
	if !ok {
		t.Fatal("expected item to be present")
	}
	if got.Attempts != 5 {
		t.Errorf("expected 5 attempts, got %d", got.Attempts)
	}

	qs.Delete(key)
	_, ok = qs.Get(key)
	if ok {
		t.Fatal("expected item to be absent after Delete")
	}
}

func TestQuarantineStore_List(t *testing.T) {
	qs := retry.NewQuarantineStore()

	keys := []types.WorkItemKey{
		{Kind: "Widget", Namespace: "ns", Name: "a"},
		{Kind: "Widget", Namespace: "ns", Name: "b"},
		{Kind: "Gadget", Namespace: "ns", Name: "c"},
	}
	for _, k := range keys {
		qs.Put(&retry.PoisonItem{Key: k})
	}

	all := qs.List("")
	if len(all) != 3 {
		t.Errorf("expected 3 items, got %d", len(all))
	}

	widgets := qs.List("Widget")
	if len(widgets) != 2 {
		t.Errorf("expected 2 Widget items, got %d", len(widgets))
	}

	gadgets := qs.List("Gadget")
	if len(gadgets) != 1 {
		t.Errorf("expected 1 Gadget item, got %d", len(gadgets))
	}
}

func TestQuarantineStore_Len(t *testing.T) {
	qs := retry.NewQuarantineStore()
	if qs.Len() != 0 {
		t.Errorf("expected empty store, got Len=%d", qs.Len())
	}
	qs.Put(&retry.PoisonItem{Key: types.WorkItemKey{Kind: "X", Namespace: "ns", Name: "y"}})
	if qs.Len() != 1 {
		t.Errorf("expected Len=1, got %d", qs.Len())
	}
}
