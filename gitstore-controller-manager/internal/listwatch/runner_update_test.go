// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type updatePolicyItem struct {
	Name       string
	Generation int64
	Version    string
}

func TestWatchStopUnblocksBackpressuredOutput(t *testing.T) {
	calls := 0
	control := newWatchStop(func() { calls++ })
	events := make(chan WatchEvent[updatePolicyItem], 1)
	events <- WatchEvent[updatePolicyItem]{Type: Bookmark}
	done := make(chan bool, 1)
	go func() { done <- sendWatchEvent(control, events, WatchEvent[updatePolicyItem]{Type: Bookmark}) }()
	control.Stop()
	control.Stop()
	select {
	case sent := <-done:
		if sent || calls != 1 {
			t.Fatalf("backpressured shutdown: sent=%v stop_calls=%d", sent, calls)
		}
	case <-time.After(time.Second):
		t.Fatal("stopping a full watch left its producer blocked")
	}
}

func TestProductListUsesBoundedPagesAndReportsRetryHighWater(t *testing.T) {
	if !strings.Contains(productsListQueryByNamespace, "first: 250,") {
		t.Fatal("Product recovery must honor the 250-row connection limit")
	}
	c := cache.New[updatePolicyItem]()
	if err := c.BeginRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		ctx := context.WithValue(t.Context(), listProgressKey{}, &listProgress{observe: c.ObserveListProgress})
		observeListPage(ctx, 250)
		observeListPage(ctx, 125)
	}
	state := c.RecoveryState()
	if state.Pages != 2 || state.Rows != 375 {
		t.Fatalf("retry duplicated progress: %+v", state)
	}
}

type longRetryListWatch struct{}

func TestCanceledRecoveryRetainsOldCheckpointAndReplayWork(t *testing.T) {
	c := cache.New[updatePolicyItem]()
	key := types.WorkItemKey{Kind: "Product", Name: "pending"}
	c.Set(key, updatePolicyItem{Name: key.Name, Version: "old"})
	c.MarkSynced()
	store := checkpoint.NewMemoryStore()
	r := &Runner[updatePolicyItem]{Kind: "Product", Cache: c, Store: store,
		ListWatcher: longRetryListWatch{}, currentRV: "old-cursor"}
	r.rememberForReplay(key)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.recoverFromExpiry(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery did not honor cancellation: %v", err)
	}
	r.finalFlush(ctx)
	record, err := store.Load(t.Context(), "Product")
	if err != nil || record.ResourceVersion != "old-cursor" || len(record.ReplayKeys) != 1 || record.ReplayKeys[0] != key {
		t.Fatalf("canceled list lost resumable state: %+v, %v", record, err)
	}
	if !c.RecoveryState().Recovering {
		t.Fatal("canceled recovery reopened stale-cache dispatch")
	}
}

func (longRetryListWatch) List(context.Context) (ListResponse[updatePolicyItem], error) {
	return ListResponse[updatePolicyItem]{}, backoff.RetryAfter(16 * 60)
}

func (longRetryListWatch) Watch(context.Context, string) (Watcher[updatePolicyItem], error) {
	return nil, errors.New("watch must not open before list completes")
}

func TestListRetryHasNoFifteenMinuteElapsedLimit(t *testing.T) {
	logs, observed := observer.New(zap.WarnLevel)
	runner := &Runner[updatePolicyItem]{Kind: "Product", ListWatcher: longRetryListWatch{}, Log: zap.New(logs)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runner.retryList(ctx); done <- err }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for observed.Len() == 0 {
		select {
		case err := <-done:
			t.Fatalf("list retry exited instead of waiting beyond 15 minutes: %v", err)
		case <-deadline.C:
			t.Fatal("list retry was not scheduled")
		case <-ticker.C:
		}
	}
	if got := observed.All()[0].ContextMap()["backoff"]; got != 16*time.Minute {
		t.Fatalf("retry delay = %v, want 16 minutes", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("list retry did not stop on cancellation: %v", err)
	}
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
