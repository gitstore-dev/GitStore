// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func TestInformerCache_SetGetDelete(t *testing.T) {
	c := cache.New[string]()

	key := types.WorkItemKey{Kind: "Product", Namespace: "ns", Name: "p1"}
	c.Set(key, "value-1")

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected item to be present")
	}
	if got != "value-1" {
		t.Errorf("got %q, want %q", got, "value-1")
	}

	c.Delete(key)
	_, ok = c.Get(key)
	if ok {
		t.Fatal("expected item to be absent after Delete")
	}
}

func TestInformerCache_List(t *testing.T) {
	c := cache.New[int]()
	keys := []types.WorkItemKey{
		{Kind: "Product", Namespace: "ns", Name: "a"},
		{Kind: "Product", Namespace: "ns", Name: "b"},
	}
	for i, k := range keys {
		c.Set(k, i)
	}
	all := c.List()
	if len(all) != 2 {
		t.Errorf("expected 2 items, got %d", len(all))
	}
}

func TestInformerCache_EventHandlerFiredOnSet(t *testing.T) {
	c := cache.New[string]()

	added := make(chan types.WorkItemKey, 1)
	c.AddEventHandler(cache.EventHandler[string]{
		OnAdd: func(key types.WorkItemKey, _ string) { added <- key },
	})

	key := types.WorkItemKey{Kind: "Product", Namespace: "ns", Name: "x"}
	c.Set(key, "v")

	select {
	case got := <-added:
		if got != key {
			t.Errorf("handler got wrong key: %+v", got)
		}
	default:
		t.Fatal("OnAdd was not called")
	}
}

func TestInformerCache_EventHandlerFiredOnDelete(t *testing.T) {
	c := cache.New[string]()

	deleted := make(chan types.WorkItemKey, 1)
	c.AddEventHandler(cache.EventHandler[string]{
		OnDelete: func(key types.WorkItemKey, _ string) { deleted <- key },
	})

	key := types.WorkItemKey{Kind: "Product", Namespace: "ns", Name: "y"}
	c.Set(key, "v")
	c.Delete(key)

	select {
	case got := <-deleted:
		if got != key {
			t.Errorf("handler got wrong key: %+v", got)
		}
	default:
		t.Fatal("OnDelete was not called")
	}
}

func TestInformerCache_HasSynced(t *testing.T) {
	c := cache.New[string]()
	if c.HasSynced() {
		t.Fatal("expected HasSynced=false before MarkSynced")
	}
	c.MarkSynced()
	if !c.HasSynced() {
		t.Fatal("expected HasSynced=true after MarkSynced")
	}
}
