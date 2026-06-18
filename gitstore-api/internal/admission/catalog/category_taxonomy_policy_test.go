// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	admcatalog "github.com/gitstore-dev/gitstore/api/internal/admission/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- DetectCycles ---

func TestDetectCycles_NoCycles(t *testing.T) {
	pm := map[string]string{"a": "", "b": "a", "c": "b"}
	result := admcatalog.DetectCycles(pm)
	for name := range pm {
		assert.False(t, result[name], "node %s should not be in cycle", name)
	}
}

func TestDetectCycles_DirectCycle(t *testing.T) {
	pm := map[string]string{"a": "b", "b": "a"}
	result := admcatalog.DetectCycles(pm)
	assert.True(t, result["a"], "a must be in cycle")
	assert.True(t, result["b"], "b must be in cycle")
}

func TestDetectCycles_TailCycle_ANotInCycle(t *testing.T) {
	// A→B→C→B: A is a mere ancestor; B and C form the cycle
	pm := map[string]string{"a": "b", "b": "c", "c": "b"}
	result := admcatalog.DetectCycles(pm)
	assert.False(t, result["a"], "a is not part of the cycle")
	assert.True(t, result["b"], "b is in the cycle")
	assert.True(t, result["c"], "c is in the cycle")
}

// --- TopoSortCategories ---

func TestTopoSortCategories_RootsFirst(t *testing.T) {
	pm := map[string]string{"child": "root", "root": ""}
	cycles := admcatalog.DetectCycles(pm)
	order := admcatalog.TopoSortCategories(pm, cycles)
	require.Len(t, order, 2)
	assert.Equal(t, "root", order[0])
	assert.Equal(t, "child", order[1])
}

func TestTopoSortCategories_CycleMembersAtEnd(t *testing.T) {
	pm := map[string]string{"a": "b", "b": "a", "root": ""}
	cycles := admcatalog.DetectCycles(pm)
	order := admcatalog.TopoSortCategories(pm, cycles)
	require.Len(t, order, 3)
	assert.Equal(t, "root", order[0], "acyclic root must be first")
	// a and b are cycle members — they appear at the end (order between them is non-deterministic)
	assert.ElementsMatch(t, []string{"a", "b"}, order[1:])
}

// --- CategoryTaxonomyValidatingPolicy.Validate ---

func TestCategoryTaxonomyValidatingPolicy_Name(t *testing.T) {
	p := admcatalog.NewCategoryTaxonomyValidatingPolicy(nil, zap.NewNop())
	assert.Equal(t, "CategoryTaxonomyValidatingPolicy", p.Name())
}

func TestCategoryTaxonomyValidatingPolicy_WrongKind_ReturnsAllowed(t *testing.T) {
	p := admcatalog.NewCategoryTaxonomyValidatingPolicy(nil, zap.NewNop())
	req := admission.AdmissionRequest{Kind: "Product", Name: "x", Namespace: "ns"}
	d := p.Validate(context.Background(), req)
	_, ok := d.(admission.Allowed)
	assert.True(t, ok)
}

func TestCategoryTaxonomyValidatingPolicy_Root_BothConditionsTrue(t *testing.T) {
	p := admcatalog.NewCategoryTaxonomyValidatingPolicy(nil, zap.NewNop())
	cat := &catalog.CategoryTaxonomyResource{
		Kind:     "CategoryTaxonomy",
		Metadata: catalog.ObjectMeta{Name: "root", Namespace: "ns"},
		Spec:     catalog.CategoryTaxonomySpec{},
	}
	req := admission.AdmissionRequest{
		Kind:      "CategoryTaxonomy",
		Name:      "root",
		Namespace: "ns",
		Object:    cat,
		Operation: admission.OperationCreate,
		Trigger:   admission.TriggerGitPush,
	}
	d := p.Validate(context.Background(), req)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	conds := condMap(allowed.Conditions)
	assert.True(t, conds["ParentResolved"], "root has no parentRef so ParentResolved must be true")
	assert.True(t, conds["Acyclic"], "root is not in a cycle")
}

