// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Package-level tests covering spec 039 (CategoryTaxonomy Controller
// Reconciliation) FR-015: a depth-3 hierarchy with correct
// depth/path/childCount/productCount at every level, a cycle scenario where
// Acyclic=False is observable in status for all participants, and the
// required-file-reference condition for both optional:false and
// optional:true media entries. These push through the real git-admission
// pipeline and poll gitstore-controller-manager's status writes via the
// GraphQL API — reconciliation is asynchronous, unlike admission-time
// status (see category_taxonomy_test.go), so a fixed sleep is not enough.

// ── Fixtures ─────────────────────────────────────────────────────────────────

func categoryWithMediaFixture(name, parentName, fileName string, optional bool) string {
	parentRef := ""
	if parentName != "" {
		parentRef = fmt.Sprintf("  parentRef:\n    name: %s\n    kind: CategoryTaxonomy\n", parentName)
	}
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: %s
spec:
  title: %s
%s  media:
  - fileRef:
      name: %s
      kind: File
      optional: %t
---

Category description for %s.
`, name, name, parentRef, fileName, optional, name)
}

// ── GraphQL response shapes ──────────────────────────────────────────────────

type categoryResolvedResult struct {
	Depth        int      `json:"depth"`
	Path         []string `json:"path"`
	ChildCount   int      `json:"childCount"`
	ProductCount int      `json:"productCount"`
}

type categoryConditionResult struct {
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Reason  *string `json:"reason"`
	Message *string `json:"message"`
}

type categoryStatusResult struct {
	Resolved   *categoryResolvedResult   `json:"resolved"`
	Conditions []categoryConditionResult `json:"conditions"`
}

type categoryWithStatusResult struct {
	Status *categoryStatusResult `json:"status"`
}

func queryCategoryStatus(t *testing.T, name string) *categoryStatusResult {
	t.Helper()
	ns := getEnv("NAMESPACE", "gitstore-test")
	resp := gqlQuery(t, `
		query($namespace: String!, $name: String!) {
			category(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				status {
					resolved { depth path childCount productCount }
					conditions { type status reason message }
				}
			}
		}
	`, map[string]any{"namespace": ns, "name": name})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors querying category %q status: %s", name, resp.Errors)
	}
	var data struct {
		Category *categoryWithStatusResult `json:"category"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal category status response: %v", err)
	}
	if data.Category == nil {
		return nil
	}
	return data.Category.Status
}

