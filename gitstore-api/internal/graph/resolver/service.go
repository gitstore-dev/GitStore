// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Service layer for GraphQL resolvers
// Handles CRUD operations via the datastore abstraction layer.

package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/admissionreport"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

var productDeletionOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gitstore_product_deletion_outcomes_total",
	Help: "Product foreground deletion outcomes at the admitted lifecycle boundary.",
}, []string{"outcome"})

func init() { prometheus.MustRegister(productDeletionOutcomes) }

// SystemRepositoryName is the well-known repository auto-provisioned for
// every namespace on creation (ADR-0002/ADR-0003). It is the authoring
// target for git-backed management of the namespace's own resources.
const SystemRepositoryName = "gitstore-system"

// Service provides business logic for GraphQL operations
type Service struct {
	store     datastore.Datastore
	gitWriter GitWriter
	logger    *zap.Logger
	clock     apiruntime.Clock
	ids       apiruntime.IDGenerator

	namespacePolicy   namespaceadmission.PolicyEvaluator
	namespaceMetrics  *namespaceadmission.Metrics
	committedAdmitter admission.CommittedManifestAdmitter
	manifestValidator admission.ManifestValidator
	pushLimits        config.PushLimitsConfig
}

// GitWriter is the write subset of gitclient.Client used by the Service.
// Defined here to keep the graph package testable without a real gRPC connection.
type GitWriter interface {
	CreateRepository(ctx context.Context, repositoryID, storageClass string) (storagePath string, err error)
	DeleteRepository(ctx context.Context, repositoryID string) error
	CommitFile(ctx context.Context, p gitclient.CommitFileParams) (string, error)
	CommitFileForRepo(ctx context.Context, repositoryID string, p gitclient.CommitFileParams) (string, error)
	ResolveRefForRepo(ctx context.Context, repositoryID, ref string) (string, error)
	// NotFound must mean confirmed path absence, never a missing repository/ref.
	ReadFileForRepo(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
	DeleteFile(ctx context.Context, p gitclient.DeleteFileParams) (string, error)
	DeleteFileForRepo(ctx context.Context, repositoryID string, p gitclient.DeleteFileParams) (string, error)
	CreateTag(ctx context.Context, p gitclient.CreateTagParams) (string, error)
}

// ServiceDeps contains dependencies for GraphQL business logic.
type ServiceDeps struct {
	Store                     datastore.Datastore
	GitWriter                 GitWriter
	Logger                    *zap.Logger
	Clock                     apiruntime.Clock
	IDGenerator               apiruntime.IDGenerator
	NamespacePolicyEvaluator  namespaceadmission.PolicyEvaluator
	NamespaceMetrics          *namespaceadmission.Metrics
	CommittedManifestAdmitter admission.CommittedManifestAdmitter
	// ManifestValidator runs push pre-receive checks for mutations. When nil,
	// the admitter is used if it also implements ManifestValidator.
	ManifestValidator admission.ManifestValidator
	PushLimits        config.PushLimitsConfig
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
	namespacePolicy := deps.NamespacePolicyEvaluator
	if namespacePolicy == nil {
		namespacePolicy = namespaceadmission.NewPolicyEvaluator(deps.Store)
	}
	namespaceMetrics := deps.NamespaceMetrics
	if namespaceMetrics == nil {
		namespaceMetrics = namespaceadmission.DefaultMetrics()
	}
	manifestValidator := deps.ManifestValidator
	if manifestValidator == nil {
		manifestValidator, _ = deps.CommittedManifestAdmitter.(admission.ManifestValidator)
	}
	return &Service{
		manifestValidator: manifestValidator,
		store:             deps.Store,
		gitWriter:         deps.GitWriter,
		logger:            deps.Logger,
		clock:             clock,
		ids:               ids,
		namespacePolicy:   namespacePolicy,
		namespaceMetrics:  namespaceMetrics,
		committedAdmitter: deps.CommittedManifestAdmitter,
		pushLimits:        deps.PushLimits,
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

// CommitProductManifest routes Product authoring through Git and the shared
// committed-admission boundary. GraphQL never writes Product desired state
// directly to the datastore.
func (s *Service) CommitProductManifest(ctx context.Context, apiVersion, kind string, metadata *model.ObjectMetaInput, spec *model.ProductSpecInput, body *string, caller string, create bool) (*datastore.Product, error) {
	const productAPIVersion, productKind, productRef = "catalog.gitstore.dev/v1beta1", "Product", "refs/heads/main"
	if metadata == nil || spec == nil || metadata.Name == "" || metadata.Namespace == "" || apiVersion != productAPIVersion || kind != productKind {
		return nil, admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{Reason: "INVALID_ENVELOPE", Message: "metadata.name, metadata.namespace, Product apiVersion/kind, and spec are required", Level: admission.LevelFailure}})
	}
	if s.gitWriter == nil || s.committedAdmitter == nil || s.manifestValidator == nil {
		return nil, fmt.Errorf("Product admission runtime is unavailable")
	}
	labels, err := stringMap(metadata.Labels)
	if err != nil {
		return nil, admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{Reason: "INVALID_FIELD", Message: err.Error(), Level: admission.LevelFailure}})
	}
	annotations, err := stringMap(metadata.Annotations)
	if err != nil {
		return nil, admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{Reason: "INVALID_FIELD", Message: err.Error(), Level: admission.LevelFailure}})
	}
	repositoryID, path := "", ""
	var existing *datastore.Product
	var lookupErr error
	if create {
		mapping, lookupErr := s.store.LookupRepository(ctx, metadata.Namespace, SystemRepositoryName)
		if lookupErr != nil {
			if errors.Is(lookupErr, datastore.ErrNotFound) {
				return nil, admission.NewError(admission.CodeNotFound, "NAMESPACE_NOT_FOUND", "namespace system repository is unavailable")
			}
			return nil, fmt.Errorf("look up namespace system repository: %w", lookupErr)
		}
		repositoryID, path = mapping.RepositoryID, fmt.Sprintf("products/%s.md", metadata.Name)
	} else {
		existing, lookupErr = s.store.GetProductByName(ctx, metadata.Namespace, metadata.Name)
		if lookupErr != nil {
			if errors.Is(lookupErr, datastore.ErrNotFound) {
				return nil, admission.NewError(admission.CodeNotFound, "PRODUCT_NOT_FOUND", "product not found")
			}
			return nil, fmt.Errorf("look up product: %w", lookupErr)
		}
		if existing.DeletionTimestamp != nil {
			return nil, admission.NewError(admission.CodeFailedPrecondition, "PRODUCT_TERMINATING", "product is terminating")
		}
		if err := guardOwnerAnnotationUnchanged(existing.Annotations, annotations); err != nil {
			return nil, admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{Reason: "IMMUTABLE_OWNER", Message: err.Error(), Level: admission.LevelFailure, File: existing.SourcePath, Field: "metadata.annotations"}})
		}
		repositoryID, path = existing.RepositoryID, existing.SourcePath
		if repositoryID == "" || path == "" {
			return nil, admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_UNAVAILABLE", "Product provenance is unavailable")
		}
		if existing.GitRef != "" && existing.GitRef != productRef {
			return nil, admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_UNAVAILABLE", "Product was not authored from the default branch")
		}
	}
	current, err := s.readManifestForWrite(ctx, repositoryID, path, productRef, create)
	if err != nil {
		return nil, fmt.Errorf("read current Product manifest: %w", err)
	}
	manifestBody := []byte{}
	if body != nil {
		manifestBody = []byte(*body)
	} else if !create {
		manifestBody = markdownBody(current)
	}
	content, err := renderManifest(map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": metadata.Name, "namespace": metadata.Namespace, "labels": labels, "annotations": annotations}, "spec": productManifestSpec(spec)}, manifestBody)
	if err != nil {
		return nil, admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{Reason: "INVALID_FIELD", Message: err.Error(), Level: admission.LevelFailure, File: path}})
	}
	verb, operation := "Update", admission.OperationUpdate
	if create {
		verb, operation = "Create", admission.OperationCreate
	}
	sha := ""
	if current != nil && bytes.Equal(current, content) {
		sha, err = s.gitWriter.ResolveRefForRepo(ctx, repositoryID, productRef)
		if err != nil {
			return nil, fmt.Errorf("resolve Product repository head: %w", err)
		}
		// ResolveRef and the original HEAD read are separate operations. Bind a
		// no-op admission to the file at the resolved SHA so a concurrent writer
		// cannot have its commit attributed to the bytes we read before it.
		resolved, readErr := s.gitWriter.ReadFileForRepo(ctx, repositoryID, path, sha)
		if readErr != nil {
			return nil, fmt.Errorf("read Product manifest at resolved commit: %w", readErr)
		}
		if !bytes.Equal(resolved, content) {
			return nil, committedAdmissionError(admission.ErrCommittedManifestSuperseded, sha, "Product")
		}
	} else {
		diagnostics, validateErr := s.manifestValidator.ValidateManifest(ctx, admission.ManifestValidationRequest{RepositoryID: repositoryID, Path: path, OldContent: current, NewContent: content})
		if validateErr != nil {
			return nil, fmt.Errorf("validate Product manifest: %w", validateErr)
		}
		if len(diagnostics) > 0 {
			cataloggrpc.ObserveAdmissionRejection("Product", admission.PhasePreReceive)
			return nil, admission.Rejected(admission.PhasePreReceive, "", diagnostics)
		}
		sha, err = s.gitWriter.CommitFileForRepo(ctx, repositoryID, gitclient.CommitFileParams{Path: path, Content: content, CommitMessage: fmt.Sprintf("%s Product %s", verb, metadata.Name), AuthorName: caller})
		if err != nil {
			return nil, fmt.Errorf("commit Product manifest: %w", err)
		}
	}
	result, err := s.committedAdmitter.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{RepositoryID: repositoryID, Namespace: metadata.Namespace, ActorSubject: caller, CommitSHA: sha, RefName: productRef, Path: path, Content: content, Operation: operation})
	if err != nil {
		return nil, committedAdmissionError(err, sha, "Product")
	}
	product, err := convergeCommittedResource(result, func() (*datastore.Product, string, error) {
		record, err := s.store.GetProductByName(ctx, metadata.Namespace, metadata.Name)
		if err != nil {
			return nil, "", err
		}
		return record, record.GitCommitSHA, nil
	})
	if err != nil {
		return nil, committedAdmissionError(err, sha, "Product")
	}
	admissionreport.Report(ctx, sha, result.Warnings)
	return product, nil
}