func TestCategoryTaxonomyValidatingPolicy_ChildWithInPushParent_Resolved(t *testing.T) {
	p := admcatalog.NewCategoryTaxonomyValidatingPolicy(nil, zap.NewNop())
	parent := &catalog.CategoryTaxonomyResource{
		Kind:     "CategoryTaxonomy",
		Metadata: catalog.ObjectMeta{Name: "parent", Namespace: "ns"},
		Spec:     catalog.CategoryTaxonomySpec{},
	}
	child := &catalog.CategoryTaxonomyResource{
		Kind:     "CategoryTaxonomy",
		Metadata: catalog.ObjectMeta{Name: "child", Namespace: "ns"},
		Spec: catalog.CategoryTaxonomySpec{
			ParentRef: &catalog.ObjectReference{Name: "parent"},
		},
	}
	parentReq := admission.AdmissionRequest{Kind: "CategoryTaxonomy", Name: "parent", Namespace: "ns", Object: parent}
	req := admission.AdmissionRequest{
		Kind:      "CategoryTaxonomy",
		Name:      "child",
		Namespace: "ns",
		Object:    child,
		Operation: admission.OperationCreate,
		PushSet:   []admission.AdmissionRequest{parentReq},
	}
	d := p.Validate(context.Background(), req)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	conds := condMap(allowed.Conditions)
	assert.True(t, conds["ParentResolved"])
	assert.True(t, conds["Acyclic"])
}

func TestCategoryTaxonomyValidatingPolicy_MissingParent_ParentResolvedFalse(t *testing.T) {
	p := admcatalog.NewCategoryTaxonomyValidatingPolicy(nil, zap.NewNop())
	child := &catalog.CategoryTaxonomyResource{
		Kind:     "CategoryTaxonomy",
		Metadata: catalog.ObjectMeta{Name: "child", Namespace: "ns"},
		Spec: catalog.CategoryTaxonomySpec{
			ParentRef: &catalog.ObjectReference{Name: "ghost-parent"},
		},
	}
	req := admission.AdmissionRequest{
		Kind:      "CategoryTaxonomy",
		Name:      "child",
		Namespace: "ns",
		Object:    child,
		Operation: admission.OperationCreate,
		PushSet:   nil,
	}
	d := p.Validate(context.Background(), req)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	conds := condMap(allowed.Conditions)
	assert.False(t, conds["ParentResolved"], "missing parent must yield ParentResolved=false")
}

func TestCategoryTaxonomyValidatingPolicy_CycleMember_AcyclicFalse(t *testing.T) {
	p := admcatalog.NewCategoryTaxonomyValidatingPolicy(nil, zap.NewNop())
	// A and B form a direct cycle in the same push
	catA := &catalog.CategoryTaxonomyResource{
		Kind:     "CategoryTaxonomy",
		Metadata: catalog.ObjectMeta{Name: "a", Namespace: "ns"},
		Spec:     catalog.CategoryTaxonomySpec{ParentRef: &catalog.ObjectReference{Name: "b"}},
	}
	catB := &catalog.CategoryTaxonomyResource{
		Kind:     "CategoryTaxonomy",
		Metadata: catalog.ObjectMeta{Name: "b", Namespace: "ns"},
		Spec:     catalog.CategoryTaxonomySpec{ParentRef: &catalog.ObjectReference{Name: "a"}},
	}
	reqB := admission.AdmissionRequest{Kind: "CategoryTaxonomy", Name: "b", Namespace: "ns", Object: catB}
	reqA := admission.AdmissionRequest{
		Kind:      "CategoryTaxonomy",
		Name:      "a",
		Namespace: "ns",
		Object:    catA,
		Operation: admission.OperationCreate,
		PushSet:   []admission.AdmissionRequest{reqB},
	}
	d := p.Validate(context.Background(), reqA)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok, "cycle members are admitted with Acyclic=false, not hard-denied")
	conds := condMap(allowed.Conditions)
	assert.False(t, conds["Acyclic"], "cycle member must have Acyclic=false")
}

// condMap extracts a type→status map from conditions for easy assertions.
func condMap(conds []admission.AdmissionCondition) map[string]bool {
	m := make(map[string]bool, len(conds))
	for _, c := range conds {
		m[c.Type] = c.Status
	}
	return m
}
