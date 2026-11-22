// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type updatePolicyPages struct {
	longRetryListWatch
	items []updatePolicyItem
}

func (pages updatePolicyPages) ListPages(_ context.Context, visit func([]updatePolicyItem) error) (string, error) {
	if err := visit(pages.items); err != nil {
		return "", err
	}
	return "journal@two", nil
}

func TestDiskObserverRetainsProjectionAndHandoffWithoutOwnWork(t *testing.T) {
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Collection")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	handoffs := 0
	runner := Runner[updatePolicyItem]{Kind: "Collection", Disk: store, Cache: cache.New[updatePolicyItem](),
		DisableReplay: true,
		KeyFunc: func(item updatePolicyItem) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Collection", Namespace: "shop", Name: item.Name}
		},
		RevisionFunc: func(item updatePolicyItem) string { return item.Version },
		BeforeDiskChange: func(context.Context, *updatePolicyItem, *updatePolicyItem) error {
			handoffs++
			return nil
		}}
	runner.applyDefaults()
	check := func() {
		t.Helper()
		if counts := store.Counts(); counts.Pending != 0 || counts.Poison != 0 {
			t.Fatalf("observer generated its own reconciliation work: %+v", counts)
		}
	}
	initial := updatePolicyItem{Name: "collection", Generation: 1, Version: "1"}
	if err := runner.listToDisk(t.Context(), updatePolicyPages{items: []updatePolicyItem{initial}}); err != nil {
		t.Fatal(err)
	}
	check()
	next := initial
	next.Version = "2"
	if err := runner.applyDiskEvent(t.Context(), WatchEvent[updatePolicyItem]{Type: Modified, Object: next, ResourceVersion: "journal@three"}); err != nil {
		t.Fatal(err)
	}
	check()
	if err := runner.listToDisk(t.Context(), updatePolicyPages{}); err != nil {
		t.Fatal(err)
	}
	check()
	if handoffs != 3 {
		t.Fatalf("observer handoffs=%d, want creation, update and cold-relist removal", handoffs)
	}
}

func TestDiskUnchangedVersionCanCarryDurableRevalidation(t *testing.T) {
	for _, requeue := range []bool{false, true} {
		t.Run(fmt.Sprintf("requeue=%t", requeue), func(t *testing.T) {
			dir := t.TempDir()
			store, err := checkpoint.OpenDiskStore(dir, "Translation")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			source := updatePolicyItem{Name: "de-title", Version: "4", Generation: 2}
			key := types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: source.Name}
			runner := Runner[updatePolicyItem]{Kind: "Translation", Disk: store, Cache: cache.New[updatePolicyItem](),
				RequeueUnchanged: requeue,
				KeyFunc:          func(updatePolicyItem) types.WorkItemKey { return key },
				RevisionFunc:     func(item updatePolicyItem) string { return item.Version }}
			runner.applyDefaults()
			generation, err := store.BeginSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishSnapshot(t.Context(), generation, "journal@zero"); err != nil {
				t.Fatal(err)
			}
			if err := runner.applyDiskEvent(t.Context(), WatchEvent[updatePolicyItem]{Type: Added, Object: source, ResourceVersion: "journal@one"}); err != nil {
				t.Fatal(err)
			}
			work, err := store.Pending(t.Context(), "", 1)
			if err != nil || len(work) != 1 {
				t.Fatalf("initial work=%v, err=%v", work, err)
			}
			if _, err := store.Acknowledge(t.Context(), key, work[0].Token); err != nil {
				t.Fatal(err)
			}
			if err := runner.applyDiskEvent(t.Context(), WatchEvent[updatePolicyItem]{Type: Modified, Object: source, ResourceVersion: "journal@two"}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = checkpoint.OpenDiskStore(dir, "Translation")
			if err != nil {
				t.Fatal(err)
			}
			work, err = store.Pending(t.Context(), "", 1)
			if err != nil || (len(work) == 1) != requeue {
				t.Fatalf("replacement pending=%v, requeue=%t, err=%v", work, requeue, err)
			}
			_, cursor, err := store.Active(t.Context())
			if err != nil || cursor != "journal@two" {
				t.Fatalf("cursor=%q, err=%v", cursor, err)
			}
			if requeue && work[0].Token == 0 {
				t.Fatal("revalidation must retain a real acknowledgement token")
			}
		})
	}
}

