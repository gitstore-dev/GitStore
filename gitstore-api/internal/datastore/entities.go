// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "time"

// NamespaceTier is the enumeration of allowed namespace tiers.
type NamespaceTier string

const (
	NamespaceTierUser         NamespaceTier = "user"
	NamespaceTierOrganisation NamespaceTier = "organisation"
	NamespaceTierEnterprise   NamespaceTier = "enterprise"
)

// Namespace is the primary isolation boundary for repositories.
type Namespace struct {
	ID                 string
	Identifier         string
	DisplayName        string
	Tier               NamespaceTier
	ParentEnterpriseID *string
	CreatedAt          time.Time
	CreatedBy          string
	UpdatedAt          time.Time
	UpdatedBy          string
}

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

// Repository represents a git repository with a stable internal identity.
// The physical storage path is derived from ID using the fanout formula and is never stored.
type Repository struct {
	ID            string    // UUIDv7 stable identifier (repo_id)
	NamespaceID   string    // UUIDv7 of the owning namespace
	Name          string    // Human-readable name within the namespace (mutable on rename)
	DefaultBranch string    // e.g. "main"
	StorageClass  string    // Storage tier tag; default "default"
	CreatedAt     time.Time
	CreatedBy     string
	UpdatedAt     time.Time
	UpdatedBy     string
}

// NamespaceMapping is the join record binding (NamespaceID, Name) → RepoID.
// Primary lookup: (NamespaceID, Name) → RepoID.
// Secondary lookup: RepoID → (NamespaceID, Name) for reverse resolution.
type NamespaceMapping struct {
	NamespaceID string // UUIDv7 of the owning namespace (partition key)
	Name        string // Repository name within the namespace (clustering key)
	RepoID      string // Target repo_id (UUIDv7)
}
