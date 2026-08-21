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

type deletionStatusClient struct{}

func (deletionStatusClient) Apply(context.Context, types.WorkItemKey, *status.StatusPatch) error {
	return nil
}

type restartDeletionClient struct {
	mu            sync.Mutex
	remainingPage bool
	decouples     int
	completions   int
}

func (c *restartDeletionClient) DecoupleProducts(context.Context, string, string, string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decouples++
	if c.remainingPage {
		c.remainingPage = false
		return true, nil
	}
	return false, nil
}

func (c *restartDeletionClient) CompleteDeletion(context.Context, string, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completions++
	return nil
}

func TestIntegration_CategoryDeletionResumesAfterControllerRestart(t *testing.T) {
	deletedAt := time.Now().UTC()
	resolved, err := json.Marshal(categorytaxonomy.ResolvedCategoryTaxonomy{Path: []string{"parent"}})
	require.NoError(t, err)
	category := categorytaxonomy.CategoryTaxonomy{
		UID: "parent-uid", Namespace: "acme", Name: "parent", ResourceVersion: "5",
		DeletionTimestamp: &deletedAt, Finalizers: []string{"gitstore.dev/foreground-deletion"},
		Status: status.ResourceStatus{ResourceVersion: "5", Resolved: resolved, Conditions: []*status.Condition{
			{Type: "ParentResolved", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Acyclic", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Ready", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Terminating", Status: "True", LastTransitionTime: deletedAt},
		}},
	}
	categories := cache.New[categorytaxonomy.CategoryTaxonomy]()
	key := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "parent"}
	categories.Set(key, category)
	categories.MarkSynced()
	deletion := &restartDeletionClient{remainingPage: true}
	counter := func(context.Context, string, string) (int64, error) { return 1, nil }

	firstController := categorytaxonomy.NewReconciler(cache.AsReadOnly(categories), deletionStatusClient{}, counter, nil, deletion)
	_, continued := firstController.Reconcile(context.Background(), key).(types.RequeueAfter)
	assert.True(t, continued, "the first controller must leave a bounded continuation")

	// The second controller observes the same durable terminating record after
	// restart and drains the remaining page. Products do not block completion.
	secondController := categorytaxonomy.NewReconciler(cache.AsReadOnly(categories), deletionStatusClient{}, counter, nil, deletion)
	_, completed := secondController.Reconcile(context.Background(), key).(types.Success)
	assert.True(t, completed)
	assert.Equal(t, 2, deletion.decouples)
	assert.Equal(t, 1, deletion.completions)

	categories.Delete(key)
	_, terminal := firstController.Reconcile(context.Background(), key).(types.TerminalFailure)
	assert.True(t, terminal, "a post-delete stale requeue performs no second completion")
	assert.Equal(t, 1, deletion.completions)
}
