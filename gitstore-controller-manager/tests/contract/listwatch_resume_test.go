// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// failingStore wraps a checkpoint.Store, failing the first N Save calls.
type failingStore struct {
	mu        sync.Mutex
	inner     checkpoint.Store
	failCount int
	saveCalls atomic.Int32
	savedRVs  []string
}

func (s *failingStore) Load(ctx context.Context, kind string) (checkpoint.Record, error) {
	return s.inner.Load(ctx, kind)
}

func (s *failingStore) Save(ctx context.Context, rec checkpoint.Record) error {
	n := s.saveCalls.Add(1)
	if int(n) <= s.failCount {
		return fmt.Errorf("stub save failure %d", n)
	}
	s.mu.Lock()
	s.savedRVs = append(s.savedRVs, rec.ResourceVersion)
	s.mu.Unlock()
	return s.inner.Save(ctx, rec)
}

func (s *failingStore) savedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.savedRVs)
}

func TestRunner_Resume_SkipsListPhase(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	if err := store.Save(context.Background(), widgetCheckpoint(t, "500", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	lw := &stubListWatcher[widget]{}
	r, c, _ := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-done

	if lw.listCalls.Load() != 0 {
		t.Errorf("expected List to never be called on resume, got %d calls", lw.listCalls.Load())
	}
	if !c.HasSynced() {
		t.Error("expected cache synced immediately on resume (no list phase needed)")
	}
}

func TestRunner_Resume_WatchesFromCheckpointedVersion(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	if err := store.Save(context.Background(), widgetCheckpoint(t, "500", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	lw := &stubListWatcher[widget]{}
	r, _, _ := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-done

	if lw.watchCalls.Load() == 0 {
		t.Fatal("expected Watch to be called")
	}
	if lw.watchRVs[0] != "500" {
		t.Errorf("first Watch resourceVersion = %q, want 500", lw.watchRVs[0])
	}
}

func TestRunner_Resume_RestoresCacheAndReplaysDurableWork(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	item := widget{Namespace: "ns", Name: "a", ResourceVersion: "5"}
	deletedKey := types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "deleted"}
	if err := store.Save(context.Background(), widgetCheckpoint(t, "500", []widget{item}, deletedKey)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	lw := &stubListWatcher[widget]{}
	r, c, enqueued := newRunner(t, lw, store)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-done

	if cached, ok := c.Get(widgetKey(item)); !ok || cached != item {
		t.Errorf("restored cache item = %+v (ok=%v), want %+v", cached, ok, item)
	}
	seen := map[types.WorkItemKey]bool{}
	for _, key := range enqueued.snapshot() {
		seen[key] = true
	}
	if !seen[widgetKey(item)] || !seen[deletedKey] {
		t.Errorf("replayed keys = %+v, want current and deleted keys", enqueued.snapshot())
	}
}

func TestRunner_Flush_PersistsAfterNEvents(t *testing.T) {
	inner := checkpoint.NewMemoryStore()
	if err := inner.Save(context.Background(), widgetCheckpoint(t, "0", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	store := &failingStore{inner: inner}
	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{ResourceVersion: "0"}}
	events := []listwatch.WatchEvent[widget]{
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "a"}, ResourceVersion: "1"},
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "b"}, ResourceVersion: "2"},
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "c"}, ResourceVersion: "3"},
	}
	sw := newStubWatcher(events, nil)
	lw.watchers = []*stubWatcher[widget]{sw}

	r, _, _ := newRunner(t, lw, store)
	r.FlushIntervalEvents = 3

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(400 * time.Millisecond)
	for store.savedCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected a Save after 3 events, got none")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if store.savedRVs[0] != "3" {
		t.Errorf("first flush ResourceVersion = %q, want 3", store.savedRVs[0])
	}
	cancel()
	<-done
}

func TestRunner_Flush_PersistsOnCleanShutdown(t *testing.T) {
	inner := checkpoint.NewMemoryStore()
	if err := inner.Save(context.Background(), widgetCheckpoint(t, "0", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	store := &failingStore{inner: inner}
	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{ResourceVersion: "0"}}
	events := []listwatch.WatchEvent[widget]{
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "a"}, ResourceVersion: "1"},
	}
	sw := newStubWatcher(events, nil)
	lw.watchers = []*stubWatcher[widget]{sw}

	r, _, _ := newRunner(t, lw, store)
	r.FlushIntervalEvents = 100 // won't hit the flush boundary naturally

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if store.savedCount() == 0 {
		t.Error("expected a final flush on clean shutdown even below the flush interval")
	}
}

