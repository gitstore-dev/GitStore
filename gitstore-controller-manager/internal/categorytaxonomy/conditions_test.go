// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
)

func TestComputeParentResolved_AbsentParentRef(t *testing.T) {
	self := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}
	c := seedCache(t, self)

	cond := computeParentResolved(c, self)

	if cond.Status != "TRUE" {
		t.Errorf("Status = %q, want TRUE", cond.Status)
	}
}

func TestComputeParentResolved_ResolvingParentRef(t *testing.T) {
	parent := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}
	self := CategoryTaxonomy{Namespace: "acme", Name: "computers", ParentRefName: "electronics"}
	c := seedCache(t, parent, self)

	cond := computeParentResolved(c, self)

	if cond.Status != "TRUE" {
		t.Errorf("Status = %q, want TRUE", cond.Status)
	}
}

func TestComputeParentResolved_NonexistentParentRef(t *testing.T) {
	self := CategoryTaxonomy{Namespace: "acme", Name: "computers", ParentRefName: "ghost"}
	c := seedCache(t, self)

	cond := computeParentResolved(c, self)

	if cond.Status != "FALSE" {
		t.Errorf("Status = %q, want FALSE", cond.Status)
	}
	if cond.Reason == "" || cond.Message == "" {
		t.Error("expected a reason/message identifying the missing parent")
	}
}

func TestComputeAcyclic_CycleParticipantIsFalse(t *testing.T) {
	cond := computeAcyclic(true)
	if cond.Status != "FALSE" {
		t.Errorf("Status = %q, want FALSE", cond.Status)
	}
}

func TestComputeAcyclic_NonParticipantIsTrue(t *testing.T) {
	cond := computeAcyclic(false)
	if cond.Status != "TRUE" {
		t.Errorf("Status = %q, want TRUE", cond.Status)
	}
}

func TestComputeReady_AllTrueYieldsTrue(t *testing.T) {
	parentResolved := status.Condition{Type: "ParentResolved", Status: "TRUE"}
	acyclic := status.Condition{Type: "Acyclic", Status: "TRUE"}

	cond := computeReady(parentResolved, acyclic, nil)
	if cond.Status != "TRUE" {
		t.Errorf("Status = %q, want TRUE", cond.Status)
	}
}

func TestComputeReady_AnyNonTrueYieldsFalse(t *testing.T) {
	parentResolved := status.Condition{Type: "ParentResolved", Status: "FALSE"}
	acyclic := status.Condition{Type: "Acyclic", Status: "TRUE"}

	cond := computeReady(parentResolved, acyclic, nil)
	if cond.Status != "FALSE" {
		t.Errorf("Status = %q, want FALSE", cond.Status)
	}
}

func TestComputeReady_UnknownFileRefConditionYieldsFalse(t *testing.T) {
	parentResolved := status.Condition{Type: "ParentResolved", Status: "TRUE"}
	acyclic := status.Condition{Type: "Acyclic", Status: "TRUE"}
	fileRef := &status.Condition{Type: "FileRefConfirmed", Status: "UNKNOWN"}

	cond := computeReady(parentResolved, acyclic, fileRef)
	if cond.Status != "FALSE" {
		t.Errorf("Status = %q, want FALSE (UNKNOWN is not TRUE)", cond.Status)
	}
}

func TestComputeReady_AbsentFileRefConditionDoesNotBlockReady(t *testing.T) {
	parentResolved := status.Condition{Type: "ParentResolved", Status: "TRUE"}
	acyclic := status.Condition{Type: "Acyclic", Status: "TRUE"}

	cond := computeReady(parentResolved, acyclic, nil)
	if cond.Status != "TRUE" {
		t.Errorf("Status = %q, want TRUE (no file-ref condition present)", cond.Status)
	}
}

// ── Cycle-participant freeze + recovery (FR-008) ────────────────────────────