func (s *Service) DeleteProductManifest(ctx context.Context, uid, caller string) (*datastore.Product, bool, error) {
	product, err := s.store.GetProduct(ctx, uid)
	if err != nil {
		return nil, false, gqlerror.Errorf("product not found")
	}
	if product.DeletionTimestamp != nil {
		productDeletionOutcomes.WithLabelValues("ALREADY_TERMINATING").Inc()
		return product, false, nil
	}
	owners, ok := s.store.(datastore.OwnerReferenceStore)
	if !ok {
		return nil, false, gqlerror.Errorf("Product deletion is unavailable while owner-reference indexing is disabled")
	}
	blocked, err := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{
		Namespace: product.Namespace, RepositoryID: product.RepositoryID,
	}, product.UID)
	if err != nil {
		return nil, false, gqlerror.Errorf("check Product deletion blockers: %v", err)
	}
	if blocked {
		productDeletionOutcomes.WithLabelValues("BLOCKED").Inc()
		s.logger.Info("product deletion blocked", zap.String("namespace", product.Namespace), zap.String("name", product.Name), zap.String("actor", caller))
		return product, false, gqlerror.Errorf("Product %q still has blocking ProductVariants", product.Name)
	}
	if s.gitWriter == nil || s.committedAdmitter == nil {
		return nil, false, gqlerror.Errorf("Product admission runtime is unavailable")
	}
	sha, err := s.gitWriter.DeleteFileForRepo(ctx, product.RepositoryID, gitclient.DeleteFileParams{Path: product.SourcePath, CommitMessage: fmt.Sprintf("Delete Product %s", product.Name), AuthorName: caller})
	if err != nil {
		return nil, false, gqlerror.Errorf("failed to delete Product manifest: %v", err)
	}
	refName := product.GitRef
	if refName == "" {
		refName = "refs/heads/main"
	}
	if _, err := s.committedAdmitter.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{RepositoryID: product.RepositoryID, Namespace: product.Namespace, ActorSubject: caller, CommitSHA: sha, RefName: refName, Path: product.SourcePath, Operation: admission.OperationDelete, Kind: "Product", Name: product.Name}); err != nil {
		return nil, false, committedAdmissionError(err, sha, "Product")
	}
	productDeletionOutcomes.WithLabelValues("TERMINATION_STARTED").Inc()
	s.logger.Info("product deletion termination started", zap.String("namespace", product.Namespace), zap.String("name", product.Name), zap.String("actor", caller))
	updated, err := s.store.GetProduct(ctx, uid)
	if err != nil {
		return nil, false, gqlerror.Errorf("Product deletion admission did not retain terminating product")
	}
	return updated, true, nil
}

// CompleteProductDeletion repeats the blocker check at the controller-owned
// finalizer boundary so at-least-once reconciliation cannot orphan a variant.
func (s *Service) CompleteProductDeletion(ctx context.Context, namespace, name, expectedResourceVersion, expectedUID string) (*datastore.Product, error) {
	product, err := s.store.GetProductByName(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if expectedUID == "" || expectedResourceVersion == "" || name == "" || namespace == "" ||
		product.ResourceVersion != expectedResourceVersion || product.UID != expectedUID || product.Name != name || product.Namespace != namespace {
		return product, datastore.ErrConflict
	}
	if product.DeletionTimestamp == nil {
		return product, gqlerror.Errorf("Product %q is not terminating", name)
	}
	owners, ok := s.store.(datastore.OwnerReferenceStore)
	if !ok {
		return product, gqlerror.Errorf("Product deletion is unavailable while owner-reference indexing is disabled")
	}
	blocked, err := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{Namespace: product.Namespace, RepositoryID: product.RepositoryID}, product.UID)
	if err != nil {
		return product, err
	}
	if blocked {
		productDeletionOutcomes.WithLabelValues("BLOCKED_AT_COMPLETION").Inc()
		return product, gqlerror.Errorf("Product %q still has blocking ProductVariants", name)
	}
	lifecycle, ok := s.store.(datastore.ProductLifecycleStore)
	if !ok {
		return product, gqlerror.Errorf("Product lifecycle datastore is unavailable")
	}
	if err := lifecycle.CompleteProductDeletion(ctx, product.UID, expectedResourceVersion); err != nil {
		return product, err
	}
	productDeletionOutcomes.WithLabelValues("COMPLETED").Inc()
	s.logger.Info("product deletion completed", zap.String("namespace", product.Namespace), zap.String("name", product.Name))
	return product, nil
}

func productManifestSpec(spec *model.ProductSpecInput) map[string]any {
	result := map[string]any{"tags": spec.Tags, "title": spec.Title}
	if spec.CategoryRef != nil {
		result["categoryRef"] = productReferenceManifest(spec.CategoryRef)
	}
	media := make([]any, 0, len(spec.Media))
	for _, item := range spec.Media {
		if item != nil && item.FileRef != nil {
			media = append(media, map[string]any{"fileRef": map[string]any{"name": item.FileRef.Name, "kind": item.FileRef.Kind, "optional": item.FileRef.Optional}})
		}
	}
	result["media"] = media
	options := make([]any, 0, len(spec.Options))
	for _, item := range spec.Options {
		if item != nil {
			options = append(options, map[string]any{"name": item.Name, "title": item.Title, "values": item.Values})
		}
	}
	result["options"] = options
	state := "ACTIVE"
	if spec.Lifecycle != nil && spec.Lifecycle.State != nil {
		state = string(*spec.Lifecycle.State)
	}
	result["lifecycle"] = map[string]any{"state": state}
	return result
}

func productReferenceManifest(ref *model.CatalogObjectReferenceInput) map[string]any {
	return map[string]any{"apiVersion": ref.APIVersion, "kind": ref.Kind, "name": ref.Name, "namespace": ref.Namespace}
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

// CompleteCategoryDeletion finalizes a controller-observed foreground
// deletion. It repeats the blocking-dependent check immediately before the
// resource-version-guarded delete so a child-created race cannot orphan it.
func (s *Service) CompleteCategoryDeletion(ctx context.Context, namespace, name, expectedResourceVersion, expectedUID string) (*datastore.CategoryTaxonomy, error) {
	category, err := s.store.GetCategoryTaxonomyByName(ctx, namespace, name)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, gqlerror.Errorf("failed to retrieve category deletion state")
	}
	if expectedUID == "" || expectedResourceVersion == "" || name == "" || namespace == "" ||
		category.ResourceVersion != expectedResourceVersion || category.UID != expectedUID || category.Name != name || category.Namespace != namespace {
		return category, datastore.ErrConflict
	}
	if category.DeletionTimestamp == nil || !containsString(category.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer) {
		return nil, admission.NewError(admission.CodeFailedPrecondition, "CATEGORY_NOT_TERMINATING",
			fmt.Sprintf("category %q is not awaiting foreground deletion", name))
	}
	owners, ok := s.store.(datastore.OwnerReferenceStore)
	if !ok {
		return nil, gqlerror.Errorf("category deletion is unavailable while owner-reference indexing is disabled")
	}
	scope := datastore.OwnerReferenceScope{Namespace: category.Namespace, RepositoryID: category.RepositoryID}
	hasChildren, err := owners.HasBlockingOwnerDependents(ctx, scope, category.UID)
	if err != nil {
		return nil, gqlerror.Errorf("failed to recheck category deletion dependents")
	}
	if hasChildren {
		return category, admission.NewError(admission.CodeFailedPrecondition, "CHILD_CATEGORIES_PRESENT",
			fmt.Sprintf("category %q still has child categories", name))
	}
	products, err := owners.ListNonBlockingProductOwnerDependents(ctx, scope, category.UID, "", 1)
	if err != nil {
		return nil, gqlerror.Errorf("failed to recheck category Product dependents")
	}
	if len(products.Items) > 0 {
		return category, admission.NewError(admission.CodeFailedPrecondition, "PRODUCT_DECOUPLING_INCOMPLETE",
			fmt.Sprintf("category %q still has products to decouple", name))
	}
	lifecycle, ok := s.store.(datastore.CategoryTaxonomyDeletionStore)
	if !ok {
		return nil, gqlerror.Errorf("category deletion lifecycle is unavailable")
	}
	deleted, err := lifecycle.CompleteCategoryTaxonomyDeletion(ctx, namespace, name, expectedResourceVersion, expectedUID)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return category, err
	}
	return deleted, nil
}

