// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
)

// TestIntegration_Disconnect_ReconnectsWithBackoff covers FR-005 scenario 1:
// when a watch stream closes with an error (simulated disconnect), the
// Runner must attempt to reconnect (call Watch again) rather than giving up.
func TestIntegration_Disconnect_ReconnectsWithBackoff(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{ResourceVersion: "1"},
	}
	// First watcher closes immediately with a generic error (a disconnect,
	// not an expired cursor). The Runner has no more scripted watchers, so
	// stubListWatcher.Watch falls back to an already-closed clean stream —
	// enough to prove a reconnect attempt (a second Watch call) occurs.
	disconnected := newStubWatcher[widget](nil, context.Canceled)
	disconnected.closeNow()
	lw.watchers = []*stubWatcher[widget]{disconnected}

	r, _, _ := newRunner(t, lw, store)
	r.MaxBackoff = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(1500 * time.Millisecond)
	for lw.watchCallCount() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected at least 2 Watch calls (initial + reconnect), got %d", lw.watchCallCount())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// TestIntegration_Disconnect_ResourcesChangedDuringOutageReconciledExactlyOnce
// covers FR-005 scenario 2: resources that change while the watch is
// disconnected must be reconciled exactly once after reconnect — no gaps,
// no duplicates for resources that didn't change.
func TestIntegration_Disconnect_ResourcesChangedDuringOutageReconciledExactlyOnce(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	unchanged := widget{Namespace: "ns", Name: "unchanged", ResourceVersion: "1"}
	changed := widget{Namespace: "ns", Name: "changed", ResourceVersion: "1"}

	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{
			Items:           []widget{unchanged, changed},
			ResourceVersion: "1",
		},
	}
	// The pre-outage watcher delivers nothing and disconnects. During the
	// "outage" (between the two Watch calls), "changed" is mutated —
	// represented here by the post-reconnect watcher delivering a single
	// Modified event for it once the Runner reconnects.
	preOutage := newStubWatcher[widget](nil, context.Canceled)
	preOutage.closeNow()
	postReconnect := newStubWatcher[widget]([]listwatch.WatchEvent[widget]{
		{Type: listwatch.Modified, Object: widget{Namespace: "ns", Name: "changed", ResourceVersion: "2"}, ResourceVersion: "2"},
	}, nil)
	lw.watchers = []*stubWatcher[widget]{preOutage, postReconnect}

	r, _, enqueued := newRunner(t, lw, store)
	r.MaxBackoff = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(1500 * time.Millisecond)
	for lw.watchCallCount() < 2 {
		select {
		case <-deadline:
			t.Fatal("expected a reconnect (second Watch call) after disconnect")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Give the post-reconnect event a moment to be processed and enqueued.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	unchangedCount, changedCount := 0, 0
	for _, k := range enqueued.snapshot() {
		if k.Name == "unchanged" {
			unchangedCount++
		}
		if k.Name == "changed" {
			changedCount++
		}
	}
	// "unchanged" is enqueued once from the bootstrap list and never again —
	// it did not change during the outage.
	if unchangedCount != 1 {
		t.Errorf("'unchanged' enqueue count = %d, want exactly 1 (bootstrap only, no duplicate)", unchangedCount)
	}
	// "changed" is enqueued once from bootstrap and once more for the
	// post-reconnect Modified event — exactly once for the outage-era change.
	if changedCount != 2 {
		t.Errorf("'changed' enqueue count = %d, want exactly 2 (bootstrap + one reconnect event)", changedCount)
	}
}

// TestIntegration_ReplayWindowExceeded_FallsBackToFullBootstrap covers
// FR-006: when Watch returns listwatch.ErrWatchExpired (the replay window
// has been exceeded), the Runner must discard its checkpoint, perform a
// fresh List, persist a new checkpoint, and resume watching — never fail
// permanently.
func TestIntegration_ReplayWindowExceeded_FallsBackToFullBootstrap(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	item := widget{Namespace: "ns", Name: "a", ResourceVersion: "1"}

	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{Items: []widget{item}, ResourceVersion: "100"},
	}
	expiredWatcher := newStubWatcher[widget](nil, listwatch.ErrWatchExpired)
	expiredWatcher.closeNow()
	finalWatcher := newStubWatcher[widget](nil, nil)
	finalWatcher.closeNow()
	lw.watchers = []*stubWatcher[widget]{expiredWatcher, finalWatcher}

	r, _, _ := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(1200 * time.Millisecond)
	for lw.listCalls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("expected a fresh List after watch cursor expired (replay window exceeded)")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Wait for the re-list checkpoint to persist and the post-relist Watch
	// to be attempted, proving the Runner resumed rather than terminating.
	saveDeadline := time.After(1000 * time.Millisecond)
	for {
		rec, err := store.Load(context.Background(), "Widget")
		if err == nil && rec.ResourceVersion == "100" {
			break
		}
		select {
		case <-saveDeadline:
			t.Fatal("expected a fresh checkpoint to be persisted after replay-window recovery")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if lw.watchCallCount() < 2 {
		t.Errorf("expected at least 2 Watch calls (expired + post-relist resume), got %d", lw.watchCallCount())
	}
}