func TestReconcile_CycleParticipant_PathDepthFrozen_AcyclicFalse(t *testing.T) {
	// a -> b -> a (two-node cycle)
	prevResolved, _ := json.Marshal(ResolvedCategoryTaxonomy{Depth: 3, Path: []string{"x", "y", "z", "a"}})
	a := CategoryTaxonomy{Namespace: "acme", Name: "a", ParentRefName: "b", ResourceVersion: "1"}
	a.Status = status.ResourceStatus{ResourceVersion: "1", Resolved: prevResolved}
	b := CategoryTaxonomy{Namespace: "acme", Name: "b", ParentRefName: "a", ResourceVersion: "1"}
	c := seedCache(t, a, b)

	sc := &fakeStatusClient{}
	r := NewReconciler(c, sc, noProducts, nil)

	r.Reconcile(context.Background(), key("acme", "a"))

	if sc.callCount() != 1 {
		t.Fatalf("expected exactly 1 Apply call, got %d", sc.callCount())
	}
	var resolved ResolvedCategoryTaxonomy
	if err := json.Unmarshal(sc.calls[0].Resolved, &resolved); err != nil {
		t.Fatalf("unmarshal patch.Resolved: %v", err)
	}
	if resolved.Depth != 3 {
		t.Errorf("Depth = %d, want 3 (frozen at prior value)", resolved.Depth)
	}
	wantPath := []string{"x", "y", "z", "a"}
	for i, p := range wantPath {
		if i >= len(resolved.Path) || resolved.Path[i] != p {
			t.Fatalf("Path = %v, want %v (frozen at prior value)", resolved.Path, wantPath)
		}
	}

	var acyclicFound bool
	for _, cond := range sc.calls[0].Conditions {
		if cond.Type == "Acyclic" {
			acyclicFound = true
			if cond.Status != "FALSE" {
				t.Errorf("Acyclic.Status = %q, want FALSE", cond.Status)
			}
		}
	}
	if !acyclicFound {
		t.Error("expected an Acyclic condition in the patch")
	}
}

func TestReconcile_CycleParticipant_FirstReconcile_PathNeverNull(t *testing.T) {
	// a -> b -> a, neither has ever been reconciled before (no prior
	// resolved status at all) — Path must still be a valid non-null
	// [String!]! slice, not the Go zero value (nil), which would marshal to
	// JSON null and fail the GraphQL schema's non-null Path validation.
	a := CategoryTaxonomy{Namespace: "acme", Name: "a", ParentRefName: "b", ResourceVersion: "1"}
	b := CategoryTaxonomy{Namespace: "acme", Name: "b", ParentRefName: "a", ResourceVersion: "1"}
	c := seedCache(t, a, b)

	sc := &fakeStatusClient{}
	r := NewReconciler(c, sc, noProducts, nil)

	r.Reconcile(context.Background(), key("acme", "a"))

	if sc.callCount() != 1 {
		t.Fatalf("expected exactly 1 Apply call, got %d", sc.callCount())
	}
	var resolved ResolvedCategoryTaxonomy
	if err := json.Unmarshal(sc.calls[0].Resolved, &resolved); err != nil {
		t.Fatalf("unmarshal patch.Resolved: %v", err)
	}
	if resolved.Path == nil {
		t.Fatal("Path must not be nil on a cycle participant's first-ever reconcile (would marshal to JSON null, rejected by the non-null GraphQL schema)")
	}
}

func TestReconcile_CycleBroken_AcyclicTrueAndRecomputes(t *testing.T) {
	// "a" no longer references "b" -- cycle is broken; "b" is now a root.
	prevResolved, _ := json.Marshal(ResolvedCategoryTaxonomy{Depth: 3, Path: []string{"x", "y", "z", "b"}})
	b := CategoryTaxonomy{Namespace: "acme", Name: "b", ResourceVersion: "2"}
	b.Status = status.ResourceStatus{ResourceVersion: "1", Resolved: prevResolved}
	c := seedCache(t, b)

	sc := &fakeStatusClient{}
	r := NewReconciler(c, sc, noProducts, nil)

	r.Reconcile(context.Background(), key("acme", "b"))

	if sc.callCount() != 1 {
		t.Fatalf("expected exactly 1 Apply call, got %d", sc.callCount())
	}
	var resolved ResolvedCategoryTaxonomy
	if err := json.Unmarshal(sc.calls[0].Resolved, &resolved); err != nil {
		t.Fatalf("unmarshal patch.Resolved: %v", err)
	}
	if resolved.Depth != 0 {
		t.Errorf("Depth = %d, want 0 (recomputed as root)", resolved.Depth)
	}

	for _, cond := range sc.calls[0].Conditions {
		if cond.Type == "Acyclic" && cond.Status != "TRUE" {
			t.Errorf("Acyclic.Status = %q, want TRUE", cond.Status)
		}
	}
}
