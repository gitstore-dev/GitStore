// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

// CategoryTaxonomyValidatingPolicy implements admission.ValidatingAdmissionPolicy
// for Kind == "CategoryTaxonomy". It emits ParentResolved and Acyclic conditions.
// It never returns Denied; all check results surface as False conditions.
type CategoryTaxonomyValidatingPolicy struct {
	store datastore.Datastore
	log   *zap.Logger
}

// NewCategoryTaxonomyValidatingPolicy constructs the policy.
// store may be nil; out-of-push parent lookups are skipped when nil.
func NewCategoryTaxonomyValidatingPolicy(store datastore.Datastore, log *zap.Logger) *CategoryTaxonomyValidatingPolicy {
	return &CategoryTaxonomyValidatingPolicy{store: store, log: log}
}

func (p *CategoryTaxonomyValidatingPolicy) Name() string { return "CategoryTaxonomyValidatingPolicy" }

// Validate checks the CategoryTaxonomy resource and returns Allowed with conditions.
// Uses req.PushSet to resolve in-push parents and detect intra-push cycles.
func (p *CategoryTaxonomyValidatingPolicy) Validate(ctx context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	if req.Kind != "CategoryTaxonomy" {
		return admission.DecisionAllow()
	}
	resource, ok := req.Object.(*catalog.CategoryTaxonomyResource)
	if !ok || resource == nil {
		return admission.DecisionAllow()
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = req.Namespace
	}
	name := resource.Metadata.Name

	// Build a parent map from the PushSet for cycle and parent-resolution detection.
	pushParentMap := buildPushParentMap(req.PushSet)
	// Include the current resource itself.
	selfParent := ""
	if resource.Spec.ParentRef != nil {
		selfParent = resource.Spec.ParentRef.Name
	}
	pushParentMap[name] = selfParent

	// Detect cycles in the full push set.
	cycleMembers := DetectCycles(pushParentMap)
	inCycle := cycleMembers[name]

	// Resolve parent.
	parentResolved := true // root (no parentRef) is always resolved
	if resource.Spec.ParentRef != nil && resource.Spec.ParentRef.Name != "" {
		parentName := resource.Spec.ParentRef.Name
		parentResolved = false
		// A self-loop (parentRef == own name) is always unresolvable.
		if parentName == name {
			// parentResolved stays false; inCycle already captures the self-loop
		} else if _, inPush := pushParentMap[parentName]; inPush {
			// Check in-push set first.
			parentResolved = true
		} else if p.store != nil {
			// Fall back to datastore lookup.
			if parent, err := p.store.GetCategoryTaxonomyByName(ctx, namespace, parentName); err == nil && parent != nil {
				parentResolved = true
			}
		}
	}

	return admission.DecisionAllow(
		admission.AdmissionCondition{
			Type:   string(catalog.ConditionParentResolved),
			Status: parentResolved,
		},
		admission.AdmissionCondition{
			Type:   string(catalog.ConditionAcyclic),
			Status: !inCycle,
		},
	)
}

// buildPushParentMap extracts a name→parentName map from a PushSet slice,
// including only CategoryTaxonomy resources.
func buildPushParentMap(pushSet []admission.AdmissionRequest) map[string]string {
	m := make(map[string]string, len(pushSet))
	for _, r := range pushSet {
		if r.Kind != "CategoryTaxonomy" {
			continue
		}
		cat, ok := r.Object.(*catalog.CategoryTaxonomyResource)
		if !ok || cat == nil {
			continue
		}
		parent := ""
		if cat.Spec.ParentRef != nil {
			parent = cat.Spec.ParentRef.Name
		}
		m[cat.Metadata.Name] = parent
	}
	return m
}

// DetectCycles returns the set of category names involved in intra-push cycles.
// Uses DFS with three-color marking (0=white, 1=gray, 2=black). When a back
// edge is found, every node on the current gray path is marked as in-cycle —
// not just the node that triggered the back edge — so chains like A→B→C→B
// correctly flag both B and C but not A.
func DetectCycles(parentMap map[string]string) map[string]bool {
	inCycle := make(map[string]bool)
	color := make(map[string]int, len(parentMap))
	var grayStack []string
	var visit func(name string)
	visit = func(name string) {
		if color[name] == 2 {
			return
		}
		if color[name] == 1 {
			for i, n := range grayStack {
				if n == name {
					for _, m := range grayStack[i:] {
						inCycle[m] = true
					}
					break
				}
			}
			return
		}
		parent, inPush := parentMap[name]
		if !inPush || parent == "" {
			color[name] = 2
			return
		}
		color[name] = 1
		grayStack = append(grayStack, name)
		visit(parent)
		grayStack = grayStack[:len(grayStack)-1]
		color[name] = 2
	}
	for name := range parentMap {
		visit(name)
	}
	return inCycle
}

// TopoSortCategories returns category names from parentMap in topological order
// (parents before children). Cycle members are placed immediately before any
// non-cycle node that references them; cycle members with no non-cycle dependents
// are appended at the end.
func TopoSortCategories(parentMap map[string]string, cycleMembers map[string]bool) []string {
	visited := make(map[string]bool, len(parentMap))
	order := make([]string, 0, len(parentMap))
	var visit func(name string)
	visit = func(name string) {
		if visited[name] || cycleMembers[name] {
			return
		}
		visited[name] = true
		parent := parentMap[name]
		if parent != "" {
			if _, inPush := parentMap[parent]; inPush {
				if cycleMembers[parent] && !visited[parent] {
					// Cycle-member parent: record it before this child so
					// inPushAncestorPaths is populated when this node is processed.
					visited[parent] = true
					order = append(order, parent)
				}
				visit(parent)
			}
		}
		order = append(order, name)
	}
	for name := range parentMap {
		visit(name)
	}
	// Append any cycle members not yet added as a parent of a non-cycle node.
	for name := range parentMap {
		if cycleMembers[name] && !visited[name] {
			order = append(order, name)
		}
	}
	return order
}
