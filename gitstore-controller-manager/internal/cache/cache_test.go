// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cache_test

import (
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

var k1 = types.WorkItemKey{Kind: "Product", Namespace: "ns", Name: "p1"}
var k2 = types.WorkItemKey{Kind: "Product", Namespace: "ns", Name: "p2"}

func TestCache_SetGet(t *testing.T) {
	c := cache.New[string]()
	c.Set(k1, "hello")
	got, ok := c.Get(k1)
	if !ok || got != "hello" {
		t.Errorf("got (%q, %v), want (hello, true)", got, ok)
	}
}

func TestCache_Delete(t *testing.T) {
	c := cache.New[string]()
	c.Set(k1, "v")
	c.Delete(k1)
	_, ok := c.Get(k1)
	if ok {
		t.Error("expected item absent after Delete")
	}
}

func TestCache_List(t *testing.T) {
	c := cache.New[int]()
	c.Set(k1, 1)
	c.Set(k2, 2)
	if len(c.List()) != 2 {
		t.Errorf("expected 2, got %d", len(c.List()))
	}
}

func TestCache_OnAdd_Called(t *testing.T) {
	c := cache.New[string]()
	var got types.WorkItemKey
	c.AddEventHandler(cache.EventHandler[string]{
		OnAdd: func(key types.WorkItemKey, _ string) { got = key },
	})
	c.Set(k1, "v")
	if got != k1 {
		t.Errorf("OnAdd not called with correct key: %+v", got)
	}
}

func TestCache_OnUpdate_Called(t *testing.T) {
	c := cache.New[string]()
	var updated bool
	c.AddEventHandler(cache.EventHandler[string]{
		OnUpdate: func(_ types.WorkItemKey, old, new string) {
			if old == "v1" && new == "v2" {
				updated = true
			}
		},
	})
	c.Set(k1, "v1")
	c.Set(k1, "v2")
	if !updated {
		t.Error("OnUpdate was not called")
	}
}

func TestCache_OnDelete_Called(t *testing.T) {
	c := cache.New[string]()
	var deleted bool
	c.AddEventHandler(cache.EventHandler[string]{
		OnDelete: func(_ types.WorkItemKey, _ string) { deleted = true },
	})
	c.Set(k1, "v")
	c.Delete(k1)
	if !deleted {
		t.Error("OnDelete was not called")
	}
}

func TestCache_HasSynced(t *testing.T) {
	c := cache.New[string]()
	if c.HasSynced() {
		t.Error("expected false before MarkSynced")
	}
	c.MarkSynced()
	if !c.HasSynced() {
		t.Error("expected true after MarkSynced")
	}
}

func TestCache_ConcurrentAccess(t *testing.T) {
	c := cache.New[int]()
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			k := types.WorkItemKey{Kind: "X", Namespace: "ns", Name: string(rune('a' + n%26))}
			c.Set(k, n)
			c.Get(k)
		}(i)
	}
	wg.Wait()
}
