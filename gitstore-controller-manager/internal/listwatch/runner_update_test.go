// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type updatePolicyItem struct {
	Name       string
	Generation int64
	Version    string
}

func TestRunnerModifiedEventUpdatePolicies(t *testing.T) {
	t.Parallel()

	itemCache := cache.New[updatePolicyItem]()
	key := types.WorkItemKey{Kind: "Test", Name: "item"}
	itemCache.Set(key, updatePolicyItem{Name: "item", Generation: 2, Version: "4"})

	var enqueued []types.WorkItemKey
	runner := &Runner[updatePolicyItem]{
		Kind:                "Test",
		Cache:               itemCache,
		KeyFunc:             func(item updatePolicyItem) types.WorkItemKey { return types.WorkItemKey{Kind: "Test", Name: item.Name} },
		RevisionFunc:        func(item updatePolicyItem) string { return item.Version },
		AcceptUpdate:        func(oldObj, newObj updatePolicyItem) bool { return newObj.Generation >= oldObj.Generation },
		ShouldEnqueueUpdate: func(oldObj, newObj updatePolicyItem) bool { return newObj.Generation > oldObj.Generation },
		Enqueue:             func(key types.WorkItemKey) error { enqueued = append(enqueued, key); return nil },
		FlushIntervalEvents: 100,
	}

	err := runner.handleEvent(context.Background(), WatchEvent[updatePolicyItem]{
		Type: Modified, Object: updatePolicyItem{Name: "item", Generation: 2, Version: "5"}, ResourceVersion: "10",
	}, map[types.WorkItemKey]string{})
	if err != nil {
		t.Fatalf("handleEvent(status update) error = %v", err)
	}
	cached, _ := itemCache.Get(key)
	if cached.Version != "5" {
		t.Fatalf("cached status version = %q, want 5", cached.Version)
	}
	if len(enqueued) != 0 {
		t.Fatalf("status update enqueued %d items, want 0", len(enqueued))
	}

	err = runner.handleEvent(context.Background(), WatchEvent[updatePolicyItem]{
		Type: Modified, Object: updatePolicyItem{Name: "item", Generation: 1, Version: "6"}, ResourceVersion: "11",
	}, map[types.WorkItemKey]string{})
	if err != nil {
		t.Fatalf("handleEvent(stale update) error = %v", err)
	}
	cached, _ = itemCache.Get(key)
	if cached.Version != "5" {
		t.Fatalf("stale update replaced cache with version %q", cached.Version)
	}

	err = runner.handleEvent(context.Background(), WatchEvent[updatePolicyItem]{
		Type: Modified, Object: updatePolicyItem{Name: "item", Generation: 3, Version: "7"}, ResourceVersion: "12",
	}, map[types.WorkItemKey]string{})
	if err != nil {
		t.Fatalf("handleEvent(spec update) error = %v", err)
	}
	if len(enqueued) != 1 || enqueued[0] != key {
		t.Fatalf("spec update enqueues = %#v, want [%#v]", enqueued, key)
	}
}
