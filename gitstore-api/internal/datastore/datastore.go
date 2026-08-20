// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
)

// Sentinel errors returned by all backends.
var (
	ErrNotFound        = errors.New("datastore: not found")
	ErrAlreadyExists   = errors.New("datastore: already exists")
	ErrInvalidArgument = errors.New("datastore: invalid argument")
	// ErrConflict is returned by status-only partial-merge writes when the
	// caller's resourceVersion precondition does not match the resource's
	// current value (optimistic concurrency, spec 040 FR-009).
	ErrConflict = errors.New("datastore: resourceVersion conflict")
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

// CategoryTaxonomyStatusPatch is a partial-merge update to a
// CategoryTaxonomy's .status sub-resource (spec 040 FR-008). Only non-nil
// fields are applied; ResourceVersion is always required and checked
// against the resource's current value before any field is written.
type CategoryTaxonomyStatusPatch struct {
	ResourceVersion     string
	ObservedGeneration  *int64
	LastAppliedRevision *string
	Conditions          []catalog.Condition // nil = unchanged; non-nil = full replacement
	Resolved            *catalog.ResolvedCategoryTaxonomy
}

type NamespaceStatusPatch struct {
	ResourceVersion     string
	ObservedGeneration  *int64
	LastAppliedRevision *string
	Conditions          []catalog.Condition
}

func ApplyNamespaceStatusPatch(namespace *Namespace, patch NamespaceStatusPatch) error {
	if patch.ResourceVersion != namespace.ResourceVersion {
		return ErrConflict
	}
	var status catalog.NamespaceStatus
	if len(namespace.Status) > 0 {
		if err := json.Unmarshal(namespace.Status, &status); err != nil {
			return fmt.Errorf("datastore: unmarshal existing Namespace status: %w", err)
		}
	}
	if patch.ObservedGeneration != nil {
		status.ObservedGeneration = *patch.ObservedGeneration
	}
	if patch.LastAppliedRevision != nil {
		status.LastAppliedRevision = *patch.LastAppliedRevision
	}
	if patch.Conditions != nil {
		status.Conditions = patch.Conditions
	}
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("datastore: marshal updated Namespace status: %w", err)
	}
	namespace.Status = data
	AdvanceNamespaceSystemVersion(namespace)
	return nil
}

// ApplyCategoryTaxonomyStatusPatch merges patch into c's status field
// in place: it checks the resourceVersion precondition (returning
// ErrConflict on mismatch), applies only non-nil patch fields to the
// existing status JSON, re-marshals it back into c.Status, and advances
// c.ResourceVersion. Shared by every backend so the merge/precondition
// semantics are identical across memdb and scylla (spec 040 FR-008,
// FR-009; research.md R6).
func ApplyCategoryTaxonomyStatusPatch(c *CategoryTaxonomy, patch CategoryTaxonomyStatusPatch) error {
	if patch.ResourceVersion != c.ResourceVersion {
		return ErrConflict
	}

	var status catalog.CategoryTaxonomyStatus
	if len(c.Status) > 0 {
		if err := json.Unmarshal(c.Status, &status); err != nil {
			return fmt.Errorf("datastore: unmarshal existing status: %w", err)
		}
	}

	if patch.ObservedGeneration != nil {
		status.ObservedGeneration = *patch.ObservedGeneration
	}
	if patch.LastAppliedRevision != nil {
		status.LastAppliedRevision = *patch.LastAppliedRevision
	}
	if patch.Conditions != nil {
		status.Conditions = patch.Conditions
	}
	if patch.Resolved != nil {
		status.Resolved = patch.Resolved
	}

	b, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("datastore: marshal updated status: %w", err)
	}
	c.Status = b
	c.ResourceVersion = nextResourceVersion(c.ResourceVersion)
	return nil
}