// DecoupleCategoryProducts handles one bounded reverse-index page. Products
// retain their authored categoryRef while their resolved owner reference and
// CategoryResolved condition are updated atomically with the source record.
func (s *Service) DecoupleCategoryProducts(ctx context.Context, namespace, name, expectedResourceVersion string) ([]*datastore.Product, bool, error) {
	category, err := s.store.GetCategoryTaxonomyByName(ctx, namespace, name)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, gqlerror.Errorf("failed to retrieve category deletion state")
	}
	if category.DeletionTimestamp == nil {
		return nil, false, gqlerror.Errorf("category %q is not terminating", name)
	}
	if category.ResourceVersion != expectedResourceVersion {
		return nil, false, datastore.ErrConflict
	}
	owners, ok := s.store.(datastore.OwnerReferenceStore)
	if !ok {
		return nil, false, gqlerror.Errorf("category deletion is unavailable while owner-reference indexing is disabled")
	}
	page, err := owners.ListNonBlockingProductOwnerDependents(ctx, datastore.OwnerReferenceScope{
		Namespace: category.Namespace, RepositoryID: category.RepositoryID,
	}, category.UID, "", datastore.DefaultPageSize)
	if err != nil {
		return nil, false, gqlerror.Errorf("list category Product dependents: %v", err)
	}

	updated := make([]*datastore.Product, 0, len(page.Items))
	for _, dependent := range page.Items {
		product, err := s.store.GetProduct(ctx, dependent.DependentUID)
		if errors.Is(err, datastore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, gqlerror.Errorf("retrieve category Product dependent: %v", err)
		}
		// A Product update raced with the page read. Do not overwrite
		// Git-authored state; its current projection will be retried by a
		// later bounded continuation.
		if product.ResourceVersion != dependent.ResourceVersion {
			continue
		}
		var references []catalog.OwnerReference
		if len(product.OwnerReferences) > 0 {
			if err := json.Unmarshal(product.OwnerReferences, &references); err != nil {
				return nil, false, gqlerror.Errorf("decode Product owner references: %v", err)
			}
		}
		filtered := references[:0]
		for _, reference := range references {
			if reference.UID != category.UID {
				filtered = append(filtered, reference)
			}
		}
		product.OwnerReferences, err = json.Marshal(filtered)
		if err != nil {
			return nil, false, gqlerror.Errorf("encode Product owner references: %v", err)
		}

		var productStatus catalog.ProductStatus
		if len(product.Status) > 0 {
			if err := json.Unmarshal(product.Status, &productStatus); err != nil {
				return nil, false, gqlerror.Errorf("decode Product status: %v", err)
			}
		}
		productStatus.Conditions = mergeProductConditions(productStatus.Conditions, []catalog.Condition{{
			Type:               catalog.ConditionCategoryResolved,
			Status:             catalog.ConditionFalse,
			ObservedGeneration: product.Generation,
			LastTransitionTime: s.clock.Now().UTC(),
			Reason:             "CategoryDeleted",
			Message:            "Referenced category is being deleted.",
		}})
		product.Status, err = json.Marshal(productStatus)
		if err != nil {
			return nil, false, gqlerror.Errorf("encode Product status: %v", err)
		}
		datastore.AdvanceProductSystemVersion(product)
		if err := s.store.UpdateProduct(ctx, product); err != nil {
			return nil, false, gqlerror.Errorf("decouple Product %q: %v", product.Name, err)
		}
		updated = append(updated, product)
	}

	remaining, err := owners.ListNonBlockingProductOwnerDependents(ctx, datastore.OwnerReferenceScope{
		Namespace: category.Namespace, RepositoryID: category.RepositoryID,
	}, category.UID, "", 1)
	if err != nil {
		return nil, false, gqlerror.Errorf("check remaining category Product dependents: %v", err)
	}
	return updated, len(remaining.Items) > 0, nil
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
	started := time.Now()
	defer func() { s.namespaceMetrics.ObserveAdmissionStage("total", time.Since(started)) }()
	resource, err := namespaceResourceFromCreateInput(input, s.pushLimits)
	var content []byte
	if err == nil {
		content, err = validateNamespaceResource(resource)
	}
	s.namespaceMetrics.ObserveValidationDuration(namespaceadmission.StageStructural, time.Since(started))
	if err != nil {
		s.recordNamespaceGraphQLError("CREATE", namespaceInputName(input.Metadata), err)
		return nil, err
	}
	preflight, err := s.evaluateNamespacePolicy(ctx, resource, admission.OperationCreate)
	if err != nil {
		s.recordNamespaceGraphQLError("CREATE", resource.Metadata.Name, err)
		return nil, err
	}
	return s.commitAndAdmitNamespace(ctx, resource, content, callerUsername, true, preflight)
}

// CommitRepositoryManifest writes one Repository envelope and synchronously
// materializes that exact committed file through the shared admission runtime.
// The post-receive batch path uses the same runtime asynchronously.
func (s *Service) CommitRepositoryManifest(ctx context.Context, apiVersion, kind string, metadata *model.ObjectMetaInput, spec *model.RepositorySpecInput, callerUsername string, create bool) (*admission.CommittedManifestResult, error) {
	if apiVersion != repositoryAPIVersion || kind != repositoryKind || metadata == nil || spec == nil {
		return nil, gqlerror.Errorf("invalid Repository resource envelope")
	}
	if metadata.Name == "" || metadata.Namespace == "" || metadata.Name == SystemRepositoryName {
		return nil, gqlerror.Errorf("invalid Repository metadata")
	}
	if s.committedAdmitter == nil {
		return nil, gqlerror.Errorf("Repository admission runtime is unavailable")
	}
	if s.gitWriter == nil {
		return nil, gqlerror.Errorf("Repository Git writer is unavailable")
	}
	mapping, err := s.store.LookupRepository(ctx, metadata.Namespace, SystemRepositoryName)
	if err != nil {
		return nil, gqlerror.Errorf("namespace system repository is unavailable")
	}
	datastore.NormalizeNamespaceMappingContract(mapping)
	labels, err := stringMap(metadata.Labels)
	if err != nil {
		return nil, gqlerror.Errorf("metadata.labels: %v", err)
	}
	annotations, err := stringMap(metadata.Annotations)
	if err != nil {
		return nil, gqlerror.Errorf("metadata.annotations: %v", err)
	}
	resource := catalog.RepositoryResource{APIVersion: apiVersion, Kind: kind, Metadata: catalog.ObjectMeta{Name: metadata.Name, Namespace: metadata.Namespace, Labels: labels, Annotations: annotations}, Spec: catalog.RepositorySpec{Visibility: model.RepositoryVisibilityPrivate.String()}}
	if spec.DefaultBranch != nil {
		resource.Spec.DefaultBranch = *spec.DefaultBranch
	}
	if spec.Visibility != nil {
		resource.Spec.Visibility = spec.Visibility.String()
	}
	if spec.StorageClass != nil {
		resource.Spec.StorageClass = *spec.StorageClass
	}
	if err := s.preflightRepositoryManifestOperation(ctx, metadata.Namespace, metadata.Name, resource.Spec.StorageClass, annotations, create); err != nil {
		return nil, err
	}
	content, err := yaml.Marshal(resource)
	if err != nil {
		return nil, gqlerror.Errorf("encode Repository manifest: %v", err)
	}
	verb := "Update"
	if create {
		verb = "Create"
	}
	path := fmt.Sprintf("repositories/%s.md", metadata.Name)
	manifest := append(append([]byte("---\n"), content...), []byte("---\n")...)
	sha, err := s.gitWriter.CommitFileForRepo(ctx, mapping.RepositoryID, gitclient.CommitFileParams{Path: path, Content: manifest, CommitMessage: fmt.Sprintf("%s Repository %s", verb, metadata.Name), AuthorName: callerUsername})
	if err != nil {
		return nil, gqlerror.Errorf("failed to commit Repository manifest: %v", err)
	}
	op := admission.OperationUpdate
	if create {
		op = admission.OperationCreate
	}
	result, err := s.committedAdmitter.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
		RepositoryID: mapping.RepositoryID, Namespace: metadata.Namespace, ActorSubject: callerUsername,
		CommitSHA: sha, RefName: "refs/heads/main", Path: path, Content: manifest, Operation: op,
	})
	if err != nil {
		return nil, gqlerror.Errorf("Repository admission failed: %v", err)
	}
	return result, nil
}

