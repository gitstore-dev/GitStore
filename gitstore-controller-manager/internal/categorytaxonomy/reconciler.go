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
	"fmt"
	"slices"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// CategoryTaxonomy is the cache entity populated by the Runner[CategoryTaxonomy]
// list-then-watch loop against watchCategories/categories.
type CategoryTaxonomy struct {
	UID             string
	Namespace       string
	Name            string
	Generation      int64
	ResourceVersion string
	// ParentRefName is empty when this category has no parent (root candidate).
	// Mirrors spec.parentRef.name.
	ParentRefName string
	// Media mirrors spec.media, used by the required-file-reference
	// condition (US3, FR-010/FR-011).
	Media  []MediaRef
	Status status.ResourceStatus
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
	cache        cache.CacheAccessor[CategoryTaxonomy]
	statusClient status.StatusClient
	productCount ProductCounter
	enqueue      EnqueueFunc
}

// NewReconciler returns a Reconciler reading from c, writing status through
// statusClient, resolving productCount via productCounter, and re-enqueueing
// affected descendants via enqueue (research.md R2). enqueue may be nil if
// the caller does not need descendant propagation (e.g. in a test that only
// exercises a single node).
func NewReconciler(c cache.CacheAccessor[CategoryTaxonomy], statusClient status.StatusClient, productCounter ProductCounter, enqueue EnqueueFunc) *Reconciler {
	return &Reconciler{cache: c, statusClient: statusClient, productCount: productCounter, enqueue: enqueue}
}

// Reconcile implements types.Reconciler. See contracts/reconciler-contract.md
// for the full 8-step algorithm this follows.
func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	current, ok := r.cache.Get(key)
	if !ok {
		return types.ResultTerminal(fmt.Errorf("categorytaxonomy: %s/%s not found in cache", key.Namespace, key.Name))
	}

	parentMap := make(map[string]string)
	for _, item := range r.cache.List() {
		if item.Namespace == key.Namespace {
			parentMap[item.Name] = item.ParentRefName
		}
	}
	inCycle := detectCycles(parentMap)

	previous := previousResolved(current.Status.Resolved)

	var resolved ResolvedCategoryTaxonomy
	if inCycle[current.Name] {
		// FR-008: cycle participants keep their last-observed Path/Depth —
		// never recomputed, never reset — while ChildCount/ProductCount
		// still reflect current cache state.
		if previous != nil {
			resolved.Depth = previous.Depth
			resolved.Path = previous.Path
		}
		var childCount int64
		for _, item := range r.cache.List() {
			if item.Namespace == key.Namespace && item.ParentRefName == key.Name {
				childCount++
			}
		}
		resolved.ChildCount = childCount
	} else {
		productCount := int64(0)
		if r.productCount != nil {
			pc, err := r.productCount(ctx, key.Namespace, key.Name)
			if err != nil {
				return types.ResultTransient(fmt.Errorf("categorytaxonomy: count products: %w", err))
			}
			productCount = pc
		}
		resolved = computeHierarchy(r.cache, current, productCount)
	}

	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		return types.ResultTransient(fmt.Errorf("categorytaxonomy: marshal resolved status: %w", err))
	}

	parentResolvedCond := computeParentResolved(r.cache, current)
	acyclicCond := computeAcyclic(inCycle[current.Name])
	fileRefCond := computeFileRefCondition(current)
	readyCond := computeReady(parentResolvedCond, acyclicCond, fileRefCond)

	conditions := []*status.Condition{&parentResolvedCond, &acyclicCond}
	if fileRefCond != nil {
		conditions = append(conditions, fileRefCond)
	}
	conditions = append(conditions, &readyCond)
	preserveLastTransitionTimes(conditions, current.Status.Conditions)

	gen := current.Generation
	patch := &status.StatusPatch{
		ResourceVersion:    current.ResourceVersion,
		ObservedGeneration: &gen,
		Resolved:           resolvedJSON,
		Conditions:         conditions,
	}

	if patch.IsNoOp(current.Status) {
		return types.ResultOK()
	}

	// Any Apply failure -- including types.ErrConflict -- is retried: a
	// conflict means the cache is stale and will be corrected on the next
	// dispatch once the watch delivers the newer version (FR-014).
	if err := r.statusClient.Apply(ctx, key, patch); err != nil {
		return types.ResultTransient(err)
	}

	if !inCycle[current.Name] && hierarchyChanged(previous, resolved) {
		r.reenqueueChildren(key)
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

func hierarchyChanged(previous *ResolvedCategoryTaxonomy, current ResolvedCategoryTaxonomy) bool {
	if previous == nil {
		return true
	}
	return previous.Depth != current.Depth || !slices.Equal(previous.Path, current.Path)
}

func (r *Reconciler) reenqueueChildren(parent types.WorkItemKey) {
	if r.enqueue == nil {
		return
	}
	for _, item := range r.cache.List() {
		if item.Namespace == parent.Namespace && item.ParentRefName == parent.Name {
			_ = r.enqueue(types.WorkItemKey{Kind: parent.Kind, Namespace: item.Namespace, Name: item.Name})
		}
	}
}
