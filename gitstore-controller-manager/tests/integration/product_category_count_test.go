// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// recordingStatusClient is a status.StatusClient that always accepts a
// patch (no optimistic-concurrency check) and records every applied patch
// per key. Unlike fixtures_test.go's fakeStatusClient, this test's
// CategoryTaxonomy cache entries are static (never updated to reflect a
// prior Apply's new resourceVersion, since nothing here simulates the
// watch feedback loop that would do so in a real deployment) — a
// conflict-checking client would therefore spuriously reject every
// reconcile after the first for a given key.
type recordingStatusClient struct {
	mu      sync.Mutex
	applied map[types.WorkItemKey][]*status.StatusPatch
	calls   map[types.WorkItemKey]int
}

func newRecordingStatusClient() *recordingStatusClient {
	return &recordingStatusClient{
		applied: make(map[types.WorkItemKey][]*status.StatusPatch),
		calls:   make(map[types.WorkItemKey]int),
	}
}

func (c *recordingStatusClient) Apply(_ context.Context, key types.WorkItemKey, patch *status.StatusPatch) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[key]++
	c.applied[key] = append(c.applied[key], patch)
	return nil
}

func (c *recordingStatusClient) appliedPatches(key types.WorkItemKey) []*status.StatusPatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*status.StatusPatch, len(c.applied[key]))
	copy(out, c.applied[key])
	return out
}

func (c *recordingStatusClient) callCount(key types.WorkItemKey) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

// activeProductCache is a swappable pointer to the Product cache the
// ProductCounter below reads from, so a test can simulate a Runner[Product]
// restart (a fresh Cache instance replacing the pre-restart one) without
// needing a second CategoryTaxonomy Manager/Reconciler wired to it.
type activeProductCache struct {
	mu    sync.Mutex
	cache *cache.Cache[categorytaxonomy.Product]
}

func (a *activeProductCache) set(c *cache.Cache[categorytaxonomy.Product]) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = c
}

func (a *activeProductCache) get() *cache.Cache[categorytaxonomy.Product] {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cache
}

// newProductCategoryCountFixture builds the shared pieces of the
// product-category-count integration scenario: a CategoryTaxonomy cache
// seeded with three categories (two participating in the test, one that
// must remain untouched), an activeProductCache, a recordingStatusClient,
// and a Reconciler whose ProductCounter is computed live from whichever
// Product cache is currently active (mirroring
// categorytaxonomy.NewProductCounter's client-side-filter approach, but
// against the in-process cache instead of a real gitstore-api — see
// contracts/product-watch-contract.md's test obligations).
func newProductCategoryCountFixture(t *testing.T) (
	catCache *cache.Cache[categorytaxonomy.CategoryTaxonomy],
	productCache *activeProductCache,
	statusClient *recordingStatusClient,
	mgr *manager.Manager,
) {
	t.Helper()

	catCache = cache.New[categorytaxonomy.CategoryTaxonomy]()
	statusClient = newRecordingStatusClient()
	for _, name := range []string{"electronics", "computers", "untouched"} {
		key := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: name}
		catCache.Set(key, categorytaxonomy.CategoryTaxonomy{UID: "cat-" + name, Namespace: "acme", Name: name, Generation: 1, ResourceVersion: "1"})
	}
	catCache.MarkSynced()

	productCache = &activeProductCache{}
	productCache.set(cache.New[categorytaxonomy.Product]())

	productCounter := func(_ context.Context, namespace, name string) (int64, error) {
		var count int64
		for _, p := range productCache.get().List() {
			if p.Namespace == namespace && p.CategoryRefName == name {
				count++
			}
		}
		return count, nil
	}

	mgr = manager.New()
	reconciler := categorytaxonomy.NewReconciler(cache.AsReadOnly(catCache), statusClient, productCounter, mgr.Enqueue)
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            "CategoryTaxonomy",
		Reconciler:      reconciler,
		Cache:           catCache,
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	enqueueCategory := func(namespace, categoryName string) {
		_ = mgr.Enqueue(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: namespace, Name: categoryName})
	}
	productCache.get().AddEventHandler(categorytaxonomy.NewProductCategoryEnqueueHandler(enqueueCategory))

	return catCache, productCache, statusClient, mgr
}