func (s *Service) preflightRepositoryManifestOperation(ctx context.Context, namespace, name, proposedStorageClass string, annotations map[string]string, create bool) error {
	mapping, err := s.store.LookupRepository(ctx, namespace, name)
	if errors.Is(err, datastore.ErrNotFound) {
		if create {
			return nil
		}
		return gqlerror.Errorf("repository %q does not exist", name)
	}
	if err != nil {
		return gqlerror.Errorf("failed to validate Repository operation")
	}
	if create {
		return gqlerror.Errorf("repository %q already exists", name)
	}
	existing, err := s.store.GetRepository(ctx, mapping.RepositoryID)
	if err != nil {
		return gqlerror.Errorf("failed to validate Repository operation")
	}
	intent, err := admission.ReadDeletionIntent(existing.Status)
	if err != nil {
		return err
	}
	if existing.DeletionTimestamp != nil || intent != nil {
		return admission.NewError(admission.CodeFailedPrecondition, "REPOSITORY_TERMINATING", "repository deletion is pending")
	}
	if err := guardOwnerAnnotationUnchanged(existing.Annotations, annotations); err != nil {
		return err
	}
	if repositoryStorageClassDowngrade(existing.StorageClass, proposedStorageClass) {
		return gqlerror.Errorf("repository storageClass downgrade is not allowed")
	}
	return nil
}

func repositoryStorageClassDowngrade(current, proposed string) bool {
	ranks := map[string]int{"standard": 1, "premium": 2}
	currentRank, knownCurrent := ranks[strings.ToLower(current)]
	proposedRank, knownProposed := ranks[strings.ToLower(proposed)]
	return knownCurrent && knownProposed && proposedRank < currentRank
}

// UpdateNamespace commits and admits a replacement Namespace spec.
func (s *Service) UpdateNamespace(ctx context.Context, input model.UpdateNamespaceInput, callerUsername string) (*datastore.Namespace, error) {
	started := time.Now()
	defer func() { s.namespaceMetrics.ObserveAdmissionStage("total", time.Since(started)) }()
	resource, err := namespaceResourceFromUpdateInput(input, s.pushLimits)
	var content []byte
	if err == nil {
		content, err = validateNamespaceResource(resource)
	}
	s.namespaceMetrics.ObserveValidationDuration(namespaceadmission.StageStructural, time.Since(started))
	if err != nil {
		s.recordNamespaceGraphQLError("UPDATE", namespaceInputName(input.Metadata), err)
		return nil, err
	}
	// A missing Namespace is reported by evaluateNamespacePolicy below; any
	// other lookup failure fails closed so the owner-annotation guard cannot
	// be bypassed.
	existing, lookupErr := s.store.GetNamespaceByName(ctx, resource.Metadata.Name)
	switch {
	case lookupErr == nil:
		intent, err := admission.ReadDeletionIntent(existing.Status)
		if err != nil {
			return nil, err
		}
		if existing.DeletionTimestamp != nil || intent != nil {
			return nil, admissionMutationGraphQLError(admission.NewError(admission.CodeFailedPrecondition, "NAMESPACE_TERMINATING", "namespace deletion is pending"))
		}
		if err := guardOwnerAnnotationUnchanged(existing.Annotations, resource.Metadata.Annotations); err != nil {
			return nil, err
		}
	case !errors.Is(lookupErr, datastore.ErrNotFound):
		s.recordNamespaceGraphQLError("UPDATE", resource.Metadata.Name, lookupErr)
		return nil, gqlerror.Errorf("failed to validate Namespace operation")
	}
	preflight, err := s.evaluateNamespacePolicy(ctx, resource, admission.OperationUpdate)
	if err != nil {
		s.recordNamespaceGraphQLError("UPDATE", resource.Metadata.Name, err)
		return nil, err
	}
	return s.commitAndAdmitNamespace(ctx, resource, content, callerUsername, false, preflight)
}

// TransferNamespaceOwner reassigns a namespace's owner via a git commit that
// updates the reserved owner annotation (ADR-0010 §14). The two-condition
// transfer rule is authorized by the caller (GraphQLFieldAuthorizer) against
// authorized before this method runs; this method only rejects the transfer
// if the owner changed since that decision. It deliberately does not call
// guardOwnerAnnotationUnchanged, since changing the owner is exactly what
// this method exists to do.
func (s *Service) TransferNamespaceOwner(ctx context.Context, authorized *datastore.Namespace, targetOwnerSub, callerUsername string) (*datastore.Namespace, error) {
	ns, err := s.store.GetNamespace(ctx, authorized.UID)
	if err != nil {
		return nil, gqlerror.Errorf("namespace not found")
	}
	if ns.EffectiveOwnerSub() != authorized.EffectiveOwnerSub() {
		return nil, gqlerror.Errorf("namespace owner changed since authorization; retry the transfer")
	}
	var spec catalog.NamespaceSpec
	if len(ns.Spec) > 0 {
		if err := json.Unmarshal(ns.Spec, &spec); err != nil {
			return nil, gqlerror.Errorf("failed to decode current Namespace spec: %v", err)
		}
	}
	annotations := make(map[string]string, len(ns.Annotations)+1)
	for k, v := range ns.Annotations {
		annotations[k] = v
	}
	annotations[datastore.OwnerAnnotationKey] = targetOwnerSub
	resource := &catalog.NamespaceResource{
		APIVersion: ns.APIVersion,
		Kind:       ns.Kind,
		Metadata: catalog.ObjectMeta{
			Name:        ns.Name,
			Labels:      ns.Labels,
			Annotations: annotations,
		},
		Spec: spec,
	}
	content, err := validateNamespaceResource(resource)
	if err != nil {
		return nil, err
	}
	preflight, err := s.evaluateNamespacePolicy(ctx, resource, admission.OperationUpdate)
	if err != nil {
		return nil, err
	}
	return s.commitAndAdmitNamespace(ctx, resource, content, callerUsername, false, preflight)
}

func (s *Service) evaluateNamespacePolicy(
	ctx context.Context,
	resource *catalog.NamespaceResource,
	operation admission.Operation,
) (namespaceadmission.Preflight, error) {
	started := time.Now()
	tier, _ := namespaceadmission.TierFromManifest(resource.Spec.Tier)
	decision, preflight, err := s.namespacePolicy.Evaluate(ctx, namespaceadmission.PolicyCheck{
		Operation:        operation,
		Name:             resource.Metadata.Name,
		Tier:             tier,
		CapturePreflight: true,
	})
	s.namespaceMetrics.ObserveValidationDuration(namespaceadmission.StagePolicy, time.Since(started))
	if err != nil {
		return namespaceadmission.Preflight{}, gqlerror.Errorf("Namespace policy evaluation failed")
	}
	if decision == nil {
		if !preflight.Captured {
			return namespaceadmission.Preflight{}, gqlerror.Errorf("Namespace policy evaluation did not capture durable preflight state")
		}
		return preflight, nil
	}
	if decision.Reason == namespaceadmission.ReasonNamespaceNotFound {
		return namespaceadmission.Preflight{}, NewNamespaceNotFoundError(fmt.Sprintf("namespace %q not found", resource.Metadata.Name))
	}
	return namespaceadmission.Preflight{}, NewNamespacePolicyError(admission.PhasePreReceive, "", decision.Reason, decision.Message)
}

func validateNamespaceResource(resource *catalog.NamespaceResource) ([]byte, error) {
	return encodeNamespaceResource(resource, nil)
}

func encodeNamespaceResource(resource *catalog.NamespaceResource, body []byte) ([]byte, error) {
	yamlBody, err := yaml.Marshal(resource)
	if err != nil {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidEnvelope, "failed to encode Namespace manifest")
	}
	content := append([]byte("---\n"), yamlBody...)
	content = append(content, []byte("---\n")...)
	content = append(content, body...)
	if _, _, err := validate.NewParser().ParseResource(bytes.NewReader(content)); err != nil {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidEnvelope, fmt.Sprintf("Namespace manifest validation failed: %v", err))
	}
	return content, nil
}

