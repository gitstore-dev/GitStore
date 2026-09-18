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
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_CategoryDeletionHighCardinalityContinuation exercises the
// controller boundary with a large logical dependent count. The fake client
// models a datastore page: the controller must schedule exactly one bounded
// continuation rather than fanning out one work item per Product.
func TestIntegration_CategoryDeletionHighCardinalityContinuation(t *testing.T) {
	deletedAt := time.Now().UTC()
	categories := cache.New[categorytaxonomy.CategoryTaxonomy]()
	key := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "high-cardinality"}
	// Resolved must match what a fresh reconcile computes for this root
	// category (Path=[name], ProductCount=5_000_000 from counter below), or
	// the category's own hierarchy patch is never a no-op and every
	// reconcile keeps rewriting its own status instead of proceeding to
	// DecoupleProducts with a resourceVersion the server actually has (the
	// fix for a P1-adjacent finding on PR #426).
	resolved, err := json.Marshal(categorytaxonomy.ResolvedCategoryTaxonomy{Path: []string{"high-cardinality"}, ProductCount: 5_000_000})
	require.NoError(t, err)
	categories.Set(key, categorytaxonomy.CategoryTaxonomy{
		Namespace: "acme", Name: "high-cardinality", ResourceVersion: "3",
		DeletionTimestamp: &deletedAt, Finalizers: []string{"gitstore.dev/foreground-deletion"},
		Status: status.ResourceStatus{ResourceVersion: "3", Resolved: resolved, Conditions: []*status.Condition{
			{Type: "ParentResolved", Status: "TRUE", LastTransitionTime: deletedAt},
			{Type: "Acyclic", Status: "TRUE", LastTransitionTime: deletedAt},
			{Type: "Ready", Status: "TRUE", LastTransitionTime: deletedAt},
			{Type: "Terminating", Status: "TRUE", LastTransitionTime: deletedAt, Reason: "DeletionRequested", Message: "CategoryTaxonomy is awaiting foreground deletion completion."},
		}},
	})
	categories.MarkSynced()
	deletion := &scriptedPages{pages: 1_001}
	counter := func(context.Context, string, string) (int64, error) { return 5_000_000, nil }
	reconciler := categorytaxonomy.NewReconciler(cache.AsReadOnly(categories), deletionStatusClient{}, counter, nil, deletion)

	for i := 0; i < 1_000; i++ {
		_, requeued := reconciler.Reconcile(context.Background(), key).(types.RequeueAfter)
		assert.True(t, requeued)
	}
	assert.Equal(t, 1_000, deletion.decouples)
	assert.Zero(t, deletion.completions)
}

type scriptedPages struct {
	mu          sync.Mutex
	pages       int
	decouples   int
	completions int
}

func (c *scriptedPages) DecoupleProducts(context.Context, string, string, string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decouples++
	c.pages--
	return c.pages > 0, nil
}

func (c *scriptedPages) CompleteDeletion(context.Context, string, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completions++
	return nil
}
