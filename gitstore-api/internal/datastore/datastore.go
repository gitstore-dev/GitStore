// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors returned by all backends.
var (
	ErrNotFound        = errors.New("datastore: not found")
	ErrAlreadyExists   = errors.New("datastore: already exists")
	ErrInvalidArgument = errors.New("datastore: invalid argument")
)

// DefaultPageSize is used when First/Last is zero.
const DefaultPageSize = 100

// PageParams defines keyset pagination parameters for any list operation.
type PageParams struct {
	First  int    // forward page size (0 = DefaultPageSize)
	After  string // opaque cursor for forward pagination (items older than this)
	Last   int    // backward page size (0 = unused)
	Before string // opaque cursor for backward pagination (items newer than this)
}

// Limit returns the effective page size.
func (p PageParams) Limit() int {
	if p.Last > 0 {
		return p.Last
	}
	if p.First > 0 {
		return p.First
	}
	return DefaultPageSize
}

// PageCursor is a decoded keyset cursor position.
type PageCursor struct {
	CreatedAt time.Time
	ID        string
}

// PageResult wraps a paginated result from the datastore.
type PageResult[T any] struct {
	Items       []*T
	HasNext     bool
	HasPrevious bool
	TotalCount  int32 // -1 if unknown/expensive to compute
}

// Datastore is the persistence contract for all backends.
//
// All implementations must be safe for concurrent use.
// The abstraction never retries or reconnects internally; storage errors are
// propagated immediately to callers (FR-007a).
type Datastore interface {
	// Product operations
	CreateProduct(ctx context.Context, p *Product) error
	GetProduct(ctx context.Context, uid string) (*Product, error)
	GetProductByName(ctx context.Context, namespace, name string) (*Product, error)
	ListProducts(ctx context.Context, namespace string, page PageParams) (*PageResult[Product], error)
	UpdateProduct(ctx context.Context, p *Product) error
	DeleteProduct(ctx context.Context, uid string) error

	// CategoryTaxonomy operations
	CreateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error
	GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*CategoryTaxonomy, error)
	ListCategoryTaxonomies(ctx context.Context, namespace string, page PageParams) (*PageResult[CategoryTaxonomy], error)
	UpdateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error

	// Collection operations
	CreateCollection(ctx context.Context, c *Collection) error
	GetCollection(ctx context.Context, id string) (*Collection, error)
	GetCollectionBySlug(ctx context.Context, slug string) (*Collection, error)
	ListCollections(ctx context.Context, page PageParams) (*PageResult[Collection], error)
	UpdateCollection(ctx context.Context, c *Collection) error
	DeleteCollection(ctx context.Context, id string) error

	// Namespace operations
	CreateNamespace(ctx context.Context, ns *Namespace) error
	GetNamespace(ctx context.Context, id string) (*Namespace, error)
	GetNamespaceByIdentifier(ctx context.Context, identifier string) (*Namespace, error)
	ListNamespaces(ctx context.Context, page PageParams) (*PageResult[Namespace], error)
	DeleteNamespace(ctx context.Context, id string) error

	// Repository operations
	CreateRepository(ctx context.Context, r *Repository) error
	GetRepository(ctx context.Context, id string) (*Repository, error)
	ListRepositoriesByNamespace(ctx context.Context, namespaceID string, page PageParams) (*PageResult[Repository], error)
	UpdateRepository(ctx context.Context, r *Repository) error
	DeleteRepository(ctx context.Context, id string) error

	// NamespaceMapping operations (lookup contract)
	CreateNamespaceMapping(ctx context.Context, m *NamespaceMapping) error
	LookupRepository(ctx context.Context, namespaceID, name string) (*NamespaceMapping, error)
	LookupNamespaceByRepoID(ctx context.Context, repoID string) (*NamespaceMapping, error)
	RenameRepository(ctx context.Context, namespaceID, oldName, newName string) error
	TransferRepository(ctx context.Context, repoID, fromNamespaceID, toNamespaceID string) error
	DeleteNamespaceMapping(ctx context.Context, namespaceID, name string) error

	// Lifecycle
	Close() error
}
