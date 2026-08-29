// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStatusClient struct {
	mu       sync.Mutex
	applyErr error
	calls    []*status.StatusPatch
}

func (f *fakeStatusClient) Apply(ctx context.Context, key types.WorkItemKey, patch *status.StatusPatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, patch)
	return f.applyErr
}

func (f *fakeStatusClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func noProducts(context.Context, string, string) (int64, error) { return 0, nil }

type fakeDeletionClient struct {
	hasMore       bool
	decoupleCalls int
	completeCalls int
	decoupleErr   error
	completeErr   error
}

func (f *fakeDeletionClient) DecoupleProducts(context.Context, string, string, string) (bool, error) {
	f.decoupleCalls++
	return f.hasMore, f.decoupleErr
}

func (f *fakeDeletionClient) CompleteDeletion(context.Context, string, string, string) error {
	f.completeCalls++
	return f.completeErr
}

func TestReconcile_MissingFromCache_ReturnsTerminal(t *testing.T) {
	c := seedCache(t) // empty
	sc := &fakeStatusClient{}
	r := NewReconciler(c, sc, noProducts, nil)

	result := r.Reconcile(context.Background(), key("acme", "ghost"))
	if _, ok := result.(types.TerminalFailure); !ok {
		t.Fatalf("Reconcile result = %T, want types.TerminalFailure", result)
	}
	if sc.callCount() != 0 {
		t.Errorf("expected no Apply call for a missing resource, got %d", sc.callCount())
	}
}

func TestReconcile_ParentRefChanged_ReenqueuesDirectChildren(t *testing.T) {
	parent := CategoryTaxonomy{Namespace: "acme", Name: "computers"}
	child1 := CategoryTaxonomy{Namespace: "acme", Name: "laptops", ParentRefName: "computers"}
	child2 := CategoryTaxonomy{Namespace: "acme", Name: "desktops", ParentRefName: "computers"}
	// parent's last-observed Resolved has a stale Path (as if it used to be
	// nested under a different root) so this reconcile's freshly computed
	// Path differs, triggering descendant re-enqueue.
	stale, _ := json.Marshal(ResolvedCategoryTaxonomy{Depth: 1, Path: []string{"old-root", "computers"}})
	parent.Status = status.ResourceStatus{ResourceVersion: "5", Resolved: stale}
	c := seedCache(t, parent, child1, child2)

	sc := &fakeStatusClient{}
	var mu sync.Mutex
	var enqueued []types.WorkItemKey
	enqueue := func(k types.WorkItemKey) error {
		mu.Lock()
		defer mu.Unlock()
		enqueued = append(enqueued, k)
		return nil
	}
	r := NewReconciler(c, sc, noProducts, enqueue)

	result := r.Reconcile(context.Background(), key("acme", "computers"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(enqueued) != 2 {
		t.Fatalf("enqueued = %v, want 2 direct children", enqueued)
	}
	want := []types.WorkItemKey{key("acme", "laptops"), key("acme", "desktops")}
	for _, w := range want {
		if !slices.Contains(enqueued, w) {
			t.Errorf("expected %v to be re-enqueued, got %v", w, enqueued)
		}
	}
}

func TestReconcile_NoOpWhenPatchMatchesCurrentStatus(t *testing.T) {
	root := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}
	resolved, err := json.Marshal(ResolvedCategoryTaxonomy{Depth: 0, Path: []string{"electronics"}, ChildCount: 0, ProductCount: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	root.ResourceVersion = "1"
	now := time.Now()
	root.Status = status.ResourceStatus{
		ResourceVersion: "1",
		Resolved:        resolved,
		Conditions: []*status.Condition{
			{Type: "ParentResolved", Status: "True", LastTransitionTime: now},
			{Type: "Acyclic", Status: "True", LastTransitionTime: now},
			{Type: "Ready", Status: "True", LastTransitionTime: now},
		},
	}
	c := seedCache(t, root)

	sc := &fakeStatusClient{}
	r := NewReconciler(c, sc, noProducts, nil)

	result := r.Reconcile(context.Background(), key("acme", "electronics"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if sc.callCount() != 0 {
		t.Errorf("expected no Apply call when patch is a no-op, got %d calls: %+v", sc.callCount(), sc.calls)
	}
}

func TestReconcile_ConflictMapsToTransientFailure(t *testing.T) {
	root := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}
	c := seedCache(t, root)
	sc := &fakeStatusClient{applyErr: types.ErrConflict}
	r := NewReconciler(c, sc, noProducts, nil)

	result := r.Reconcile(context.Background(), key("acme", "electronics"))
	tf, ok := result.(types.TransientFailure)
	if !ok {
		t.Fatalf("Reconcile result = %T, want types.TransientFailure", result)
	}
	if !errors.Is(tf.Err, types.ErrConflict) {
		t.Errorf("TransientFailure.Err = %v, want errors.Is(..., types.ErrConflict)", tf.Err)
	}
}

func TestReconcile_TerminatingCategoryDecouplesProductsThenCompletes(t *testing.T) {
	deletedAt := time.Now().UTC().Truncate(time.Second)
	resolved, err := json.Marshal(ResolvedCategoryTaxonomy{Path: []string{"electronics"}})
	if err != nil {
		t.Fatal(err)
	}
	root := CategoryTaxonomy{
		Namespace: "acme", Name: "electronics", ResourceVersion: "4",
		DeletionTimestamp: &deletedAt, Finalizers: []string{datastoreForegroundDeletionFinalizer},
		Status: status.ResourceStatus{
			ResourceVersion: "4", Resolved: resolved,
			Conditions: []*status.Condition{
				{Type: "ParentResolved", Status: "True", LastTransitionTime: deletedAt},
				{Type: "Acyclic", Status: "True", LastTransitionTime: deletedAt},
				{Type: "Ready", Status: "True", LastTransitionTime: deletedAt},
				{Type: "Terminating", Status: "True", LastTransitionTime: deletedAt},
			},
		},
	}
	deletion := &fakeDeletionClient{}
	r := NewReconciler(seedCache(t, root), &fakeStatusClient{}, noProducts, nil, deletion)

	result := r.Reconcile(context.Background(), key("acme", "electronics"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if deletion.decoupleCalls != 1 || deletion.completeCalls != 1 {
		t.Fatalf("deletion calls = decouple %d, complete %d; want 1 each", deletion.decoupleCalls, deletion.completeCalls)
	}
}

func TestReconcile_TerminatingCategoryContinuesAfterBoundedProductPage(t *testing.T) {
	deletedAt := time.Now().UTC().Truncate(time.Second)
	resolved, err := json.Marshal(ResolvedCategoryTaxonomy{Path: []string{"electronics"}})
	if err != nil {
		t.Fatal(err)
	}
	root := CategoryTaxonomy{
		Namespace: "acme", Name: "electronics", ResourceVersion: "4",
		DeletionTimestamp: &deletedAt, Finalizers: []string{datastoreForegroundDeletionFinalizer},
		Status: status.ResourceStatus{
			ResourceVersion: "4", Resolved: resolved,
			Conditions: []*status.Condition{
				{Type: "ParentResolved", Status: "True", LastTransitionTime: deletedAt},
				{Type: "Acyclic", Status: "True", LastTransitionTime: deletedAt},
				{Type: "Ready", Status: "True", LastTransitionTime: deletedAt},
				{Type: "Terminating", Status: "True", LastTransitionTime: deletedAt},
			},
		},
	}
	deletion := &fakeDeletionClient{hasMore: true}
	r := NewReconciler(seedCache(t, root), &fakeStatusClient{}, noProducts, nil, deletion)

	if _, ok := r.Reconcile(context.Background(), key("acme", "electronics")).(types.RequeueAfter); !ok {
		t.Fatalf("expected bounded Product page to schedule a continuation")
	}
	if deletion.completeCalls != 0 {
		t.Fatalf("completion must wait for remaining Product pages")
	}
}

func TestReconcile_TerminationStatusIsVisibleBeforeLifecycleOperations(t *testing.T) {
	deletedAt := time.Now().UTC().Truncate(time.Second)
	root := CategoryTaxonomy{
		Namespace: "acme", Name: "electronics", ResourceVersion: "4", Generation: 7,
		DeletionTimestamp: &deletedAt, Finalizers: []string{datastoreForegroundDeletionFinalizer},
	}
	statusClient := &fakeStatusClient{}
	deletion := &fakeDeletionClient{}
	result := NewReconciler(seedCache(t, root), statusClient, noProducts, nil, deletion).
		Reconcile(context.Background(), key("acme", "electronics"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	require.Equal(t, 1, statusClient.callCount())
	conditions := statusClient.calls[0].Conditions
	require.Condition(t, func() bool {
		for _, condition := range conditions {
			if condition.Type == "Terminating" && condition.Status == "True" && condition.Reason == "DeletionRequested" {
				return true
			}
		}
		return false
	})
	assert.Equal(t, 1, deletion.decoupleCalls)
	assert.Equal(t, 1, deletion.completeCalls)
}

func TestReconcile_TerminatingCategoryRetriesDecouplingAndCompletionConflicts(t *testing.T) {
	deletedAt := time.Now().UTC().Truncate(time.Second)
	resolved, err := json.Marshal(ResolvedCategoryTaxonomy{Path: []string{"electronics"}})
	require.NoError(t, err)
	root := CategoryTaxonomy{
		Namespace: "acme", Name: "electronics", ResourceVersion: "4",
		DeletionTimestamp: &deletedAt, Finalizers: []string{datastoreForegroundDeletionFinalizer},
		Status: status.ResourceStatus{ResourceVersion: "4", Resolved: resolved, Conditions: []*status.Condition{
			{Type: "ParentResolved", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Acyclic", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Ready", Status: "True", LastTransitionTime: deletedAt},
			{Type: "Terminating", Status: "True", LastTransitionTime: deletedAt},
		}},
	}
	for name, deletion := range map[string]*fakeDeletionClient{
		"decoupling failure":  {decoupleErr: errors.New("temporary page failure")},
		"completion conflict": {completeErr: types.ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			result := NewReconciler(seedCache(t, root), &fakeStatusClient{}, noProducts, nil, deletion).
				Reconcile(context.Background(), key("acme", "electronics"))
			_, transient := result.(types.TransientFailure)
			assert.True(t, transient)
			if deletion.decoupleErr != nil {
				assert.Zero(t, deletion.completeCalls)
			} else {
				assert.Equal(t, 1, deletion.completeCalls)
			}
		})
	}
}

func TestReconcile_RemovedCategoryIsTerminalWithoutDeletionCalls(t *testing.T) {
	deletion := &fakeDeletionClient{}
	result := NewReconciler(seedCache(t), &fakeStatusClient{}, noProducts, nil, deletion).
		Reconcile(context.Background(), key("acme", "removed"))
	_, terminal := result.(types.TerminalFailure)
	assert.True(t, terminal)
	assert.Zero(t, deletion.decoupleCalls)
	assert.Zero(t, deletion.completeCalls)
}