func resolvedProductCount(t *testing.T, statusClient *recordingStatusClient, key types.WorkItemKey) int64 {
	t.Helper()
	patches := statusClient.appliedPatches(key)
	if len(patches) == 0 {
		return 0
	}
	var resolved categorytaxonomy.ResolvedCategoryTaxonomy
	if err := json.Unmarshal(patches[len(patches)-1].Resolved, &resolved); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	return resolved.ProductCount
}

func waitForCallCount(t *testing.T, statusClient *recordingStatusClient, key types.WorkItemKey, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if statusClient.callCount(key) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d reconcile call(s) on %+v, got %d", want, key, statusClient.callCount(key))
}

// T027: product create/delete/reassignment converges productCount on the
// right category(ies) only — FR-004/FR-010, SC-003.
func TestIntegration_ProductCategoryCount_CreateDeleteReassignConverges(t *testing.T) {
	catCache, productCache, statusClient, mgr := newProductCategoryCountFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	electronicsKey := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics"}
	computersKey := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "computers"}
	untouchedKey := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "untouched"}

	// Create: a product referencing "electronics" appears.
	productKey := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
	productCache.get().Set(productKey, categorytaxonomy.Product{UID: "p1", Namespace: "acme", Name: "widget", ResourceVersion: "1", CategoryRefName: "electronics"})

	waitForCallCount(t, statusClient, electronicsKey, 1)
	if got := resolvedProductCount(t, statusClient, electronicsKey); got != 1 {
		t.Errorf("after create: electronics productCount = %d, want 1", got)
	}

	// Reassignment: move the product from "electronics" to "computers".
	productCache.get().Set(productKey, categorytaxonomy.Product{UID: "p1", Namespace: "acme", Name: "widget", ResourceVersion: "2", CategoryRefName: "computers"})

	waitForCallCount(t, statusClient, electronicsKey, 2)
	waitForCallCount(t, statusClient, computersKey, 1)
	if got := resolvedProductCount(t, statusClient, electronicsKey); got != 0 {
		t.Errorf("after reassignment: electronics productCount = %d, want 0", got)
	}
	if got := resolvedProductCount(t, statusClient, computersKey); got != 1 {
		t.Errorf("after reassignment: computers productCount = %d, want 1", got)
	}
	if calls := statusClient.callCount(untouchedKey); calls != 0 {
		t.Errorf("untouched category reconciled %d time(s), want 0 (FR-004)", calls)
	}

	// Delete: the product is removed from the cache entirely.
	productCache.get().Delete(productKey)

	waitForCallCount(t, statusClient, computersKey, 2)
	if got := resolvedProductCount(t, statusClient, computersKey); got != 0 {
		t.Errorf("after delete: computers productCount = %d, want 0", got)
	}
	if calls := statusClient.callCount(untouchedKey); calls != 0 {
		t.Errorf("untouched category reconciled %d time(s), want 0 (FR-004)", calls)
	}

	_ = catCache // referenced only for fixture symmetry with registerCategoryTaxonomy's shape
}

func productKeyFor(p categorytaxonomy.Product) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Product", Namespace: p.Namespace, Name: p.Name}
}

func productRevision(p categorytaxonomy.Product) string { return p.ResourceVersion }

