// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// widget is the test resource type used across listwatch contract tests.
type widget struct {
	Namespace, Name, ResourceVersion string
}

func widgetKey(w widget) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Widget", Namespace: w.Namespace, Name: w.Name}
}

func widgetRevision(w widget) string { return w.ResourceVersion }

type streamedWidgetListWatcher struct {
	*stubListWatcher[widget]
	rows    int
	pages   atomic.Int32
	entered chan struct{}
	pause   <-chan struct{}
}

func (lw *streamedWidgetListWatcher) List(context.Context) (listwatch.ListResponse[widget], error) {
	return listwatch.ListResponse[widget]{}, errors.New("disk runner called the unbounded List method")
}

func (lw *streamedWidgetListWatcher) ListPages(ctx context.Context, visit func([]widget) error) (string, error) {
	if lw.entered != nil {
		select {
		case lw.entered <- struct{}{}:
		default:
		}
	}
	if lw.pause != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-lw.pause:
		}
	}
	for start := 0; start < lw.rows; start += 128 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var page []widget
		for i := start; i < min(lw.rows, start+128); i++ {
			page = append(page, widget{Namespace: "shop", Name: fmt.Sprintf("w-%05d", i), ResourceVersion: "1"})
		}
		lw.pages.Add(1)
		if err := visit(page); err != nil {
			return "", err
		}
	}
	return "snapshot", nil
}

func TestDiskRunnerStreamsThenResumesWithoutRestoringCatalogIntoMemory(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.OpenDiskStore(dir, "Widget")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	makeRunner := func(lw *streamedWidgetListWatcher) *listwatch.Runner[widget] {
		return &listwatch.Runner[widget]{
			Kind: "Widget", Cache: cache.New[widget](), Disk: store, ListWatcher: lw,
			KeyFunc: widgetKey, RevisionFunc: widgetRevision, WaitForWatchBookmark: true,
			MaxBackoff: time.Millisecond, Log: zap.NewNop(),
		}
	}
	start := func(runner *listwatch.Runner[widget]) (context.CancelFunc, <-chan struct{}) {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("disk runner: %v", err)
			}
		}()
		t.Cleanup(func() { cancel(); <-done })
		return cancel, done
	}
	watcher := newStubWatcher[widget](nil, nil)
	lw := &streamedWidgetListWatcher{stubListWatcher: &stubListWatcher[widget]{watchers: []*stubWatcher[widget]{watcher}}, rows: 1000}
	runner := makeRunner(lw)
	cancel, done := start(runner)
	require.Eventually(t, runner.Cache.HasSynced, 5*time.Second, time.Millisecond)
	require.True(t, runner.Cache.RecoveryState().Recovering)
	require.Empty(t, runner.Cache.List())
	require.EqualValues(t, 1000, store.Counts().Pending)
	watcher.ch <- listwatch.WatchEvent[widget]{Type: listwatch.Bookmark, ResourceVersion: "validated"}
	require.Eventually(t, func() bool { return !runner.Cache.RecoveryState().Recovering }, time.Second, time.Millisecond)
	changed := widget{Namespace: "shop", Name: "w-00000", ResourceVersion: "2"}
	watcher.ch <- listwatch.WatchEvent[widget]{Type: listwatch.Modified, Object: changed, ResourceVersion: "event-2"}
	require.Eventually(t, func() bool {
		_, cursor, err := store.Active(t.Context())
		return err == nil && cursor == "event-2"
	}, time.Second, time.Millisecond)
	cancel()
	<-done
	staging, err := store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.PutSnapshotPage(t.Context(), staging, []checkpoint.DiskItem{
		{Key: widgetKey(changed), Version: "unpublished", Value: json.RawMessage(`{"Name":"wrong"}`)},
	}))
	require.NoError(t, store.Close())
	store, err = checkpoint.OpenDiskStore(dir, "Widget")
	require.NoError(t, err)
	resumedWatcher := newStubWatcher([]listwatch.WatchEvent[widget]{{Type: listwatch.Bookmark, ResourceVersion: "resumed"}}, nil)
	resumedLW := &streamedWidgetListWatcher{stubListWatcher: &stubListWatcher[widget]{watchers: []*stubWatcher[widget]{resumedWatcher}}, rows: 1000}
	resumed := makeRunner(resumedLW)
	_, _ = start(resumed)
	require.Eventually(t, func() bool { return resumed.Cache.HasSynced() && !resumed.Cache.RecoveryState().Recovering }, 3*time.Second, time.Millisecond)
	require.Zero(t, resumedLW.pages.Load(), "a valid cursor should resume without a full re-list")
	resumedLW.mu.Lock()
	versions := append([]string(nil), resumedLW.watchRVs...)
	resumedLW.mu.Unlock()
	require.Equal(t, []string{"event-2"}, versions)
	got, found, err := resumed.Lookup(t.Context(), widgetKey(changed))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "2", got.ResourceVersion)
	require.Empty(t, resumed.Cache.List())
}