func (s *Service) commitAndAdmitNamespace(
	ctx context.Context,
	resource *catalog.NamespaceResource,
	content []byte,
	callerUsername string,
	create bool,
	preflight namespaceadmission.Preflight,
) (*datastore.Namespace, error) {
	if s.gitWriter == nil {
		return nil, gqlerror.Errorf("namespace Git writer is unavailable")
	}
	systemNamespace, err := s.store.GetNamespaceByName(ctx, "gitstore-system")
	if err != nil {
		return nil, gqlerror.Errorf("bootstrap namespace gitstore-system is unavailable")
	}
	mapping, err := s.store.LookupRepository(ctx, systemNamespace.Name, SystemRepositoryName)
	if err != nil {
		s.logger.Error(
			"bootstrap repository lookup failed",
			zap.String("namespace", systemNamespace.Name),
			zap.String("repository", SystemRepositoryName),
			zap.Error(err),
		)
		return nil, gqlerror.Errorf("bootstrap repository gitstore-system/gitstore-system is unavailable")
	}
	datastore.NormalizeNamespaceMappingContract(mapping)

	verb := "Update"
	if create {
		verb = "Create"
	}
	path := fmt.Sprintf("namespaces/%s.md", resource.Metadata.Name)
	body := []byte(nil)
	if !create {
		var currentContent []byte
		var readErr error
		_, body, currentContent, readErr = s.readNamespaceAtCommit(
			ctx,
			mapping.RepositoryID,
			path,
			"refs/heads/main",
			resource.Metadata.Name,
		)
		if readErr != nil && status.Code(readErr) != codes.NotFound {
			return nil, gqlerror.Errorf("failed to read current Namespace manifest: %v", readErr)
		}
		if readErr == nil {
			body = namespaceMarkdownBody(currentContent, body)
		}
		content, err = encodeNamespaceResource(resource, body)
		if err != nil {
			return nil, err
		}
	}
	commitStarted := time.Now()
	sha, err := s.gitWriter.CommitFileForRepo(ctx, mapping.RepositoryID, gitclient.CommitFileParams{
		Path:          path,
		Content:       content,
		CommitMessage: fmt.Sprintf("%s Namespace %s", verb, resource.Metadata.Name),
		AuthorName:    callerUsername,
	})
	s.namespaceMetrics.ObserveAdmissionStage("git_commit", time.Since(commitStarted))
	if err != nil {
		return nil, gqlerror.Errorf("failed to commit Namespace manifest: %v", err)
	}
	convergenceStarted := time.Now()
	namespace, err := s.convergeCommittedNamespace(
		ctx,
		mapping.RepositoryID,
		path,
		resource,
		callerUsername,
		sha,
		content,
		namespaceAdmissionOperation(create),
	)
	s.namespaceMetrics.ObserveAdmissionStage("admission_convergence", time.Since(convergenceStarted))
	if err != nil {
		var mapped error
		switch {
		case errors.Is(err, namespaceadmission.ErrBootstrapNamespace):
			mapped = NewNamespacePolicyError(admission.PhasePostReceive, sha, namespaceadmission.ReasonBootstrapNamespace, fmt.Sprintf("bootstrap namespace %q is system-managed", resource.Metadata.Name))
		case errors.Is(err, namespaceadmission.ErrTierDemotion):
			mapped = NewNamespacePolicyError(admission.PhasePostReceive, sha, namespaceadmission.ReasonTierDemotion, "namespace tier demotion is not allowed")
		case errors.Is(err, namespaceadmission.ErrNamespaceTerminating):
			mapped = NewNamespacePolicyError(admission.PhasePostReceive, sha, namespaceadmission.ReasonNamespaceTerminating, fmt.Sprintf("namespace %q is terminating", resource.Metadata.Name))
		case errors.Is(err, namespaceadmission.ErrNamespaceAlreadyExists):
			mapped = NewNamespacePolicyError(admission.PhasePostReceive, sha, namespaceadmission.ReasonNamespaceAlreadyExists, fmt.Sprintf("namespace with identifier %q already exists", resource.Metadata.Name))
		case errors.Is(err, namespaceadmission.ErrNamespaceNotFound):
			mapped = NewNamespaceNotFoundError(fmt.Sprintf("namespace %q not found", resource.Metadata.Name))
		case errors.Is(err, namespaceadmission.ErrAuthoringRefCheck):
			return nil, gqlerror.Errorf("failed to verify Namespace commit: %v", err)
		case errors.Is(err, admission.ErrCommittedManifestSuperseded), errors.Is(err, namespaceadmission.ErrAuthoringRefSuperseded):
			mapped = NewNamespaceConflictError(namespaceadmission.ReasonSuperseded, "Namespace commit was superseded by a newer commit")
		case errors.Is(err, datastore.ErrConflict):
			mapped = NewNamespaceConflictError(namespaceadmission.ReasonResourceVersionConflict, fmt.Sprintf("namespace %q changed while the update was applied", resource.Metadata.Name))
		default:
			return nil, gqlerror.Errorf("Namespace admission failed: %v", err)
		}
		s.recordNamespaceGraphQLError(namespaceOperation(create), resource.Metadata.Name, mapped)
		return nil, mapped
	}
	return namespace, nil
}

func (s *Service) convergeCommittedNamespace(
	ctx context.Context,
	repositoryID, path string,
	resource *catalog.NamespaceResource,
	actor, committedSHA string,
	committedContent []byte,
	operation admission.Operation,
) (*datastore.Namespace, error) {
	if s.committedAdmitter == nil {
		return nil, gqlerror.Errorf("namespace admission runtime is unavailable")
	}
	// Policy preflight is still performed before the Git write above, so bad
	// requests never create a commit. Once committed, all materialization is
	// delegated to cataloggrpc's shared committed-manifest path. This prevents
	// GraphQL from retaining a second direct datastore admission implementation.
	result, err := s.committedAdmitter.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
		RepositoryID: repositoryID,
		Namespace:    "gitstore-system",
		ActorSubject: actor,
		CommitSHA:    committedSHA,
		RefName:      "refs/heads/main",
		Path:         path,
		Content:      committedContent,
		Operation:    operation,
	})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Kind != namespaceKind || result.Name != resource.Metadata.Name {
		return nil, fmt.Errorf("committed Namespace admission returned an unexpected result")
	}
	namespace, err := s.store.GetNamespaceByName(ctx, result.Name)
	if err != nil {
		return nil, err
	}
	if namespace.GitCommitSHA != result.CommitSHA {
		return nil, admission.ErrCommittedManifestSuperseded
	}
	return namespace, nil
}

func (s *Service) readNamespaceAtCommit(
	ctx context.Context,
	repositoryID, path, commitSHA, expectedName string,
) (*catalog.NamespaceResource, []byte, []byte, error) {
	content, err := s.gitWriter.ReadFileForRepo(ctx, repositoryID, path, commitSHA)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: read current Namespace manifest: %w", namespaceadmission.ErrAuthoringRefCheck, err)
	}
	parsed, body, err := validate.NewParser().ParseResource(bytes.NewReader(content))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: parse current Namespace manifest: %v", namespaceadmission.ErrAuthoringRefCheck, err)
	}
	if parsed == nil || parsed.Namespace == nil || parsed.Namespace.Metadata.Name != expectedName {
		return nil, nil, nil, fmt.Errorf("%w: current Namespace manifest at %s no longer declares %q", namespaceadmission.ErrAuthoringRefCheck, path, expectedName)
	}
	return parsed.Namespace, body, append([]byte(nil), content...), nil
}

func namespaceMarkdownBody(content, parsedBody []byte) []byte {
	if len(parsedBody) > 0 {
		return append([]byte(nil), parsedBody...)
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 || !bytes.Equal(bytes.TrimSpace(lines[0]), []byte("---")) {
		return nil
	}
	for index := 1; index < len(lines); index++ {
		if !bytes.Equal(bytes.TrimSpace(lines[index]), []byte("---")) {
			continue
		}
		return bytes.Join(lines[index+1:], []byte("\n"))
	}
	return nil
}

func namespaceInputName(metadata *model.NamespaceMetadataInput) string {
	if metadata == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(metadata.Name))
}

func namespaceOperation(create bool) string {
	if create {
		return string(admission.OperationCreate)
	}
	return string(admission.OperationUpdate)
}

func namespaceAdmissionOperation(create bool) admission.Operation {
	if create {
		return admission.OperationCreate
	}
	return admission.OperationUpdate
}

func (s *Service) recordNamespaceGraphQLError(operation, name string, err error) {
	var graphErr *gqlerror.Error
	if !errors.As(err, &graphErr) {
		return
	}
	code, _ := graphErr.Extensions["code"].(string)
	diagnostics, _ := graphErr.Extensions["diagnostics"].([]map[string]any)
	if code == "" || len(diagnostics) == 0 {
		return
	}
	reason, _ := diagnostics[0]["reason"].(string)
	if reason == "" {
		return
	}
	s.namespaceMetrics.ObserveRejection(namespaceadmission.Reason(reason))
	s.logger.Warn("Namespace mutation rejected",
		zap.String("operation", operation),
		zap.String("code", code),
		zap.String("reason", reason),
		zap.String("namespace", name),
		zap.Bool("conflict", code == string(admission.CodeConflict)))
}

func namespaceResourceFromCreateInput(input model.CreateNamespaceInput, pushLimits config.PushLimitsConfig) (*catalog.NamespaceResource, error) {
	return namespaceResourceFromInput(input.APIVersion, input.Kind, input.Metadata, input.Spec, pushLimits)
}

func namespaceResourceFromUpdateInput(input model.UpdateNamespaceInput, pushLimits config.PushLimitsConfig) (*catalog.NamespaceResource, error) {
	return namespaceResourceFromInput(input.APIVersion, input.Kind, input.Metadata, input.Spec, pushLimits)
}

