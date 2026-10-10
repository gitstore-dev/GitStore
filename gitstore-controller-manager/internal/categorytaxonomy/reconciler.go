// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package categorytaxonomy implements the CategoryTaxonomy reconciler:
// hierarchy (depth/path/childCount/productCount) computation, cycle
// detection, and the ParentResolved/Acyclic/Ready/required-file-reference
// conditions, writing back through the status-subresource contract shipped
// by spec 040.
package categorytaxonomy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// CategoryTaxonomy is the cache entity populated by the Runner[CategoryTaxonomy]
// list-then-watch loop against watchCategories/categories.
type CategoryTaxonomy struct {
	UID               string
	Namespace         string
	Name              string
	Generation        int64
	ResourceVersion   string
	OwnerReferences   []OwnerReference
	Finalizers        []string
	DeletionTimestamp *time.Time
	// ParentRefName is empty when this category has no parent (root candidate).
	// Mirrors spec.parentRef.name.
	ParentRefName string
	// Media mirrors spec.media, used by the required-file-reference
	// condition (US3, FR-010/FR-011).
	Media  []MediaRef
	Status status.ResourceStatus
}

// OwnerReference is the lifecycle-relevant subset of metadata.ownerReferences.
type OwnerReference struct {
	UID                string
	Kind               string
	BlockOwnerDeletion bool
}

// MediaRef mirrors one spec.media[].fileRef entry.
type MediaRef struct {
	Name     string
	Optional bool
}

// ResolvedCategoryTaxonomy is the JSON payload the reconciler marshals into
// StatusPatch.Resolved. Mirrors gitstore-api/internal/catalog.ResolvedCategoryTaxonomy
// field-for-field (spec 040 R9's renamed shape) so the JSON round-trips
// identically on both sides.
type ResolvedCategoryTaxonomy struct {
	// Depth is 0 for a root category.
	Depth int8 `json:"depth"`
	// Path is the ancestor path from root to self (root-to-self order);
	// single-element for a root category.
	Path         []string `json:"path"`
	ChildCount   int64    `json:"childCount"`
	ProductCount int64    `json:"productCount"`
}

// ProductCounter returns the number of products whose spec.categoryRef.name
// equals name, in namespace (research.md R4 — a client-side filter over the
// existing products query, not a new server-side field).
type ProductCounter func(ctx context.Context, namespace, name string) (int64, error)

// EnqueueFunc re-queues a work item for reconciliation, matching
// manager.Manager.Enqueue's signature.
type EnqueueFunc func(types.WorkItemKey) error

// Reconciler implements types.Reconciler for the CategoryTaxonomy kind.
type Reconciler struct {
	lookup       cache.LookupFunc[CategoryTaxonomy]
	childCount   ProductCounter
	children     func(context.Context, types.WorkItemKey) error
	statusClient status.StatusClient
	productCount ProductCounter
	deletion     DeletionClient
}

// NewReconciler returns a Reconciler reading from c, writing status through
// statusClient, resolving productCount via productCounter, and re-enqueueing
// affected descendants via enqueue (research.md R2). enqueue may be nil if
// the caller does not need descendant propagation (e.g. in a test that only
// exercises a single node).
func NewReconciler(c cache.CacheAccessor[CategoryTaxonomy], statusClient status.StatusClient, productCounter ProductCounter, enqueue EnqueueFunc, deletionClients ...DeletionClient) *Reconciler {
	childCount := func(ctx context.Context, namespace, name string) (int64, error) {
		var count int64
		for _, item := range c.List() {
			if item.Namespace == namespace && item.ParentRefName == name {
				count++
			}
		}
		return count, ctx.Err()
	}
	children := func(ctx context.Context, parent types.WorkItemKey) error {
		if enqueue == nil {
			return nil
		}
		for _, item := range c.List() {
			if item.Namespace == parent.Namespace && item.ParentRefName == parent.Name {
				if err := enqueue(types.WorkItemKey{Kind: parent.Kind, Namespace: item.Namespace, Name: item.Name}); err != nil {
					return err
				}
			}
		}
		return ctx.Err()
	}
	return NewReconcilerWithLookup(cache.LookupFrom(c), statusClient, productCounter, childCount, children, deletionClients...)
}

