// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Service layer for GraphQL resolvers
// Handles CRUD operations via the datastore abstraction layer.

package resolver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// identifierRegex matches valid namespace identifiers: DNS label, 1-63 chars.
var identifierRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$|^[a-z0-9]$`)

// SystemRepositoryName is the well-known repository auto-provisioned for
// every namespace on creation (ADR-0002/ADR-0003). It is the authoring
// target for git-backed management of the namespace's own resources.
const SystemRepositoryName = "gitstore-system"

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
	clock     apiruntime.Clock
	ids       apiruntime.IDGenerator
}

// GitWriter is the write subset of gitclient.Client used by the Service.
// Defined here to keep the graph package testable without a real gRPC connection.
type GitWriter interface {
	CreateRepository(ctx context.Context, repositoryID, storageClass string) (storagePath string, err error)
	DeleteRepository(ctx context.Context, repositoryID string) error
	CommitFile(ctx context.Context, p gitclient.CommitFileParams) (string, error)
	CommitFileForRepo(ctx context.Context, repositoryID string, p gitclient.CommitFileParams) (string, error)
	ResolveRefForRepo(ctx context.Context, repositoryID, ref string) (string, error)
	DeleteFile(ctx context.Context, p gitclient.DeleteFileParams) (string, error)
	CreateTag(ctx context.Context, p gitclient.CreateTagParams) (string, error)
}

// ServiceDeps contains dependencies for GraphQL business logic.
type ServiceDeps struct {
	Store       datastore.Datastore
	GitWriter   GitWriter
	Logger      *zap.Logger
	Clock       apiruntime.Clock
	IDGenerator apiruntime.IDGenerator
}

// NewService creates a new service instance backed by the datastore.
func NewService(deps ServiceDeps) (*Service, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("resolver: datastore is required")
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("resolver: logger is required")
	}
	clock := deps.Clock
	if clock == nil {
		clock = apiruntime.SystemClock{}
	}
	ids := deps.IDGenerator
	if ids == nil {
		ids = apiruntime.UUIDGenerator{}
	}
	return &Service{
		store:     deps.Store,
		gitWriter: deps.GitWriter,
		logger:    deps.Logger,
		clock:     clock,
		ids:       ids,
	}, nil
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
		UID:  s.ids.NewID(),
		Name: getStringOrEmpty(input, "name"),
		Body: getStringOrEmpty(input, "body"),
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

// CreateNamespace validates and commits a Namespace manifest, then applies the
// same admission function used by the post-receive pipeline.
// Authorization is enforced in GraphQL middleware before this method is called.
func (s *Service) CreateNamespace(ctx context.Context, input model.CreateNamespaceInput, callerUsername string) (*datastore.Namespace, error) {
	resource, err := namespaceResourceFromCreateInput(input)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.GetNamespaceByName(ctx, resource.Metadata.Name); err == nil {
		return nil, gqlerror.Errorf("namespace with identifier %q already exists", resource.Metadata.Name)
	} else if !errors.Is(err, datastore.ErrNotFound) {
		return nil, gqlerror.Errorf("failed to check namespace existence")
	}
	return s.commitAndAdmitNamespace(ctx, resource, callerUsername, true)
}

// UpdateNamespace commits and admits a replacement Namespace spec.
func (s *Service) UpdateNamespace(ctx context.Context, input model.UpdateNamespaceInput, callerUsername string) (*datastore.Namespace, error) {
	resource, err := namespaceResourceFromUpdateInput(input)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.GetNamespaceByName(ctx, resource.Metadata.Name); err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, gqlerror.Errorf("namespace %q not found", resource.Metadata.Name)
		}
		return nil, gqlerror.Errorf("failed to retrieve namespace")
	}
	return s.commitAndAdmitNamespace(ctx, resource, callerUsername, false)
}

func (s *Service) commitAndAdmitNamespace(ctx context.Context, resource *catalog.NamespaceResource, callerUsername string, create bool) (*datastore.Namespace, error) {
	if s.gitWriter == nil {
		return nil, gqlerror.Errorf("namespace Git writer is unavailable")
	}
	systemNamespace, err := s.store.GetNamespaceByName(ctx, "gitstore-system")
	if err != nil {
		return nil, gqlerror.Errorf("bootstrap namespace gitstore-system is unavailable")
	}
	mapping, err := s.store.LookupRepository(ctx, systemNamespace.ID, SystemRepositoryName)
	if err != nil {
		return nil, gqlerror.Errorf("bootstrap repository gitstore-system/gitstore-system is unavailable")
	}

	yamlBody, err := yaml.Marshal(resource)
	if err != nil {
		return nil, gqlerror.Errorf("failed to encode Namespace manifest")
	}
	content := append([]byte("---\n"), yamlBody...)
	content = append(content, []byte("---\n")...)
	if _, _, err := validate.NewParser().ParseResource(bytes.NewReader(content)); err != nil {
		return nil, gqlerror.Errorf("Namespace manifest validation failed: %v", err)
	}
	verb := "Update"
	if create {
		verb = "Create"
	}
	sha, err := s.gitWriter.CommitFileForRepo(ctx, mapping.RepoID, gitclient.CommitFileParams{
		Path:          fmt.Sprintf("namespaces/%s.md", resource.Metadata.Name),
		Content:       content,
		CommitMessage: fmt.Sprintf("%s Namespace %s", verb, resource.Metadata.Name),
		AuthorName:    callerUsername,
	})
	if err != nil {
		return nil, gqlerror.Errorf("failed to commit Namespace manifest: %v", err)
	}
	currentHead, err := s.gitWriter.ResolveRefForRepo(ctx, mapping.RepoID, "refs/heads/main")
	if err != nil {
		return nil, gqlerror.Errorf("failed to verify Namespace commit: %v", err)
	}
	if currentHead != sha {
		return nil, gqlerror.Errorf("Namespace commit was superseded by a newer commit")
	}

	now := s.clock.Now().UTC()
	namespace, _, err := namespaceadmission.ApplyManifest(
		ctx,
		s.store,
		s.ids,
		resource,
		now,
		"main@sha1:"+sha,
		callerUsername,
	)
	if err != nil {
		switch {
		case errors.Is(err, namespaceadmission.ErrBootstrapNamespace):
			return nil, gqlerror.Errorf("bootstrap namespace %q is system-managed", resource.Metadata.Name)
		case errors.Is(err, namespaceadmission.ErrTierDemotion):
			return nil, gqlerror.Errorf("namespace tier demotion is not allowed")
		default:
			return nil, gqlerror.Errorf("Namespace admission failed: %v", err)
		}
	}
	return namespace, nil
}

func namespaceResourceFromCreateInput(input model.CreateNamespaceInput) (*catalog.NamespaceResource, error) {
	return namespaceResourceFromInput(input.APIVersion, input.Kind, input.Metadata, input.Spec)
}

func namespaceResourceFromUpdateInput(input model.UpdateNamespaceInput) (*catalog.NamespaceResource, error) {
	return namespaceResourceFromInput(input.APIVersion, input.Kind, input.Metadata, input.Spec)
}

func namespaceResourceFromInput(apiVersion, kind string, metadata *model.NamespaceMetadataInput, spec *model.NamespaceSpecInput) (*catalog.NamespaceResource, error) {
	if apiVersion != namespaceAPIVersion {
		return nil, gqlerror.Errorf("apiVersion must be %q", namespaceAPIVersion)
	}
	if kind != namespaceKind {
		return nil, gqlerror.Errorf("kind must be %q", namespaceKind)
	}
	if metadata == nil || spec == nil {
		return nil, gqlerror.Errorf("metadata and spec are required")
	}
	identifier := strings.ToLower(strings.TrimSpace(metadata.Name))
	if !identifierRegex.MatchString(identifier) {
		return nil, gqlerror.Errorf("invalid identifier: must match DNS label format (lowercase alphanumeric and hyphens, 1-63 chars, no leading/trailing hyphen)")
	}
	if namespaceadmission.IsBootstrap(identifier) {
		return nil, gqlerror.Errorf("bootstrap namespace %q is system-managed", identifier)
	}
	if _, reserved := reservedIdentifiers[identifier]; reserved {
		return nil, gqlerror.Errorf("identifier %q is reserved", identifier)
	}
	if !spec.Tier.IsValid() {
		return nil, gqlerror.Errorf("invalid namespace tier %q: must be USER or ORGANIZATION", spec.Tier)
	}
	labels, err := stringMap(metadata.Labels)
	if err != nil {
		return nil, gqlerror.Errorf("metadata.labels: %v", err)
	}
	annotations, err := stringMap(metadata.Annotations)
	if err != nil {
		return nil, gqlerror.Errorf("metadata.annotations: %v", err)
	}
	title := ""
	if spec.Title != nil {
		title = *spec.Title
	}
	resourceSpec := catalog.NamespaceSpec{
		Title: title,
		Tier:  string(spec.Tier),
	}
	if defaults := spec.RepositoryDefaults; defaults != nil {
		resourceSpec.RepositoryDefaults = &catalog.NamespaceRepositoryDefaults{
			DefaultBranch: stringOrEmpty(defaults.DefaultBranch),
		}
		if defaults.Visibility != nil {
			resourceSpec.RepositoryDefaults.Visibility = defaults.Visibility.String()
		}
	}
	if defaults := spec.PushPolicyDefaults; defaults != nil {
		resourceSpec.PushPolicyDefaults = &catalog.NamespacePushPolicyDefaults{
			MaxPackSizeBytes: int64OrZero(defaults.MaxPackSizeBytes),
			MaxFileSizeBytes: int64OrZero(defaults.MaxFileSizeBytes),
		}
	}
	return &catalog.NamespaceResource{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata: catalog.ObjectMeta{
			Name:        identifier,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: resourceSpec,
	}, nil
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64OrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func stringMap(input map[string]any) (map[string]string, error) {
	if input == nil {
		return nil, nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a string", key)
		}
		output[key] = text
	}
	return output, nil
}

// ProvisionSystemRepository ensures the well-known SystemRepositoryName
// repository exists for namespaceID, creating it if absent (FR-007). A
// repository that already exists — including one created by a concurrent or
// retried call — is treated as a successful idempotent outcome, never an
// error (FR-008).
func (s *Service) ProvisionSystemRepository(ctx context.Context, namespaceID, callerUsername string) error {
	if _, err := s.store.LookupRepository(ctx, namespaceID, SystemRepositoryName); err == nil {
		s.logger.Info("system repository already provisioned",
			zap.String("namespace_id", namespaceID),
			zap.String("name", SystemRepositoryName),
		)
		return nil
	} else if !errors.Is(err, datastore.ErrNotFound) {
		return err
	}

	if _, err := s.CreateRepository(ctx, namespaceID, SystemRepositoryName, "", "default", callerUsername); err != nil {
		// A concurrent/retried provisioning attempt may have won the race
		// between the lookup above and this create — re-check before
		// surfacing the error.
		if _, lookupErr := s.store.LookupRepository(ctx, namespaceID, SystemRepositoryName); lookupErr == nil {
			s.logger.Info("system repository provisioned concurrently, treating as idempotent success",
				zap.String("namespace_id", namespaceID),
				zap.String("name", SystemRepositoryName),
			)
			return nil
		}
		return err
	}

	s.logger.Info("system repository provisioned",
		zap.String("namespace_id", namespaceID),
		zap.String("name", SystemRepositoryName),
	)
	return nil
}

// GetNamespaceByName retrieves a namespace by its canonical name.
func (s *Service) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	ns, err := s.store.GetNamespaceByName(ctx, name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			s.logger.Debug("namespace not found", zap.String("name", name))
			return nil, gqlerror.Errorf("namespace %q not found", name)
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

// DeleteNamespace deletes a namespace after safety checks.
// Authorization is enforced in GraphQL middleware before this method is called.
func (s *Service) DeleteNamespace(ctx context.Context, ns *datastore.Namespace) error {
	if ns == nil || ns.ID == "" {
		return gqlerror.Errorf("namespace deletion target is missing")
	}
	if namespaceadmission.IsBootstrap(ns.Name) {
		return gqlerror.Errorf("bootstrap namespace %q is system-managed", ns.Name)
	}

	current, err := s.store.GetNamespaceByName(ctx, ns.Name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return gqlerror.Errorf("namespace %q not found", ns.Name)
		}
		return gqlerror.Errorf("failed to delete namespace")
	}
	if current.ID != ns.ID {
		return gqlerror.Errorf("namespace %q no longer refers to the requested resource", ns.Name)
	}
	if current.DeletionTimestamp != nil {
		return nil
	}

	hasRepos, err := s.store.HasRepositories(ctx, current.ID)
	if err != nil {
		s.logger.Error("failed to check for existing repositories",
			zap.String("name", current.Name),
			zap.Error(err),
		)
		return gqlerror.Errorf("failed to delete namespace")
	}
	if hasRepos {
		s.logger.Info("namespace deletion rejected: contains repositories",
			zap.String("name", current.Name),
		)
		return gqlerror.Errorf("namespace %q contains repositories and cannot be deleted", current.Name)
	}

	now := s.clock.Now().UTC()
	current.DeletionTimestamp = &now
	if !containsString(current.Finalizers, datastore.NamespaceForegroundDeletionFinalizer) {
		current.Finalizers = append(current.Finalizers, datastore.NamespaceForegroundDeletionFinalizer)
	}
	expectedResourceVersion := current.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(current)
	current.UpdateTimestamp = now
	if err := s.store.UpdateNamespace(ctx, current, expectedResourceVersion); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			return gqlerror.Errorf("namespace %q changed while deletion was requested", current.Name)
		}
		s.logger.Error("failed to delete namespace",
			zap.String("name", current.Name),
			zap.Error(err),
		)
		return gqlerror.Errorf("failed to delete namespace")
	}
	return nil
}

func (s *Service) CompleteNamespaceDeletion(ctx context.Context, name, expectedResourceVersion string) (*datastore.Namespace, error) {
	if namespaceadmission.IsBootstrap(name) {
		return nil, gqlerror.Errorf("bootstrap namespace %q is system-managed", name)
	}
	current, err := s.store.GetNamespaceByName(ctx, name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, nil
		}
		return nil, gqlerror.Errorf("failed to complete namespace deletion")
	}
	if current.ResourceVersion != expectedResourceVersion {
		return current, datastore.ErrConflict
	}
	if current.DeletionTimestamp == nil ||
		!containsString(current.Finalizers, datastore.NamespaceForegroundDeletionFinalizer) {
		return nil, gqlerror.Errorf("namespace %q is not awaiting foreground deletion", name)
	}
	hasRepos, err := s.store.HasRepositories(ctx, current.ID)
	if err != nil {
		return nil, gqlerror.Errorf("failed to complete namespace deletion")
	}
	if hasRepos {
		return nil, gqlerror.Errorf("namespace %q still contains repositories", name)
	}
	if err := s.store.DeleteNamespaceWithResourceVersion(ctx, current.ID, expectedResourceVersion); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			latest, getErr := s.store.GetNamespaceByName(ctx, name)
			if getErr != nil {
				return nil, gqlerror.Errorf("namespace deletion conflict, and current version could not be read")
			}
			return latest, datastore.ErrConflict
		}
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, nil
		}
		return nil, gqlerror.Errorf("failed to complete namespace deletion")
	}
	return current, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
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
	repoID, err := s.ids.NewV7ID()
	if err != nil {
		return nil, gqlerror.Errorf("failed to generate repository ID")
	}
	now := s.clock.Now().UTC()
	repo := &datastore.Repository{
		ID:                repoID,
		NamespaceID:       namespaceID,
		Name:              name,
		DefaultBranch:     defaultBranch,
		StorageClass:      storageClass,
		CreationTimestamp: now,
		CreationActor:     callerUsername,
		UpdateTimestamp:   now,
		UpdateActor:       callerUsername,
	}
	datastore.NormalizeRepositoryContract(repo)
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
	repo.UpdateTimestamp = s.clock.Now().UTC()
	repo.UpdateActor = callerUsername
	expectedResourceVersion := repo.ResourceVersion
	datastore.AdvanceRepositorySpecVersion(repo)
	if err := s.store.UpdateRepository(ctx, repo, expectedResourceVersion); err != nil {
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
	repo.UpdateTimestamp = s.clock.Now().UTC()
	repo.UpdateActor = callerUsername
	expectedResourceVersion := repo.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(repo)
	if err := s.store.UpdateRepository(ctx, repo, expectedResourceVersion); err != nil {
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
func (s *Service) DeleteRepository(ctx context.Context, repoID, _ string) error {
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return gqlerror.Errorf("repository not found")
		}
		return gqlerror.Errorf("failed to retrieve repository")
	}
	hasCatalogResources, err := s.store.HasCatalogResources(ctx, repoID)
	if err != nil {
		s.logger.Error("failed to check for existing catalog resources",
			zap.String("repo_id", repoID),
			zap.Error(err),
		)
		return gqlerror.Errorf("failed to delete repository")
	}
	if hasCatalogResources {
		s.logger.Info("repository deletion rejected: contains catalog resources",
			zap.String("repo_id", repoID),
		)
		return gqlerror.Errorf("repository %q contains catalog resources and cannot be deleted", repo.Name)
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

// ── ProductVariant ─────────────────────────────────────────────────────────

// GetProductVariants returns paginated ProductVariants for a namespace.
func (s *Service) GetProductVariants(ctx context.Context, namespace string, params datastore.PageParams) (*datastore.PageResult[datastore.ProductVariant], error) {
	result, err := s.store.ListProductVariants(ctx, namespace, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list product variants: %w", err)
	}
	return result, nil
}

// GetProductVariantByUID returns a ProductVariant by UID.
func (s *Service) GetProductVariantByUID(ctx context.Context, uid string) (*datastore.ProductVariant, error) {
	v, err := s.store.GetProductVariant(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("product variant not found: %s: %w", uid, err)
	}
	return v, nil
}

// GetProductVariantByName returns a ProductVariant by namespace/name.
func (s *Service) GetProductVariantByName(ctx context.Context, namespace, name string) (*datastore.ProductVariant, error) {
	v, err := s.store.GetProductVariantByName(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("product variant not found: %s/%s: %w", namespace, name, err)
	}
	return v, nil
}

// GetProductVariantsByProductRef returns all ProductVariants for a given product name in a namespace.
func (s *Service) GetProductVariantsByProductRef(ctx context.Context, namespace, productRefName string) ([]*datastore.ProductVariant, error) {
	return s.store.ListProductVariantsByProductRef(ctx, namespace, productRefName)
}

// Helper functions
func getStringOrEmpty(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