func namespaceResourceFromInput(apiVersion, kind string, metadata *model.NamespaceMetadataInput, spec *model.NamespaceSpecInput, pushLimits config.PushLimitsConfig) (*catalog.NamespaceResource, error) {
	if apiVersion != namespaceAPIVersion {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidEnvelope, fmt.Sprintf("apiVersion must be %q", namespaceAPIVersion))
	}
	if kind != namespaceKind {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidEnvelope, fmt.Sprintf("kind must be %q", namespaceKind))
	}
	if metadata == nil || spec == nil {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidEnvelope, "metadata and spec are required")
	}
	identifier := strings.ToLower(strings.TrimSpace(metadata.Name))
	if err := namespaceadmission.ValidateIdentifier(identifier); err != nil {
		reason := namespaceadmission.ReasonInvalidIdentifier
		if errors.Is(err, namespaceadmission.ErrReservedIdentifier) {
			reason = namespaceadmission.ReasonReservedIdentifier
		}
		return nil, NewNamespaceStructuralError(reason, err.Error())
	}
	if !spec.Tier.IsValid() {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidTier, fmt.Sprintf("invalid namespace tier %q: must be USER or ORGANIZATION", spec.Tier))
	}
	labels, err := stringMap(metadata.Labels)
	if err != nil {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidEnvelope, fmt.Sprintf("metadata.labels: %v", err))
	}
	annotations, err := stringMap(metadata.Annotations)
	if err != nil {
		return nil, NewNamespaceStructuralError(namespaceadmission.ReasonInvalidEnvelope, fmt.Sprintf("metadata.annotations: %v", err))
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
		maxPackSizeBytes := int64OrZero(defaults.MaxPackSizeBytes)
		maxFileSizeBytes := int64OrZero(defaults.MaxFileSizeBytes)
		if maxPackSizeBytes > pushLimits.MaxPackSizeBytes {
			return nil, NewNamespaceStructuralError(namespaceadmission.ReasonPushPolicyExceedsCeiling, fmt.Sprintf("spec.pushPolicyDefaults.maxPackSizeBytes (%d) exceeds the platform ceiling of %d bytes (push_limits.max_pack_size)", maxPackSizeBytes, pushLimits.MaxPackSizeBytes))
		}
		if maxFileSizeBytes > pushLimits.MaxFileSizeBytes {
			return nil, NewNamespaceStructuralError(namespaceadmission.ReasonPushPolicyExceedsCeiling, fmt.Sprintf("spec.pushPolicyDefaults.maxFileSizeBytes (%d) exceeds the platform ceiling of %d bytes (push_limits.max_file_size)", maxFileSizeBytes, pushLimits.MaxFileSizeBytes))
		}
		resourceSpec.PushPolicyDefaults = &catalog.NamespacePushPolicyDefaults{
			MaxPackSizeBytes: maxPackSizeBytes,
			MaxFileSizeBytes: maxFileSizeBytes,
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
// repository exists for namespace, creating it if absent (FR-007). A
// repository that already exists — including one created by a concurrent or
// retried call — is treated as a successful idempotent outcome, never an
// error (FR-008).
func (s *Service) ProvisionSystemRepository(ctx context.Context, namespace, callerUsername string) error {
	namespaceName, err := s.canonicalNamespaceName(ctx, namespace)
	if err != nil {
		return err
	}
	current, err := s.store.GetNamespaceByName(ctx, namespaceName)
	if err != nil {
		return err
	}
	intent, err := admission.ReadDeletionIntent(current.Status)
	if err != nil {
		return err
	}
	if current.DeletionTimestamp != nil || intent != nil {
		return admission.NewError(admission.CodeFailedPrecondition, "NAMESPACE_TERMINATING", "namespace deletion is pending")
	}
	if _, err := s.store.LookupRepository(ctx, namespaceName, SystemRepositoryName); err == nil {
		s.logger.Info("system repository already provisioned",
			zap.String("namespace", namespaceName),
			zap.String("name", SystemRepositoryName),
		)
		return nil
	} else if !errors.Is(err, datastore.ErrNotFound) {
		return err
	}

	if _, err := s.CreateRepository(ctx, namespaceName, SystemRepositoryName, "", "default", callerUsername); err != nil {
		// A concurrent/retried provisioning attempt may have won the race
		// between the lookup above and this create — re-check before
		// surfacing the error.
		if _, lookupErr := s.store.LookupRepository(ctx, namespaceName, SystemRepositoryName); lookupErr == nil {
			s.logger.Info("system repository provisioned concurrently, treating as idempotent success",
				zap.String("namespace", namespaceName),
				zap.String("name", SystemRepositoryName),
			)
			return nil
		}
		return err
	}

	s.logger.Info("system repository provisioned",
		zap.String("namespace", namespaceName),
		zap.String("name", SystemRepositoryName),
	)
	return nil
}

func (s *Service) canonicalNamespaceName(ctx context.Context, namespace string) (string, error) {
	if namespace == "" {
		return "", gqlerror.Errorf("namespace is required")
	}
	if current, err := s.store.GetNamespaceByName(ctx, namespace); err == nil {
		return current.Name, nil
	} else if !errors.Is(err, datastore.ErrNotFound) {
		return "", gqlerror.Errorf("failed to retrieve namespace")
	}
	current, err := s.store.GetNamespace(ctx, namespace)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return "", gqlerror.Errorf("namespace %q not found", namespace)
		}
		return "", gqlerror.Errorf("failed to retrieve namespace")
	}
	return current.Name, nil
}

// effectivePushPolicy resolves the push-size limits a new Repository under
// namespaceName should carry: the namespace's pushPolicyDefaults when set
// (already validated at Namespace admission to be at or below the platform
// ceiling), falling back to the ceiling itself. There is no more "0 = no
// limit" — every Repository is created with a positive, enforceable limit.
func (s *Service) effectivePushPolicy(ctx context.Context, namespaceName string) (maxPackSizeBytes, maxFileSizeBytes int64, err error) {
	maxPackSizeBytes = s.pushLimits.MaxPackSizeBytes
	maxFileSizeBytes = s.pushLimits.MaxFileSizeBytes
	ns, nsErr := s.store.GetNamespaceByName(ctx, namespaceName)
	if nsErr != nil {
		if errors.Is(nsErr, datastore.ErrNotFound) {
			return maxPackSizeBytes, maxFileSizeBytes, nil
		}
		return 0, 0, nsErr
	}
	if len(ns.Spec) == 0 {
		return maxPackSizeBytes, maxFileSizeBytes, nil
	}
	var spec catalog.NamespaceSpec
	if jsonErr := json.Unmarshal(ns.Spec, &spec); jsonErr != nil || spec.PushPolicyDefaults == nil {
		return maxPackSizeBytes, maxFileSizeBytes, nil
	}
	if spec.PushPolicyDefaults.MaxPackSizeBytes > 0 {
		maxPackSizeBytes = spec.PushPolicyDefaults.MaxPackSizeBytes
	}
	if spec.PushPolicyDefaults.MaxFileSizeBytes > 0 {
		maxFileSizeBytes = spec.PushPolicyDefaults.MaxFileSizeBytes
	}
	return maxPackSizeBytes, maxFileSizeBytes, nil
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
	var (
		ns  *datastore.Namespace
		err error
	)
	if _, parseErr := uuid.Parse(id); parseErr == nil {
		ns, err = s.store.GetNamespace(ctx, id)
	} else {
		ns, err = s.store.GetNamespaceByName(ctx, id)
	}
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
func (s *Service) DeleteNamespace(ctx context.Context, ns *datastore.Namespace) (namespaceadmission.DeletionOutcome, error) {
	if ns == nil || namespaceUID(ns) == "" || ns.Name == "" {
		return "", gqlerror.Errorf("namespace deletion target is missing")
	}

	current, err := s.store.GetNamespaceByName(ctx, ns.Name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return "", gqlerror.Errorf("namespace %q not found", ns.Name)
		}
		return "", gqlerror.Errorf("failed to delete namespace")
	}
	if namespaceUID(current) != namespaceUID(ns) {
		return "", gqlerror.Errorf("namespace %q no longer refers to the requested resource", ns.Name)
	}
	if current.DeletionTimestamp != nil {
		outcome := namespaceadmission.DeletionOutcomeAlreadyTerminating
		s.namespaceMetrics.ObserveDeletionOutcome(outcome)
		s.logger.Info("Namespace deletion completed",
			zap.String("operation", "delete"),
			zap.String("namespace", current.Name),
			zap.String("outcome", string(outcome)),
			zap.Int("blocker_count", 0))
		return outcome, nil
	}

	blockers := make([]namespaceadmission.Reason, 0, 2)
	if namespaceadmission.IsBootstrap(current.Name) {
		blockers = append(blockers, namespaceadmission.ReasonBootstrapNamespace)
	}
	hasRepos, err := datastore.NamespaceDeletionBlocked(ctx, s.store, current)
	if err != nil {
		s.logger.Error("failed to check for existing repositories",
			zap.String("operation", "delete"),
			zap.String("namespace", current.Name),
			zap.Error(err),
		)
		return "", gqlerror.Errorf("failed to delete namespace")
	}
	if hasRepos {
		blockers = append(blockers, namespaceadmission.ReasonNamespaceNotEmpty)
	}
	blockers = namespaceadmission.OrderDeletionBlockers(blockers)
	if len(blockers) > 0 {
		reasons := make([]string, len(blockers))
		for i, blocker := range blockers {
			reasons[i] = string(blocker)
			s.namespaceMetrics.ObserveDeletionBlocked(blocker)
		}
		s.logger.Warn("Namespace deletion rejected",
			zap.String("operation", "delete"),
			zap.String("namespace", current.Name),
			zap.Strings("reasons", reasons),
			zap.Int("blocker_count", len(blockers)))
		return "", NewNamespaceDeletionBlockedError(blockers, fmt.Sprintf("namespace %q cannot be deleted", current.Name))
	}

	if err := s.deleteInfrastructureManifest(ctx, current, deletionActor(ctx)); err != nil {
		latest, getErr := s.store.GetNamespaceByName(ctx, current.Name)
		if getErr == nil && latest.UID == current.UID && latest.DeletionTimestamp != nil {
			s.namespaceMetrics.ObserveDeletionOutcome(namespaceadmission.DeletionOutcomeAlreadyTerminating)
			return namespaceadmission.DeletionOutcomeAlreadyTerminating, nil
		}
		if errors.Is(err, datastore.ErrNamespaceNotEmpty) {
			return "", NewNamespaceDeletionBlockedError([]namespaceadmission.Reason{namespaceadmission.ReasonNamespaceNotEmpty}, "namespace contains repositories or catalog resources")
		}
		if errors.Is(err, datastore.ErrConflict) {
			mapped := NewNamespaceConflictError(namespaceadmission.ReasonResourceVersionConflict, "namespace changed before deletion was admitted")
			s.recordNamespaceGraphQLError("DELETE", current.Name, mapped)
			return "", mapped
		}
		s.logger.Error("failed to admit namespace deletion", zap.String("namespace", current.Name), zap.Error(err))
		return "", admissionMutationGraphQLError(err)
	}
	outcome := namespaceadmission.DeletionOutcomeTerminationStarted
	s.namespaceMetrics.ObserveDeletionOutcome(outcome)
	s.logger.Info("Namespace deletion completed",
		zap.String("operation", "delete"),
		zap.String("namespace", current.Name),
		zap.String("outcome", string(outcome)),
		zap.Int("blocker_count", 0))
	return outcome, nil
}