func NewReconcilerWithLookup(lookup cache.LookupFunc[CategoryTaxonomy], statusClient status.StatusClient, productCounter, childCount ProductCounter, children func(context.Context, types.WorkItemKey) error, deletionClients ...DeletionClient) *Reconciler {
	var deletion DeletionClient
	if len(deletionClients) > 0 {
		deletion = deletionClients[0]
	}
	return &Reconciler{lookup: lookup, childCount: childCount, children: children, statusClient: statusClient, productCount: productCounter, deletion: deletion}
}

// Reconcile implements types.Reconciler. See contracts/reconciler-contract.md
// for the full 8-step algorithm this follows.
func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	current, ok, err := r.lookup(ctx, key)
	if err != nil {
		return types.ResultTransient(fmt.Errorf("categorytaxonomy: read projection: %w", err))
	}
	if !ok {
		// A queued key can outlive its object after watch replay, deletion, or a
		// checkpointed controller restart. Absence is the reconciled state.
		return types.ResultOK()
	}

	previous := previousResolved(current.Status.Resolved)

	productCount := int64(0)
	if r.productCount != nil {
		pc, err := r.productCount(ctx, key.Namespace, key.Name)
		if err != nil {
			return types.ResultTransient(fmt.Errorf("categorytaxonomy: count products: %w", err))
		}
		productCount = pc
	}
	resolved, inCycle, parentResolvedCond, err := boundedHierarchy(ctx, r.lookup, r.childCount, current, productCount)
	if errors.Is(err, errHierarchyDepth) {
		return types.ResultTerminal(err)
	}
	if err != nil {
		return types.ResultTransient(fmt.Errorf("categorytaxonomy: resolve hierarchy: %w", err))
	}
	if inCycle {
		// FR-008: cycle participants keep their last-observed Path/Depth —
		// never recomputed, never reset — while ChildCount/ProductCount
		// still reflect current cache state.
		if previous != nil {
			resolved.Depth = previous.Depth
			resolved.Path = previous.Path
		} else {
			// No prior resolved value exists yet (first-ever reconcile of a
			// cycle participant) — Path must still be a valid non-null
			// [String!]! per the GraphQL schema. Fall back to a single-element
			// slice containing self's own name, the same sentinel a root
			// category's Path uses, since nothing has been walked yet.
			resolved.Path = []string{current.Name}
		}
	}

	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		return types.ResultTransient(fmt.Errorf("categorytaxonomy: marshal resolved status: %w", err))
	}

	acyclicCond := computeAcyclic(inCycle)
	// current.Status.Conditions comes from the real GraphQL list/watch
	// path, where ConditionStatus is the enum wire value ("TRUE"/"FALSE");
	// acyclicCond.Status uses this package's statusTrue/statusFalse
	// constants, which now match that same wire casing directly, so a
	// plain equality compare is safe here.
	acyclicChanged := conditionStatusByType(current.Status.Conditions, conditionAcyclic) != acyclicCond.Status
	fileRefCond := computeFileRefCondition(current)
	readyCond := computeReady(parentResolvedCond, acyclicCond, fileRefCond)

	conditions := []*status.Condition{&parentResolvedCond, &acyclicCond}
	if fileRefCond != nil {
		conditions = append(conditions, fileRefCond)
	}
	conditions = append(conditions, &readyCond)
	if current.DeletionTimestamp != nil || slices.Contains(current.Finalizers, datastoreForegroundDeletionFinalizer) {
		transition := time.Now().UTC()
		if current.DeletionTimestamp != nil {
			transition = *current.DeletionTimestamp
		}
		conditions = append(conditions, &status.Condition{
			Type:               "Terminating",
			Status:             statusTrue,
			ObservedGeneration: current.Generation,
			LastTransitionTime: transition,
			Reason:             "DeletionRequested",
			Message:            "CategoryTaxonomy is awaiting foreground deletion completion.",
		})
	}
	preserveLastTransitionTimes(conditions, current.Status.Conditions)

	gen := current.Generation
	patch := &status.StatusPatch{
		ResourceVersion:    current.ResourceVersion,
		ObservedGeneration: &gen,
		Resolved:           resolvedJSON,
		Conditions:         conditions,
	}

	if patch.IsNoOp(current.Status) {
		return r.reconcileDeletion(ctx, current)
	}
	// Persist dependent work first: successful status writeback must not hide
	// an enqueue failure behind IsNoOp on the next attempt.
	if (hierarchyChanged(previous, resolved) || acyclicChanged) && r.children != nil {
		if err := r.children(ctx, key); err != nil {
			return types.ResultTransient(fmt.Errorf("categorytaxonomy: persist child reconciliation: %w", err))
		}
	}

	// Any Apply failure -- including types.ErrConflict -- is retried: a
	// conflict means the cache is stale and will be corrected on the next
	// dispatch once the watch delivers the newer version (FR-014).
	if err := r.statusClient.Apply(ctx, key, patch); err != nil {
		return types.ResultTransient(err)
	}
	if current.DeletionTimestamp != nil || slices.Contains(current.Finalizers, datastoreForegroundDeletionFinalizer) {
		// Do not call reconcileDeletion with `current` here: Apply just
		// advanced this category's resourceVersion server-side, so
		// DecoupleProducts/CompleteDeletion would immediately conflict
		// against the write this very call just made. Let the watch event
		// that write generates re-trigger this same key; the next reconcile
		// observes patch.IsNoOp(current.Status)==true (its own status now
		// matches) and proceeds straight to reconcileDeletion with a
		// resourceVersion that actually matches the server. The bounded
		// delay is a fallback only, in case that watch event is delayed.
		return types.ResultAfter(categoryDeletionRetryInterval)
	}

	return types.ResultOK()
}

