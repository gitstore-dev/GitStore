// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"errors"
)

// Sentinel errors returned by all backends.
var (
	ErrNotFound        = errors.New("datastore: not found")
	ErrAlreadyExists   = errors.New("datastore: already exists")
	ErrInvalidArgument = errors.New("datastore: invalid argument")
)

// ProductFilter scopes ListProducts. All fields are optional.
type ProductFilter struct {
	CategoryID string // empty = no filter
	After      string // cursor for forward pagination
	First      int    // 0 = no limit
}

// Datastore is the persistence contract for all backends.
//
// All implementations must be safe for concurrent use.
// The abstraction never retries or reconnects internally; storage errors are
// propagated immediately to callers (FR-007a).
type Datastore interface {
	// Product operations
	CreateProduct(ctx context.Context, p *Product) error
	GetProduct(ctx context.Context, id string) (*Product, error)
	GetProductBySKU(ctx context.Context, sku string) (*Product, error)
	ListProducts(ctx context.Context, filter ProductFilter) ([]*Product, error)
	UpdateProduct(ctx context.Context, p *Product) error
	DeleteProduct(ctx context.Context, id string) error

	// Category operations
	CreateCategory(ctx context.Context, c *Category) error
	GetCategory(ctx context.Context, id string) (*Category, error)
	GetCategoryBySlug(ctx context.Context, slug string) (*Category, error)
	ListCategories(ctx context.Context) ([]*Category, error)
	UpdateCategory(ctx context.Context, c *Category) error
	DeleteCategory(ctx context.Context, id string) error

	// Collection operations
	CreateCollection(ctx context.Context, c *Collection) error
	GetCollection(ctx context.Context, id string) (*Collection, error)
	GetCollectionBySlug(ctx context.Context, slug string) (*Collection, error)
	ListCollections(ctx context.Context) ([]*Collection, error)
	UpdateCollection(ctx context.Context, c *Collection) error
	DeleteCollection(ctx context.Context, id string) error

	// Namespace operations
	CreateNamespace(ctx context.Context, ns *Namespace) error
	GetNamespace(ctx context.Context, id string) (*Namespace, error)
	GetNamespaceByIdentifier(ctx context.Context, identifier string) (*Namespace, error)
	ListNamespaces(ctx context.Context) ([]*Namespace, error)
	DeleteNamespace(ctx context.Context, id string) error

	// Lifecycle
	Close() error
}
