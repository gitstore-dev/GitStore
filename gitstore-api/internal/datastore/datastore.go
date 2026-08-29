// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// ErrNamespaceNotActive means a repository creation lost the durable
	// race with Namespace termination.
	ErrNamespaceNotActive = errors.New("datastore: namespace is not active")
	// ErrNamespaceNotEmpty means a Namespace deletion mark observed a
	// committed or in-flight repository creation.
	ErrNamespaceNotEmpty = errors.New("datastore: namespace is not empty")
)

// DefaultPageSize is used when First/Last is zero.
const DefaultPageSize = 100

// MaxOwnerDependentPageSize is the hard upper bound for reverse-owner pages.
// It prevents a caller from turning a controller continuation into an
// unbounded datastore read.
const MaxOwnerDependentPageSize = 100

// CategoryTaxonomyForegroundDeletionFinalizer holds a CategoryTaxonomy while
// its controller rechecks blocking dependents and decouples Products.
const CategoryTaxonomyForegroundDeletionFinalizer = "gitstore.dev/foreground-deletion"

// MarkCategoryTaxonomyTerminating writes the lifecycle condition that makes a
// foreground deletion visible before a controller observes the next watch
// event. It does not advance resourceVersion; callers include it in their
// atomic deletion-mark transition.
func MarkCategoryTaxonomyTerminating(category *CategoryTaxonomy, at time.Time) error {
	var status catalog.CategoryTaxonomyStatus
	if len(category.Status) > 0 {
		if err := json.Unmarshal(category.Status, &status); err != nil {
			return fmt.Errorf("datastore: unmarshal category taxonomy status: %w", err)
		}
	}
	condition := catalog.Condition{
		Type:               catalog.ConditionTerminating,
		Status:             catalog.ConditionTrue,
		ObservedGeneration: category.Generation,
		LastTransitionTime: at,
		Reason:             "DeletionRequested",
		Message:            "CategoryTaxonomy is awaiting foreground deletion completion.",
	}
	replaced := false
	for index := range status.Conditions {
		if status.Conditions[index].Type == catalog.ConditionTerminating {
			status.Conditions[index] = condition
			replaced = true
			break
		}
	}
	if !replaced {
		status.Conditions = append(status.Conditions, condition)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("datastore: marshal terminating category taxonomy status: %w", err)
	}
	category.Status = raw
	return nil
}

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

// OwnerReferenceScope restricts dependent lookups to the owner's repository.
// Dependents may be authored in another repository in the same namespace;
// their system-managed owner reference carries this owner scope.
type OwnerReferenceScope struct {
	Namespace    string
	RepositoryID string
}

// OwnerDependent is the minimal, stable record returned by a reverse
// owner-reference query. Its cursor is DependentUID, ordered ascending.
type OwnerDependent struct {
	DependentUID    string
	DependentKind   string
	Name            string
	ResourceVersion string
}

// OwnerDependentPage is a bounded, keyset-paginated dependent page.
type OwnerDependentPage struct {
	Items      []OwnerDependent
	NextCursor string
}

// OwnerReferenceStore is implemented by durable stores that maintain the
// reverse owner-reference projection. It intentionally remains additive to
// Datastore so older test stores and rolling-upgrade readers continue to work.
type OwnerReferenceStore interface {
	HasBlockingOwnerDependents(ctx context.Context, scope OwnerReferenceScope, ownerUID string) (bool, error)
	ListBlockingOwnerDependents(ctx context.Context, scope OwnerReferenceScope, ownerUID, after string, limit int) (OwnerDependentPage, error)
	ListNonBlockingProductOwnerDependents(ctx context.Context, scope OwnerReferenceScope, ownerUID, after string, limit int) (OwnerDependentPage, error)
}

// CategoryTaxonomyDeletionStore provides the durable foreground-deletion
// lifecycle used by both Git admission and the existing deleteCategory API.
type CategoryTaxonomyDeletionStore interface {
	MarkCategoryTaxonomyDeletion(ctx context.Context, namespace, name, expectedResourceVersion string, at time.Time) (*CategoryTaxonomy, error)
	CompleteCategoryTaxonomyDeletion(ctx context.Context, namespace, name, expectedResourceVersion string) (*CategoryTaxonomy, error)
}

// GlobalRepositoryLister is the optional contract for backends that provide a
// globally ordered Repository connection. Backends whose exact count requires
// scanning historical partitions return TotalCount = -1.
type GlobalRepositoryLister interface {
	ListRepositories(ctx context.Context, page PageParams) (*PageResult[Repository], error)
}

