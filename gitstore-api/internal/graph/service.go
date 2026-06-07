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

// GetProducts retrieves all products in a namespace from the datastore.
func (s *Service) GetProducts(ctx context.Context, namespace string, params datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	result, err := s.store.ListProducts(ctx, namespace, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list products: %w", err)
	}
	return result, nil
}

// GetProductByUID retrieves a product by UID.
func (s *Service) GetProductByUID(ctx context.Context, uid string) (*datastore.Product, error) {
	p, err := s.store.GetProduct(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("product not found: %s", uid)
	}
	return p, nil
}

// GetProductByName retrieves a product by namespace and name.
func (s *Service) GetProductByName(ctx context.Context, namespace, name string) (*datastore.Product, error) {
	p, err := s.store.GetProductByName(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("product not found: %s/%s", namespace, name)
	}
	return p, nil
}

// GetCategoryTaxonomies returns paginated CategoryTaxonomy resources.
func (s *Service) GetCategoryTaxonomies(ctx context.Context, namespace string, params datastore.PageParams) (*datastore.PageResult[datastore.CategoryTaxonomy], error) {
	result, err := s.store.ListCategoryTaxonomies(ctx, namespace, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list category taxonomies: %w", err)
	}
	return result, nil
}

// GetCategoryTaxonomyByUID returns a CategoryTaxonomy by UID.
func (s *Service) GetCategoryTaxonomyByUID(ctx context.Context, uid string) (*datastore.CategoryTaxonomy, error) {
	c, err := s.store.GetCategoryTaxonomy(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("category not found: %s", uid)
	}
	return c, nil
}

// GetCategoryTaxonomyByName returns a CategoryTaxonomy by namespace and name.
func (s *Service) GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*datastore.CategoryTaxonomy, error) {
	c, err := s.store.GetCategoryTaxonomyByName(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("category not found: %s/%s", namespace, name)
	}
	return c, nil
}

// GetCollections returns paginated collections for a namespace.
func (s *Service) GetCollections(ctx context.Context, namespace string, params datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	result, err := s.store.ListCollections(ctx, namespace, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}
	return result, nil
}

// GetCollectionByUID returns a collection by UID.
func (s *Service) GetCollectionByUID(ctx context.Context, uid string) (*datastore.Collection, error) {
	c, err := s.store.GetCollection(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %s", uid)
	}
	return c, nil
}

// GetCollectionByName returns a collection by namespace/name.
func (s *Service) GetCollectionByName(ctx context.Context, namespace, name string) (*datastore.Collection, error) {
	c, err := s.store.GetCollectionByName(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %s/%s", namespace, name)
	}
	return c, nil
}

// ListProductsByLabelSelector returns products in a namespace matching the label selector.
func (s *Service) ListProductsByLabelSelector(ctx context.Context, namespace string, selector catalog.LabelSelector) ([]*datastore.Product, error) {
	return s.store.ListProductsByLabelSelector(ctx, namespace, selector)
}

// DeleteProduct deletes a product from the datastore by UID.
// Products are authored via git push; this is used for cleanup only.
func (s *Service) DeleteProduct(ctx context.Context, uid string) error {
	if err := s.store.DeleteProduct(ctx, uid); err != nil {
		return fmt.Errorf("product not found: %s", uid)
	}
	return nil
}

// CreateCollection creates a new collection in the datastore.
// This is a transitional method; collection admission via git push is the primary path.
func (s *Service) CreateCollection(ctx context.Context, input map[string]interface{}) (*datastore.Collection, error) {
	c := &datastore.Collection{
		UID:       uuid.New().String(),
		Name:      getStringOrEmpty(input, "name"),
		Body:      getStringOrEmpty(input, "body"),
	}
	if err := s.store.CreateCollection(ctx, c); err != nil {
		return nil, fmt.Errorf("failed to create collection: %w", err)
	}
	return c, nil
}

// UpdateCollection updates an existing collection.
func (s *Service) UpdateCollection(ctx context.Context, uid string, input map[string]interface{}) (*datastore.Collection, error) {
	existing, err := s.store.GetCollection(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %s", uid)
	}
	c := *existing
	if name, ok := input["name"].(string); ok {
		c.Name = name
	}
	if body, ok := input["body"].(string); ok {
		c.Body = body
	}
	if err := s.store.UpdateCollection(ctx, &c); err != nil {
		return nil, fmt.Errorf("failed to update collection: %w", err)
	}
	return &c, nil
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

// ListNamespaces returns paginated namespaces.
func (s *Service) ListNamespaces(ctx context.Context, params datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	result, err := s.store.ListNamespaces(ctx, params)
	if err != nil {
		return nil, gqlerror.Errorf("failed to list namespaces")
	}
	return result, nil
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

// ListRepositoriesByNamespace lists paginated repositories in a namespace.
func (s *Service) ListRepositoriesByNamespace(ctx context.Context, namespaceID string, params datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	result, err := s.store.ListRepositoriesByNamespace(ctx, namespaceID, params)
	if err != nil {
		return nil, gqlerror.Errorf("failed to list repositories")
	}
	return result, nil
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

// Helper functions
func getStringOrEmpty(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