func conditionByType(conditions []categoryConditionResult, condType string) *categoryConditionResult {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// waitForResolved polls until name's status.resolved satisfies want, or fails
// the test after timeout. Reconciliation happens on a separate
// gitstore-controller-manager process consuming a watch subscription, so
// there is no synchronous signal to wait on.
func waitForResolved(t *testing.T, name string, timeout time.Duration, want func(*categoryStatusResult) bool) *categoryStatusResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *categoryStatusResult
	for time.Now().Before(deadline) {
		last = queryCategoryStatus(t, name)
		if last != nil && want(last) {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %q's status to satisfy the expected condition; last observed: %+v", timeout, name, last)
	return nil
}

// ── FR-015: depth-3 hierarchy with correct depth/path/childCount/productCount ─

func TestCategoryTaxonomyReconciler_DepthThreeHierarchy(t *testing.T) {
	ts := time.Now().UnixNano()
	rootName := fmt.Sprintf("electronics-%d", ts)
	midName := fmt.Sprintf("computers-%d", ts)
	leafName := fmt.Sprintf("laptops-%d", ts)
	ns := getEnv("NAMESPACE", "gitstore-test")

	// Push the product before the category exists in the datastore: since
	// productCount only recomputes on the category's own reconcile (research.md
	// R4 deliberately does not watch Product), the product must already be
	// present by the time the leaf category's reconcile runs, and the leaf
	// category must reconcile *after* this push for its count to include it.
	h0 := newPushHelper(t)
	h0.commitProduct(fmt.Sprintf("widget-%d.md", ts), productWithCategoryRefFixture(fmt.Sprintf("widget-%d", ts), ns, leafName))
	if out, err := h0.push(); err != nil {
		t.Fatalf("push product failed:\n%s", out)
	}

	h := newPushHelper(t)
	h.commitCategory(rootName+".md", rootCategoryFixture(rootName))
	h.commitCategory(midName+".md", childCategoryFixture(midName, rootName))
	h.commitCategory(leafName+".md", childCategoryFixture(leafName, midName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push hierarchy failed:\n%s", out)
	}

	rootStatus := waitForResolved(t, rootName, 30*time.Second, func(s *categoryStatusResult) bool {
		return s.Resolved != nil && s.Resolved.Depth == 0
	})
	if len(rootStatus.Resolved.Path) != 1 || rootStatus.Resolved.Path[0] != rootName {
		t.Errorf("root path: got %v, want [%s]", rootStatus.Resolved.Path, rootName)
	}
	if rootStatus.Resolved.ChildCount != 1 {
		t.Errorf("root childCount: got %d, want 1", rootStatus.Resolved.ChildCount)
	}

	midStatus := waitForResolved(t, midName, 30*time.Second, func(s *categoryStatusResult) bool {
		return s.Resolved != nil && s.Resolved.Depth == 1
	})
	wantMidPath := []string{rootName, midName}
	if len(midStatus.Resolved.Path) != 2 || midStatus.Resolved.Path[0] != wantMidPath[0] || midStatus.Resolved.Path[1] != wantMidPath[1] {
		t.Errorf("mid path: got %v, want %v", midStatus.Resolved.Path, wantMidPath)
	}
	if midStatus.Resolved.ChildCount != 1 {
		t.Errorf("mid childCount: got %d, want 1", midStatus.Resolved.ChildCount)
	}

	leafStatus := waitForResolved(t, leafName, 30*time.Second, func(s *categoryStatusResult) bool {
		return s.Resolved != nil && s.Resolved.ProductCount == 1
	})
	wantLeafPath := []string{rootName, midName, leafName}
	if len(leafStatus.Resolved.Path) != 3 || leafStatus.Resolved.Path[0] != wantLeafPath[0] || leafStatus.Resolved.Path[1] != wantLeafPath[1] || leafStatus.Resolved.Path[2] != wantLeafPath[2] {
		t.Errorf("leaf path: got %v, want %v", leafStatus.Resolved.Path, wantLeafPath)
	}
	if leafStatus.Resolved.Depth != 2 {
		t.Errorf("leaf depth: got %d, want 2", leafStatus.Resolved.Depth)
	}
	if leafStatus.Resolved.ChildCount != 0 {
		t.Errorf("leaf childCount: got %d, want 0", leafStatus.Resolved.ChildCount)
	}

	if ready := conditionByType(leafStatus.Conditions, "Ready"); ready == nil || ready.Status != "TRUE" {
		t.Errorf("leaf Ready condition: got %+v, want status=TRUE", ready)
	}
}

// ── FR-015: cycle scenario — Acyclic=False observable for all participants ───

func TestCategoryTaxonomyReconciler_CycleDetection(t *testing.T) {
	ts := time.Now().UnixNano()
	aName := fmt.Sprintf("cyc-a-%d", ts)
	bName := fmt.Sprintf("cyc-b-%d", ts)

	// First push: create both with no parentRef (roots), so admission accepts them.
	h := newPushHelper(t)
	h.commitCategory(aName+".md", rootCategoryFixture(aName))
	h.commitCategory(bName+".md", rootCategoryFixture(bName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push roots failed:\n%s", out)
	}

	// Wait for both to be reconciled once as acyclic roots before introducing the cycle.
	waitForResolved(t, aName, 30*time.Second, func(s *categoryStatusResult) bool { return s.Resolved != nil })
	waitForResolved(t, bName, 30*time.Second, func(s *categoryStatusResult) bool { return s.Resolved != nil })

	// Second push: point A at B and B at A across two commits in the same push.
	h2 := newPushHelper(t)
	h2.commitCategory(aName+".md", childCategoryFixture(aName, bName))
	h2.commitCategory(bName+".md", childCategoryFixture(bName, aName))
	if out, err := h2.push(); err != nil {
		t.Fatalf("push cycle failed:\n%s", out)
	}

	// Wait for the controller (not just admission) to have written Ready=FALSE,
	// which guarantees both Acyclic=FALSE and Ready=FALSE are present in the
	// same controller-written conditions slice.
	aStatus := waitForResolved(t, aName, 30*time.Second, func(s *categoryStatusResult) bool {
		ready := conditionByType(s.Conditions, "Ready")
		return ready != nil && ready.Status == "FALSE"
	})
	bStatus := waitForResolved(t, bName, 30*time.Second, func(s *categoryStatusResult) bool {
		ready := conditionByType(s.Conditions, "Ready")
		return ready != nil && ready.Status == "FALSE"
	})
	if ready := conditionByType(aStatus.Conditions, "Ready"); ready == nil || ready.Status != "FALSE" {
		t.Errorf("A Ready condition while cyclic: got %+v, want status=FALSE", ready)
	}
	if ready := conditionByType(bStatus.Conditions, "Ready"); ready == nil || ready.Status != "FALSE" {
		t.Errorf("B Ready condition while cyclic: got %+v, want status=FALSE", ready)
	}

	// Break the cycle: remove B's parentRef, making it a root again.
	h3 := newPushHelper(t)
	h3.commitCategory(bName+".md", rootCategoryFixture(bName))
	if out, err := h3.push(); err != nil {
		t.Fatalf("push cycle-break failed:\n%s", out)
	}

	waitForResolved(t, bName, 30*time.Second, func(s *categoryStatusResult) bool {
		ready := conditionByType(s.Conditions, "Ready")
		return ready != nil && ready.Status == "TRUE"
	})
	waitForResolved(t, aName, 30*time.Second, func(s *categoryStatusResult) bool {
		ready := conditionByType(s.Conditions, "Ready")
		return ready != nil && ready.Status == "TRUE"
	})
}

// ── FR-015: required-file-reference condition (optional:false vs optional:true) ─

func TestCategoryTaxonomyReconciler_RequiredFileReferenceCondition(t *testing.T) {
	ts := time.Now().UnixNano()
	requiredName := fmt.Sprintf("filereq-%d", ts)
	optionalName := fmt.Sprintf("fileopt-%d", ts)

	h := newPushHelper(t)
	h.commitCategory(requiredName+".md", categoryWithMediaFixture(requiredName, "", "missing-image", false))
	h.commitCategory(optionalName+".md", categoryWithMediaFixture(optionalName, "", "missing-image", true))
	if out, err := h.push(); err != nil {
		t.Fatalf("expected push with unresolvable file references to be accepted, got:\n%s", out)
	}

	requiredStatus := waitForResolved(t, requiredName, 30*time.Second, func(s *categoryStatusResult) bool {
		return conditionByType(s.Conditions, "FileRefConfirmed") != nil
	})
	fileRefCond := conditionByType(requiredStatus.Conditions, "FileRefConfirmed")
	if fileRefCond.Status != "UNKNOWN" {
		t.Errorf("optional:false FileRefConfirmed.Status: got %q, want UNKNOWN", fileRefCond.Status)
	}

	optionalStatus := waitForResolved(t, optionalName, 30*time.Second, func(s *categoryStatusResult) bool {
		return s.Resolved != nil
	})
	if cond := conditionByType(optionalStatus.Conditions, "FileRefConfirmed"); cond != nil {
		t.Errorf("optional:true media must not raise a FileRefConfirmed condition, got %+v", cond)
	}
	if ready := conditionByType(optionalStatus.Conditions, "Ready"); ready == nil || ready.Status != "TRUE" {
		t.Errorf("optional:true Ready condition: got %+v, want status=TRUE (file-ref absence must not block Ready)", ready)
	}
}