func TestDiskRelistDependencyHandoffPrecedesSnapshotPublication(t *testing.T) {
	for _, kind := range []string{"Product", "ProductVariant", "Category", "Collection", "File"} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%t", kind, restart), func(t *testing.T) {
				dir := t.TempDir()
				store, err := checkpoint.OpenDiskStore(dir, kind)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if store != nil {
						if err := store.Close(); err != nil {
							t.Error(err)
						}
					}
				})
				runner := Runner[updatePolicyItem]{
					Kind: kind, Disk: store, Cache: cache.New[updatePolicyItem](),
					KeyFunc: func(item updatePolicyItem) types.WorkItemKey {
						return types.WorkItemKey{Kind: kind, Namespace: "tenant", Name: item.Name}
					},
					RevisionFunc: func(item updatePolicyItem) string { return item.Version },
				}
				runner.applyDefaults()
				generation, err := store.BeginSnapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				var originals []checkpoint.DiskItem
				for _, name := range []string{"changed", "removed", "unchanged"} {
					item, err := runner.diskItem(updatePolicyItem{Name: name, Generation: 1, Version: "1"})
					if err != nil {
						t.Fatal(err)
					}
					originals = append(originals, item)
				}
				if err := store.PutSnapshotPage(t.Context(), generation, originals); err != nil {
					t.Fatal(err)
				}
				if err := store.FinishSnapshot(t.Context(), generation, "journal@one"); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				injected, seen := false, make(map[string]bool)
				runner.BeforeDiskChange = func(ctx context.Context, previous, current *updatePolicyItem) error {
					_, cursor, err := store.Active(ctx)
					if err != nil || cursor != "journal@one" {
						t.Fatalf("snapshot published before dependency handoff: cursor=%q err=%v", cursor, err)
					}
					var name string
					if current != nil {
						name = current.Name
					} else {
						name = previous.Name
					}
					switch name {
					case "added":
						if previous != nil || current.Version != "2" {
							t.Fatalf("incorrect creation handoff: previous=%+v current=%+v", previous, current)
						}
					case "changed":
						if previous == nil || previous.Version != "1" || current.Version != "2" {
							t.Fatalf("incorrect update handoff: previous=%+v current=%+v", previous, current)
						}
					case "removed":
						if previous == nil || previous.Version != "1" || current != nil {
							t.Fatalf("incorrect deletion handoff: previous=%+v current=%+v", previous, current)
						}
					default:
						t.Fatalf("unchanged resource produced a dependency change: %q", name)
					}
					if !injected {
						injected = true
						if restart {
							cancel()
						}
						return errors.New("injected dependency checkpoint failure")
					}
					seen[name] = true
					return nil
				}
				pages := updatePolicyPages{items: []updatePolicyItem{
					{Name: "added", Generation: 2, Version: "2"},
					{Name: "changed", Generation: 2, Version: "2"},
					{Name: "unchanged", Generation: 1, Version: "1"},
				}}
				err = runner.listToDisk(ctx, pages)
				if restart {
					if !errors.Is(err, context.Canceled) || runner.Cache.HasSynced() {
						t.Fatalf("failed handoff published the snapshot: synced=%t err=%v", runner.Cache.HasSynced(), err)
					}
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store = nil
					store, err = checkpoint.OpenDiskStore(dir, kind)
					if err != nil {
						t.Fatal(err)
					}
					runner.Disk = store
					err = runner.listToDisk(t.Context(), pages)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, cursor, err := store.Active(t.Context())
				if err != nil || cursor != "journal@two" || !injected || len(seen) != 3 || !runner.Cache.HasSynced() {
					t.Fatalf("relist lost dependency handoffs: cursor=%q injected=%t seen=%v err=%v",
						cursor, injected, seen, err)
				}
			})
		}
	}
}

func TestDiskDependencyHandoffPrecedesProjectionAndCursor(t *testing.T) {
	for _, test := range []struct {
		name    string
		event   EventType
		version string
	}{
		{"created", Added, "2"}, {"modified", Modified, "2"},
		{"deleted", Deleted, "2"}, {"reused-source-version", Modified, "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			eventType := test.event
			ctx := t.Context()
			store, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			key := types.WorkItemKey{Kind: "Product", Namespace: "tenant", Name: "target"}
			original := updatePolicyItem{Name: key.Name, Generation: 1, Version: "1"}
			generation, err := store.BeginSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if eventType != Added {
				if err := store.PutSnapshotPage(ctx, generation, []checkpoint.DiskItem{{
					Key: key, Version: "1", Value: json.RawMessage(`{"Name":"target","Generation":1,"Version":"1"}`),
				}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.FinishSnapshot(ctx, generation, "journal@one"); err != nil {
				t.Fatal(err)
			}
			blocked := errors.New("dependency checkpoint unavailable")
			fail, calls := true, 0
			runner := Runner[updatePolicyItem]{
				Kind: "Product", Disk: store,
				KeyFunc:      func(updatePolicyItem) types.WorkItemKey { return key },
				RevisionFunc: func(item updatePolicyItem) string { return item.Version },
				BeforeDiskChange: func(_ context.Context, previous, current *updatePolicyItem) error {
					calls++
					if eventType == Added {
						if previous != nil {
							t.Fatalf("new target unexpectedly had prior state: %+v", previous)
						}
					} else if previous == nil || *previous != original {
						t.Fatalf("handoff lost authoritative prior projection: %+v", previous)
					}
					if (current == nil) != (eventType == Deleted) {
						t.Fatalf("incorrect deletion handoff: %+v", current)
					}
					if current != nil && current.Generation != 2 {
						t.Fatalf("handoff lost the incoming target change: %+v", current)
					}
					if fail {
						return blocked
					}
					return nil
				},
			}
			value := updatePolicyItem{Name: key.Name, Generation: 2, Version: test.version}
			if eventType == Deleted {
				value = updatePolicyItem{Name: key.Name}
			}
			event := WatchEvent[updatePolicyItem]{Type: eventType, Object: value, ResourceVersion: "journal@two"}
			if err := runner.applyDiskEvent(ctx, event); !errors.Is(err, blocked) {
				t.Fatalf("handoff failure was not propagated: %v", err)
			}
			_, cursor, err := store.Active(ctx)
			if err != nil || cursor != "journal@one" {
				t.Fatalf("failed handoff advanced cursor: %q, %v", cursor, err)
			}
			item, found, _, err := store.Read(ctx, key)
			if err != nil || found != (eventType != Added) || found && item.Version != "1" {
				t.Fatalf("failed handoff changed source projection: %+v, found=%v, err=%v", item, found, err)
			}
			fail = false
			if err := runner.applyDiskEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			_, cursor, err = store.Active(ctx)
			if err != nil || cursor != "journal@two" || calls != 2 {
				t.Fatalf("successful retry did not commit once: cursor=%q calls=%d err=%v", cursor, calls, err)
			}
			item, found, _, err = store.Read(ctx, key)
			if err != nil || found != (eventType != Deleted) || found && item.Version != test.version {
				t.Fatalf("successful handoff lost projection transition: %+v, found=%v, err=%v", item, found, err)
			}
		})
	}
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