// stubWatcher is a test double for listwatch.Watcher[T]. Events are
// delivered from a script slice; closing happens either when the script is
// exhausted (err defaults to nil, as with a clean Stop()) or Stop is called.
type stubWatcher[T any] struct {
	mu      sync.Mutex
	ch      chan listwatch.WatchEvent[T]
	err     error
	stopped bool
}

func newStubWatcher[T any](events []listwatch.WatchEvent[T], closeErr error) *stubWatcher[T] {
	w := &stubWatcher[T]{
		ch:  make(chan listwatch.WatchEvent[T], len(events)+1),
		err: closeErr,
	}
	for _, ev := range events {
		w.ch <- ev
	}
	// Do not close yet if the caller wants to append more events after
	// construction (see stubListWatcher.pushWatch). Callers that want an
	// immediately-closed stream call closeNow.
	return w
}

func TestDiskRunnerExpiryKeepsAdmissionClosedThroughSnapshotAndBookmark(t *testing.T) {
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Widget")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	gen, err := store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	for i := range 3 {
		item := widget{Namespace: "shop", Name: fmt.Sprintf("w-%05d", i), ResourceVersion: "1"}
		data, err := json.Marshal(item)
		require.NoError(t, err)
		require.NoError(t, store.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{{Key: widgetKey(item), Version: "1", Value: data}}))
	}
	require.NoError(t, store.FinishSnapshot(t.Context(), gen, "expired"))
	watcher := newStubWatcher[widget](nil, nil)
	pause := make(chan struct{})
	lw := &streamedWidgetListWatcher{
		stubListWatcher: &stubListWatcher[widget]{
			watchErrQueue: []error{listwatch.ErrWatchExpired},
			watchers:      []*stubWatcher[widget]{nil, watcher},
		},
		rows: 2, entered: make(chan struct{}, 1), pause: pause,
	}
	runner := &listwatch.Runner[widget]{
		Kind: "Widget", Disk: store, Cache: cache.New[widget](), ListWatcher: lw,
		KeyFunc: widgetKey, RevisionFunc: widgetRevision, WaitForWatchBookmark: true, Log: zap.NewNop(),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) })
	select {
	case <-lw.entered:
	case <-time.After(time.Second):
		t.Fatal("expired cursor did not start streaming recovery")
	}
	require.True(t, runner.Cache.RecoveryState().Recovering)
	_, cursor, err := store.Active(t.Context())
	require.NoError(t, err)
	require.Equal(t, "expired", cursor)
	close(pause)
	require.Eventually(t, func() bool { return lw.watchCalls.Load() == 2 }, 3*time.Second, time.Millisecond)
	require.True(t, runner.Cache.RecoveryState().Recovering, "opening a watch must not release dispatch")
	require.EqualValues(t, 3, store.Counts().Pending, "replacement lost unfinished deletion work")
	data, _, err := store.DataPage(t.Context(), "", 10)
	require.NoError(t, err)
	require.Len(t, data, 2)
	watcher.ch <- listwatch.WatchEvent[widget]{Type: listwatch.Bookmark, ResourceVersion: "fresh-valid"}
	require.Eventually(t, func() bool { return !runner.Cache.RecoveryState().Recovering }, time.Second, time.Millisecond)
}

