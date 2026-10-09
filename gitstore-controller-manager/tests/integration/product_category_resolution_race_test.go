// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/product"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

type raceCompletionClient struct{}

func (raceCompletionClient) CompleteDeletion(context.Context, string, string, string) error {
	return nil
}

func productResolutionReplica(t *testing.T, resourceVersion string, statusClient status.StatusClient) *product.Reconciler {
	t.Helper()
	productCache := cache.New[categorytaxonomy.Product]()
	key := productResolutionRaceKey()
	productCache.Set(key, categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: resourceVersion, Generation: 1,
		CategoryRefName: "laptops",
		Status:          status.ResourceStatus{ResourceVersion: resourceVersion},
	})
	categoryCache := cache.New[categorytaxonomy.CategoryTaxonomy]()
	categoryCache.Set(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "laptops"}, categorytaxonomy.CategoryTaxonomy{
		Namespace: "acme", Name: "laptops", UID: "Q2F0ZWdvcnk6MQ==",
	})
	return product.NewReconciler(cache.AsReadOnly(productCache), cache.AsReadOnly(categoryCache), statusClient, raceCompletionClient{})
}

func productResolutionRaceKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
}

// TestIntegration_ProductCategoryResolution_ExactlyOneReplicaWinsPerAttempt
// covers T035/PR-001: two controller replicas racing to reconcile the same
// Product's category resolution share one underlying API-shaped
// StatusClient. Exactly one replica's write wins; the other observes
// types.ErrConflict and requeues rather than silently overwriting or
// duplicating the winner's write.
func TestIntegration_ProductCategoryResolution_ExactlyOneReplicaWinsPerAttempt(t *testing.T) {
	client := newFakeStatusClient()
	key := productResolutionRaceKey()
	client.setResourceVersion(key, "rv-1")

	replicaA := productResolutionReplica(t, "rv-1", client)
	replicaB := productResolutionReplica(t, "rv-1", client)

	start := make(chan struct{})
	results := make(chan types.ReconcileResult, 2)
	for _, r := range []*product.Reconciler{replicaA, replicaB} {
		go func(r *product.Reconciler) {
			<-start
			results <- r.Reconcile(context.Background(), key)
		}(r)
	}
	close(start)
	first, second := <-results, <-results

	require.Len(t, client.appliedPatches(key), 1, "exactly one replica's write must be applied")

	successCount, requeueCount := 0, 0
	for _, result := range []types.ReconcileResult{first, second} {
		switch result.(type) {
		case types.Success:
			successCount++
		case types.RequeueAfter:
			requeueCount++
		default:
			t.Fatalf("unexpected result type %T", result)
		}
	}
	require.Equal(t, 1, successCount, "the winning replica must report success")
	require.Equal(t, 1, requeueCount, "the losing replica must requeue on conflict, not fail terminally")
}

// TestIntegration_ProductCategoryResolution_ReplicaReplacementConverges
// covers the replica-replacement half of PR-001: a stale replica's write is
// rejected, and a replacement replica with the current resourceVersion
// converges the Product without the stale replica's retry causing a second,
// duplicate write once it eventually observes the current state.
func TestIntegration_ProductCategoryResolution_ReplicaReplacementConverges(t *testing.T) {
	client := newFakeStatusClient()
	key := productResolutionRaceKey()
	client.setResourceVersion(key, "rv-current")

	staleReplica := productResolutionReplica(t, "rv-stale", client)
	currentReplica := productResolutionReplica(t, "rv-current", client)

	stale := staleReplica.Reconcile(context.Background(), key)
	_, ok := stale.(types.RequeueAfter)
	require.True(t, ok, "a stale replica must requeue rather than overwrite current state")
	require.Empty(t, client.appliedPatches(key))

	current := currentReplica.Reconcile(context.Background(), key)
	_, ok = current.(types.Success)
	require.True(t, ok, "the replacement replica with current state must converge to success (category resolves)")
	require.Len(t, client.appliedPatches(key), 1, "the replacement replica with current state must converge")
}