func TestRunner_Backpressure_PausesOnWriteFailure_ResumesOnSuccess(t *testing.T) {
	inner := checkpoint.NewMemoryStore()
	if err := inner.Save(context.Background(), widgetCheckpoint(t, "0", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	store := &failingStore{inner: inner, failCount: 2}
	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{ResourceVersion: "0"}}
	events := []listwatch.WatchEvent[widget]{
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "a"}, ResourceVersion: "1"},
		{Type: listwatch.Added, Object: widget{Namespace: "ns", Name: "b"}, ResourceVersion: "2"},
	}
	sw := newStubWatcher(events, nil)
	lw.watchers = []*stubWatcher[widget]{sw}

	r, _, enqueued := newRunner(t, lw, store)
	r.FlushIntervalEvents = 1 // flush after the very first event

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// While Save keeps failing, the second event must not be drained.
	time.Sleep(150 * time.Millisecond)
	if enqueued.len() > 1 {
		t.Errorf("expected at most 1 event processed while checkpoint writes are failing, got %d", enqueued.len())
	}
	failuresBefore := testutil.ToFloat64(health.CheckpointWriteFailuresTotal.WithLabelValues("Widget"))
	if failuresBefore < 2 {
		t.Errorf("expected at least 2 recorded write failures, got %v", failuresBefore)
	}

	deadline := time.After(1 * time.Second)
	for store.savedCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected Save to eventually succeed and unblock consumption")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunner_Backpressure_BoundedReplayWindow(t *testing.T) {
	inner := checkpoint.NewMemoryStore()
	if err := inner.Save(context.Background(), widgetCheckpoint(t, "0", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	store := &failingStore{inner: inner, failCount: 5}
	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{ResourceVersion: "0"}}
	var events []listwatch.WatchEvent[widget]
	for i := 1; i <= 10; i++ {
		events = append(events, listwatch.WatchEvent[widget]{
			Type:            listwatch.Added,
			Object:          widget{Namespace: "ns", Name: fmt.Sprintf("w%d", i)},
			ResourceVersion: fmt.Sprintf("%d", i),
		})
	}
	sw := newStubWatcher(events, nil)
	lw.watchers = []*stubWatcher[widget]{sw}

	r, _, enqueued := newRunner(t, lw, store)
	r.FlushIntervalEvents = 2 // flush boundary every 2 events

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// While Save keeps failing (5 failures configured), no more than
	// FlushIntervalEvents events should ever be unpersisted at once.
	time.Sleep(200 * time.Millisecond)
	if enqueued.len() > 2 {
		t.Errorf("expected at most FlushIntervalEvents=2 events processed while writes fail, got %d", enqueued.len())
	}
	cancel()
	<-done
}

func TestRunner_TransientReconnect_UsesInMemoryCheckpoint_NotPersisted(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	if err := store.Save(context.Background(), widgetCheckpoint(t, "10", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	lw := &stubListWatcher[widget]{}
	// First watcher: delivers one event advancing currentRV to "20", then
	// closes with a transient (non-expiry) error.
	firstEvents := []listwatch.WatchEvent[widget]{
		{Type: listwatch.Bookmark, ResourceVersion: "20"},
	}
	firstWatcher := newStubWatcher(firstEvents, errors.New("transient network error"))
	firstWatcher.closeNow()
	secondWatcher := newStubWatcher[widget](nil, nil)
	secondWatcher.closeNow()
	lw.watchers = []*stubWatcher[widget]{firstWatcher, secondWatcher}

	r, _, _ := newRunner(t, lw, store)
	r.MaxBackoff = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(800 * time.Millisecond)
	for lw.watchCalls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("expected a reconnect Watch call after transient close")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if lw.watchRVs[0] != "10" {
		t.Errorf("initial Watch resourceVersion = %q, want 10 (from persisted checkpoint)", lw.watchRVs[0])
	}
	if lw.watchRVs[1] != "20" {
		t.Errorf("reconnect Watch resourceVersion = %q, want 20 (in-memory cursor, not persisted 10)", lw.watchRVs[1])
	}
}

func TestRunner_WatchOpenExpiry_RelistsImmediately(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	if err := store.Save(context.Background(), widgetCheckpoint(t, "10", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	lw := &stubListWatcher[widget]{
		listResp:      listwatch.ListResponse[widget]{ResourceVersion: "20"},
		watchErrQueue: []error{listwatch.ErrWatchExpired},
	}
	r, _, _ := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(400 * time.Millisecond)
	for lw.listCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected immediate re-list after Watch rejected expired cursor")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunner_ReconnectCancellation_PerformsFinalFlush(t *testing.T) {
	inner := checkpoint.NewMemoryStore()
	if err := inner.Save(context.Background(), widgetCheckpoint(t, "0", nil)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	store := &failingStore{inner: inner}
	watcher := newStubWatcher([]listwatch.WatchEvent[widget]{
		{Type: listwatch.Bookmark, ResourceVersion: "1"},
	}, errors.New("transient"))
	watcher.closeNow()
	lw := &stubListWatcher[widget]{watchers: []*stubWatcher[widget]{watcher}}
	r, _, _ := newRunner(t, lw, store)
	r.FlushIntervalEvents = 100
	r.MaxBackoff = time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	rec, err := inner.Load(context.Background(), "Widget")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rec.ResourceVersion != "1" {
		t.Errorf("final checkpoint ResourceVersion = %q, want 1", rec.ResourceVersion)
	}
}

func TestRunner_TransientReconnect_BackoffCappedAtMax(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{ResourceVersion: "0"}}

	const attempts = 4
	watchers := make([]*stubWatcher[widget], 0, attempts+1)
	for range attempts {
		w := newStubWatcher[widget](nil, errors.New("transient"))
		w.closeNow()
		watchers = append(watchers, w)
	}
	final := newStubWatcher[widget](nil, nil)
	final.closeNow()
	watchers = append(watchers, final)
	lw.watchers = watchers

	r, _, _ := newRunner(t, lw, store)
	r.MaxBackoff = 40 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for lw.watchCalls.Load() < int32(attempts+1) {
		select {
		case <-deadline:
			t.Fatalf("expected %d reconnect attempts, got %d", attempts+1, lw.watchCalls.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	elapsed := time.Since(start)
	cancel()
	<-done

	// With MaxBackoff=40ms and 4 reconnects, elapsed time must stay bounded
	// (well under what an unbounded/uncapped exponential backoff would take).
	if elapsed > 2*time.Second {
		t.Errorf("reconnect attempts took %v, expected backoff capped near MaxBackoff", elapsed)
	}
}
