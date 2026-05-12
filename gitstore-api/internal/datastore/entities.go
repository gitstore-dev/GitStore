// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "time"

// Product represents a sellable item in the catalogue.
type Product struct {
	ID                string
	SKU               string
	Title             string
	Price             float64
	Currency          string
	InventoryStatus   string
	InventoryQuantity *int
	CategoryID        string
	CollectionIDs     []string
	Images            []string
	Metadata          map[string]any
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Body              string
}

// Category represents a hierarchical classification.
// Computed fields (Parent, Children, Path, Depth) are not stored;
// they are built by the catalog layer after loading.
type Category struct {
	ID           string
	Name         string
	Slug         string
	ParentID     *string
	DisplayOrder int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Body         string
}

// Collection represents a flat grouping of products.
type Collection struct {
	ID           string
	Name         string
	Slug         string
	DisplayOrder int
	ProductIDs   []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Body         string
}