// nextResourceVersion advances an opaque numeric-string resourceVersion.
// Mirrors gitstore-api/internal/cataloggrpc/server.go's identically-named
// admission-time helper (kept separate to avoid an import cycle between
// datastore and cataloggrpc).
func nextResourceVersion(current string) string {
	n, err := strconv.ParseInt(current, 10, 64)
	if err != nil || n < 1 {
		return "1"
	}
	return strconv.FormatInt(n+1, 10)
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
	GetCategoryTaxonomy(ctx context.Context, uid string) (*CategoryTaxonomy, error)
	GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*CategoryTaxonomy, error)
	ListCategoryTaxonomies(ctx context.Context, namespace string, page PageParams) (*PageResult[CategoryTaxonomy], error)
	UpdateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error
	DeleteCategoryTaxonomy(ctx context.Context, uid string) error
	// UpdateCategoryTaxonomyStatus applies a partial-merge status-only
	// write, distinct from UpdateCategoryTaxonomy (which replaces the full
	// object). Returns the updated CategoryTaxonomy on success, ErrConflict
	// if patch.ResourceVersion does not match the current value, or
	// ErrNotFound if no resource matches namespace/name (spec 040 FR-009,
	// FR-010, FR-012; research.md R6).
	UpdateCategoryTaxonomyStatus(ctx context.Context, namespace, name string, patch CategoryTaxonomyStatusPatch) (*CategoryTaxonomy, error)

	// ProductVariant operations
	CreateProductVariant(ctx context.Context, v *ProductVariant) error
	GetProductVariant(ctx context.Context, uid string) (*ProductVariant, error)
	GetProductVariantByName(ctx context.Context, namespace, name string) (*ProductVariant, error)
	GetProductVariantBySKU(ctx context.Context, namespace, sku string) (*ProductVariant, error)
	ListProductVariants(ctx context.Context, namespace string, page PageParams) (*PageResult[ProductVariant], error)
	ListProductVariantsByProductRef(ctx context.Context, namespace, productRefName string) ([]*ProductVariant, error)
	UpdateProductVariant(ctx context.Context, v *ProductVariant) error
	DeleteProductVariant(ctx context.Context, uid string) error

	// Collection operations
	CreateCollection(ctx context.Context, c *Collection) error
	GetCollection(ctx context.Context, uid string) (*Collection, error)
	GetCollectionByName(ctx context.Context, namespace, name string) (*Collection, error)
	ListCollections(ctx context.Context, namespace string, page PageParams) (*PageResult[Collection], error)
	UpdateCollection(ctx context.Context, c *Collection) error
	DeleteCollection(ctx context.Context, uid string) error
	ListProductsByLabelSelector(ctx context.Context, namespace string, selector catalog.LabelSelector) ([]*Product, error)

	// Namespace operations
	CreateNamespace(ctx context.Context, ns *Namespace) error
	GetNamespace(ctx context.Context, id string) (*Namespace, error)
	GetNamespaceByName(ctx context.Context, name string) (*Namespace, error)
	ListNamespaces(ctx context.Context, page PageParams) (*PageResult[Namespace], error)
	UpdateNamespace(ctx context.Context, ns *Namespace, expectedResourceVersion string) error
	DeleteNamespace(ctx context.Context, id string) error
	DeleteNamespaceWithResourceVersion(ctx context.Context, id, expectedResourceVersion string) error
	// HasRepositories reports whether at least one Repository record
	// currently has NamespaceID == namespaceID. Used by DeleteNamespace to
	// enforce FR-001 (reject deletion while repositories remain). Must be
	// an existence check (LIMIT 1 / equivalent), not a full count.
	HasRepositories(ctx context.Context, namespaceID string) (bool, error)

	// Repository operations
	CreateRepository(ctx context.Context, r *Repository) error
	GetRepository(ctx context.Context, id string) (*Repository, error)
	ListRepositoriesByNamespace(ctx context.Context, namespaceID string, page PageParams) (*PageResult[Repository], error)
	// UpdateRepository replaces a repository only when its persisted
	// resourceVersion matches expectedResourceVersion. It returns ErrConflict
	// when another writer has advanced the record since it was read.
	UpdateRepository(ctx context.Context, r *Repository, expectedResourceVersion string) error
	DeleteRepository(ctx context.Context, id string) error
	// HasCatalogResources reports whether at least one Product,
	// ProductVariant, CategoryTaxonomy, or Collection record currently has
	// RepositoryID == repoID. Used by DeleteRepository to enforce FR-004
	// (reject deletion while catalog resources remain). Must be an
	// existence check (LIMIT 1 / equivalent), not a full count.
	HasCatalogResources(ctx context.Context, repoID string) (bool, error)

	// NamespaceMapping operations (lookup contract)
	CreateNamespaceMapping(ctx context.Context, m *NamespaceMapping) error
	LookupRepository(ctx context.Context, namespaceID, name string) (*NamespaceMapping, error)
	LookupNamespaceByRepoID(ctx context.Context, repoID string) (*NamespaceMapping, error)
	RenameRepository(ctx context.Context, namespaceID, oldName, newName string) error
	TransferRepository(ctx context.Context, repoID, fromNamespaceID, toNamespaceID string) error
	DeleteNamespaceMapping(ctx context.Context, namespaceID, name string) error

	// Close lifecycle function
	Close() error
}
