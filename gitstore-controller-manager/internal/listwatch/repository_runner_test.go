// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	repository "github.com/gitstore-dev/gitstore/controller-manager/internal/repository"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type repositoryTestWatch struct {
	ch  chan WatchEvent[repository.Repository]
	err error
}

func (w *repositoryTestWatch) Events() <-chan WatchEvent[repository.Repository] { return w.ch }
func (w *repositoryTestWatch) Err() error                                       { return w.err }
func (w *repositoryTestWatch) Stop()                                            {}

type repositoryTestListWatch struct {
	mu         sync.Mutex
	lists      []ListResponse[repository.Repository]
	listCalls  int
	watches    []*repositoryTestWatch
	watchCalls int
}

func (w *repositoryTestListWatch) List(context.Context) (ListResponse[repository.Repository], error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := w.listCalls
	w.listCalls++
	if i >= len(w.lists) {
		i = len(w.lists) - 1
	}
	return w.lists[i], nil
}
func (w *repositoryTestListWatch) Watch(context.Context, string) (Watcher[repository.Repository], error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := w.watchCalls
	w.watchCalls++
	return w.watches[i], nil
}

func repositoryRunner(t *testing.T, lw *repositoryTestListWatch, store checkpoint.Store, enqueued *[]types.WorkItemKey) *Runner[repository.Repository] {
	t.Helper()
	c := cache.New[repository.Repository]()
	return &Runner[repository.Repository]{Kind: "Repository", ListWatcher: lw, Cache: c, Store: store, FlushIntervalEvents: 1, MaxBackoff: time.Millisecond,
		KeyFunc: func(r repository.Repository) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Repository", Namespace: r.Namespace, Name: r.Name}
		}, RevisionFunc: func(r repository.Repository) string { return r.ResourceVersion },
		Enqueue: func(k types.WorkItemKey) error { *enqueued = append(*enqueued, k); return nil }}
}
func repo(rv string) repository.Repository {
	return repository.Repository{Namespace: "acme", Name: "catalog", ResourceVersion: rv}
}

func TestRepositoryRunnerBootstrapDrainDeduplicatesListedWatchState(t *testing.T) {
	w := &repositoryTestWatch{ch: make(chan WatchEvent[repository.Repository], 2)}
	w.ch <- WatchEvent[repository.Repository]{Type: Modified, Object: repo("1"), ResourceVersion: "2"}
	close(w.ch)
	// A cleanly closed stream is transient and the production Runner reconnects.
	// Keep the second stream open until context cancellation so this test does
	// not race its one-element fake watch sequence.
	idle := &repositoryTestWatch{ch: make(chan WatchEvent[repository.Repository])}
	lw := &repositoryTestListWatch{lists: []ListResponse[repository.Repository]{{Items: []repository.Repository{repo("1")}, ResourceVersion: "1"}}, watches: []*repositoryTestWatch{w, idle}}
	var enqueued []types.WorkItemKey
	r := repositoryRunner(t, lw, checkpoint.NewMemoryStore(), &enqueued)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	if len(enqueued) != 1 {
		t.Fatalf("enqueues=%v, want one bootstrap enqueue despite duplicate watch event", enqueued)
	}
	if !r.Cache.HasSynced() {
		t.Fatal("repository cache was not synced")
	}
}

func TestRepositoryRunnerResumesCheckpointWithoutRelist(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	raw, _ := json.Marshal([]repository.Repository{repo("4")})
	_ = store.Save(context.Background(), checkpoint.Record{Kind: "Repository", ResourceVersion: "4", Snapshot: raw})
	w := &repositoryTestWatch{ch: make(chan WatchEvent[repository.Repository])}
	lw := &repositoryTestListWatch{lists: []ListResponse[repository.Repository]{{Items: []repository.Repository{repo("ignored")}}}, watches: []*repositoryTestWatch{w}}
	var enqueued []types.WorkItemKey
	r := repositoryRunner(t, lw, store, &enqueued)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	if lw.listCalls != 0 {
		t.Fatalf("List calls=%d, want checkpoint resume without relist", lw.listCalls)
	}
}

func TestRepositoryRunnerExpiryRelistsAndEnqueuesChangedRevision(t *testing.T) {
	first := &repositoryTestWatch{ch: make(chan WatchEvent[repository.Repository]), err: ErrWatchExpired}
	close(first.ch)
	second := &repositoryTestWatch{ch: make(chan WatchEvent[repository.Repository])}
	lw := &repositoryTestListWatch{lists: []ListResponse[repository.Repository]{{Items: []repository.Repository{repo("1")}, ResourceVersion: "1"}, {Items: []repository.Repository{repo("2")}, ResourceVersion: "2"}}, watches: []*repositoryTestWatch{first, second}}
	var enqueued []types.WorkItemKey
	r := repositoryRunner(t, lw, checkpoint.NewMemoryStore(), &enqueued)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	if lw.listCalls < 2 {
		t.Fatalf("List calls=%d, want bootstrap plus expiry relist", lw.listCalls)
	}
	if len(enqueued) != 2 {
		t.Fatalf("enqueues=%v, want initial and changed relist revision", enqueued)
	}
}
