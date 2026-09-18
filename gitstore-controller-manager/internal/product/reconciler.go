// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package product owns Product category resolution and finalizer
// completion for terminating Products.
package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const (
	foregroundDeletionFinalizer = "gitstore.dev/foreground-deletion"

	conditionCategoryResolved = "CategoryResolved"
	conditionReady            = "Ready"

	// ConditionStatus values are GraphQL enum wire values, rather than Go
	// bool strings. StatusPatch.IsNoOp compares these values exactly.
	statusTrue  = "TRUE"
	statusFalse = "FALSE"

	conflictRequeueDelay = 100 * time.Millisecond
	// categoryUnresolvedRequeueDelay is the FR-011 bounded-interval fallback
	// retry for CategoryResolved=False — the watch-driven re-enqueue (R3)
	// converges immediately once a matching category exists; this interval
	// only covers cases that re-enqueue might miss (e.g. replayed/missed
	// events).
	categoryUnresolvedRequeueDelay = time.Minute
)

// resolvedCategory is the JSON payload marshaled into StatusPatch.Resolved,
// matching gitstore-api/internal/catalog.ResolvedProductStatusInput's shape
// (contracts/product-status-category-ref.graphqls).
type resolvedCategory struct {
	Category *resolvedCategoryRef `json:"category,omitempty"`
}

type resolvedCategoryRef struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type CompletionClient interface {
	CompleteDeletion(context.Context, string, string, string) error
}

// Reconciler implements types.Reconciler for the Product kind: it resolves
// spec.categoryRef against the CategoryTaxonomy cache and writes
// CategoryResolved/Ready status (spec 062), and completes foreground
// deletion for terminating Products (pre-existing behavior, unchanged).
type Reconciler struct {
	cache         cache.CacheAccessor[categorytaxonomy.Product]
	categoryCache cache.CacheAccessor[categorytaxonomy.CategoryTaxonomy]
	statusClient  status.StatusClient
	completion    CompletionClient
}

// NewReconciler returns a Product reconciler reading Products from c,
// resolving spec.categoryRef against categoryCache, writing status through
// statusClient, and completing foreground deletion through completion.
func NewReconciler(c cache.CacheAccessor[categorytaxonomy.Product], categoryCache cache.CacheAccessor[categorytaxonomy.CategoryTaxonomy], statusClient status.StatusClient, completion CompletionClient) *Reconciler {
	return &Reconciler{cache: c, categoryCache: categoryCache, statusClient: statusClient, completion: completion}
}

func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	p, ok := r.cache.Get(key)
	if !ok {
		return types.ResultOK()
	}
	if p.DeletionTimestamp != nil && hasFinalizer(p.Finalizers) {
		if err := r.completion.CompleteDeletion(ctx, p.Namespace, p.Name, p.ResourceVersion); err != nil {
			if errors.Is(err, types.ErrConflict) {
				return types.ResultAfter(conflictRequeueDelay)
			}
			return types.ResultTransient(fmt.Errorf("product: complete deletion: %w", err))
		}
		return types.ResultOK()
	}
	return r.reconcileActive(ctx, key, p)
}

// reconcileActive runs the uniform resolution algorithm (data-model.md's
// "Reconciler resolution algorithm") unconditionally, regardless of why this
// Product was enqueued (initial admission, spec update, or a
// CategoryTaxonomy-driven re-enqueue).
func (r *Reconciler) reconcileActive(ctx context.Context, key types.WorkItemKey, current categorytaxonomy.Product) types.ReconcileResult {
	found, resolved := r.resolveCategory(current)
	// A Product with no categoryRef at all has nothing to resolve — that is
	// a valid, uncategorized Product (spec.categoryRef is nullable), not an
	// unresolved reference. Only an actually-set-but-unmatched categoryRef
	// is CategoryNotFound.
	categoryResolved := found || current.CategoryRefName == ""
	admitted := conditionTrue(current.Status.Conditions, "AdmissionAccepted")

	conditions := mergeCategoryConditions(current, found, categoryResolved, admitted)
	generation := current.Generation
	patch := &status.StatusPatch{
		ResourceVersion:    current.ResourceVersion,
		ObservedGeneration: &generation,
		Conditions:         conditions,
	}
	resolvedJSON, err := json.Marshal(resolvedCategory{Category: resolved})
	if err != nil {
		return types.ResultTransient(fmt.Errorf("product: marshal resolved status: %w", err))
	}
	patch.Resolved = resolvedJSON

	if !patch.IsNoOp(current.Status) {
		if err := r.statusClient.Apply(ctx, key, patch); err != nil {
			if errors.Is(err, types.ErrConflict) {
				// Another replica (or a newer watch event) won the status
				// write. Re-enter through the queue so the next attempt
				// observes fresh cache state instead of exhausting one
				// stale retry budget.
				return types.ResultAfter(conflictRequeueDelay)
			}
			return types.ResultTransient(fmt.Errorf("product: update status: %w", err))
		}
	}
	if !categoryResolved {
		// FR-011: bounded-interval fallback retry for CategoryNotFound (a set
		// but unmatched categoryRef only — a Product with no categoryRef has
		// nothing to retry). The watch-driven re-enqueue (R3) is the primary
		// convergence path; this is only a fallback for missed/replayed
		// events.
		return types.ResultAfter(categoryUnresolvedRequeueDelay)
	}
	return types.ResultOK()
}

