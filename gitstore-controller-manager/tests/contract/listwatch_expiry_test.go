// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
)

func TestRunner_ExpiryRecovery_DiscardsCheckpointAndRelists(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{
			Items:           []widget{{Namespace: "ns", Name: "a", ResourceVersion: "1"}},
			ResourceVersion: "100",
		},
	}
	expiredWatcher := newStubWatcher[widget](nil, listwatch.ErrWatchExpired)
	expiredWatcher.closeNow()
	finalWatcher := newStubWatcher[widget](nil, nil)
	finalWatcher.closeNow()
	lw.watchers = []*stubWatcher[widget]{expiredWatcher, finalWatcher}

	r, _, _ := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(800 * time.Millisecond)
	for lw.listCalls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("expected a re-list after watch cursor expired")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if lw.watchCalls.Load() < 2 {
		t.Fatalf("expected at least 2 Watch calls (initial expired + post-relist), got %d", lw.watchCalls.Load())
	}
	// The reconnect after re-list must use the fresh list's resourceVersion
	// (100), not the stale cursor from before expiry.
	if lw.watchRVs[len(lw.watchRVs)-1] != "100" {
		t.Errorf("post-relist Watch resourceVersion = %q, want 100", lw.watchRVs[len(lw.watchRVs)-1])
	}
}

func TestRunner_ExpiryRecovery_EnqueuesOnlyChangedResources(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	unchanged := widget{Namespace: "ns", Name: "unchanged", ResourceVersion: "1"}
	changedBefore := widget{Namespace: "ns", Name: "changed", ResourceVersion: "1"}
	changedAfter := widget{Namespace: "ns", Name: "changed", ResourceVersion: "2"}

	// First List (bootstrap) returns both at revision "1". After expiry, the
	// re-list reports "unchanged" still at "1" but "changed" now at "2" — so
	// only "changed" should be re-enqueued by the expiry-recovery diff.
	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{Items: []widget{unchanged, changedAfter}, ResourceVersion: "20"},
		listRespQueue: []listwatch.ListResponse[widget]{
			{Items: []widget{unchanged, changedBefore}, ResourceVersion: "10"},
		},
	}

	expiredWatcher := newStubWatcher[widget](nil, listwatch.ErrWatchExpired)
	expiredWatcher.closeNow()
	finalWatcher := newStubWatcher[widget](nil, nil)
	finalWatcher.closeNow()
	lw.watchers = []*stubWatcher[widget]{expiredWatcher, finalWatcher}

	r, c, enqueued := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(800 * time.Millisecond)
	for lw.listCalls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("expected a re-list after watch cursor expired")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if cached, ok := c.Get(widgetKey(changedAfter)); !ok || cached.ResourceVersion != "2" {
		t.Errorf("expected cache to hold the re-listed changed resource at revision 2, got %+v (ok=%v)", cached, ok)
	}

	unchangedCount, changedCount := 0, 0
	for _, k := range enqueued.snapshot() {
		if k.Name == "unchanged" {
			unchangedCount++
		}
		if k.Name == "changed" {
			changedCount++
		}
	}
	// "unchanged" is enqueued once (from the initial bootstrap list) but
	// never again by the re-list diff, since its revision didn't change.
	if unchangedCount != 1 {
		t.Errorf("expected 'unchanged' to be enqueued exactly once (bootstrap only), got %d", unchangedCount)
	}
	// "changed" is enqueued once from bootstrap and once more from the
	// re-list diff, since its revision changed from 1 to 2.
	if changedCount != 2 {
		t.Errorf("expected 'changed' to be enqueued twice (bootstrap + re-list diff), got %d", changedCount)
	}
}

func TestRunner_ExpiryRecovery_RemovesAndEnqueuesMissingResources(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	deleted := widget{Namespace: "ns", Name: "deleted", ResourceVersion: "1"}
	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{ResourceVersion: "20"},
		listRespQueue: []listwatch.ListResponse[widget]{
			{Items: []widget{deleted}, ResourceVersion: "10"},
		},
	}
	expired := newStubWatcher[widget](nil, listwatch.ErrWatchExpired)
	expired.closeNow()
	lw.watchers = []*stubWatcher[widget]{expired}

	r, c, enqueued := newRunner(t, lw, store)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(800 * time.Millisecond)
	for lw.listCalls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("expected expiry recovery re-list")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if _, ok := c.Get(widgetKey(deleted)); ok {
		t.Error("expected resource omitted from recovery snapshot to be removed from cache")
	}
	count := 0
	for _, key := range enqueued.snapshot() {
		if key == widgetKey(deleted) {
			count++
		}
	}
	if count != 2 {
		t.Errorf("deleted key enqueue count = %d, want 2 (bootstrap and deletion)", count)
	}
}

func TestRunner_ExpiryRecovery_RepeatedExpiry_NoLostWorkItems(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	firstRoundOnly := widget{Namespace: "ns", Name: "round1", ResourceVersion: "1"}
	secondRoundOnly := widget{Namespace: "ns", Name: "round2", ResourceVersion: "1"}

	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{Items: []widget{firstRoundOnly, secondRoundOnly}, ResourceVersion: "2"},
		listRespQueue: []listwatch.ListResponse[widget]{
			{Items: []widget{firstRoundOnly}, ResourceVersion: "1"},
			{Items: []widget{firstRoundOnly, secondRoundOnly}, ResourceVersion: "2"},
		},
	}
	expired1 := newStubWatcher[widget](nil, listwatch.ErrWatchExpired)
	expired1.closeNow()
	expired2 := newStubWatcher[widget](nil, listwatch.ErrWatchExpired)
	expired2.closeNow()
	final := newStubWatcher[widget](nil, nil)
	final.closeNow()
	lw.watchers = []*stubWatcher[widget]{expired1, expired2, final}

	r, _, enqueued := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(1500 * time.Millisecond)
	for lw.listCalls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("expected 3 List calls (initial + 2 expiry recoveries), got %d", lw.listCalls.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	keys := enqueued.snapshot()
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k.Name] = true
	}
	if !seen["round1"] {
		t.Error("expected round1 resource to have been enqueued at least once")
	}
	if !seen["round2"] {
		t.Error("expected round2 resource to have been enqueued at least once")
	}
}
