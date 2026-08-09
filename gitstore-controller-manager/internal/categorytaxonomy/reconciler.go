// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package categorytaxonomy implements the CategoryTaxonomy reconciler:
// hierarchy (depth/path/childCount/productCount) computation, cycle
// detection, and the ParentResolved/Acyclic/Ready/required-file-reference
// conditions, writing back through the status-subresource contract shipped
// by spec 040.
package categorytaxonomy

import "github.com/gitstore-dev/gitstore/controller-manager/internal/status"

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
	Status        status.ResourceStatus
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