const (
	datastoreForegroundDeletionFinalizer = "gitstore.dev/foreground-deletion"
	categoryDeletionRetryInterval        = time.Second
)

func (r *Reconciler) reconcileDeletion(ctx context.Context, current CategoryTaxonomy) types.ReconcileResult {
	if r.deletion == nil || (current.DeletionTimestamp == nil && !slices.Contains(current.Finalizers, datastoreForegroundDeletionFinalizer)) {
		return types.ResultOK()
	}
	hasMore, err := r.deletion.DecoupleProducts(ctx, current.Namespace, current.Name, current.ResourceVersion)
	if err != nil {
		health.CategoryDeletionRetriesTotal.Inc()
		return types.ResultTransient(fmt.Errorf("categorytaxonomy: decouple Products: %w", err))
	}
	if hasMore {
		health.CategoryDeletionProductPagesTotal.Inc()
		return types.ResultAfter(categoryDeletionRetryInterval)
	}
	if err := r.deletion.CompleteDeletion(ctx, current.Namespace, current.Name, current.ResourceVersion, current.UID); err != nil {
		if errors.Is(err, types.ErrConflict) {
			health.CategoryDeletionConflictsTotal.Inc()
		} else {
			health.CategoryDeletionRetriesTotal.Inc()
		}
		return types.ResultTransient(fmt.Errorf("categorytaxonomy: complete deletion: %w", err))
	}
	return types.ResultOK()
}

func previousResolved(raw json.RawMessage) *ResolvedCategoryTaxonomy {
	if len(raw) == 0 {
		return nil
	}
	var r ResolvedCategoryTaxonomy
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	return &r
}

// preserveLastTransitionTimes copies LastTransitionTime from a matching
// (same Type and Status) prior condition onto each freshly computed one, so
// a condition that hasn't actually transitioned doesn't get a new timestamp
// on every reconcile — which would otherwise defeat IsNoOp's no-op
// suppression (FR-013) even when nothing observable changed.
func preserveLastTransitionTimes(fresh []*status.Condition, prior []*status.Condition) {
	priorByType := make(map[string]*status.Condition, len(prior))
	for _, p := range prior {
		if p != nil {
			priorByType[p.Type] = p
		}
	}
	for _, f := range fresh {
		if p, ok := priorByType[f.Type]; ok && p.Status == f.Status {
			f.LastTransitionTime = p.LastTransitionTime
		}
	}
}

func conditionStatusByType(conditions []*status.Condition, condType string) string {
	for _, c := range conditions {
		if c != nil && c.Type == condType {
			return c.Status
		}
	}
	return ""
}

func hierarchyChanged(previous *ResolvedCategoryTaxonomy, current ResolvedCategoryTaxonomy) bool {
	if previous == nil {
		return true
	}
	return previous.Depth != current.Depth || !slices.Equal(previous.Path, current.Path)
}
