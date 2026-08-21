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
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/assert"
)

// TestIntegration_CategoryDeletionHighCardinalityContinuation exercises the
// controller boundary with a large logical dependent count. The fake client
// models a datastore page: the controller must schedule exactly one bounded
// continuation rather than fanning out one work item per Product.
func TestIntegration_CategoryDeletionHighCardinalityContinuation(t *testing.T) {
	deletedAt := time.Now().UTC()
	categories := cache.New[categorytaxonomy.CategoryTaxonomy]()
	key := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "high-cardinality"}
	categories.Set(key, categorytaxonomy.CategoryTaxonomy{
		Namespace: "acme", Name: "high-cardinality", ResourceVersion: "3",
		DeletionTimestamp: &deletedAt, Finalizers: []string{"gitstore.dev/foreground-deletion"},
		Status: status.ResourceStatus{ResourceVersion: "3", Conditions: []*status.Condition{
			{Type: "ParentResolved", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Acyclic", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Ready", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Terminating", Status: "True", LastTransitionTime: deletedAt},
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
