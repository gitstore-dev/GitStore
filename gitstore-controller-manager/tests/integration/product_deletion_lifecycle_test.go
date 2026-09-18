// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/product"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

// deletionCompletionAPI models the expected-resource-version finalizer API
// shared by controller replicas. It deliberately makes the removal atomic so
// the test exercises the same at-least-once boundary as GraphQL.
type deletionCompletionAPI struct {
	mu              sync.Mutex
	present         bool
	resourceVersion string
	removals        int
}

func (a *deletionCompletionAPI) CompleteDeletion(_ context.Context, _, _, resourceVersion string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.present || resourceVersion != a.resourceVersion {
		return types.ErrConflict
	}
	a.present = false
	a.removals++
	return nil
}

func (a *deletionCompletionAPI) removalCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.removals
}

func terminatingProduct(resourceVersion string) categorytaxonomy.Product {
	now := time.Now().UTC()
	return categorytaxonomy.Product{
		UID:               "product-uid",
		Namespace:         "acme",
		Name:              "widget",
		ResourceVersion:   resourceVersion,
		DeletionTimestamp: &now,
		Finalizers:        []string{"gitstore.dev/foreground-deletion"},
	}
}

func productReplica(resourceCache *cache.Cache[categorytaxonomy.Product], completion product.CompletionClient) *product.Reconciler {
	// These tests only exercise terminating Products, so the resolve path
	// (which would need a real category cache/status client) is never
	// reached; nil is safe here.
	return product.NewReconciler(cache.AsReadOnly(resourceCache), nil, nil, completion)
}

func terminatingProductKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
}

// TestIntegration_ProductDeletion_FinalRemovalOccursOnceDuringReplicaRace
// covers the finalizer race: two controllers can observe the same terminating
// Product, but the expected-version completion endpoint may remove it once
// only. The losing replica is requeued until the durable delete reaches its
// local cache, at which point it becomes a no-op.
func TestIntegration_ProductDeletion_FinalRemovalOccursOnceDuringReplicaRace(t *testing.T) {
	key := terminatingProductKey()
	api := &deletionCompletionAPI{present: true, resourceVersion: "rv-8"}
	cacheA := cache.New[categorytaxonomy.Product]()
	cacheB := cache.New[categorytaxonomy.Product]()
	cacheA.Set(key, terminatingProduct("rv-8"))
	cacheB.Set(key, terminatingProduct("rv-8"))
	replicaA := productReplica(cacheA, api)
	replicaB := productReplica(cacheB, api)

	start := make(chan struct{})
	results := make(chan types.ReconcileResult, 2)
	for _, replica := range []*product.Reconciler{replicaA, replicaB} {
		go func(r *product.Reconciler) {
			<-start
			results <- r.Reconcile(t.Context(), key)
		}(replica)
	}
	close(start)
	first, second := <-results, <-results

	require.Equal(t, 1, api.removalCount(), "the shared finalizer endpoint must remove once")
	resultsByType := map[string]int{resultName(first): 1}
	resultsByType[resultName(second)]++
	require.Equal(t, 1, resultsByType["success"])
	require.Equal(t, 1, resultsByType["requeue"])

	// The winning completion emits a durable delete. Once both replicas apply
	// it, a delayed retry from the loser must not call completion again.
	cacheA.Delete(key)
	cacheB.Delete(key)
	result := replicaA.Reconcile(t.Context(), key)
	_, ok := result.(types.Success)
	require.True(t, ok, "a replica that has observed final removal is a no-op")
	require.Equal(t, 1, api.removalCount())
}

// TestIntegration_ProductController_ReplicaHandoffRejectsStaleState proves a
// controller that survives a handoff cannot complete deletion from a stale
// cache snapshot. The replacement replica's current snapshot wins; after the
// delete event reaches the old replica, its queued retry is harmless.
func TestIntegration_ProductController_ReplicaHandoffRejectsStaleState(t *testing.T) {
	key := terminatingProductKey()
	api := &deletionCompletionAPI{present: true, resourceVersion: "rv-current"}
	oldCache := cache.New[categorytaxonomy.Product]()
	newCache := cache.New[categorytaxonomy.Product]()
	oldCache.Set(key, terminatingProduct("rv-stale"))
	newCache.Set(key, terminatingProduct("rv-current"))
	oldReplica := productReplica(oldCache, api)
	newReplica := productReplica(newCache, api)

	stale := oldReplica.Reconcile(t.Context(), key)
	_, ok := stale.(types.RequeueAfter)
	require.True(t, ok, "a stale controller must wait for durable watch convergence")
	require.Equal(t, 0, api.removalCount())

	current := newReplica.Reconcile(t.Context(), key)
	_, ok = current.(types.Success)
	require.True(t, ok, "the replacement replica with the current version completes deletion")
	require.Equal(t, 1, api.removalCount())

	oldCache.Delete(key) // durable final-delete event observed after handoff
	settled := oldReplica.Reconcile(t.Context(), key)
	_, ok = settled.(types.Success)
	require.True(t, ok)
	require.Equal(t, 1, api.removalCount(), "stale retry cannot cause a second removal")
}

func resultName(result types.ReconcileResult) string {
	switch result.(type) {
	case types.Success:
		return "success"
	case types.RequeueAfter:
		return "requeue"
	default:
		return "other"
	}
}