// resolveCategory looks up current.CategoryRefName in the CategoryTaxonomy
// cache, scoped to current.Namespace. A match counts as found only when it
// is not itself Terminating (no DeletionTimestamp, no foreground-deletion
// finalizer) — research.md R8's Terminating-as-not-found rule, which is
// what makes DecoupleCategoryProducts' transient CategoryDeleted reason
// converge to CategoryNotFound instead of flapping back through True.
func (r *Reconciler) resolveCategory(current categorytaxonomy.Product) (found bool, ref *resolvedCategoryRef) {
	if current.CategoryRefName == "" {
		return false, nil
	}
	for _, candidate := range r.categoryCache.List() {
		if candidate.Namespace != current.Namespace || candidate.Name != current.CategoryRefName {
			continue
		}
		if candidate.DeletionTimestamp != nil || slices.Contains(candidate.Finalizers, foregroundDeletionFinalizer) {
			return false, nil
		}
		// candidate.UID is already the Relay-encoded id: it is populated
		// straight from the metadata.uid GraphQL field, which every kind's
		// resolver already encodes API-side. This process has no access to
		// that encoding scheme and must never attempt to re-derive it.
		return true, &resolvedCategoryRef{Name: candidate.Name, UID: candidate.UID}
	}
	return false, nil
}

// mergeCategoryConditions builds fresh CategoryResolved/Ready conditions and
// merges them into current's other conditions. Ready=True iff admitted and
// categoryResolved (FR-004) — AdmissionAccepted is a precondition of the
// controller ever observing a reconcilable Product, so checking it here is
// defensive, not redundant (research.md R4). categoryResolved is true both
// when found (an actual categoryRef resolved) and when the Product has no
// categoryRef at all — spec.categoryRef is nullable, and an uncategorized
// Product is a valid, Ready-eligible state, not an unresolved reference.
func mergeCategoryConditions(current categorytaxonomy.Product, found, categoryResolved, admitted bool) []*status.Condition {
	conditions := make([]*status.Condition, 0, len(current.Status.Conditions)+2)
	for _, prior := range current.Status.Conditions {
		if prior == nil || prior.Type == conditionCategoryResolved || prior.Type == conditionReady {
			continue
		}
		copied := *prior
		conditions = append(conditions, &copied)
	}

	categoryCondition := &status.Condition{
		Type:               conditionCategoryResolved,
		Status:             statusFalse,
		ObservedGeneration: current.Generation,
		LastTransitionTime: time.Now(),
		Reason:             "CategoryNotFound",
		Message:            "spec.categoryRef does not resolve to an existing CategoryTaxonomy in this namespace.",
	}
	switch {
	case found:
		categoryCondition.Status = statusTrue
		categoryCondition.Reason = "CategoryFound"
		categoryCondition.Message = "spec.categoryRef resolves to an existing CategoryTaxonomy in this namespace."
	case categoryResolved:
		categoryCondition.Status = statusTrue
		categoryCondition.Reason = "NoCategoryReference"
		categoryCondition.Message = "spec.categoryRef is not set; the Product is uncategorized."
	}

	ready := admitted && categoryResolved
	readyCondition := &status.Condition{
		Type:               conditionReady,
		Status:             statusFalse,
		ObservedGeneration: current.Generation,
		LastTransitionTime: time.Now(),
		Reason:             "CategoryUnresolved",
		Message:            "AdmissionAccepted and CategoryResolved must both be True.",
	}
	if ready {
		readyCondition.Status = statusTrue
		readyCondition.Reason = "ProductReady"
		readyCondition.Message = "AdmissionAccepted and CategoryResolved are both True."
	}

	preserveTransitionTime(categoryCondition, current.Status.Conditions)
	preserveTransitionTime(readyCondition, current.Status.Conditions)
	return append(conditions, categoryCondition, readyCondition)
}

func preserveTransitionTime(fresh *status.Condition, prior []*status.Condition) {
	for _, condition := range prior {
		if condition != nil && condition.Type == fresh.Type && condition.Status == fresh.Status {
			fresh.LastTransitionTime = condition.LastTransitionTime
			return
		}
	}
}

func conditionTrue(conditions []*status.Condition, conditionType string) bool {
	for _, condition := range conditions {
		if condition != nil && condition.Type == conditionType {
			return strings.EqualFold(condition.Status, "true")
		}
	}
	return false
}

func hasFinalizer(values []string) bool {
	return slices.Contains(values, foregroundDeletionFinalizer)
}
