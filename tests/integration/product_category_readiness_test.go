// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Package-level tests covering spec 062 (Product Category and Readiness
// Reconciliation): a Product's spec.categoryRef resolves against the
// gitstore-controller-manager Product reconciler and converges CategoryResolved/
// Ready via updateProductStatus. These push through the real git-admission
// pipeline and poll the GraphQL API for the controller's asynchronous status
// write, mirroring categorytaxonomy_reconciler_test.go's pattern.

// ── GraphQL response shapes ───────────────────────────────────────────────────

type productResolvedCategoryResult struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type productResolvedResult struct {
	Category *productResolvedCategoryResult `json:"category"`
}

type productConditionResult struct {
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Reason  *string `json:"reason"`
	Message *string `json:"message"`
}

type productStatusResult struct {
	Resolved   *productResolvedResult   `json:"resolved"`
	Conditions []productConditionResult `json:"conditions"`
}

type productWithStatusResult struct {
	Status *productStatusResult `json:"status"`
}

func queryProductStatus(t *testing.T, namespace, name string) *productStatusResult {
	t.Helper()
	resp := gqlQuery(t, `
		query($namespace: String!, $name: String!) {
			product(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				status {
					resolved { category { name uid } }
					conditions { type status reason message }
				}
			}
		}
	`, map[string]any{"namespace": namespace, "name": name})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors querying product %q status: %s", name, resp.Errors)
	}
	var data struct {
		Product *productWithStatusResult `json:"product"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal product status response: %v", err)
	}
	if data.Product == nil {
		return nil
	}
	return data.Product.Status
}

// waitForProductStatus polls until namespace/name's status satisfies want, or
// fails the test after timeout. Reconciliation happens on a separate
// gitstore-controller-manager process, so there is no synchronous signal to
// wait on.
func waitForProductStatus(t *testing.T, namespace, name string, timeout time.Duration, want func(*productStatusResult) bool) *productStatusResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *productStatusResult
	for time.Now().Before(deadline) {
		last = queryProductStatus(t, namespace, name)
		if last != nil && want(last) {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s/%s's status to satisfy the expected condition; last observed: %+v", timeout, namespace, name, last)
	return nil
}

func queryCategoryID(t *testing.T, namespace, name string) string {
	t.Helper()
	resp := gqlQuery(t, `
		query($namespace: String!, $name: String!) {
			category(by: {namespacePath: {namespace: $namespace, name: $name}}) { id }
		}
	`, map[string]any{"namespace": namespace, "name": name})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors querying category %q id: %s", name, resp.Errors)
	}
	var data struct {
		Category *struct {
			ID string `json:"id"`
		} `json:"category"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal category id response: %v", err)
	}
	if data.Category == nil {
		t.Fatalf("category %s/%s not found", namespace, name)
	}
	return data.Category.ID
}

func deleteCategoryByID(t *testing.T, id string) {
	t.Helper()
	resp := gqlQuery(t, `
		mutation($id: ID!) {
			deleteCategory(input: {id: $id}) { deletedCategoryId }
		}
	`, map[string]any{"id": id})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors deleting category %q: %s", id, resp.Errors)
	}
}

func productCondition(status *productStatusResult, conditionType string) *productConditionResult {
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return &status.Conditions[i]
		}
	}
	return nil
}

// ── User Story 1: category already exists — Product becomes Ready immediately ─

