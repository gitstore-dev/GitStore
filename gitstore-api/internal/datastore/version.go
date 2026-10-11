// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"fmt"
	"math/big"
)

// ValidateCatalogUpdateVersion binds a desired-state write to the snapshot
// authorized by the caller. An empty version is a literal initial version, never
// a wildcard. Every successful replacement must advance the token exactly once.
func ValidateCatalogUpdateVersion(current, expected, desired string) error {
	if current != expected {
		return fmt.Errorf("%w: resource version changed", ErrConflict)
	}
	if current == "" {
		current = "0"
	}
	before, beforeOK := new(big.Int).SetString(current, 10)
	after, afterOK := new(big.Int).SetString(desired, 10)
	if !beforeOK || !afterOK || before.Sign() < 0 || after.Sign() < 1 ||
		after.Cmp(new(big.Int).Add(before, big.NewInt(1))) != 0 {
		return fmt.Errorf("%w: resource version must advance exactly once", ErrConflict)
	}
	return nil
}

// AdvanceCollectionSystemVersion advances the optimistic-concurrency token.
func AdvanceCollectionSystemVersion(collection *Collection) {
	if collection == nil {
		return
	}
	collection.ResourceVersion = nextResourceVersion(collection.ResourceVersion)
}

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

// AdvanceServiceAccountSystemVersion advances a ServiceAccount's
// ResourceVersion for a mutation (e.g. SetServiceAccountDisabled) that does
// not touch PublicKeys/Generation and so does not route through
// ApplyServiceAccountKeyUpdate.
func AdvanceServiceAccountSystemVersion(sa *ServiceAccount) {
	if sa == nil {
		return
	}
	sa.ResourceVersion = nextResourceVersion(sa.ResourceVersion)
}