// T033: a Product change delivered to the watch stream right before a
// Runner[Product] restart is not lost — the affected CategoryTaxonomy
// still converges after resume, with no manual re-trigger (FR-006, US4).
// Mirrors reconcile_retry_resume_test.go's two-run-against-the-same-Store
// pattern, applied to the Product-driven trigger path instead of a
// reconciled kind's own resume.
func TestIntegration_ProductCategoryCount_SurvivesRunnerRestart(t *testing.T) {
	store := checkpoint.NewMemoryStore()

	catCache, productCache, statusClient, mgr := newProductCategoryCountFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	electronicsKey := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics"}

	enqueueCategory := func(namespace, categoryName string) {
		_ = mgr.Enqueue(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: namespace, Name: categoryName})
	}
	handler := categorytaxonomy.NewProductCategoryEnqueueHandler(enqueueCategory)

	// --- First run: list is empty (no products exist yet), then a single
	// watch event delivers the new product referencing "electronics" — the
	// last thing to happen before the simulated restart, so its effect on
	// the cache handler (and thus the CategoryTaxonomy enqueue) must survive.
	// Replaces the fixture's default Product cache with one wired to the
	// same handler, so the fixture's ProductCounter (which always reads
	// productCache.get()) observes this run's state.
	productCache1 := cache.New[categorytaxonomy.Product]()
	productCache1.AddEventHandler(handler)
	productCache.set(productCache1)

	lw1 := &stubListWatcher[categorytaxonomy.Product]{
		listResp: listwatch.ListResponse[categorytaxonomy.Product]{ResourceVersion: "0"},
	}
	newProduct := categorytaxonomy.Product{UID: "p1", Namespace: "acme", Name: "widget", ResourceVersion: "1", CategoryRefName: "electronics"}
	watcher1 := newStubWatcher[categorytaxonomy.Product]([]listwatch.WatchEvent[categorytaxonomy.Product]{
		{Type: listwatch.Added, Object: newProduct, ResourceVersion: "1"},
	}, nil)
	lw1.watchers = []*stubWatcher[categorytaxonomy.Product]{watcher1}

	runner1 := &listwatch.Runner[categorytaxonomy.Product]{
		Kind:                "Product",
		ListWatcher:         lw1,
		Cache:               productCache1,
		Store:               store,
		KeyFunc:             productKeyFor,
		RevisionFunc:        productRevision,
		FlushIntervalEvents: 1, // flush the checkpoint after every event, so the restart below observes the pre-restart state
		MaxBackoff:          time.Second,
		Enqueue:             func(types.WorkItemKey) error { return nil },
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() { done1 <- runner1.Run(ctx1) }()

	waitForCallCount(t, statusClient, electronicsKey, 1)
	if got := resolvedProductCount(t, statusClient, electronicsKey); got != 1 {
		t.Fatalf("before restart: electronics productCount = %d, want 1", got)
	}

	cancel1()
	<-done1

	// --- Simulated restart: a fresh Runner[Product]/cache against the same
	// Store. The checkpoint's restored snapshot re-seeds the new cache with
	// the already-known product, and productCache2.AddEventHandler is
	// registered *before* the restore fires the replay, mirroring how
	// registerProductWatch (cmd/controller/main.go) wires the handler
	// before Run is called.
	preRestartCalls := statusClient.callCount(electronicsKey)

	productCache2 := cache.New[categorytaxonomy.Product]()
	productCache2.AddEventHandler(handler)
	productCache.set(productCache2)

	lw2 := &stubListWatcher[categorytaxonomy.Product]{}
	runner2 := &listwatch.Runner[categorytaxonomy.Product]{
		Kind:                "Product",
		ListWatcher:         lw2,
		Cache:               productCache2,
		Store:               store,
		KeyFunc:             productKeyFor,
		RevisionFunc:        productRevision,
		FlushIntervalEvents: 1,
		MaxBackoff:          time.Second,
		Enqueue:             func(types.WorkItemKey) error { return nil },
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- runner2.Run(ctx2) }()

	// On resume, restoring the checkpoint replays every ReplayKey through
	// the cache's Set, which re-fires OnAdd/OnUpdate for the same product —
	// re-enqueuing "electronics" is a correctness-safe no-op per research.md
	// R6 (IsNoOp already suppresses a redundant status write), not data loss.
	waitForCallCount(t, statusClient, electronicsKey, preRestartCalls+1)
	if got := resolvedProductCount(t, statusClient, electronicsKey); got != 1 {
		t.Errorf("after restart: electronics productCount = %d, want 1 (must still converge)", got)
	}

	cancel2()
	<-done2
	_ = catCache
}
