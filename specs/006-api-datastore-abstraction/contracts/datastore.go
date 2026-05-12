// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package datastore defines the persistence contract for gitstore-api.
//
// This file is the canonical specification of the Datastore interface.
// It is not compiled as part of the service — it lives in the specs directory
// as a design artifact. The authoritative implementation lives at:
//   gitstore-api/internal/datastore/datastore.go
//
// All backends (memdb, scylla, future) MUST implement this interface exactly.
// Callers MUST NOT import backend-specific packages.

package datastore

import (
	"context"
	"errors"
	"time"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrNotFound is returned when a requested entity does not exist.
	ErrNotFound = errors.New("datastore: not found")

	// ErrAlreadyExists is returned when an entity with the same primary key
	// or unique constraint already exists.
	ErrAlreadyExists = errors.New("datastore: already exists")

	// ErrInvalidArgument is returned for malformed inputs (empty ID, nil
	// entity, etc.).
	ErrInvalidArgument = errors.New("datastore: invalid argument")
)

// ─── Domain types ─────────────────────────────────────────────────────────────
// These are re-exported from gitstore-api/internal/catalog for clarity.
// The real implementation uses catalog.Product, catalog.Category, etc.

// Product represents a sellable item in the catalog.
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
// Computed hierarchy fields (Parent, Children, Path, Depth) are NOT stored
// by the datastore; they are built by catalog.BuildCategoryHierarchy().
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

// ─── Filter types ─────────────────────────────────────────────────────────────

// ProductFilter scopes ListProducts results. All fields are optional.
// When CategoryID is non-empty only products belonging to that category
// are returned. Pagination fields follow the existing cursor-based scheme
// used in the GraphQL layer.
type ProductFilter struct {
	CategoryID string // optional; empty = no filter
	After      string // cursor for forward pagination (optional)
	First      int    // maximum items to return; 0 = no limit
}

// ─── Datastore interface ──────────────────────────────────────────────────────

// Datastore is the persistence contract that all backends must satisfy.
//
// Error contract:
//   - ErrNotFound:       entity with the given key does not exist.
//   - ErrAlreadyExists:  unique-key conflict on Create operations.
//   - ErrInvalidArgument: caller supplied an empty ID or nil entity.
//   - Any other error:   unrecoverable backend failure; propagated as-is.
//
// The abstraction MUST NOT retry or reconnect internally (FR-007a).
// Transient backend errors are always propagated immediately to the caller.
//
// Implementations MUST be safe for concurrent use from multiple goroutines.
type Datastore interface {
	// ── Product ──────────────────────────────────────────────────────────────

	// CreateProduct stores a new product. Returns ErrAlreadyExists if a
	// product with the same ID or SKU already exists.
	CreateProduct(ctx context.Context, p *Product) error

	// GetProduct retrieves a product by its UUID. Returns ErrNotFound if
	// the product does not exist.
	GetProduct(ctx context.Context, id string) (*Product, error)

	// GetProductBySKU retrieves a product by its SKU. Returns ErrNotFound
	// if no product has the given SKU.
	GetProductBySKU(ctx context.Context, sku string) (*Product, error)

	// ListProducts returns products matching filter. An empty filter returns
	// all products. Order is implementation-defined but MUST be stable for
	// the same filter within a single backend session.
	ListProducts(ctx context.Context, filter ProductFilter) ([]*Product, error)

	// UpdateProduct replaces the stored product with the provided value.
	// Returns ErrNotFound if the product does not exist.
	UpdateProduct(ctx context.Context, p *Product) error

	// DeleteProduct removes a product by its UUID. Returns ErrNotFound if
	// the product does not exist.
	DeleteProduct(ctx context.Context, id string) error

	// ── Category ─────────────────────────────────────────────────────────────

	// CreateCategory stores a new category. Returns ErrAlreadyExists if a
	// category with the same ID or slug already exists.
	CreateCategory(ctx context.Context, c *Category) error

	// GetCategory retrieves a category by its UUID. Returns ErrNotFound.
	GetCategory(ctx context.Context, id string) (*Category, error)

	// GetCategoryBySlug retrieves a category by its URL slug. Returns ErrNotFound.
	GetCategoryBySlug(ctx context.Context, slug string) (*Category, error)

	// ListCategories returns all categories. Order is implementation-defined.
	ListCategories(ctx context.Context) ([]*Category, error)

	// UpdateCategory replaces the stored category. Returns ErrNotFound.
	UpdateCategory(ctx context.Context, c *Category) error

	// DeleteCategory removes a category by UUID. Returns ErrNotFound.
	DeleteCategory(ctx context.Context, id string) error

	// ── Collection ───────────────────────────────────────────────────────────

	// CreateCollection stores a new collection. Returns ErrAlreadyExists.
	CreateCollection(ctx context.Context, c *Collection) error

	// GetCollection retrieves a collection by UUID. Returns ErrNotFound.
	GetCollection(ctx context.Context, id string) (*Collection, error)

	// GetCollectionBySlug retrieves a collection by URL slug. Returns ErrNotFound.
	GetCollectionBySlug(ctx context.Context, slug string) (*Collection, error)

	// ListCollections returns all collections. Order is implementation-defined.
	ListCollections(ctx context.Context) ([]*Collection, error)

	// UpdateCollection replaces the stored collection. Returns ErrNotFound.
	UpdateCollection(ctx context.Context, c *Collection) error

	// DeleteCollection removes a collection by UUID. Returns ErrNotFound.
	DeleteCollection(ctx context.Context, id string) error

	// ── Lifecycle ────────────────────────────────────────────────────────────

	// Close releases all resources held by the backend (connections, memory,
	// file handles). After Close returns, no other method may be called.
	Close() error
}

// ─── Factory contract ─────────────────────────────────────────────────────────

// BackendType is the configuration discriminator for the active backend.
type BackendType string

const (
	BackendMemdb  BackendType = "memdb"
	BackendScylla BackendType = "scylla"
)

// Config carries the configuration required by factory.NewDatastore.
// This mirrors DatastoreConfig + ScyllaConfig in config.go exactly.
type Config struct {
	Backend BackendType
	Scylla  ScyllaConfig
}

// ScyllaConfig carries ScyllaDB connection parameters.
// Credentials and TLS are optional (FR-013).
type ScyllaConfig struct {
	Hosts    []string // e.g. ["localhost:9042"]
	Keyspace string   // e.g. "gitstore"
	Username string   // optional
	Password string   // optional
	TLS      bool     // optional; defaults to false
}
