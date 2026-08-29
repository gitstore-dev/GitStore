// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "math/big"

// nextResourceVersion advances a positive decimal resourceVersion. Invalid,
// empty, and non-positive values normalize to the initial version.
func nextResourceVersion(current string) string {
	version, ok := new(big.Int).SetString(current, 10)
	if !ok || version.Sign() < 1 {
		return "1"
	}
	return version.Add(version, big.NewInt(1)).String()
}

// AdvanceCategoryTaxonomySystemVersion advances a category resourceVersion for
// API/controller-managed lifecycle metadata without changing generation.
func AdvanceCategoryTaxonomySystemVersion(category *CategoryTaxonomy) {
	if category == nil {
		return
	}
	category.ResourceVersion = nextResourceVersion(category.ResourceVersion)
}

// AdvanceProductSystemVersion advances Product lifecycle metadata without
// mutating authored spec or generation.
func AdvanceProductSystemVersion(product *Product) {
	if product == nil {
		return
	}
	product.ResourceVersion = nextResourceVersion(product.ResourceVersion)
}