func (w *stubWatcher[T]) closeNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.stopped = true
		close(w.ch)
	}
}

func (w *stubWatcher[T]) Events() <-chan listwatch.WatchEvent[T] { return w.ch }
func (w *stubWatcher[T]) Err() error                             { return w.err }
func (w *stubWatcher[T]) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.stopped = true
		close(w.ch)
	}
}

// stubListWatcher is a hand-written test double implementing
// listwatch.ListWatcher[T], mirroring the stubReconciler convention used in
// spec 026's contract tests. Configurable to fail List a fixed number of
// times before succeeding, and to serve a scripted sequence of watchers.
type stubListWatcher[T any] struct {
	mu sync.Mutex

	listResp      listwatch.ListResponse[T]
	listRespQueue []listwatch.ListResponse[T] // if non-empty, List pops from here in order, then falls back to listResp
	listErr       error
	listFailures  int // number of times List fails before succeeding
	listCalls     atomic.Int32

	watchers      []*stubWatcher[T]
	watchCalls    atomic.Int32
	watchRVs      []string // resourceVersion argument observed on each Watch call
	watchErr      error
	watchErrQueue []error
}

func (lw *stubListWatcher[T]) List(_ context.Context) (listwatch.ListResponse[T], error) {
	n := lw.listCalls.Add(1)
	if int(n) <= lw.listFailures {
		return listwatch.ListResponse[T]{}, fmt.Errorf("stub list failure %d", n)
	}
	if lw.listErr != nil {
		return listwatch.ListResponse[T]{}, lw.listErr
	}
	lw.mu.Lock()
	defer lw.mu.Unlock()
	callIdx := int(n) - lw.listFailures - 1
	if callIdx < len(lw.listRespQueue) {
		return lw.listRespQueue[callIdx], nil
	}
	return lw.listResp, nil
}

func (lw *stubListWatcher[T]) Watch(_ context.Context, resourceVersion string) (listwatch.Watcher[T], error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.watchRVs = append(lw.watchRVs, resourceVersion)
	idx := int(lw.watchCalls.Add(1)) - 1
	if idx < len(lw.watchErrQueue) && lw.watchErrQueue[idx] != nil {
		return nil, lw.watchErrQueue[idx]
	}
	if lw.watchErr != nil {
		return nil, lw.watchErr
	}
	if idx < len(lw.watchers) {
		return lw.watchers[idx], nil
	}
	// No more scripted watchers — return an already-closed stream so the
	// Runner's watch loop exits cleanly instead of blocking forever.
	w := newStubWatcher[T](nil, nil)
	w.closeNow()
	return w, nil
}

func widgetCheckpoint(t *testing.T, resourceVersion string, items []widget, replayKeys ...types.WorkItemKey) checkpoint.Record {
	t.Helper()
	if items == nil {
		items = []widget{}
	}
	snapshot, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal checkpoint snapshot: %v", err)
	}
	return checkpoint.Record{
		Kind:            "Widget",
		ResourceVersion: resourceVersion,
		Snapshot:        snapshot,
		ReplayKeys:      replayKeys,
	}
}

// enqueueRecorder collects WorkItemKeys passed to a Runner's Enqueue
// callback under a mutex, so tests can safely read them from a different
// goroutine than the one running Runner.Run.
type enqueueRecorder struct {
	mu   sync.Mutex
	keys []types.WorkItemKey
}

func (e *enqueueRecorder) record(k types.WorkItemKey) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.keys = append(e.keys, k)
	return nil
}

func (e *enqueueRecorder) snapshot() []types.WorkItemKey {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]types.WorkItemKey, len(e.keys))
	copy(out, e.keys)
	return out
}

func (e *enqueueRecorder) len() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.keys)
}

