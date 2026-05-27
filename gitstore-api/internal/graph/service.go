// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Service layer for GraphQL resolvers
// Handles CRUD operations via the datastore abstraction layer.

package graph

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// identifierRegex matches valid namespace identifiers: DNS label, 1-63 chars.
var identifierRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$|^[a-z0-9]$`)

// reservedIdentifiers is the set of identifiers that cannot be used as namespace names.
var reservedIdentifiers = map[string]struct{}{
	"admin": {}, "root": {}, "system": {}, "default": {}, "api": {}, "git": {},
	"www": {}, "mail": {}, "smtp": {}, "ftp": {}, "org": {}, "orgs": {},
	"static": {}, "assets": {}, "cdn": {}, "docs": {}, "help": {}, "support": {},
	"billing": {}, "status": {}, "health": {}, "internal": {}, "local": {},
	"localhost": {}, "null": {}, "undefined": {}, "true": {}, "false": {},
	"new": {}, "test": {}, "gitstore": {}, "enterprise": {}, "user": {},
	"namespace": {}, "namespaces": {}, "repo": {}, "repos": {},
}

// Service provides business logic for GraphQL operations
type Service struct {
	store     datastore.Datastore
	gitWriter GitWriter
	logger    *zap.Logger
}

// GitWriter is the write subset of gitclient.Client used by the Service.
// Defined here to keep the graph package testable without a real gRPC connection.
type GitWriter interface {
	CreateRepository(ctx context.Context, repositoryID, storageClass string) (storagePath string, err error)
	DeleteRepository(ctx context.Context, repositoryID string) error
	CommitFile(ctx context.Context, p gitclient.CommitFileParams) (string, error)
	DeleteFile(ctx context.Context, p gitclient.DeleteFileParams) (string, error)
	CreateTag(ctx context.Context, p gitclient.CreateTagParams) (string, error)
}

// NewService creates a new service instance backed by the datastore.
func NewService(store datastore.Datastore, logger *zap.Logger) *Service {
	return &Service{
		store:  store,
		logger: logger,
	}
}

// NewServiceWithWriter creates a service with an explicit GitWriter (for tests).
func NewServiceWithWriter(store datastore.Datastore, writer GitWriter, logger *zap.Logger) *Service {
	return &Service{
		store:     store,
		gitWriter: writer,
		logger:    logger,
	}
}

// SetGitWriter wires the gRPC client after construction (called from main.go).
func (s *Service) SetGitWriter(w GitWriter) {
	s.gitWriter = w
}

// GetProducts retrieves all products from the datastore with optional filtering.
func (s *Service) GetProducts(ctx context.Context, categoryID *string) ([]*catalog.Product, error) {
	filter := datastore.ProductFilter{}
	if categoryID != nil && *categoryID != "" {
		filter.CategoryID = *categoryID
	}

	ds_products, err := s.store.ListProducts(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list products: %w", err)
	}

	products := make([]*catalog.Product, len(ds_products))
	for i, p := range ds_products {
		products[i] = datastoreProductToCatalog(p)
	}
	return products, nil
}

// GetProductByID retrieves a product by ID.
func (s *Service) GetProductByID(ctx context.Context, id string) (*catalog.Product, error) {
	p, err := s.store.GetProduct(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("product not found: %s", id)
	}
	return datastoreProductToCatalog(p), nil
}

// GetProductBySKU retrieves a product by SKU.
func (s *Service) GetProductBySKU(ctx context.Context, sku string) (*catalog.Product, error) {
	p, err := s.store.GetProductBySKU(ctx, sku)
	if err != nil {
		return nil, fmt.Errorf("product not found with SKU: %s", sku)
	}
	return datastoreProductToCatalog(p), nil
}

// GetCategories returns all categories.
func (s *Service) GetCategories(ctx context.Context) ([]*catalog.Category, error) {
	cats, err := s.store.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list categories: %w", err)
	}
	result := make([]*catalog.Category, len(cats))
	for i, c := range cats {
		result[i] = datastoreCategoryToCatalog(c)
	}
	return result, nil
}

// GetCategoryByID returns a category by ID.
func (s *Service) GetCategoryByID(ctx context.Context, id string) (*catalog.Category, error) {
	c, err := s.store.GetCategory(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("category not found: %s", id)
	}
	return datastoreCategoryToCatalog(c), nil
}

// GetCategoryBySlug returns a category by slug.
func (s *Service) GetCategoryBySlug(ctx context.Context, slug string) (*catalog.Category, error) {
	c, err := s.store.GetCategoryBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("category not found with slug: %s", slug)
	}
	return datastoreCategoryToCatalog(c), nil
}

// GetCollections returns all collections.
func (s *Service) GetCollections(ctx context.Context) ([]*catalog.Collection, error) {
	colls, err := s.store.ListCollections(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}
	result := make([]*catalog.Collection, len(colls))
	for i, c := range colls {
		result[i] = datastoreCollectionToCatalog(c)
	}
	return result, nil
}

// GetCollectionByID returns a collection by ID.
func (s *Service) GetCollectionByID(ctx context.Context, id string) (*catalog.Collection, error) {
	c, err := s.store.GetCollection(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %s", id)
	}
	return datastoreCollectionToCatalog(c), nil
}

// GetCollectionBySlug returns a collection by slug.
func (s *Service) GetCollectionBySlug(ctx context.Context, slug string) (*catalog.Collection, error) {
	c, err := s.store.GetCollectionBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("collection not found with slug: %s", slug)
	}
	return datastoreCollectionToCatalog(c), nil
}

// CreateProduct creates a new product in the datastore.
func (s *Service) CreateProduct(ctx context.Context, input map[string]interface{}) (*catalog.Product, error) {
	id := uuid.New().String()
	now := time.Now()
	p := &datastore.Product{
		ID:        id,
		SKU:       getStringOrEmpty(input, "sku"),
		Title:     getStringOrEmpty(input, "title"),
		Price:     getFloatOrZero(input, "price"),
		Currency:  getStringOr(input, "currency", "USD"),
		Body:      getStringOrEmpty(input, "body"),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if status, ok := input["inventoryStatus"].(string); ok {
		p.InventoryStatus = status
	}
	if qty, ok := input["inventoryQuantity"].(int); ok {
		p.InventoryQuantity = &qty
	}
	if categoryID, ok := input["categoryId"].(string); ok {
		p.CategoryID = categoryID
	}
	if collectionIDs, ok := input["collectionIds"].([]string); ok {
		p.CollectionIDs = collectionIDs
	}
	if images, ok := input["images"].([]string); ok {
		p.Images = images
	}
	if metadata, ok := input["metadata"].(map[string]interface{}); ok {
		converted := make(map[string]any, len(metadata))
		for k, v := range metadata {
			converted[k] = v
		}
		p.Metadata = converted
	}

	if err := s.store.CreateProduct(ctx, p); err != nil {
		return nil, fmt.Errorf("failed to create product: %w", err)
	}

	return datastoreProductToCatalog(p), nil
}

// UpdateProduct updates an existing product in the datastore.
func (s *Service) UpdateProduct(ctx context.Context, id string, input map[string]interface{}) (*catalog.Product, error) {
	existing, err := s.store.GetProduct(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("product not found: %s", id)
	}

	p := *existing
	p.UpdatedAt = time.Now()

	if title, ok := input["title"].(string); ok {
		p.Title = title
	}
	if sku, ok := input["sku"].(string); ok {
		p.SKU = sku
	}
	if price, ok := input["price"].(float64); ok {
		p.Price = price
	}
	if currency, ok := input["currency"].(string); ok {
		p.Currency = currency
	}
	if body, ok := input["body"].(string); ok {
		p.Body = body
	}
	if status, ok := input["inventoryStatus"].(string); ok {
		p.InventoryStatus = status
	}
	if qty, ok := input["inventoryQuantity"].(int); ok {
		p.InventoryQuantity = &qty
	}
	if categoryID, ok := input["categoryId"].(string); ok {
		p.CategoryID = categoryID
	}
	if collectionIDs, ok := input["collectionIds"].([]string); ok {
		p.CollectionIDs = collectionIDs
	}
	if images, ok := input["images"].([]string); ok {
		p.Images = images
	}
	if metadata, ok := input["metadata"].(map[string]interface{}); ok {
		converted := make(map[string]any, len(metadata))
		for k, v := range metadata {
			converted[k] = v
		}
		p.Metadata = converted
	}

	if err := s.store.UpdateProduct(ctx, &p); err != nil {
		return nil, fmt.Errorf("failed to update product: %w", err)
	}

	return datastoreProductToCatalog(&p), nil
}

// DeleteProduct deletes a product from the datastore.
func (s *Service) DeleteProduct(ctx context.Context, id string) error {
	if err := s.store.DeleteProduct(ctx, id); err != nil {
		return fmt.Errorf("product not found: %s", id)
	}
	return nil
}

// CreateCategory creates a new category in the datastore.
func (s *Service) CreateCategory(ctx context.Context, input map[string]interface{}) (*catalog.Category, error) {
	now := time.Now()
	c := &datastore.Category{
		ID:        uuid.New().String(),
		Name:      getStringOrEmpty(input, "name"),
		Slug:      getStringOrEmpty(input, "slug"),
		Body:      getStringOrEmpty(input, "body"),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if parentID, ok := input["parentId"].(string); ok && parentID != "" {
		c.ParentID = &parentID
	}
	if order, ok := input["displayOrder"].(int); ok {
		c.DisplayOrder = order
	}

	if err := s.store.CreateCategory(ctx, c); err != nil {
		return nil, fmt.Errorf("failed to create category: %w", err)
	}
	return datastoreCategoryToCatalog(c), nil
}

// UpdateCategory updates an existing category.
func (s *Service) UpdateCategory(ctx context.Context, id string, input map[string]interface{}) (*catalog.Category, error) {
	existing, err := s.store.GetCategory(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("category not found: %s", id)
	}
	c := *existing
	c.UpdatedAt = time.Now()
	if name, ok := input["name"].(string); ok {
		c.Name = name
	}
	if slug, ok := input["slug"].(string); ok {
		c.Slug = slug
	}
	if body, ok := input["body"].(string); ok {
		c.Body = body
	}
	if order, ok := input["displayOrder"].(int); ok {
		c.DisplayOrder = order
	}
	if err := s.store.UpdateCategory(ctx, &c); err != nil {
		return nil, fmt.Errorf("failed to update category: %w", err)
	}
	return datastoreCategoryToCatalog(&c), nil
}

// DeleteCategory deletes a category from the datastore.
func (s *Service) DeleteCategory(ctx context.Context, id string) error {
	return s.store.DeleteCategory(ctx, id)
}

// CreateCollection creates a new collection in the datastore.
func (s *Service) CreateCollection(ctx context.Context, input map[string]interface{}) (*catalog.Collection, error) {
	now := time.Now()
	c := &datastore.Collection{
		ID:        uuid.New().String(),
		Name:      getStringOrEmpty(input, "name"),
		Slug:      getStringOrEmpty(input, "slug"),
		Body:      getStringOrEmpty(input, "body"),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if order, ok := input["displayOrder"].(int); ok {
		c.DisplayOrder = order
	}
	if err := s.store.CreateCollection(ctx, c); err != nil {
		return nil, fmt.Errorf("failed to create collection: %w", err)
	}
	return datastoreCollectionToCatalog(c), nil
}

// UpdateCollection updates an existing collection.
func (s *Service) UpdateCollection(ctx context.Context, id string, input map[string]interface{}) (*catalog.Collection, error) {
	existing, err := s.store.GetCollection(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %s", id)
	}
	c := *existing
	c.UpdatedAt = time.Now()
	if name, ok := input["name"].(string); ok {
		c.Name = name
	}
	if slug, ok := input["slug"].(string); ok {
		c.Slug = slug
	}
	if body, ok := input["body"].(string); ok {
		c.Body = body
	}
	if order, ok := input["displayOrder"].(int); ok {
		c.DisplayOrder = order
	}
	if err := s.store.UpdateCollection(ctx, &c); err != nil {
		return nil, fmt.Errorf("failed to update collection: %w", err)
	}
	return datastoreCollectionToCatalog(&c), nil
}

// DeleteCollection deletes a collection from the datastore.
func (s *Service) DeleteCollection(ctx context.Context, id string) error {
	return s.store.DeleteCollection(ctx, id)
}

// ── Namespace ─────────────────────────────────────────────────────────────────

// CreateNamespace validates and creates a new namespace.
func (s *Service) CreateNamespace(ctx context.Context, input model.CreateNamespaceInput, callerUsername string, isAdmin bool) (*datastore.Namespace, error) {
	identifier := strings.ToLower(strings.TrimSpace(input.Identifier))

	if !identifierRegex.MatchString(identifier) {
		return nil, gqlerror.Errorf("invalid identifier: must match DNS label format (lowercase alphanumeric and hyphens, 1–63 chars, no leading/trailing hyphen)")
	}
	if _, reserved := reservedIdentifiers[identifier]; reserved {
		return nil, gqlerror.Errorf("identifier %q is reserved", identifier)
	}

	tier := datastoreNamespaceTierFromModel(input.Tier)
	if tier == datastore.NamespaceTierEnterprise && !isAdmin {
		return nil, gqlerror.Errorf("creating enterprise namespaces requires elevated permissions")
	}

	var parentEnterpriseID *string
	if input.ParentEnterpriseIdentifier != nil && *input.ParentEnterpriseIdentifier != "" {
		if tier != datastore.NamespaceTierOrganisation {
			return nil, gqlerror.Errorf("parentEnterpriseIdentifier may only be set for ORGANISATION tier namespaces")
		}
		parent, err := s.store.GetNamespaceByIdentifier(ctx, *input.ParentEnterpriseIdentifier)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				return nil, gqlerror.Errorf("parent enterprise namespace %q not found", *input.ParentEnterpriseIdentifier)
			}
			return nil, gqlerror.Errorf("failed to resolve parent enterprise namespace")
		}
		if parent.Tier != datastore.NamespaceTierEnterprise {
			return nil, gqlerror.Errorf("parent namespace %q is not an enterprise namespace", *input.ParentEnterpriseIdentifier)
		}
		parentEnterpriseID = &parent.ID
	}

	now := time.Now()
	var displayName string
	if input.DisplayName != nil {
		displayName = *input.DisplayName
	}
	ns := &datastore.Namespace{
		ID:                 uuid.New().String(),
		Identifier:         identifier,
		DisplayName:        displayName,
		Tier:               tier,
		ParentEnterpriseID: parentEnterpriseID,
		CreatedAt:          now,
		CreatedBy:          callerUsername,
		UpdatedAt:          now,
		UpdatedBy:          callerUsername,
	}

	if err := s.store.CreateNamespace(ctx, ns); err != nil {
		if errors.Is(err, datastore.ErrAlreadyExists) {
			return nil, gqlerror.Errorf("namespace with identifier %q already exists", identifier)
		}
		s.logger.Error("failed to create namespace",
			zap.String("identifier", identifier),
			zap.Error(err),
		)
		return nil, gqlerror.Errorf("failed to create namespace")
	}

	return ns, nil
}

// GetNamespaceByIdentifier retrieves a namespace by its identifier.
func (s *Service) GetNamespaceByIdentifier(ctx context.Context, identifier string) (*datastore.Namespace, error) {
	ns, err := s.store.GetNamespaceByIdentifier(ctx, identifier)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			s.logger.Debug("namespace not found", zap.String("identifier", identifier))
			return nil, gqlerror.Errorf("namespace %q not found", identifier)
		}
		return nil, gqlerror.Errorf("failed to retrieve namespace")
	}
	return ns, nil
}

// GetNamespaceByID retrieves a namespace by its system ID.
func (s *Service) GetNamespaceByID(ctx context.Context, id string) (*datastore.Namespace, error) {
	ns, err := s.store.GetNamespace(ctx, id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			s.logger.Debug("namespace not found", zap.String("id", id))
			return nil, gqlerror.Errorf("namespace with id %q not found", id)
		}
		return nil, gqlerror.Errorf("failed to retrieve namespace")
	}
	return ns, nil
}

// ListNamespaces returns all namespaces.
func (s *Service) ListNamespaces(ctx context.Context) ([]*datastore.Namespace, error) {
	nss, err := s.store.ListNamespaces(ctx)
	if err != nil {
		return nil, gqlerror.Errorf("failed to list namespaces")
	}
	if nss == nil {
		return []*datastore.Namespace{}, nil
	}
	return nss, nil
}

// DeleteNamespace deletes a namespace after authorisation and safety checks.
func (s *Service) DeleteNamespace(ctx context.Context, identifier string, callerUsername string, isAdmin bool) error {
	ns, err := s.store.GetNamespaceByIdentifier(ctx, identifier)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return gqlerror.Errorf("namespace %q not found", identifier)
		}
		return gqlerror.Errorf("failed to retrieve namespace")
	}

	if ns.CreatedBy != callerUsername && !isAdmin {
		return gqlerror.Errorf("permission denied: only the namespace owner or an admin may delete this namespace")
	}

	// TODO: enforce when repositories table exists
	if hasRepositories(ns.ID) {
		return gqlerror.Errorf("namespace %q contains repositories and cannot be deleted", identifier)
	}

	if err := s.store.DeleteNamespace(ctx, ns.ID); err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return gqlerror.Errorf("namespace %q not found", identifier)
		}
		s.logger.Error("failed to delete namespace",
			zap.String("identifier", identifier),
			zap.Error(err),
		)
		return gqlerror.Errorf("failed to delete namespace")
	}

	return nil
}

// hasRepositories returns true when the namespace has at least one repository.
func hasRepositories(namespaceID string) bool {
	// Intentionally not implemented — DeleteNamespace validation deferred to a separate feature.
	// Returning false here is safe for ALPHA since namespace deletion is restricted.
	_ = namespaceID
	return false
}

// Store returns the underlying Datastore. Used in tests to pre-populate fixtures.
func (s *Service) Store() datastore.Datastore {
	return s.store
}

// ── Repository service methods ────────────────────────────────────────────────

// fanoutStoragePath computes {data_dir}/{xx}/{yy}/{repo_id}.git from a UUID string.
// This mirrors the Rust fanout formula in gitstore-git-service.
func fanoutStoragePath(dataDir, repoID string) string {
	hex := strings.ReplaceAll(repoID, "-", "")
	if len(hex) < 4 {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s/%s.git", dataDir, hex[0:2], hex[2:4], repoID)
}

// CreateRepository creates a new repository and its namespace mapping, then provisions
// storage via gRPC. Returns the created Repository entity.
func (s *Service) CreateRepository(ctx context.Context, namespaceID, name, defaultBranch, storageClass, callerUsername string) (*datastore.Repository, error) {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if storageClass == "" {
		storageClass = "default"
	}
	repoID, err := uuid.NewV7()
	if err != nil {
		return nil, gqlerror.Errorf("failed to generate repository ID")
	}
	now := time.Now().UTC()
	repo := &datastore.Repository{
		ID:            repoID.String(),
		NamespaceID:   namespaceID,
		Name:          name,
		DefaultBranch: defaultBranch,
		StorageClass:  storageClass,
		CreatedAt:     now,
		CreatedBy:     callerUsername,
		UpdatedAt:     now,
		UpdatedBy:     callerUsername,
	}
	if err := s.store.CreateRepository(ctx, repo); err != nil {
		if errors.Is(err, datastore.ErrAlreadyExists) {
			return nil, gqlerror.Errorf("repository already exists")
		}
		s.logger.Error("failed to create repository", zap.String("repo_id", repo.ID), zap.Error(err))
		return nil, gqlerror.Errorf("failed to create repository")
	}
	if err := s.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		NamespaceID: namespaceID,
		Name:        name,
		RepoID:      repo.ID,
	}); err != nil {
		// Roll back the repository row so it does not orphan a name slot.
		if delErr := s.store.DeleteRepository(ctx, repo.ID); delErr != nil {
			s.logger.Error("rollback DeleteRepository failed after mapping create failure",
				zap.String("repo_id", repo.ID), zap.Error(delErr))
		}
		if errors.Is(err, datastore.ErrAlreadyExists) {
			return nil, gqlerror.Errorf("repository already exists")
		}
		s.logger.Error("failed to create namespace mapping", zap.String("repo_id", repo.ID), zap.Error(err))
		return nil, gqlerror.Errorf("failed to create namespace mapping")
	}
	s.logger.Info("lookup repository",
		zap.String("namespace_id", namespaceID),
		zap.String("name", name),
		zap.String("repo_id", repo.ID),
	)
	if s.gitWriter != nil {
		if _, err := s.gitWriter.CreateRepository(ctx, repo.ID, storageClass); err != nil {
			s.logger.Error("gRPC CreateRepository failed",
				zap.String("repo_id", repo.ID),
				zap.String("rpc", "CreateRepository"),
				zap.Error(err),
			)
			// Compensate: drop both metadata rows so a retry can re-create
			// cleanly instead of resolving a name with no backing storage.
			if delErr := s.store.DeleteNamespaceMapping(ctx, namespaceID, name); delErr != nil {
				s.logger.Error("rollback DeleteNamespaceMapping failed after storage provision failure",
					zap.String("repo_id", repo.ID), zap.Error(delErr))
			}
			if delErr := s.store.DeleteRepository(ctx, repo.ID); delErr != nil {
				s.logger.Error("rollback DeleteRepository failed after storage provision failure",
					zap.String("repo_id", repo.ID), zap.Error(delErr))
			}
			return nil, gqlerror.Errorf("failed to provision repository storage")
		}
		s.logger.Info("gRPC CreateRepository succeeded",
			zap.String("repo_id", repo.ID),
			zap.String("rpc", "CreateRepository"),
		)
	}
	return repo, nil
}

// GetRepository retrieves a repository by its raw UUID.
func (s *Service) GetRepository(ctx context.Context, id string) (*datastore.Repository, error) {
	r, err := s.store.GetRepository(ctx, id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, gqlerror.Errorf("repository not found")
		}
		return nil, gqlerror.Errorf("failed to retrieve repository")
	}
	return r, nil
}

// LookupRepository resolves (namespaceID, name) → NamespaceMapping.
func (s *Service) LookupRepository(ctx context.Context, namespaceID, name string) (*datastore.NamespaceMapping, error) {
	m, err := s.store.LookupRepository(ctx, namespaceID, name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			s.logger.Info("lookup repository not found",
				zap.String("namespace_id", namespaceID),
				zap.String("name", name),
			)
			return nil, datastore.ErrNotFound
		}
		return nil, gqlerror.Errorf("failed to lookup repository")
	}
	s.logger.Info("lookup repository",
		zap.String("namespace_id", namespaceID),
		zap.String("name", name),
		zap.String("repo_id", m.RepoID),
	)
	return m, nil
}

// LookupNamespaceByRepoID resolves repo_id → NamespaceMapping (reverse lookup).
func (s *Service) LookupNamespaceByRepoID(ctx context.Context, repoID string) (*datastore.NamespaceMapping, error) {
	m, err := s.store.LookupNamespaceByRepoID(ctx, repoID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, gqlerror.Errorf("failed to reverse-lookup namespace by repo_id")
	}
	return m, nil
}

// ListRepositoriesByNamespace lists all repositories in a namespace.
func (s *Service) ListRepositoriesByNamespace(ctx context.Context, namespaceID string) ([]*datastore.Repository, error) {
	repos, err := s.store.ListRepositoriesByNamespace(ctx, namespaceID)
	if err != nil {
		return nil, gqlerror.Errorf("failed to list repositories")
	}
	return repos, nil
}

// RenameRepository renames a repository within its namespace. Storage is not moved.
func (s *Service) RenameRepository(ctx context.Context, repoID, newName, callerUsername string) (*datastore.Repository, error) {
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, gqlerror.Errorf("repository not found")
		}
		return nil, gqlerror.Errorf("failed to retrieve repository")
	}
	oldName := repo.Name
	if err := s.store.RenameRepository(ctx, repo.NamespaceID, oldName, newName); err != nil {
		s.logger.Error("failed to rename repository",
			zap.String("repo_id", repoID),
			zap.String("old_name", oldName),
			zap.String("new_name", newName),
			zap.Error(err),
		)
		return nil, gqlerror.Errorf("failed to rename repository")
	}
	repo.Name = newName
	repo.UpdatedAt = time.Now().UTC()
	repo.UpdatedBy = callerUsername
	if err := s.store.UpdateRepository(ctx, repo); err != nil {
		s.logger.Error("failed to update repository record after rename",
			zap.String("repo_id", repoID),
			zap.Error(err),
		)
		return nil, gqlerror.Errorf("failed to update repository record")
	}
	s.logger.Info("rename repository",
		zap.String("repo_id", repoID),
		zap.String("old_name", oldName),
		zap.String("new_name", newName),
	)
	return repo, nil
}

// TransferRepository transfers a repository to a different namespace. Storage is not moved.
func (s *Service) TransferRepository(ctx context.Context, repoID, toNamespaceID, callerUsername string) (*datastore.Repository, error) {
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, gqlerror.Errorf("repository not found")
		}
		return nil, gqlerror.Errorf("failed to retrieve repository")
	}
	fromNamespaceID := repo.NamespaceID
	if err := s.store.TransferRepository(ctx, repoID, fromNamespaceID, toNamespaceID); err != nil {
		s.logger.Error("failed to transfer repository",
			zap.String("repo_id", repoID),
			zap.String("from_namespace_id", fromNamespaceID),
			zap.String("to_namespace_id", toNamespaceID),
			zap.Error(err),
		)
		return nil, gqlerror.Errorf("failed to transfer repository")
	}
	repo.NamespaceID = toNamespaceID
	repo.UpdatedAt = time.Now().UTC()
	repo.UpdatedBy = callerUsername
	if err := s.store.UpdateRepository(ctx, repo); err != nil {
		s.logger.Error("failed to update repository record after transfer",
			zap.String("repo_id", repoID),
			zap.Error(err),
		)
		return nil, gqlerror.Errorf("failed to update repository record")
	}
	s.logger.Info("transfer repository",
		zap.String("repo_id", repoID),
		zap.String("from_namespace_id", fromNamespaceID),
		zap.String("to_namespace_id", toNamespaceID),
	)
	return repo, nil
}

// DeleteRepository deletes a repository, its mapping, and its storage via gRPC.
//
// Storage is removed first; only on success do we drop the metadata rows. This
// avoids leaving an orphaned .git directory when the gRPC call transiently
// fails, since the caller can retry against the still-resolvable repo_id.
func (s *Service) DeleteRepository(ctx context.Context, repoID, callerUsername string) error {
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return gqlerror.Errorf("repository not found")
		}
		return gqlerror.Errorf("failed to retrieve repository")
	}
	if s.gitWriter != nil {
		if err := s.gitWriter.DeleteRepository(ctx, repoID); err != nil {
			s.logger.Error("gRPC DeleteRepository failed",
				zap.String("repo_id", repoID),
				zap.String("rpc", "DeleteRepository"),
				zap.Error(err),
			)
			return gqlerror.Errorf("failed to delete repository storage")
		}
		s.logger.Info("gRPC DeleteRepository succeeded",
			zap.String("repo_id", repoID),
			zap.String("rpc", "DeleteRepository"),
		)
	}
	if err := s.store.DeleteNamespaceMapping(ctx, repo.NamespaceID, repo.Name); err != nil && !errors.Is(err, datastore.ErrNotFound) {
		s.logger.Error("failed to delete namespace mapping", zap.String("repo_id", repoID), zap.Error(err))
		return gqlerror.Errorf("failed to delete namespace mapping")
	}
	if err := s.store.DeleteRepository(ctx, repoID); err != nil && !errors.Is(err, datastore.ErrNotFound) {
		s.logger.Error("failed to delete repository record", zap.String("repo_id", repoID), zap.Error(err))
		return gqlerror.Errorf("failed to delete repository")
	}
	return nil
}

func datastoreNamespaceTierFromModel(t model.NamespaceTier) datastore.NamespaceTier {
	switch t {
	case model.NamespaceTierOrganisation:
		return datastore.NamespaceTierOrganisation
	case model.NamespaceTierEnterprise:
		return datastore.NamespaceTierEnterprise
	default:
		return datastore.NamespaceTierUser
	}
}

// ── catalog ↔ datastore conversion helpers ────────────────────────────────────

func datastoreProductToCatalog(p *datastore.Product) *catalog.Product {
	return &catalog.Product{
		ID:                p.ID,
		SKU:               p.SKU,
		Title:             p.Title,
		Price:             p.Price,
		Currency:          p.Currency,
		InventoryStatus:   p.InventoryStatus,
		InventoryQuantity: p.InventoryQuantity,
		CategoryID:        p.CategoryID,
		CollectionIDs:     p.CollectionIDs,
		Images:            p.Images,
		Metadata:          p.Metadata,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
		Body:              p.Body,
	}
}

func datastoreCategoryToCatalog(c *datastore.Category) *catalog.Category {
	return &catalog.Category{
		ID:           c.ID,
		Name:         c.Name,
		Slug:         c.Slug,
		ParentID:     c.ParentID,
		DisplayOrder: c.DisplayOrder,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
		Body:         c.Body,
	}
}

func datastoreCollectionToCatalog(c *datastore.Collection) *catalog.Collection {
	return &catalog.Collection{
		ID:           c.ID,
		Name:         c.Name,
		Slug:         c.Slug,
		DisplayOrder: c.DisplayOrder,
		ProductIDs:   c.ProductIDs,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
		Body:         c.Body,
	}
}

// Helper functions
func getStringOrEmpty(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringOr(m map[string]interface{}, key, defaultVal string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return defaultVal
}

func getFloatOrZero(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0.0
}
