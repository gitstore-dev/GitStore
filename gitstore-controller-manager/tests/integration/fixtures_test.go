// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// widget is the test resource type used across integration scenarios,
// mirroring tests/contract/listwatch_bootstrap_test.go's fixture of the same
// name and shape.
type widget struct {
	Namespace, Name, ResourceVersion string
}

func widgetKey(w widget) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Widget", Namespace: w.Namespace, Name: w.Name}
}

func widgetRevision(w widget) string { return w.ResourceVersion }

// stubWatcher is a test double for listwatch.Watcher[T]. Events are
// delivered from a script slice; closing happens either when Stop is called
// or the caller invokes closeNow directly.
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
	return w
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
// listwatch.ListWatcher[T]. Configurable to fail List a fixed number of
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

func (lw *stubListWatcher[T]) watchCallCount() int { return int(lw.watchCalls.Load()) }

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

// scriptedReconciler implements types.Reconciler, returning a scripted
// sequence of ReconcileResult values across successive Reconcile calls. Once
// the script is exhausted, the last scripted result repeats. Safe for
// concurrent use.
type scriptedReconciler struct {
	mu     sync.Mutex
	script []types.ReconcileResult
	calls  atomic.Int64

	// perKey, if non-nil, overrides script on a per-WorkItemKey basis.
	perKey map[types.WorkItemKey][]types.ReconcileResult
	idx    map[types.WorkItemKey]int
}

func newScriptedReconciler(script ...types.ReconcileResult) *scriptedReconciler {
	return &scriptedReconciler{script: script}
}

func (r *scriptedReconciler) Reconcile(_ context.Context, key types.WorkItemKey) types.ReconcileResult {
	r.calls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.perKey != nil {
		script, ok := r.perKey[key]
		if ok {
			if r.idx == nil {
				r.idx = make(map[types.WorkItemKey]int)
			}
			i := r.idx[key]
			if i >= len(script) {
				i = len(script) - 1
			}
			result := script[i]
			if r.idx[key] < len(script)-1 {
				r.idx[key]++
			}
			return result
		}
	}

	if len(r.script) == 0 {
		return types.ResultOK()
	}
	i := int(r.calls.Load()) - 1
	if i >= len(r.script) {
		i = len(r.script) - 1
	}
	return r.script[i]
}

func (r *scriptedReconciler) callCount() int64 { return r.calls.Load() }

// fakeStatusClient implements status.StatusClient. It records every applied
// patch keyed by WorkItemKey and can be configured to return
// types.ErrConflict for a specific (key, call-number) pair.
type fakeStatusClient struct {
	mu sync.Mutex

	applied  map[types.WorkItemKey][]*status.StatusPatch
	calls    map[types.WorkItemKey]int
	conflict map[types.WorkItemKey]int // call number (1-indexed) that should return ErrConflict for that key
}

func newFakeStatusClient() *fakeStatusClient {
	return &fakeStatusClient{
		applied:  make(map[types.WorkItemKey][]*status.StatusPatch),
		calls:    make(map[types.WorkItemKey]int),
		conflict: make(map[types.WorkItemKey]int),
	}
}

// failOnCall configures Apply to return types.ErrConflict on the nth call
// (1-indexed) for the given key, without recording that patch as applied.
func (c *fakeStatusClient) failOnCall(key types.WorkItemKey, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conflict[key] = n
}

func (c *fakeStatusClient) Apply(_ context.Context, key types.WorkItemKey, patch *status.StatusPatch) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[key]++
	if n, ok := c.conflict[key]; ok && c.calls[key] == n {
		return types.ErrConflict
	}
	c.applied[key] = append(c.applied[key], patch)
	return nil
}

func (c *fakeStatusClient) appliedPatches(key types.WorkItemKey) []*status.StatusPatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*status.StatusPatch, len(c.applied[key]))
	copy(out, c.applied[key])
	return out
}

func (c *fakeStatusClient) callCount(key types.WorkItemKey) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}