func namespaceUID(ns *datastore.Namespace) string {
	if ns == nil {
		return ""
	}
	if ns.UID != "" {
		return ns.UID
	}
	return ns.ID
}

func (s *Service) CompleteNamespaceDeletion(ctx context.Context, name, expectedResourceVersion, expectedUID string) (*datastore.Namespace, error) {
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
	if expectedUID == "" || expectedResourceVersion == "" || name == "" ||
		current.ResourceVersion != expectedResourceVersion || current.UID != expectedUID || current.Name != name {
		return current, datastore.ErrConflict
	}
	intent, err := admission.ReadDeletionIntent(current.Status)
	if err != nil {
		return current, err
	}
	if current.DeletionTimestamp == nil && intent != nil {
		if err := s.deleteInfrastructureManifest(ctx, current, intent.Actor); err != nil {
			return current, err
		}
		return current, datastore.ErrConflict
	}
	if current.DeletionTimestamp == nil ||
		!containsString(current.Finalizers, datastore.NamespaceForegroundDeletionFinalizer) {
		return nil, gqlerror.Errorf("namespace %q is not awaiting foreground deletion", name)
	}
	if err := s.verifyInfrastructureRemoval(ctx, current.Status, current.UID); err != nil {
		return current, err
	}
	if err := s.finalizeNamespaceSystemRepository(ctx, current); err != nil {
		return current, err
	}
	hasRepos, err := s.store.HasRepositories(ctx, current.Name)
	if err != nil {
		return nil, gqlerror.Errorf("failed to complete namespace deletion")
	}
	if hasRepos {
		return nil, gqlerror.Errorf("namespace %q still contains repositories", name)
	}
	if len(current.Finalizers) != 1 {
		return current, gqlerror.Errorf("namespace has other finalizers")
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
func (s *Service) CreateRepository(ctx context.Context, namespace, name, defaultBranch, storageClass, callerUsername string) (*datastore.Repository, error) {
	namespaceName, err := s.canonicalNamespaceName(ctx, namespace)
	if err != nil {
		return nil, err
	}
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
	maxPackSizeBytes, maxFileSizeBytes, err := s.effectivePushPolicy(ctx, namespaceName)
	if err != nil {
		return nil, gqlerror.Errorf("failed to resolve namespace push policy")
	}
	now := s.clock.Now().UTC()
	spec, err := json.Marshal(&model.RepositorySpec{
		DefaultBranch: defaultBranch,
		Visibility:    model.RepositoryVisibilityPrivate,
		PushPolicy: &model.RepositoryPushPolicy{
			MaxPackSizeBytes: maxPackSizeBytes,
			MaxFileSizeBytes: maxFileSizeBytes,
		},
	})
	if err != nil {
		return nil, gqlerror.Errorf("failed to encode repository spec")
	}
	repo := &datastore.Repository{
		APIVersion:        repositoryAPIVersion,
		Kind:              repositoryKind,
		UID:               repoID,
		Namespace:         namespaceName,
		Name:              name,
		RepositoryID:      repoID,
		Labels:            map[string]string{},
		Annotations:       map[string]string{},
		OwnerReferences:   json.RawMessage(`[]`),
		Finalizers:        []string{},
		Spec:              spec,
		Body:              "",
		DefaultBranch:     defaultBranch,
		StorageClass:      storageClass,
		MaxPackSizeBytes:  maxPackSizeBytes,
		MaxFileSizeBytes:  maxFileSizeBytes,
		CreationTimestamp: now,
		CreationActor:     callerUsername,
		UpdateTimestamp:   now,
		UpdateActor:       callerUsername,
	}
	datastore.NormalizeRepositoryContract(repo)
	if err := s.store.CreateRepositoryInActiveNamespace(ctx, repo); err != nil {
		if errors.Is(err, datastore.ErrAlreadyExists) {
			return nil, gqlerror.Errorf("repository already exists")
		}
		if errors.Is(err, datastore.ErrNamespaceNotActive) {
			return nil, gqlerror.Errorf("namespace %q is terminating", namespaceName)
		}
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, gqlerror.Errorf("namespace %q not found", namespaceName)
		}
		s.logger.Error("failed to create repository", zap.String("repo_id", repo.UID), zap.Error(err))
		return nil, gqlerror.Errorf("failed to create repository")
	}
	if err := s.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace:    namespaceName,
		Name:         name,
		RepositoryID: repo.UID,
	}); err != nil {
		// Roll back the repository row so it does not orphan a name slot.
		if delErr := s.store.DeleteRepository(ctx, repo.UID); delErr != nil {
			s.logger.Error("rollback DeleteRepository failed after mapping create failure",
				zap.String("repo_id", repo.UID), zap.Error(delErr))
		}
		if errors.Is(err, datastore.ErrAlreadyExists) {
			return nil, gqlerror.Errorf("repository already exists")
		}
		s.logger.Error("failed to create namespace mapping", zap.String("repo_id", repo.UID), zap.Error(err))
		return nil, gqlerror.Errorf("failed to create namespace mapping")
	}
	s.logger.Info("lookup repository",
		zap.String("namespace", namespaceName),
		zap.String("name", name),
		zap.String("repo_id", repo.UID),
	)
	if s.gitWriter != nil {
		if _, err := s.gitWriter.CreateRepository(ctx, repo.UID, storageClass); err != nil {
			s.logger.Error("gRPC CreateRepository failed",
				zap.String("repo_id", repo.UID),
				zap.String("rpc", "CreateRepository"),
				zap.Error(err),
			)
			// Compensate: drop both metadata rows so a retry can re-create
			// cleanly instead of resolving a name with no backing storage.
			if delErr := s.store.DeleteNamespaceMapping(ctx, namespaceName, name); delErr != nil {
				s.logger.Error("rollback DeleteNamespaceMapping failed after storage provision failure",
					zap.String("repo_id", repo.UID), zap.Error(delErr))
			}
			if delErr := s.store.DeleteRepository(ctx, repo.UID); delErr != nil {
				s.logger.Error("rollback DeleteRepository failed after storage provision failure",
					zap.String("repo_id", repo.UID), zap.Error(delErr))
			}
			return nil, gqlerror.Errorf("failed to provision repository storage")
		}
		s.logger.Info("gRPC CreateRepository succeeded",
			zap.String("repo_id", repo.UID),
			zap.String("rpc", "CreateRepository"),
		)
	}
	return repo, nil
}

// ProvisionRepositoryStorage is the controller-only bridge to git-service for
// a Repository that has already been admitted. It deliberately never creates
// Repository metadata or namespace mappings: admission remains the sole
// authoring path, and git-service's create operation is idempotent on retries.
func (s *Service) ProvisionRepositoryStorage(ctx context.Context, namespace, name string) (*datastore.Repository, error) {
	namespaceName, err := s.canonicalNamespaceName(ctx, namespace)
	if err != nil {
		return nil, err
	}
	mapping, err := s.store.LookupRepository(ctx, namespaceName, name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, gqlerror.Errorf("repository not found")
		}
		return nil, gqlerror.Errorf("failed to retrieve repository")
	}
	repository, err := s.store.GetRepository(ctx, mapping.RepositoryID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, gqlerror.Errorf("repository not found")
		}
		return nil, gqlerror.Errorf("failed to retrieve repository")
	}
	datastore.NormalizeRepositoryContract(repository)
	if repository.Name == SystemRepositoryName {
		return nil, gqlerror.Errorf("repository %q is system-managed", SystemRepositoryName)
	}
	if repository.DeletionTimestamp != nil {
		return nil, gqlerror.Errorf("repository %q is terminating", repository.Name)
	}
	intent, err := admission.ReadDeletionIntent(repository.Status)
	if err != nil {
		return nil, err
	}
	if intent != nil {
		return nil, gqlerror.Errorf("repository %q deletion is pending", repository.Name)
	}
	if !repositoryAdmissionAccepted(repository.Status) {
		return nil, gqlerror.Errorf("repository %q has not been admitted", repository.Name)
	}
	if s.gitWriter == nil {
		return nil, gqlerror.Errorf("repository storage provisioning is unavailable")
	}
	if _, err := s.gitWriter.CreateRepository(ctx, repository.UID, repository.StorageClass); err != nil && status.Code(err) != codes.AlreadyExists {
		s.logger.Error("gRPC CreateRepository failed",
			zap.String("repo_id", repository.UID),
			zap.String("rpc", "CreateRepository"),
			zap.Error(err),
		)
		return nil, gqlerror.Errorf("failed to provision repository storage")
	}
	s.logger.Info("gRPC CreateRepository succeeded",
		zap.String("repo_id", repository.UID),
		zap.String("rpc", "CreateRepository"),
	)
	return repository, nil
}