// RepositoryNamespaceLookup is the canonical reverse Repository mapping
// contract. LookupNamespaceByRepoID remains on Datastore during migration.
type RepositoryNamespaceLookup interface {
	LookupNamespaceByRepositoryID(ctx context.Context, repositoryID string) (*NamespaceMapping, error)
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

type FileStatusPatch struct {
	ResourceVersion     string
	ObservedGeneration  *int64
	LastAppliedRevision *string
	Conditions          []catalog.Condition
	Resolved            *catalog.ResolvedFileDefinition
}

func ApplyFileStatusPatch(f *File, patch FileStatusPatch) error {
	if patch.ResourceVersion != f.ResourceVersion {
		return ErrConflict
	}
	var status catalog.FileStatus
	if len(f.Status) > 0 {
		if err := json.Unmarshal(f.Status, &status); err != nil {
			return fmt.Errorf("datastore: unmarshal file status: %w", err)
		}
	}
	if patch.ObservedGeneration != nil {
		status.ObservedGeneration = *patch.ObservedGeneration
	}
	if patch.LastAppliedRevision != nil {
		status.LastAppliedRevision = *patch.LastAppliedRevision
	}
	if patch.Conditions != nil {
		if err := catalog.ValidateFileConditions(patch.Conditions); err != nil {
			return err
		}
		status.Conditions = patch.Conditions
	}
	if patch.Resolved != nil {
		status.Resolved = patch.Resolved
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	f.Status = raw
	f.ResourceVersion = nextResourceVersion(f.ResourceVersion)
	return nil
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

// Datastore is the persistence contract for all backends.
//
// All implementations must be safe for concurrent use.
// The abstraction never retries or reconnects internally; storage errors are
// propagated immediately to callers (FR-007a).
type Datastore interface {
	// File operations
	CreateFile(ctx context.Context, f *File) error
	GetFile(ctx context.Context, uid string) (*File, error)
	GetFileByName(ctx context.Context, namespace, name string) (*File, error)
	ListFiles(ctx context.Context, namespace string, page PageParams) (*PageResult[File], error)
	// UpdateFile replaces a File only when expectedResourceVersion still
	// matches the durable row, returning ErrConflict otherwise.
	UpdateFile(ctx context.Context, f *File, expectedResourceVersion string) error
	DeleteFile(ctx context.Context, uid string) error
	DeleteFileWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error

	// Product operations
	UpdateFileStatus(ctx context.Context, namespace, name string, patch FileStatusPatch) (*File, error)
	CreateProduct(ctx context.Context, p *Product) error
	GetProduct(ctx context.Context, uid string) (*Product, error)
	GetProductByName(ctx context.Context, namespace, name string) (*Product, error)
	ListProducts(ctx context.Context, namespace string, page PageParams) (*PageResult[Product], error)
	UpdateProduct(ctx context.Context, p *Product) error
	DeleteProduct(ctx context.Context, uid string) error
	DeleteProductWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error

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
	DeleteProductVariantWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error

	// Collection operations
	CreateCollection(ctx context.Context, c *Collection) error
	GetCollection(ctx context.Context, uid string) (*Collection, error)
	GetCollectionByName(ctx context.Context, namespace, name string) (*Collection, error)
	ListCollections(ctx context.Context, namespace string, page PageParams) (*PageResult[Collection], error)
	UpdateCollection(ctx context.Context, c *Collection) error
	DeleteCollection(ctx context.Context, uid string) error
	DeleteCollectionWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error
	ListProductsByLabelSelector(ctx context.Context, namespace string, selector catalog.LabelSelector) ([]*Product, error)

	// Namespace operations
	CreateNamespace(ctx context.Context, ns *Namespace) error
	GetNamespace(ctx context.Context, uid string) (*Namespace, error)
	GetNamespaceByName(ctx context.Context, name string) (*Namespace, error)
	ListNamespaces(ctx context.Context, page PageParams) (*PageResult[Namespace], error)
	UpdateNamespace(ctx context.Context, ns *Namespace, expectedResourceVersion string) error
	// MarkNamespaceDeletion atomically verifies the resourceVersion and that no
	// repository can commit across the empty-to-terminating transition.
	MarkNamespaceDeletion(ctx context.Context, ns *Namespace, expectedResourceVersion string) error
	DeleteNamespace(ctx context.Context, uid string) error
	DeleteNamespaceWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error
	// HasRepositories reports whether at least one Repository record
	// currently belongs to namespace. Used by DeleteNamespace to
	// enforce FR-001 (reject deletion while repositories remain). Must be
	// an existence check (LIMIT 1 / equivalent), not a full count.
	HasRepositories(ctx context.Context, namespace string) (bool, error)

	// Repository operations
	CreateRepository(ctx context.Context, r *Repository) error
	// CreateRepositoryInActiveNamespace atomically or conditionally proves the
	// owning Namespace is active before the repository can commit.
	CreateRepositoryInActiveNamespace(ctx context.Context, r *Repository) error
	GetRepository(ctx context.Context, uid string) (*Repository, error)
	ListRepositoriesByNamespace(ctx context.Context, namespace string, page PageParams) (*PageResult[Repository], error)
	// UpdateRepository replaces a repository only when its persisted
	// resourceVersion matches expectedResourceVersion. It returns ErrConflict
	// when another writer has advanced the record since it was read.
	UpdateRepository(ctx context.Context, r *Repository, expectedResourceVersion string) error
	DeleteRepository(ctx context.Context, uid string) error
	// HasCatalogResources reports whether at least one Product,
	// ProductVariant, CategoryTaxonomy, or Collection record currently has
	// RepositoryID == repositoryID. Used by DeleteRepository to enforce FR-004
	// (reject deletion while catalog resources remain). Must be an
	// existence check (LIMIT 1 / equivalent), not a full count.
	HasCatalogResources(ctx context.Context, repositoryID string) (bool, error)

	// NamespaceMapping operations (lookup contract)
	CreateNamespaceMapping(ctx context.Context, m *NamespaceMapping) error
	LookupRepository(ctx context.Context, namespace, name string) (*NamespaceMapping, error)
	LookupNamespaceByRepoID(ctx context.Context, repositoryID string) (*NamespaceMapping, error)
	RenameRepository(ctx context.Context, namespace, oldName, newName string) error
	// TransferRepository moves the authoritative Repository and mapping only
	// after durably reserving an active target Namespace.
	TransferRepository(ctx context.Context, repositoryID, fromNamespace, toNamespace string) error
	DeleteNamespaceMapping(ctx context.Context, namespace, name string) error

	// Close lifecycle function
	Close() error
}