func newRunner(t *testing.T, lw *stubListWatcher[widget], store checkpoint.Store) (*listwatch.Runner[widget], *cache.Cache[widget], *enqueueRecorder) {
	t.Helper()
	c := cache.New[widget]()
	rec := &enqueueRecorder{}
	r := &listwatch.Runner[widget]{
		Kind:                "Widget",
		ListWatcher:         lw,
		Cache:               c,
		Store:               store,
		KeyFunc:             widgetKey,
		RevisionFunc:        widgetRevision,
		FlushIntervalEvents: 100,
		MaxBackoff:          time.Second,
		Enqueue:             rec.record,
	}
	return r, c, rec
}

func TestRunner_Bootstrap_ListsAndEnqueuesAll(t *testing.T) {
	items := make([]widget, 0, 50)
	for i := range 50 {
		items = append(items, widget{Namespace: "ns", Name: fmt.Sprintf("w%d", i), ResourceVersion: "1"})
	}

	lw := &stubListWatcher[widget]{listResp: listwatch.ListResponse[widget]{Items: items, ResourceVersion: "100"}}
	r, c, enqueued := newRunner(t, lw, checkpoint.NewMemoryStore())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if !c.HasSynced() {
		t.Fatal("expected cache to be synced after bootstrap list")
	}
	if enqueued.len() != 50 {
		t.Errorf("expected 50 enqueued keys, got %d", enqueued.len())
	}
}

func TestRunner_Bootstrap_PersistsListCheckpointBeforeWatch(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	item := widget{Namespace: "ns", Name: "a", ResourceVersion: "1"}
	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{Items: []widget{item}, ResourceVersion: "100"},
	}
	r, _, _ := newRunner(t, lw, store)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(200 * time.Millisecond)
	for {
		rec, err := store.Load(context.Background(), "Widget")
		if err == nil {
			if rec.ResourceVersion != "100" {
				t.Fatalf("checkpoint ResourceVersion = %q, want 100", rec.ResourceVersion)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("bootstrap list checkpoint was not persisted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunner_Bootstrap_NoDispatchBeforeSynced(t *testing.T) {
	lw := &stubListWatcher[widget]{
		listErr: errors.New("api unavailable"),
	}
	r, c, enqueued := newRunner(t, lw, checkpoint.NewMemoryStore())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-done

	if c.HasSynced() {
		t.Error("expected cache NOT synced while List keeps failing")
	}
	if enqueued.len() != 0 {
		t.Errorf("expected zero enqueued keys while List keeps failing, got %d", enqueued.len())
	}
}

func TestRunner_Bootstrap_NoDuplicateAcrossListWatchTransition(t *testing.T) {
	w := widget{Namespace: "ns", Name: "dup", ResourceVersion: "5"}
	lw := &stubListWatcher[widget]{
		listResp: listwatch.ListResponse[widget]{Items: []widget{w}, ResourceVersion: "5"},
	}
	dupEvent := listwatch.WatchEvent[widget]{Type: listwatch.Added, Object: w, ResourceVersion: "5"}
	sw := newStubWatcher([]listwatch.WatchEvent[widget]{dupEvent}, nil)
	lw.watchers = []*stubWatcher[widget]{sw}

	r, _, enqueued := newRunner(t, lw, checkpoint.NewMemoryStore())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	count := 0
	for _, k := range enqueued.snapshot() {
		if k == widgetKey(w) {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected key to be enqueued exactly once across list-watch transition, got %d", count)
	}
}

func TestRunner_Bootstrap_ListFailure_RetriesWithBackoff_NeverMarksSynced(t *testing.T) {
	w := widget{Namespace: "ns", Name: "w1", ResourceVersion: "1"}
	lw := &stubListWatcher[widget]{
		listFailures: 3,
		listResp:     listwatch.ListResponse[widget]{Items: []widget{w}, ResourceVersion: "10"},
	}
	r, c, enqueued := newRunner(t, lw, checkpoint.NewMemoryStore())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		if c.HasSynced() {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cache never synced after List eventually succeeded")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if lw.listCalls.Load() < 4 {
		t.Errorf("expected at least 4 List calls (3 failures + 1 success), got %d", lw.listCalls.Load())
	}
	if enqueued.len() != 1 {
		t.Errorf("expected exactly 1 enqueued key after eventual success, got %d", enqueued.len())
	}
	cancel()
	<-done
}