// TestProductCategoryReadiness_CategoryAlreadyExists covers T023/spec.md US1's
// Independent Test and quickstart.md §1: a Product referencing an
// already-existing CategoryTaxonomy converges to CategoryResolved=True/
// CategoryFound and Ready=True, with resolved.category.uid equal to the
// category's own opaque id (FR-016), without any further author action.
func TestProductCategoryReadiness_CategoryAlreadyExists(t *testing.T) {
	ts := time.Now().UnixNano()
	ns := getEnv("NAMESPACE", "gitstore-test")
	categoryName := fmt.Sprintf("laptops-%d", ts)
	productName := fmt.Sprintf("macbook-%d", ts)

	h := newPushHelper(t)
	h.commitCategory(categoryName+".md", rootCategoryFixture(categoryName))
	h.commitProduct(productName+".md", productWithCategoryRefFixture(productName, ns, categoryName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push category+product failed:\n%s", out)
	}

	categoryID := queryCategoryID(t, ns, categoryName)

	status := waitForProductStatus(t, ns, productName, 30*time.Second, func(s *productStatusResult) bool {
		ready := productCondition(s, "Ready")
		return ready != nil && ready.Status == "TRUE"
	})

	categoryResolved := productCondition(status, "CategoryResolved")
	if categoryResolved == nil || categoryResolved.Status != "TRUE" || categoryResolved.Reason == nil || *categoryResolved.Reason != "CategoryFound" {
		t.Errorf("CategoryResolved condition: got %+v, want status=TRUE reason=CategoryFound", categoryResolved)
	}
	if status.Resolved == nil || status.Resolved.Category == nil {
		t.Fatalf("status.resolved.category is nil, want a resolved category reference")
	}
	if status.Resolved.Category.Name != categoryName {
		t.Errorf("resolved.category.name: got %q, want %q", status.Resolved.Category.Name, categoryName)
	}
	if status.Resolved.Category.UID != categoryID {
		t.Errorf("resolved.category.uid: got %q, want the category's own id %q (FR-016)", status.Resolved.Category.UID, categoryID)
	}

	// SC-003/FR-007: re-push the Product with an unrelated spec change (title)
	// but the same categoryRef, and confirm CategoryResolved doesn't flap
	// lastTransitionTime-driven state and stays converged across the second
	// reconcile (T024). Git requires an actual content change to commit.
	h2 := newPushHelper(t)
	h2.commitProduct(productName+".md", fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
spec:
  title: %s Updated
  categoryRef:
    name: %s
    kind: CategoryTaxonomy
---

Product in category %s.
`, productName, ns, productName, categoryName, categoryName))
	if out, err := h2.push(); err != nil {
		t.Fatalf("re-push product failed:\n%s", out)
	}
	time.Sleep(2 * time.Second)
	restatus := queryProductStatus(t, ns, productName)
	reready := productCondition(restatus, "Ready")
	if reready == nil || reready.Status != "TRUE" {
		t.Errorf("Ready after re-push: got %+v, want status=TRUE (still converged)", reready)
	}
	recat := productCondition(restatus, "CategoryResolved")
	if recat == nil || recat.Status != "TRUE" || recat.Reason == nil || *recat.Reason != "CategoryFound" {
		t.Errorf("CategoryResolved after re-push: got %+v, want status=TRUE reason=CategoryFound (FR-007: no flap)", recat)
	}
	if restatus.Resolved == nil || restatus.Resolved.Category == nil || restatus.Resolved.Category.Name != categoryName {
		t.Errorf("resolved.category after re-push: got %+v, want unchanged {%s ...}", restatus.Resolved, categoryName)
	}
}

// ── User Story 2: category created after the Product — watch-driven convergence ─

// TestProductCategoryReadiness_CategoryCreatedAfterProduct covers T027/spec.md
// US2's Independent Test and quickstart.md §2: a Product pushed before its
// target category exists is accepted and non-blocking
// (CategoryResolved=False/CategoryNotFound), then converges to Ready=True once
// the category is created — driven by the watch-based re-enqueue (FR-009), not
// a new Product push.
func TestProductCategoryReadiness_CategoryCreatedAfterProduct(t *testing.T) {
	ts := time.Now().UnixNano()
	ns := getEnv("NAMESPACE", "gitstore-test")
	categoryName := fmt.Sprintf("gadgets-%d", ts)
	productName := fmt.Sprintf("early-bird-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(productName+".md", productWithCategoryRefFixture(productName, ns, categoryName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push product failed:\n%s", out)
	}

	waitForProductStatus(t, ns, productName, 30*time.Second, func(s *productStatusResult) bool {
		cond := productCondition(s, "CategoryResolved")
		return cond != nil && cond.Status == "FALSE" && cond.Reason != nil && *cond.Reason == "CategoryNotFound"
	})

	h2 := newPushHelper(t)
	h2.commitCategory(categoryName+".md", rootCategoryFixture(categoryName))
	if out, err := h2.push(); err != nil {
		t.Fatalf("push category failed:\n%s", out)
	}

	status := waitForProductStatus(t, ns, productName, 30*time.Second, func(s *productStatusResult) bool {
		ready := productCondition(s, "Ready")
		return ready != nil && ready.Status == "TRUE"
	})
	categoryResolved := productCondition(status, "CategoryResolved")
	if categoryResolved == nil || categoryResolved.Status != "TRUE" || categoryResolved.Reason == nil || *categoryResolved.Reason != "CategoryFound" {
		t.Errorf("CategoryResolved after category creation: got %+v, want status=TRUE reason=CategoryFound", categoryResolved)
	}
}

// ── User Story 3: a previously resolved category disappears ──────────────────

// TestProductCategoryReadiness_CategoryDeletedAfterResolution covers T032/
// spec.md US3's Independent Test: a Ready Product's category is deleted; the
// Product's status converges to (and stays at) CategoryResolved=False/
// CategoryNotFound rather than the transient CategoryDeleted reason
// DecoupleCategoryProducts writes first (FR-010, T012/T017's
// Terminating-as-not-found rule).
func TestProductCategoryReadiness_CategoryDeletedAfterResolution(t *testing.T) {
	ts := time.Now().UnixNano()
	ns := getEnv("NAMESPACE", "gitstore-test")
	categoryName := fmt.Sprintf("tablets-%d", ts)
	productName := fmt.Sprintf("slate-%d", ts)

	h := newPushHelper(t)
	h.commitCategory(categoryName+".md", rootCategoryFixture(categoryName))
	h.commitProduct(productName+".md", productWithCategoryRefFixture(productName, ns, categoryName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push category+product failed:\n%s", out)
	}

	waitForProductStatus(t, ns, productName, 30*time.Second, func(s *productStatusResult) bool {
		ready := productCondition(s, "Ready")
		return ready != nil && ready.Status == "TRUE"
	})

	categoryID := queryCategoryID(t, ns, categoryName)
	deleteCategoryByID(t, categoryID)

	// Poll well past the transient CategoryDeleted write to confirm the
	// converged reason is CategoryNotFound, not CategoryDeleted — and that it
	// stays that way (no flap back through True).
	var final *productStatusResult
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		final = queryProductStatus(t, ns, productName)
		cond := productCondition(final, "CategoryResolved")
		if cond != nil && cond.Status == "FALSE" && cond.Reason != nil && *cond.Reason == "CategoryNotFound" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	categoryResolved := productCondition(final, "CategoryResolved")
	if categoryResolved == nil || categoryResolved.Status != "FALSE" || categoryResolved.Reason == nil || *categoryResolved.Reason != "CategoryNotFound" {
		t.Fatalf("CategoryResolved after category deletion: got %+v, want status=FALSE reason=CategoryNotFound (converged, not the transient CategoryDeleted)", categoryResolved)
	}
	ready := productCondition(final, "Ready")
	if ready == nil || ready.Status != "FALSE" {
		t.Errorf("Ready after category deletion: got %+v, want status=FALSE", ready)
	}
	if final.Resolved != nil && final.Resolved.Category != nil {
		t.Errorf("resolved.category after category deletion: got %+v, want nil (FR-015)", final.Resolved.Category)
	}

	// Stability check: re-query after a short additional wait to confirm no
	// flap back through CategoryResolved=True once the category is fully gone.
	time.Sleep(2 * time.Second)
	settled := queryProductStatus(t, ns, productName)
	settledCond := productCondition(settled, "CategoryResolved")
	if settledCond == nil || settledCond.Status != "FALSE" {
		t.Errorf("CategoryResolved settled state: got %+v, want status=FALSE (no flap back to True)", settledCond)
	}
}