func repositoryAdmissionAccepted(raw json.RawMessage) bool {
	var status catalog.RepositoryStatus
	if len(raw) == 0 || json.Unmarshal(raw, &status) != nil {
		return false
	}
	for _, condition := range status.Conditions {
		if condition.Type == catalog.ConditionAdmissionAccepted && condition.Status == catalog.ConditionTrue {
			return true
		}
	}
	return false
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
	datastore.NormalizeRepositoryContract(r)
	return r, nil
}

// LookupRepository resolves (namespace, name) → NamespaceMapping.
func (s *Service) LookupRepository(ctx context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
	m, err := s.store.LookupRepository(ctx, namespace, name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			s.logger.Info("lookup repository not found",
				zap.String("namespace", namespace),
				zap.String("name", name),
			)
			return nil, datastore.ErrNotFound
		}
		return nil, gqlerror.Errorf("failed to lookup repository")
	}
	datastore.NormalizeNamespaceMappingContract(m)
	s.logger.Info("lookup repository",
		zap.String("namespace", namespace),
		zap.String("name", name),
		zap.String("repo_id", m.RepositoryID),
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
	datastore.NormalizeNamespaceMappingContract(m)
	return m, nil
}

// ListRepositoriesByNamespace lists paginated repositories in a namespace.
func (s *Service) ListRepositoriesByNamespace(ctx context.Context, namespace string, params datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	result, err := s.store.ListRepositoriesByNamespace(ctx, namespace, params)
	if err != nil {
		return nil, gqlerror.Errorf("failed to list repositories")
	}
	return result, nil
}

// ListRepositories lists repositories globally when the datastore implements
// the bounded global Repository access pattern.
func (s *Service) ListRepositories(ctx context.Context, params datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	lister, ok := s.store.(datastore.GlobalRepositoryLister)
	if !ok {
		return nil, gqlerror.Errorf("global repository listing is not supported")
	}
	result, err := lister.ListRepositories(ctx, params)
	if err != nil {
		return nil, gqlerror.Errorf("failed to list repositories")
	}
	return result, nil
}

// DeleteRepository starts foreground deletion. The Repository remains visible
// and resolvable while its controller removes storage; it is hard-deleted only
// after the foreground finalizer has been cleared.
func (s *Service) DeleteRepository(ctx context.Context, repoID, caller string) error {
	_, _, err := s.deleteRepositoryWithOutcome(ctx, repoID, caller)
	return err
}

// deleteRepositoryWithOutcome returns the persisted Repository and whether this
// request, rather than a concurrent request, started foreground termination.
func (s *Service) deleteRepositoryWithOutcome(ctx context.Context, repoID, caller string) (*datastore.Repository, bool, error) {
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, false, gqlerror.Errorf("repository not found")
		}
		return nil, false, gqlerror.Errorf("failed to retrieve repository")
	}
	datastore.NormalizeRepositoryContract(repo)
	if repo.Name == SystemRepositoryName {
		return nil, false, admission.NewError(admission.CodeFailedPrecondition, "SYSTEM_REPOSITORY", "system repository is removed only during Namespace finalization")
	}
	if repo.DeletionTimestamp != nil {
		// A repeated delete is deliberately a no-op. In particular, do not
		// repeat a drain check after termination has already been accepted.
		return repo, false, nil
	}
	hasCatalogResources, err := s.store.HasCatalogResources(ctx, repoID)
	if err != nil {
		s.logger.Error("failed to check for existing catalog resources",
			zap.String("repo_id", repoID),
			zap.Error(err),
		)
		return nil, false, gqlerror.Errorf("failed to delete repository")
	}
	if hasCatalogResources {
		s.logger.Info("repository deletion rejected: contains catalog resources",
			zap.String("repo_id", repoID),
		)
		return nil, false, gqlerror.Errorf("repository %q contains catalog resources and cannot be deleted", repo.Name)
	}

	if err := s.deleteInfrastructureManifest(ctx, repo, caller); err != nil {
		latest, getErr := s.store.GetRepository(ctx, repo.UID)
		if getErr == nil && latest.UID == repo.UID && latest.DeletionTimestamp != nil {
			return latest, false, nil
		}
		if errors.Is(err, datastore.ErrConflict) {
			return nil, false, statusConflictError("Repository", repo.Namespace, repo.Name, repo.ResourceVersion)
		}
		s.logger.Error("failed to admit repository deletion", zap.String("uid", repo.UID), zap.Error(err))
		return nil, false, admissionMutationGraphQLError(err)
	}
	updated, err := s.store.GetRepository(ctx, repo.UID)
	return updated, true, err
}

// CompleteRepositoryDeletion is the controller-only finalizer completion
// operation. It proves the resource is terminating and drained, removes the
// backing storage, clears this controller's finalizer, and garbage-collects
// the row only when no finalizer remains. Catalog resources are never
// cascaded.
func (s *Service) CompleteRepositoryDeletion(ctx context.Context, namespace, name, expectedResourceVersion, expectedUID string) (*datastore.Repository, error) {
	if expectedUID == "" || expectedResourceVersion == "" || name == "" || namespace == "" {
		return nil, datastore.ErrConflict
	}
	repositoryID := expectedUID
	mapping, err := s.store.LookupRepository(ctx, namespace, name)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, gqlerror.Errorf("failed to complete repository deletion")
	}
	if mapping != nil {
		repositoryID = mapping.RepositoryID
	}
	// A cleanup attempt may have removed the path before the authoritative row.
	// The controller's UID still identifies that exact terminating incarnation.
	repo, err := s.store.GetRepository(ctx, repositoryID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			if mapping == nil {
				return nil, nil
			}
			return nil, gqlerror.Errorf("repository mapping remains without its resource; reconciliation required")
		}
		return nil, gqlerror.Errorf("failed to complete repository deletion")
	}
	datastore.NormalizeRepositoryContract(repo)
	if repo.ResourceVersion != expectedResourceVersion || repo.UID != expectedUID || repo.Namespace != namespace || repo.Name != name {
		return repo, datastore.ErrConflict
	}
	intent, err := admission.ReadDeletionIntent(repo.Status)
	if err != nil {
		return repo, err
	}
	if repo.DeletionTimestamp == nil && intent != nil {
		if err := s.deleteInfrastructureManifest(ctx, repo, intent.Actor); err != nil {
			return repo, err
		}
		return repo, datastore.ErrConflict
	}
	if repo.Name == SystemRepositoryName {
		parent, err := s.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return repo, err
		}
		if parent.DeletionTimestamp == nil {
			return repo, gqlerror.Errorf("system repository cleanup requires terminating namespace")
		}
	} else if err := s.verifyInfrastructureRemoval(ctx, repo.Status, repo.UID); err != nil {
		return repo, err
	}
	if repo.DeletionTimestamp == nil || !containsString(repo.Finalizers, datastore.RepositoryForegroundDeletionFinalizer) {
		return nil, gqlerror.Errorf("repository %q is not awaiting foreground deletion", name)
	}
	hasCatalogResources, err := s.store.HasCatalogResources(ctx, repo.UID)
	if err != nil {
		return nil, gqlerror.Errorf("failed to complete repository deletion")
	}
	if hasCatalogResources {
		return nil, gqlerror.Errorf("repository %q still contains catalog resources", name)
	}
	if len(repo.Finalizers) != 1 {
		return repo, gqlerror.Errorf("repository has other finalizers")
	}
	lifecycle, ok := s.store.(datastore.RepositoryDeletionStore)
	if !ok || s.gitWriter == nil {
		return repo, gqlerror.Errorf("repository deletion runtime is unavailable")
	}
	if err := s.gitWriter.DeleteRepository(ctx, repo.UID); err != nil && status.Code(err) != codes.NotFound {
		s.logger.Error("gRPC DeleteRepository failed during finalizer completion", zap.String("repo_id", repo.UID), zap.Error(err))
		return nil, gqlerror.Errorf("failed to delete repository storage")
	}
	if err := lifecycle.CompleteRepositoryDeletion(ctx, repo.UID, expectedResourceVersion); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			latest, reloadErr := s.store.GetRepository(ctx, repo.UID)
			if reloadErr == nil {
				return latest, datastore.ErrConflict
			}
		}
		return nil, fmt.Errorf("complete repository deletion: %w", err)
	}
	return repo, nil
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
