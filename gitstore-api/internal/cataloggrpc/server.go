// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package cataloggrpc implements the CatalogService gRPC server for the
// gitstore-api. It handles ValidateResources (blocking pre-receive validation)
// and AdmitResources (fire-and-forget post-receive catalog storage).
package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	admcatalog "github.com/gitstore-dev/gitstore/api/internal/admission/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/google/cel-go/cel"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// GitReader is the read subset of gitclient.Client used by AdmitResources.
// Abstracted here so it can be mocked in tests.
type GitReader interface {
	ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error)
	ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
	ResolveRef(ctx context.Context, repositoryID, ref string) (string, error)
}

// ResourceParser is the parser behavior required by the CatalogService server.
type ResourceParser interface {
	ParseResource(r io.Reader) (*validate.ParsedResource, []byte, error)
}

// gitClientReader wraps *gitclient.Client to satisfy GitReader.
// Each method passes repositoryID directly to the gRPC request instead of
// mutating the shared Client.RepositoryID field, making it safe for concurrent
// AdmitResources calls targeting different repositories.
type gitClientReader struct{ c *gitclient.Client }

func (r *gitClientReader) ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error) {
	entries, err := r.c.ListFilesForRepo(ctx, repositoryID, prefix, ref)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return paths, nil
}

func (r *gitClientReader) ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
	return r.c.ReadFileForRepo(ctx, repositoryID, path, ref)
}

func (r *gitClientReader) ResolveRef(ctx context.Context, repositoryID, ref string) (string, error) {
	return r.c.ResolveRefForRepo(ctx, repositoryID, ref)
}

// Server implements catalogv1.CatalogServiceServer.
type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
	store datastore.Datastore
	git   GitReader
	log   *zap.Logger

	parser ResourceParser
	clock  apiruntime.Clock
	ids    apiruntime.IDGenerator
	celEnv *cel.Env // shared, constructed once; nil means CEL unavailable (skip rather than reject)
	chain  *admission.Chain

	namespacePolicy  namespaceadmission.PolicyEvaluator
	namespaceMetrics *namespaceadmission.Metrics
}

// ServerDeps contains dependencies for the CatalogService gRPC server.
type ServerDeps struct {
	Store                    datastore.Datastore
	GitReader                GitReader
	GitClient                *gitclient.Client
	Logger                   *zap.Logger
	Parser                   ResourceParser
	Clock                    apiruntime.Clock
	IDGenerator              apiruntime.IDGenerator
	CELEnv                   *cel.Env
	ExtraValidatingPolicies  []admission.ValidatingAdmissionPolicy
	NamespacePolicyEvaluator namespaceadmission.PolicyEvaluator
	NamespaceMetrics         *namespaceadmission.Metrics
}

// newCELEnv constructs a CEL environment for syntax-checking price eligibility expressions.
// Returns nil if the environment cannot be created (CEL unavailable); callers must tolerate nil.
func newCELEnv() *cel.Env {
	env, err := cel.NewEnv()
	if err != nil {
		return nil
	}
	return env
}

// NewServer creates a new CatalogService gRPC server.
func NewServer(deps ServerDeps) (*Server, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("cataloggrpc: datastore is required")
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("cataloggrpc: logger is required")
	}
	git := deps.GitReader
	if git == nil && deps.GitClient != nil {
		git = &gitClientReader{c: deps.GitClient}
	}
	parser := deps.Parser
	if parser == nil {
		parser = validate.NewParser()
	}
	clock := deps.Clock
	if clock == nil {
		clock = apiruntime.SystemClock{}
	}
	ids := deps.IDGenerator
	if ids == nil {
		ids = apiruntime.UUIDGenerator{}
	}
	celEnv := deps.CELEnv
	if celEnv == nil {
		celEnv = newCELEnv()
	}
	chain := admission.NewChain(deps.Logger)
	chain.RegisterValidatingPolicy(admcatalog.NewProductValidatingPolicy(deps.Logger, deps.Store))
	chain.RegisterValidatingPolicy(admcatalog.NewCollectionValidatingPolicy(deps.Logger))
	chain.RegisterValidatingPolicy(admcatalog.NewProductVariantValidatingPolicy(deps.Store, celEnv, deps.Logger))
	chain.RegisterValidatingPolicy(admcatalog.NewCategoryTaxonomyValidatingPolicy(deps.Store, deps.Logger))
	for _, p := range deps.ExtraValidatingPolicies {
		chain.RegisterValidatingPolicy(p)
	}
	namespacePolicy := deps.NamespacePolicyEvaluator
	if namespacePolicy == nil {
		namespacePolicy = namespaceadmission.NewPolicyEvaluator(deps.Store)
	}
	namespaceMetrics := deps.NamespaceMetrics
	if namespaceMetrics == nil {
		namespaceMetrics = namespaceadmission.DefaultMetrics()
	}
	return &Server{
		store:            deps.Store,
		git:              git,
		log:              deps.Logger,
		parser:           parser,
		clock:            clock,
		ids:              ids,
		celEnv:           celEnv,
		chain:            chain,
		namespacePolicy:  namespacePolicy,
		namespaceMetrics: namespaceMetrics,
	}, nil
}

func (s *Server) newUID(kind, name string) (string, bool) {
	uid, err := s.ids.NewV7ID()
	if err != nil {
		s.log.Error("admit_resources: generate UID failed",
			zap.String("kind", kind),
			zap.String("name", name),
			zap.Error(err))
		return "", false
	}
	return uid, true
}

// ValidateResources validates resource blobs extracted from an incoming push commit.
// Called blocking in the pre-receive phase. Returns all violations across all blobs.
func (s *Server) ValidateResources(
	ctx context.Context,
	req *catalogv1.ValidateResourcesRequest,
) (*catalogv1.ValidateResourcesResponse, error) {
	structuralStarted := time.Now()
	var allErrors []*catalogv1.ValidationError
	if len(req.GetTrees()) == 0 {
		return nil, grpcstatus.Error(codes.InvalidArgument, "validation trees are required")
	}
	for _, tree := range req.GetTrees() {
		allErrors = append(allErrors, s.validateResourceBlobs(ctx, req.RepositoryId, tree.GetProposedBlobs())...)
		allErrors = append(allErrors, s.validateImmutableResourceChanges(tree.GetOldBlobs(), tree.GetProposedBlobs())...)
		deletionErrors, err := s.validateRepositoryDeletions(ctx, req.RepositoryId, tree.GetOldBlobs(), tree.GetProposedBlobs())
		if err != nil {
			return nil, grpcstatus.Error(codes.Unavailable, "repository deletion validation unavailable")
		}
		allErrors = append(allErrors, deletionErrors...)
	}
	s.namespaceMetrics.ObserveValidationDuration(namespaceadmission.StageStructural, time.Since(structuralStarted))

	if len(allErrors) > 0 {
		return &catalogv1.ValidateResourcesResponse{
			Accepted: false,
			Errors:   allErrors,
		}, nil
	}

	policyStarted := time.Now()
	policyErrors, evaluated, err := s.validateNamespacePolicies(ctx, req)
	if evaluated {
		s.namespaceMetrics.ObserveValidationDuration(namespaceadmission.StagePolicy, time.Since(policyStarted))
	}
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Internal, "namespace policy evaluation failed")
	}
	if len(policyErrors) > 0 {
		for _, validationErr := range policyErrors {
			s.namespaceMetrics.ObserveRejection(namespacePolicyReason(validationErr.GetConstraint()))
		}
		return &catalogv1.ValidateResourcesResponse{Accepted: false, Errors: policyErrors}, nil
	}
	return &catalogv1.ValidateResourcesResponse{Accepted: true}, nil
}

func (s *Server) validateNamespacePolicies(
	ctx context.Context,
	req *catalogv1.ValidateResourcesRequest,
) ([]*catalogv1.ValidationError, bool, error) {
	type candidate struct {
		path      string
		name      string
		tier      datastore.NamespaceTier
		operation admission.Operation
	}
	var candidates []candidate
	appendCandidates := func(oldBlobs, proposedBlobs []*catalogv1.ResourceBlob, correlateOperation bool) {
		oldNamesByPath := make(map[string]string, len(oldBlobs))
		for _, blob := range oldBlobs {
			parsed, _, err := s.parser.ParseResource(bytes.NewReader(blob.GetContent()))
			if err == nil && parsed != nil && parsed.Namespace != nil {
				oldNamesByPath[blob.GetPath()] = parsed.Namespace.Metadata.Name
			}
		}
		for _, blob := range proposedBlobs {
			parsed, _, err := s.parser.ParseResource(bytes.NewReader(blob.GetContent()))
			if err != nil || parsed == nil || parsed.Namespace == nil {
				continue
			}
			name := parsed.Namespace.Metadata.Name
			operation := admission.Operation("UPSERT")
			if correlateOperation {
				operation = admission.OperationCreate
				if oldNamesByPath[blob.GetPath()] == name {
					operation = admission.OperationUpdate
				}
			}
			candidates = append(candidates, candidate{
				path:      blob.GetPath(),
				name:      name,
				tier:      namespaceTier(parsed.Namespace.Spec.Tier),
				operation: operation,
			})
		}
	}
	if len(req.GetTrees()) == 0 {
		return nil, false, grpcstatus.Error(codes.InvalidArgument, "validation trees are required")
	}
	var validationErrors []*catalogv1.ValidationError
	hasNamespaces := false
	for _, tree := range req.GetTrees() {
		appendCandidates(tree.GetOldBlobs(), tree.GetProposedBlobs(), true)
		oldEntries := namespaceEntriesByPath(s.parser, tree.GetOldBlobs())
		proposed := namespaceEntriesByPath(s.parser, tree.GetProposedBlobs())
		hasNamespaces = hasNamespaces || len(oldEntries) > 0 || len(proposed) > 0
		remaining := map[string]bool{}
		for _, entry := range proposed {
			remaining[entry.identity.Name] = true
		}
		for path, entry := range oldEntries {
			if remaining[entry.identity.Name] {
				continue
			}
			if err := s.validateNamespaceAuthoringTarget(ctx, req.GetRepositoryId(), path, entry.identity.Name); err != nil {
				validationErrors = append(validationErrors, &catalogv1.ValidationError{FilePath: path, Constraint: "authoring_target", Message: err.Error()})
				continue
			}
			ns, err := s.store.GetNamespaceByName(ctx, entry.identity.Name)
			if errors.Is(err, datastore.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, true, err
			}
			if err := admission.CheckInfrastructureDeletion(ctx, s.store, ns); err != nil {
				var rejected *admission.Error
				if !errors.As(err, &rejected) {
					return nil, true, err
				}
				validationErrors = append(validationErrors, &catalogv1.ValidationError{FilePath: path, Constraint: "dependent_resources", Message: err.Error()})
			}
		}
	}
	for _, candidate := range candidates {
		if candidate.operation == admission.OperationCreate {
			_, lookupErr := s.store.GetNamespaceByName(ctx, candidate.name)
			switch {
			case lookupErr == nil:
				candidate.operation = admission.OperationUpdate
			case errors.Is(lookupErr, datastore.ErrNotFound):
			default:
				return nil, true, fmt.Errorf("lookup Namespace %s for policy operation: %w", candidate.name, lookupErr)
			}
		}
		decision, _, err := s.namespacePolicy.Evaluate(ctx, namespaceadmission.PolicyCheck{
			Operation: candidate.operation,
			Name:      candidate.name,
			Tier:      candidate.tier,
		})
		if err != nil {
			return nil, true, err
		}
		if decision == nil {
			continue
		}
		decision.FilePath = candidate.path
		s.log.Warn("validate_resources: Namespace policy rejection",
			zap.String("operation", string(candidate.operation)),
			zap.String("reason", string(decision.Reason)),
			zap.String("namespace", candidate.name),
			zap.Bool("conflict", false))
		validationErrors = append(validationErrors, &catalogv1.ValidationError{
			FilePath:   candidate.path,
			Field:      decision.Field,
			Constraint: decision.Constraint(),
			Message:    decision.Message,
		})
	}
	return validationErrors, hasNamespaces, nil
}

func (s *Server) validateResourceBlobs(ctx context.Context, repositoryID string, blobs []*catalogv1.ResourceBlob) []*catalogv1.ValidationError {
	var allErrors []*catalogv1.ValidationError
	seenIdentities := make(map[string]string)
	for _, blob := range blobs {
		// Opt-in: blobs not starting with `---` are not product resources.
		trimmed := bytes.TrimLeft(blob.Content, " \t\r\n")
		if !bytes.HasPrefix(trimmed, []byte("---")) {
			continue
		}

		parsed, body, err := s.parser.ParseResource(bytes.NewReader(blob.Content))
		if err == nil && parsed != nil {
			if entry, ok, entryErr := newParsedEntry(blob.Path, parsed, body, ""); entryErr != nil {
				err = entryErr
			} else if ok {
				if previousPath, exists := seenIdentities[entry.identity.key()]; exists {
					err = fmt.Errorf("duplicate resource identity %s/%s (also declared at %s)", entry.identity.Kind, entry.identity.Name, previousPath)
				} else {
					seenIdentities[entry.identity.key()] = blob.Path
				}
			}
		}
		if err == nil && parsed != nil && parsed.Namespace != nil {
			err = s.validateNamespaceAuthoringTarget(ctx, repositoryID, blob.Path, parsed.Namespace.Metadata.Name)
		}
		if err == nil && parsed != nil && parsed.Repository != nil {
			err = s.validateRepositoryAuthoringTarget(ctx, repositoryID, blob.Path, parsed.Repository.Metadata.Namespace, parsed.Repository.Metadata.Name)
		}
		if err == nil && parsed != nil && parsed.CategoryTaxonomy != nil {
			category := parsed.CategoryTaxonomy
			if category.Spec.ParentRef != nil && category.Spec.ParentRef.Name != "" {
				namespace, resolveErr := s.resolveNamespaceIdentifier(ctx, repositoryID)
				if resolveErr != nil {
					err = fmt.Errorf("resolve category namespace: %w", resolveErr)
				} else if refNamespace := category.Spec.ParentRef.Namespace; refNamespace != "" && refNamespace != namespace {
					// Deliberately does not look the target up: the message must not
					// reveal whether a category exists in another namespace.
					err = fmt.Errorf("validate: spec.parentRef.namespace must be empty or the category's own namespace")
				} else if parent, lookupErr := s.store.GetCategoryTaxonomyByName(ctx, namespace, category.Spec.ParentRef.Name); lookupErr == nil && parent.DeletionTimestamp != nil {
					err = fmt.Errorf("parent category %q is terminating", category.Spec.ParentRef.Name)
				} else if lookupErr != nil && !errors.Is(lookupErr, datastore.ErrNotFound) {
					err = fmt.Errorf("resolve parent category %q: %w", category.Spec.ParentRef.Name, lookupErr)
				}
			}
		}
		if err == nil {
			continue
		}

		validationReason := namespaceStructuralReason(errorToValidationError(blob.Path, err.Error()))
		namespaceName := ""
		if parsed != nil && parsed.Namespace != nil {
			namespaceName = parsed.Namespace.Metadata.Name
		}
		if namespaceName != "" || isNamespaceFrontmatter(blob.Content) {
			s.namespaceMetrics.ObserveRejection(validationReason)
		}
		s.log.Warn("validate_resources: pre-receive rejection",
			zap.String("path", blob.Path),
			zap.String("operation", "VALIDATE"),
			zap.String("stage", string(namespaceadmission.StageStructural)),
			zap.String("reason", string(validationReason)),
			zap.String("namespace", namespaceName),
			zap.Bool("conflict", false),
			zap.Error(err))

		// Convert the error string into ValidationError messages.
		// validate.ParseResource returns a joined error string; split on "; " and "\n".
		msgs := splitValidationErrors(err.Error())
		for _, msg := range msgs {
			ve := errorToValidationError(blob.Path, msg)
			allErrors = append(allErrors, ve)
		}
	}
	return allErrors
}

func isNamespaceFrontmatter(content []byte) bool {
	trimmed := bytes.TrimLeft(content, " \t\r\n")
	lines := bytes.Split(trimmed, []byte("\n"))
	if len(lines) == 0 || strings.TrimSpace(string(lines[0])) != "---" {
		return false
	}
	found := false
	for _, rawLine := range lines[1:] {
		line := strings.TrimSuffix(string(rawLine), "\r")
		if strings.TrimSpace(line) == "---" {
			break
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "kind" {
			continue
		}
		if found {
			return false
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if value != "Namespace" {
			return false
		}
		found = true
	}
	return found
}

// validateImmutableResourceChanges compares only old/proposed Git blobs. It
// deliberately performs no datastore reads: pre-receive must remain bounded
// and must reject before an immutable update can advance the ref.
func (s *Server) validateImmutableResourceChanges(oldBlobs, proposedBlobs []*catalogv1.ResourceBlob) []*catalogv1.ValidationError {
	oldNamespacesByPath := namespaceEntriesByPath(s.parser, oldBlobs)
	proposedNamespacesByPath := namespaceEntriesByPath(s.parser, proposedBlobs)
	var errorsOut []*catalogv1.ValidationError
	for path, proposed := range proposedNamespacesByPath {
		old, found := oldNamespacesByPath[path]
		if !found || old.parsed.Namespace.Metadata.Name == proposed.parsed.Namespace.Metadata.Name {
			continue
		}
		errorsOut = append(errorsOut, &catalogv1.ValidationError{
			FilePath:   path,
			Field:      "metadata.name",
			Constraint: "immutable",
			Message:    "validate: metadata.name is immutable at the same repository path",
		})
		s.namespaceMetrics.ObserveRejection(namespaceadmission.ReasonImmutableName)
	}
	oldRepositoriesByPath := repositoryEntriesByPath(s.parser, oldBlobs)
	proposedRepositoriesByPath := repositoryEntriesByPath(s.parser, proposedBlobs)
	for path, proposed := range proposedRepositoriesByPath {
		old, found := oldRepositoriesByPath[path]
		if !found {
			continue
		}
		if old.parsed.Repository.Metadata.Name != proposed.parsed.Repository.Metadata.Name {
			errorsOut = append(errorsOut, &catalogv1.ValidationError{
				FilePath: path, Field: "metadata.name", Constraint: "immutable",
				Message: "validate: metadata.name is immutable at the same repository path",
			})
		}
		if old.parsed.Repository.Metadata.Namespace != proposed.parsed.Repository.Metadata.Namespace {
			errorsOut = append(errorsOut, &catalogv1.ValidationError{
				FilePath: path, Field: "metadata.namespace", Constraint: "immutable",
				Message: "validate: metadata.namespace is immutable at the same repository path",
			})
		}
		if isRepositoryStorageClassDowngrade(old.parsed.Repository.Spec.StorageClass, proposed.parsed.Repository.Spec.StorageClass) {
			errorsOut = append(errorsOut, &catalogv1.ValidationError{
				FilePath: path, Field: "spec.storageClass", Constraint: "immutable_downgrade",
				Message: "validate: Repository storageClass downgrade is not allowed",
			})
		}
	}

	oldCategoriesByPath := categoryEntriesByPath(s.parser, oldBlobs)
	for path, proposed := range categoryEntriesByPath(s.parser, proposedBlobs) {
		old, found := oldCategoriesByPath[path]
		if !found {
			continue
		}
		oldMeta, newMeta := old.parsed.CategoryTaxonomy.Metadata, proposed.parsed.CategoryTaxonomy.Metadata
		if oldMeta.Name != newMeta.Name {
			errorsOut = append(errorsOut, &catalogv1.ValidationError{
				FilePath: path, Field: "metadata.name", Constraint: "immutable",
				Message: "validate: metadata.name is immutable at the same repository path",
			})
		}
		if oldMeta.Namespace != "" && newMeta.Namespace != "" && oldMeta.Namespace != newMeta.Namespace {
			errorsOut = append(errorsOut, &catalogv1.ValidationError{
				FilePath: path, Field: "metadata.namespace", Constraint: "immutable",
				Message: "validate: metadata.namespace is immutable at the same repository path",
			})
		}
	}

	oldEntries := immutableEntries(s.parser, oldBlobs)
	proposedEntries := immutableEntries(s.parser, proposedBlobs)
	for key, proposed := range proposedEntries {
		old, found := oldEntries[key]
		if !found {
			continue
		}
		if message := immutableChangeMessage(old.parsed, proposed.parsed); message != "" {
			errorsOut = append(errorsOut, errorToValidationError(proposed.path, message))
		}
	}
	return errorsOut
}

func categoryEntriesByPath(parser ResourceParser, blobs []*catalogv1.ResourceBlob) map[string]*parsedEntry {
	entries := make(map[string]*parsedEntry, len(blobs))
	for _, blob := range blobs {
		parsed, body, err := parser.ParseResource(bytes.NewReader(blob.GetContent()))
		if err != nil || parsed == nil || parsed.CategoryTaxonomy == nil {
			continue
		}
		entry, ok, err := newParsedEntry(blob.GetPath(), parsed, body, "")
		if err == nil && ok {
			entries[blob.GetPath()] = entry
		}
	}
	return entries
}

func repositoryEntriesByPath(parser ResourceParser, blobs []*catalogv1.ResourceBlob) map[string]*parsedEntry {
	entries := make(map[string]*parsedEntry, len(blobs))
	for _, blob := range blobs {
		parsed, body, err := parser.ParseResource(bytes.NewReader(blob.GetContent()))
		if err != nil || parsed == nil || parsed.Repository == nil {
			continue
		}
		entry, ok, err := newParsedEntry(blob.GetPath(), parsed, body, "")
		if err == nil && ok {
			entries[blob.GetPath()] = entry
		}
	}
	return entries
}

func (s *Server) validateRepositoryDeletions(ctx context.Context, repositoryID string, oldBlobs, proposedBlobs []*catalogv1.ResourceBlob) ([]*catalogv1.ValidationError, error) {
	oldEntries := repositoryEntriesByPath(s.parser, oldBlobs)
	proposedIdentities := make(map[string]struct{})
	for _, entry := range repositoryEntriesByPath(s.parser, proposedBlobs) {
		proposedIdentities[entry.identity.key()] = struct{}{}
	}
	var validationErrors []*catalogv1.ValidationError
	for path, entry := range oldEntries {
		if _, remains := proposedIdentities[entry.identity.key()]; remains {
			continue
		}
		if err := s.validateRepositoryAuthoringTarget(ctx, repositoryID, path, entry.identity.Namespace, entry.identity.Name); err != nil {
			validationErrors = append(validationErrors, &catalogv1.ValidationError{
				FilePath: path, Field: "metadata.name", Constraint: "authoring_target", Message: err.Error(),
			})
			continue
		}
		existing, err := s.lookupResourceByIdentity(ctx, entry.identity)
		if errors.Is(err, datastore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("lookup Repository deletion target: %w", err)
		}
		repository, ok := existing.(*datastore.Repository)
		if !ok || repository == nil {
			continue
		}
		if repository.Name == "gitstore-system" {
			validationErrors = append(validationErrors, &catalogv1.ValidationError{
				FilePath: path, Field: "metadata.name", Constraint: "protected", Message: "validate: bootstrap Repository cannot be deleted",
			})
			continue
		}
		hasCatalogResources, err := s.store.HasCatalogResources(ctx, repository.UID)
		if err != nil {
			return nil, fmt.Errorf("check Repository deletion dependents: %w", err)
		}
		if hasCatalogResources {
			validationErrors = append(validationErrors, &catalogv1.ValidationError{
				FilePath: path, Field: "metadata.name", Constraint: "dependent_resources", Message: "validate: Repository contains catalog resources and cannot be deleted",
			})
		}
	}
	return validationErrors, nil
}

func namespaceEntriesByPath(parser ResourceParser, blobs []*catalogv1.ResourceBlob) map[string]*parsedEntry {
	entries := make(map[string]*parsedEntry, len(blobs))
	for _, blob := range blobs {
		parsed, body, err := parser.ParseResource(bytes.NewReader(blob.GetContent()))
		if err != nil || parsed == nil || parsed.Namespace == nil {
			continue
		}
		entry, ok, err := newParsedEntry(blob.GetPath(), parsed, body, "")
		if err == nil && ok {
			entries[blob.GetPath()] = entry
		}
	}
	return entries
}

func immutableEntries(parser ResourceParser, blobs []*catalogv1.ResourceBlob) map[string]*parsedEntry {
	entries := make(map[string]*parsedEntry, len(blobs))
	for _, blob := range blobs {
		parsed, body, err := parser.ParseResource(bytes.NewReader(blob.GetContent()))
		if err != nil || parsed == nil {
			continue
		}
		entry, ok, err := newParsedEntry(blob.GetPath(), parsed, body, "")
		if err == nil && ok {
			entries[entry.identity.key()] = entry
		}
	}
	return entries
}

func immutableChangeMessage(old, proposed *validate.ParsedResource) string {
	if old == nil || proposed == nil || old.Kind != proposed.Kind {
		return ""
	}
	switch proposed.Kind {
	case "File":
		if old.File.Spec.ContentType != proposed.File.Spec.ContentType {
			return "validate: spec.contentType is immutable after first admission"
		}
	case "ProductVariant":
		if old.ProductVariant.Spec.SKU != proposed.ProductVariant.Spec.SKU {
			return "validate: spec.sku is immutable after first admission"
		}
		if !reflect.DeepEqual(old.ProductVariant.Spec.ProductRef, proposed.ProductVariant.Spec.ProductRef) {
			return "validate: spec.productRef is immutable after first admission"
		}
	}
	return ""
}

func namespaceTier(value string) datastore.NamespaceTier {
	tier, ok := namespaceadmission.TierFromManifest(value)
	if !ok {
		return ""
	}
	return tier
}

// ValidateResourceDeletions checks proposed trees without changing datastore
// state. The complete old and proposed resource sets allow an atomic deletion
// or reparenting to satisfy resource-specific preconditions.
func (s *Server) ValidateResourceDeletions(
	ctx context.Context,
	req *catalogv1.ValidateResourceDeletionsRequest,
) (*catalogv1.ValidateResourceDeletionsResponse, error) {
	if req.GetRepositoryId() == "" {
		return nil, grpcstatus.Error(codes.InvalidArgument, "repository_id is required")
	}
	namespace, err := s.resolveNamespaceIdentifier(ctx, req.GetRepositoryId())
	if err != nil {
		s.log.Error("validate_category_taxonomy_deletion: cannot resolve namespace for repository",
			zap.String("repository_id", req.GetRepositoryId()),
			zap.Error(err))
		return nil, grpcstatus.Error(codes.Unavailable, "category deletion validation unavailable")
	}

	for _, tree := range req.GetTrees() {
		oldEntries, err := s.parseDeletionTreeEntries(tree.GetOldBlobs(), namespace)
		if err != nil {
			return nil, grpcstatus.Errorf(codes.InvalidArgument, "invalid old resource tree: %v", err)
		}
		proposedEntries, err := s.parseDeletionTreeEntries(tree.GetProposedBlobs(), namespace)
		if err != nil {
			return nil, grpcstatus.Errorf(codes.InvalidArgument, "invalid proposed resource tree: %v", err)
		}

		operations := deriveResourceAdmissionOperations(oldEntries, proposedEntries, nil)
		deletedCategories := make(map[string]struct{})
		deletedProducts := false
		for _, operation := range operations {
			if operation.operation != admission.OperationDelete {
				continue
			}
			switch operation.identity.Kind {
			case "CategoryTaxonomy":
				deletedCategories[operation.identity.key()] = struct{}{}
			case "Product":
				deletedProducts = true
			}
		}
		if len(deletedCategories) == 0 && !deletedProducts {
			continue
		}

		for _, entry := range proposedEntries {
			if entry.parsed.Kind != "CategoryTaxonomy" {
				continue
			}
			parentRef := entry.parsed.CategoryTaxonomy.Spec.ParentRef
			if parentRef == nil || parentRef.Name == "" {
				continue
			}
			parent := resourceIdentity{
				APIVersion: entry.identity.APIVersion,
				Kind:       "CategoryTaxonomy",
				Namespace:  entry.identity.Namespace,
				Name:       parentRef.Name,
			}
			if _, blocked := deletedCategories[parent.key()]; blocked {
				return &catalogv1.ValidateResourceDeletionsResponse{
					Accepted: false,
					Reason:   "child categories present",
				}, nil
			}
		}

		// Proposed entries cover same-push child deletions and reparenting in
		// this repository. The reverse owner-reference index additionally
		// covers children authored in other repositories of the namespace.
		owners, ok := s.store.(datastore.OwnerReferenceStore)
		if !ok {
			s.log.Error("validate_category_taxonomy_deletion: datastore does not implement OwnerReferenceStore",
				zap.String("repository_id", req.GetRepositoryId()),
				zap.String("namespace", namespace))
			return nil, grpcstatus.Error(codes.Unavailable, "category deletion validation unavailable")
		}
		for _, operation := range operations {
			if operation.operation != admission.OperationDelete || operation.identity.Kind != "CategoryTaxonomy" {
				continue
			}
			owner, lookupErr := s.store.GetCategoryTaxonomyByName(ctx, operation.identity.Namespace, operation.identity.Name)
			if lookupErr != nil {
				if errors.Is(lookupErr, datastore.ErrNotFound) {
					continue
				}
				s.log.Error("validate_category_taxonomy_deletion: lookup category taxonomy failed",
					zap.String("repository_id", req.GetRepositoryId()),
					zap.String("namespace", operation.identity.Namespace),
					zap.String("name", operation.identity.Name),
					zap.Error(lookupErr))
				return nil, grpcstatus.Error(codes.Unavailable, "category deletion validation unavailable")
			}
			cursor := ""
			for {
				page, listErr := owners.ListBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{Namespace: owner.Namespace, RepositoryID: owner.RepositoryID}, owner.UID, cursor, datastore.MaxOwnerDependentPageSize)
				if listErr != nil {
					s.log.Error("validate_category_taxonomy_deletion: list blocking owner dependents failed",
						zap.String("repository_id", req.GetRepositoryId()),
						zap.String("namespace", owner.Namespace),
						zap.String("owner_uid", owner.UID),
						zap.String("cursor", cursor),
						zap.Error(listErr))
					return nil, grpcstatus.Error(codes.Unavailable, "category deletion validation unavailable")
				}
				for _, dependent := range page.Items {
					if !proposedCategoryReleasesParent(proposedEntries, dependent, operation.identity.Name) {
						return &catalogv1.ValidateResourceDeletionsResponse{Accepted: false, Reason: "child categories present"}, nil
					}
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
		}

		// Product foreground deletion follows the same proposed-tree rule. A
		// ProductVariant deleted or retargeted in this push releases its
		// blocking owner reference; a dependent in another repository remains a
		// hard rejection through the durable reverse index.
		for _, operation := range operations {
			if operation.operation != admission.OperationDelete || operation.identity.Kind != "Product" {
				continue
			}
			owner, lookupErr := s.store.GetProductByName(ctx, operation.identity.Namespace, operation.identity.Name)
			if lookupErr != nil {
				if errors.Is(lookupErr, datastore.ErrNotFound) {
					continue
				}
				return nil, grpcstatus.Error(codes.Unavailable, "Product deletion validation unavailable")
			}
			cursor := ""
			for {
				page, listErr := owners.ListBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{Namespace: owner.Namespace, RepositoryID: owner.RepositoryID}, owner.UID, cursor, datastore.MaxOwnerDependentPageSize)
				if listErr != nil {
					return nil, grpcstatus.Error(codes.Unavailable, "Product deletion validation unavailable")
				}
				for _, dependent := range page.Items {
					if !proposedProductVariantReleasesParent(proposedEntries, dependent, operation.identity.Name) {
						return &catalogv1.ValidateResourceDeletionsResponse{Accepted: false, Reason: "ProductVariants present"}, nil
					}
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
		}
	}

	return &catalogv1.ValidateResourceDeletionsResponse{Accepted: true}, nil
}

func proposedCategoryReleasesParent(entries []*parsedEntry, dependent datastore.OwnerDependent, deletedParent string) bool {
	if dependent.DependentKind != "CategoryTaxonomy" {
		return false
	}
	for _, entry := range entries {
		if entry.identity.Kind != "CategoryTaxonomy" || entry.identity.Name != dependent.Name {
			continue
		}
		parent := entry.parsed.CategoryTaxonomy.Spec.ParentRef
		return parent == nil || parent.Name != deletedParent
	}
	return false
}

func proposedProductVariantReleasesParent(entries []*parsedEntry, dependent datastore.OwnerDependent, deletedParent string) bool {
	if dependent.DependentKind != "ProductVariant" {
		return false
	}
	for _, entry := range entries {
		if entry.identity.Kind != "ProductVariant" || entry.identity.Name != dependent.Name {
			continue
		}
		ref := entry.parsed.ProductVariant.Spec.ProductRef
		return ref == nil || ref.Name != deletedParent
	}
	return false
}

func (s *Server) parseDeletionTreeEntries(blobs []*catalogv1.ResourceBlob, defaultNamespace string) ([]*parsedEntry, error) {
	entries := make([]*parsedEntry, 0, len(blobs))
	for _, blob := range blobs {
		parsed, body, err := s.parser.ParseResource(bytes.NewReader(blob.GetContent()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", blob.GetPath(), err)
		}
		entry, ok, err := newParsedEntry(blob.GetPath(), parsed, body, defaultNamespace)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", blob.GetPath(), err)
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (s *Server) validateNamespaceAuthoringTarget(ctx context.Context, repositoryID, sourcePath, name string) error {
	repository, err := s.store.GetRepository(ctx, repositoryID)
	if err != nil {
		return fmt.Errorf("validate: Namespace manifests require repository gitstore-system/gitstore-system: %w", err)
	}
	datastore.NormalizeRepositoryContract(repository)
	namespace, err := s.store.GetNamespaceByName(ctx, repository.Namespace)
	if err != nil {
		return fmt.Errorf("validate: Namespace manifests require repository gitstore-system/gitstore-system: %w", err)
	}
	if namespace.Name != "gitstore-system" || repository.Name != "gitstore-system" {
		return fmt.Errorf("validate: Namespace manifests are accepted only in gitstore-system/gitstore-system")
	}
	expectedPath := fmt.Sprintf("namespaces/%s.md", name)
	if sourcePath != expectedPath {
		return fmt.Errorf("validate: Namespace %q must be stored at %s", name, expectedPath)
	}
	return nil
}

func (s *Server) validateRepositoryAuthoringTarget(ctx context.Context, repositoryID, sourcePath, namespaceName, name string) error {
	repository, err := s.store.GetRepository(ctx, repositoryID)
	if err != nil {
		return fmt.Errorf("validate: Repository manifests require the namespace gitstore-system repository: %w", err)
	}
	datastore.NormalizeRepositoryContract(repository)
	if repository.Name != "gitstore-system" || repository.Namespace != namespaceName {
		return fmt.Errorf("validate: Repository manifests are accepted only in %s/gitstore-system", namespaceName)
	}
	expectedPath := fmt.Sprintf("repositories/%s.md", name)
	if sourcePath != expectedPath {
		return fmt.Errorf("validate: Repository %q must be stored at %s", name, expectedPath)
	}
	return nil
}

// splitValidationErrors splits the joined error string from validate.Parse into
// individual messages.
func splitValidationErrors(errStr string) []string {
	// validate.Parse joins errors with "; " or "\n"
	var parts []string
	for part := range strings.SplitSeq(errStr, "\n") {
		for sub := range strings.SplitSeq(part, "; ") {
			if s := strings.TrimSpace(sub); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return parts
}

// errorToValidationError converts a single validate.Parse error message into a
// ValidationError proto, extracting field path and constraint where possible.
func errorToValidationError(filePath, msg string) *catalogv1.ValidationError {
	// Messages from toFriendlyError have the form:
	//   "validate: <field> <description>"
	// We extract the field name from the message.
	trimmed := strings.TrimPrefix(msg, "validate: ")

	field := ""
	constraint := ""

	// Try to extract a field path from known message patterns.
	if strings.HasPrefix(trimmed, "status") {
		field = "status"
		constraint = "system-managed"
	} else if strings.Contains(trimmed, "duplicate resource identity") {
		field = "metadata.name"
		constraint = "duplicate"
	} else if strings.Contains(trimmed, "Namespace manifests") || strings.Contains(trimmed, "must be stored at namespaces/") {
		field = "metadata.name"
		constraint = "authoring-target"
	} else if strings.Contains(trimmed, "reserved") {
		field = "metadata.name"
		constraint = "reserved"
	} else if strings.HasPrefix(trimmed, "metadata.name must match DNS label format") {
		field = "metadata.name"
		constraint = "dns-label"
	} else if strings.HasPrefix(trimmed, "spec.title exceeds") {
		field = "spec.title"
		constraint = "max=200"
	} else if strings.HasSuffix(trimmed, " is required") {
		field = strings.TrimSuffix(trimmed, " is required")
		constraint = "required"
	} else if path, _, ok := strings.Cut(trimmed, " must be \""); ok && !strings.Contains(path, " ") {
		field = path
		constraint = "eq"
	} else if strings.Contains(trimmed, "must not reference the category itself") {
		field = "spec.parentRef.name"
		constraint = "self-parent"
	} else if strings.HasPrefix(trimmed, "parent category ") && strings.HasSuffix(trimmed, " is terminating") {
		field = "spec.parentRef.name"
		constraint = "parent-terminating"
	} else if strings.HasPrefix(trimmed, "spec.parentRef.namespace must") {
		field = "spec.parentRef.namespace"
		constraint = "cross-namespace"
	} else if strings.Contains(trimmed, "malformed YAML") || strings.Contains(trimmed, "frontmatter") {
		constraint = "envelope"
	} else if strings.HasPrefix(trimmed, "metadata.") {
		// "metadata.uid is read-only..."
		parts := strings.SplitN(trimmed, " ", 2)
		if len(parts) > 0 {
			field = parts[0]
			constraint = "read-only"
		}
	} else {
		// Generic: try splitting on space to get field
		parts := strings.Fields(trimmed)
		if len(parts) > 0 {
			field = parts[0]
		}
	}

	return &catalogv1.ValidationError{
		FilePath:   filePath,
		Field:      field,
		Constraint: constraint,
		Message:    msg,
	}
}

func namespaceStructuralReason(validationErr *catalogv1.ValidationError) namespaceadmission.Reason {
	switch validationErr.GetConstraint() {
	case "immutable":
		return namespaceadmission.ReasonImmutableName
	case "authoring-target":
		return namespaceadmission.ReasonInvalidAuthoringTarget
	case "duplicate":
		return namespaceadmission.ReasonDuplicateIdentity
	case "reserved":
		return namespaceadmission.ReasonReservedIdentifier
	}
	switch validationErr.GetField() {
	case "metadata.name":
		return namespaceadmission.ReasonInvalidIdentifier
	case "spec.tier":
		return namespaceadmission.ReasonInvalidTier
	default:
		return namespaceadmission.ReasonInvalidEnvelope
	}
}

func namespacePolicyReason(constraint string) namespaceadmission.Reason {
	value := strings.TrimPrefix(constraint, "policy/")
	return namespaceadmission.Reason(strings.ToUpper(strings.ReplaceAll(value, "-", "_")))
}

// resolveNamespaceIdentifier looks up the namespace identifier string (e.g. "gitci")
// for a given repository UUID. Returns an error if the repository or its namespace
// cannot be resolved — storing catalog resources under a raw UUID is never correct.
func (s *Server) resolveNamespaceIdentifier(ctx context.Context, repositoryID string) (string, error) {
	repo, err := s.store.GetRepository(ctx, repositoryID)
	if err != nil || repo == nil {
		return "", fmt.Errorf("admit_resources: repository %s not found: %w", repositoryID, err)
	}
	datastore.NormalizeRepositoryContract(repo)
	ns, err := s.store.GetNamespaceByName(ctx, repo.Namespace)
	if err != nil || ns == nil {
		return "", fmt.Errorf("admit_resources: namespace %s not found for repository %s: %w", repo.Namespace, repositoryID, err)
	}
	return ns.Name, nil
}

func (s *Server) isAdmissionCommitCurrent(ctx context.Context, repositoryID, refName, commitSHA string) bool {
	current, ok := s.currentAdmissionCommit(ctx, repositoryID, refName, commitSHA)
	if !ok {
		return false
	}
	if isZeroOID(commitSHA) {
		if current != "" {
			s.log.Info("admit_resources: branch delete is stale; ref was recreated — skipping",
				zap.String("repository_id", repositoryID),
				zap.String("ref_name", refName),
				zap.String("current_commit_sha", current))
			return false
		}
		return true
	}
	if current != "" && current != commitSHA {
		s.log.Info("admit_resources: stale admission snapshot superseded",
			zap.String("repository_id", repositoryID),
			zap.String("ref_name", refName),
			zap.String("admitted_commit_sha", commitSHA),
			zap.String("current_commit_sha", current))
		return false
	}
	return true
}

func (s *Server) currentAdmissionCommit(ctx context.Context, repositoryID, refName, commitSHA string) (string, bool) {
	if refName == "" {
		return commitSHA, true
	}
	current, err := s.git.ResolveRef(ctx, repositoryID, refName)
	if err != nil {
		if isRefNotFound(err) {
			if !isZeroOID(commitSHA) {
				s.log.Info("admit_resources: ref no longer exists; stale admission skipped",
					zap.String("repository_id", repositoryID),
					zap.String("ref_name", refName),
					zap.String("new_commit_sha", commitSHA))
				return "", false
			}
			return "", true
		}
		s.log.Error("admit_resources: resolve ref failed",
			zap.String("repository_id", repositoryID),
			zap.String("ref_name", refName),
			zap.String("new_commit_sha", commitSHA),
			zap.Error(err))
		return "", false
	}
	return current, true
}

// AdmitResources fetches, parses, and stores catalog resources from an accepted push commit.
// Called fire-and-forget from the post-receive hook. Each product is processed independently;
// failures are logged and do not block remaining products (FR-011).
func (s *Server) AdmitResources(
	ctx context.Context,
	req *catalogv1.AdmitResourcesRequest,
) (*catalogv1.AdmitResourcesResponse, error) {
	if s.git == nil || s.store == nil {
		return &catalogv1.AdmitResourcesResponse{}, nil
	}

	newCommit := req.GetNewCommitSha()
	if newCommit == "" {
		s.log.Warn("admit_resources: missing new commit sha",
			zap.String("repository_id", req.RepositoryId),
			zap.String("ref_name", req.RefName))
		return &catalogv1.AdmitResourcesResponse{}, nil
	}

	// Resolve the namespace identifier (e.g. "gitci") from the repository UUID.
	// This is the push context namespace; catalog resources that omit metadata.namespace
	// inherit it. Storing resources under the raw repository UUID is never correct.
	repoNamespace, err := s.resolveNamespaceIdentifier(ctx, req.RepositoryId)
	if err != nil {
		s.log.Error("admit_resources: cannot resolve namespace for repository",
			zap.String("repository_id", req.RepositoryId),
			zap.Error(err))
		return &catalogv1.AdmitResourcesResponse{}, nil
	}

	now := s.clock.Now().UTC()
	actorSubject := strings.TrimSpace(req.GetActorSubject())
	if actorSubject == "" {
		return nil, grpcstatus.Error(codes.InvalidArgument, "admission actor is required")
	}

	branch := strings.TrimPrefix(req.RefName, "refs/heads/")
	for range namespaceadmission.AdmissionWriteAttempts {
		currentCommit, ok := s.currentAdmissionCommit(ctx, req.RepositoryId, req.RefName, newCommit)
		if !ok {
			return &catalogv1.AdmitResourcesResponse{}, nil
		}
		if isZeroOID(newCommit) && currentCommit != "" {
			return &catalogv1.AdmitResourcesResponse{}, nil
		}
		effectiveCommit := newCommit
		convergeNamespacesOnly := false
		if currentCommit != "" && currentCommit != newCommit {
			effectiveCommit = currentCommit
			convergeNamespacesOnly = true
		}
		changedPaths := req.GetChangedPaths()
		if convergeNamespacesOnly {
			changedPaths = namespaceChangedPaths(changedPaths)
			if len(changedPaths) == 0 {
				return &catalogv1.AdmitResourcesResponse{}, nil
			}
		}
		superseded := false
		admCtx := AdmissionContext{
			RepositoryID: req.RepositoryId,
			Namespace:    repoNamespace,
			ActorSubject: actorSubject,
			CommitSHA:    effectiveCommit,
			RefName:      req.RefName,
			Revision:     branch + "@sha1:" + effectiveCommit,
			Now:          now,
			superseded:   &superseded,
		}

		oldEntries := s.loadParsedEntries(ctx, req.RepositoryId, req.GetOldCommitSha(), admCtx.Namespace, changedPaths)
		newEntries := s.loadParsedEntries(ctx, req.RepositoryId, effectiveCommit, admCtx.Namespace, changedPaths)
		var requestedEntries []*parsedEntry
		if convergeNamespacesOnly {
			requestedEntries = s.loadParsedEntries(ctx, req.RepositoryId, newCommit, admCtx.Namespace, changedPaths)
			changedPaths = namespaceConvergencePaths(newEntries, requestedEntries)
			if len(changedPaths) == 0 {
				return &catalogv1.AdmitResourcesResponse{}, nil
			}
			oldEntries = s.loadParsedEntries(ctx, req.RepositoryId, req.GetOldCommitSha(), admCtx.Namespace, changedPaths)
			newEntries = s.loadParsedEntries(ctx, req.RepositoryId, effectiveCommit, admCtx.Namespace, changedPaths)
		}
		if !s.isAdmissionCommitCurrent(ctx, req.RepositoryId, req.RefName, effectiveCommit) {
			continue
		}
		if immutablePaths := repositoryImmutablePathChanges(oldEntries, newEntries); len(immutablePaths) > 0 {
			s.log.Warn("admit_resources: Repository immutable identity change rejected",
				zap.Strings("paths", immutablePaths),
				zap.String("repository_id", req.RepositoryId),
				zap.String("commit_sha", effectiveCommit))
			return nil, grpcstatus.Errorf(codes.FailedPrecondition, "Repository metadata.name and metadata.namespace are immutable at %s", immutablePaths[0])
		}
		ops := deriveResourceAdmissionOperations(oldEntries, newEntries, changedPaths)
		if convergeNamespacesOnly {
			ops = namespaceConvergenceOperations(ops, requestedEntries)
		}
		if err := s.applyResourceOperations(ctx, ops, admCtx); err != nil {
			s.log.Error("admit_resources: apply operations failed",
				zap.String("repository_id", req.RepositoryId),
				zap.String("commit_sha", effectiveCommit),
				zap.Error(err))
			if errors.Is(err, errCategoryDeletionBlocked) {
				return nil, grpcstatus.Error(codes.FailedPrecondition, "child categories present")
			}
			return nil, grpcstatus.Errorf(codes.Internal, "admit_resources: %v", err)
		}
		if admCtx.wasSuperseded() {
			continue
		}
		if !s.isAdmissionCommitCurrent(ctx, req.RepositoryId, req.RefName, effectiveCommit) {
			continue
		}
		return &catalogv1.AdmitResourcesResponse{}, nil
	}
	s.log.Warn("admit_resources: Namespace convergence exhausted",
		zap.String("repository_id", req.RepositoryId),
		zap.String("ref_name", req.RefName),
		zap.String("requested_commit_sha", newCommit))
	return &catalogv1.AdmitResourcesResponse{}, nil
}

func namespaceChangedPaths(paths []string) []string {
	namespacePaths := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.HasPrefix(path, "namespaces/") {
			namespacePaths = append(namespacePaths, path)
		}
	}
	return namespacePaths
}

func (s *Server) loadParsedEntries(ctx context.Context, repositoryID, ref, namespace string, changedPaths []string) []*parsedEntry {
	if ref == "" || isZeroOID(ref) {
		return nil
	}
	paths, err := s.git.ListFiles(ctx, repositoryID, "", ref)
	if err != nil {
		s.log.Error("admit_resources: list files failed",
			zap.String("repository_id", repositoryID),
			zap.String("commit_sha", ref),
			zap.Error(err))
		return nil
	}
	if len(changedPaths) > 0 {
		pathSet := make(map[string]struct{}, len(changedPaths))
		for _, path := range changedPaths {
			pathSet[path] = struct{}{}
		}
		filtered := paths[:0]
		for _, path := range paths {
			if _, ok := pathSet[path]; ok {
				filtered = append(filtered, path)
			}
		}
		paths = filtered
	}
	entries := make([]*parsedEntry, 0, len(paths))
	for _, path := range paths {
		content, err := s.git.ReadFile(ctx, repositoryID, path, ref)
		if err != nil {
			s.log.Error("admit_resources: read file failed",
				zap.String("path", path),
				zap.String("commit_sha", ref),
				zap.Error(err))
			continue
		}
		parsed, body, err := s.parser.ParseResource(bytes.NewReader(content))
		if err != nil || parsed == nil {
			if err != nil {
				s.log.Error("admit_resources: parse failed",
					zap.String("path", path),
					zap.String("commit_sha", ref),
					zap.Error(err))
			}
			continue
		}
		entry, ok, err := newParsedEntry(path, parsed, body, namespace)
		if err != nil {
			s.log.Error("admit_resources: hash resource failed",
				zap.String("path", path),
				zap.String("commit_sha", ref),
				zap.Error(err))
			continue
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (s *Server) applyResourceOperations(ctx context.Context, ops []resourceAdmissionOperation, admCtx AdmissionContext) error {
	upsertOps := make(map[string]resourceAdmissionOperation)
	var upsertEntries []*parsedEntry
	var deletes []resourceAdmissionOperation
	for _, op := range ops {
		switch op.operation {
		case admission.OperationDelete:
			deletes = append(deletes, op)
		case admission.OperationCreate, admission.OperationUpdate, namespaceConvergenceOperation:
			if op.newEntry == nil {
				continue
			}
			upsertOps[op.identity.key()] = op
			upsertEntries = append(upsertEntries, op.newEntry)
		}
	}
	// Apply dependency-removing updates before deletions. In particular, a
	// child CategoryTaxonomy may be reparented in the same accepted push that
	// removes its former parent; deleting first would consult a stale reverse
	// owner-reference projection and reject after the ref has already advanced.
	if _, err := s.admitParsedEntries(ctx, upsertEntries, admCtx, upsertOps); err != nil {
		return err
	}
	for _, op := range deletes {
		sourcePath := ""
		if op.oldEntry != nil {
			sourcePath = op.oldEntry.path
		}
		if err := s.deleteResource(ctx, op.identity, admCtx.RepositoryID, admCtx.RefName, sourcePath, admCtx.ActorSubject, op.oldEntry); err != nil {
			return err
		}
	}
	// TODO(CategoryTaxonomy controller): when a CategoryTaxonomy is deleted or
	// its parentRef changes, descendants already stored in the datastore are left
	// with a stale AncestorPath. Admission only processes the files that changed
	// in this push; unchanged children are never re-admitted here.
	// The CategoryTaxonomy controller must reconcile this: on any category
	// create/update/delete event it should walk all direct and transitive children
	// (by ParentName pointer) and recompute AncestorPath in topological order.
	// The ParentResolved=False condition on a child is the observable signal that
	// its path may be stale and reconciliation is needed.
	return nil
}

func (s *Server) admitParsedEntries(
	ctx context.Context,
	entries []*parsedEntry,
	admCtx AdmissionContext,
	explicitOps map[string]resourceAdmissionOperation,
) ([]admission.EntryDecision, error) {
	// Build intra-push category graph: name → parentName
	pushCategoryParents := make(map[string]string)
	categoryEntries := make(map[string]*parsedEntry)
	for i := range entries {
		e := entries[i]
		if e.parsed.Kind == "CategoryTaxonomy" {
			cat := e.parsed.CategoryTaxonomy
			parent := ""
			if cat.Spec.ParentRef != nil {
				parent = cat.Spec.ParentRef.Name
			}
			pushCategoryParents[cat.Metadata.Name] = parent
			categoryEntries[cat.Metadata.Name] = e
		}
	}
	cycleMembers := detectCycles(pushCategoryParents)
	topoOrder := topoSortCategories(pushCategoryParents, cycleMembers)

	// Build a PushSet of AdmissionRequests for CategoryTaxonomy resources so the
	// chain's policy can resolve in-push parents and detect cycles.
	gitCtx := &admission.GitAdmissionContext{
		RepositoryID: admCtx.RepositoryID,
		CommitSHA:    admCtx.CommitSHA,
		RefName:      admCtx.RefName,
		Revision:     admCtx.Revision,
	}
	catPushSet := make([]admission.AdmissionRequest, 0, len(categoryEntries))
	for _, e := range categoryEntries {
		cat := e.parsed.CategoryTaxonomy
		ns := cat.Metadata.Namespace
		if ns == "" {
			ns = admCtx.Namespace
		}
		var siblingOp admission.Operation
		if explOp, inExplicit := explicitOps[e.identity.key()]; inExplicit {
			siblingOp = explOp.operation
		} else {
			// This entry is a sibling referenced by the push but not itself changed.
			// Probe the DB so the push-set carries the correct operation; a future
			// policy that branches on sibling operation would otherwise see a wrong value.
			existing, err := s.lookupResourceByIdentity(ctx, e.identity)
			if err == nil && existing != nil {
				siblingOp = admission.OperationUpdate
			} else {
				siblingOp = admission.OperationCreate
			}
		}
		catPushSet = append(catPushSet, admission.AdmissionRequest{
			Object:     cat,
			Kind:       cat.Kind,
			Name:       cat.Metadata.Name,
			Namespace:  ns,
			Operation:  siblingOp,
			Trigger:    admission.TriggerGitPush,
			Now:        admCtx.Now,
			GitContext: gitCtx,
		})
	}

	// inPushAncestorPaths is populated as each category is admitted so that
	// children later in the same push see their parent's full computed path.
	inPushAncestorPaths := make(map[string]string, len(topoOrder))
	decisions := make([]admission.EntryDecision, 0, len(topoOrder))
	for _, name := range topoOrder {
		e := categoryEntries[name]
		op, existing, err := s.operationForEntry(ctx, e, explicitOps)
		if err != nil {
			return decisions, err
		}
		decisions = append(decisions, s.admitCategoryTaxonomyWithContext(ctx, e.parsed.CategoryTaxonomy, e.body, admCtx, e.path, op, existing, inPushAncestorPaths, catPushSet))
	}
	for _, e := range entries {
		if e.parsed.Kind == "CategoryTaxonomy" {
			continue // handled in the topo loop above
		}
		op, existing, err := s.operationForEntry(ctx, e, explicitOps)
		if err != nil {
			return decisions, err
		}
		switch e.parsed.Kind {
		case "Product":
			decisions = append(decisions, s.admitProduct(ctx, e.parsed.Product, e.body, admCtx, e.path, op, existing))
		case "Collection":
			s.admitCollection(ctx, e.parsed.Collection, e.body, admCtx, e.path, op, existing)
		case "ProductVariant":
			s.admitProductVariant(ctx, e.parsed.ProductVariant, e.body, admCtx, e.path, op, existing)
		case "Namespace":
			s.admitNamespace(ctx, e.parsed.Namespace, e.body, admCtx, e.path, op, existing)
		case "File":
			s.admitFile(ctx, e.parsed.File, e.body, admCtx, e.path, op, existing)
		case "Repository":
			if err := s.admitRepository(ctx, e.parsed.Repository, e.body, admCtx, e.path, op, existing); err != nil {
				return decisions, err
			}
		}
	}
	return decisions, nil
}

func (s *Server) operationForEntry(
	ctx context.Context,
	e *parsedEntry,
	explicitOps map[string]resourceAdmissionOperation,
) (admission.Operation, any, error) {
	if e == nil {
		return "", nil, nil
	}
	if op, ok := explicitOps[e.identity.key()]; ok {
		existing, err := s.lookupResourceByIdentity(ctx, e.identity)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return "", nil, fmt.Errorf("lookup %s %s/%s: %w", e.identity.Kind, e.identity.Namespace, e.identity.Name, err)
		}
		if existingNamespace, ok := existing.(*datastore.Namespace); e.identity.Kind == "Namespace" &&
			op.operation == admission.OperationCreate && ok && existingNamespace != nil {
			return admission.OperationUpdate, existingNamespace, nil
		}
		return op.operation, existing, nil
	}
	existing, err := s.lookupResourceByIdentity(ctx, e.identity)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return admission.OperationCreate, nil, nil
		}
		return "", nil, fmt.Errorf("lookup %s %s/%s: %w", e.identity.Kind, e.identity.Namespace, e.identity.Name, err)
	}
	if existing != nil {
		return admission.OperationUpdate, existing, nil
	}
	return admission.OperationCreate, nil, nil
}

func (s *Server) lookupResourceByIdentity(ctx context.Context, id resourceIdentity) (any, error) {
	switch id.Kind {
	case "Product":
		return s.store.GetProductByName(ctx, id.Namespace, id.Name)
	case "CategoryTaxonomy":
		return s.store.GetCategoryTaxonomyByName(ctx, id.Namespace, id.Name)
	case "Collection":
		return s.store.GetCollectionByName(ctx, id.Namespace, id.Name)
	case "ProductVariant":
		return s.store.GetProductVariantByName(ctx, id.Namespace, id.Name)
	case "Namespace":
		return s.store.GetNamespaceByName(ctx, id.Name)
	case "File":
		return s.store.GetFileByName(ctx, id.Namespace, id.Name)
	case "Repository":
		mapping, err := s.store.LookupRepository(ctx, id.Namespace, id.Name)
		if err != nil {
			return nil, err
		}
		return s.store.GetRepository(ctx, mapping.RepositoryID)
	default:
		return nil, datastore.ErrNotFound
	}
}

// admitRepository is deliberately the only Git-admission write path for
// non-bootstrap Repository records. The Namespace reference is resolved here,
// rather than accepted from the author, so every new Repository owns exactly
// one blocking Namespace reference from its first durable row.
const maxRepositoryAdmissionUpdateAttempts = 8

func (s *Server) admitRepository(ctx context.Context, resource *catalog.RepositoryResource, body []byte, admCtx AdmissionContext, sourcePath string, op admission.Operation, rawExisting any) error {
	if resource == nil || resource.Metadata.Name == "" || resource.Metadata.Namespace == "" || resource.Metadata.Name == "gitstore-system" {
		return nil
	}
	namespace, err := s.store.GetNamespaceByName(ctx, resource.Metadata.Namespace)
	if err != nil {
		return fmt.Errorf("admit Repository: resolve namespace: %w", err)
	}
	if namespace.DeletionTimestamp != nil {
		return nil
	}
	existing, _ := rawExisting.(*datastore.Repository)
	if existing != nil {
		intent, err := admission.ReadDeletionIntent(existing.Status)
		if err != nil {
			return err
		}
		if existing.DeletionTimestamp != nil || intent != nil {
			return admission.NewError(admission.CodeFailedPrecondition, "REPOSITORY_TERMINATING", "repository deletion is pending")
		}
	}
	if op == admission.OperationUpdate && existing == nil {
		return nil
	}
	if existing != nil && (existing.Name != resource.Metadata.Name || existing.Namespace != resource.Metadata.Namespace) {
		return nil
	}
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		return fmt.Errorf("admit Repository: marshal spec: %w", err)
	}
	ownerReferences, err := json.Marshal([]catalog.OwnerReference{{APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", Name: namespace.Name, UID: namespace.UID, BlockOwnerDeletion: true}})
	if err != nil {
		return fmt.Errorf("admit Repository: marshal owner references: %w", err)
	}
	if existing == nil {
		uid, ok := s.newUID(resource.Kind, resource.Metadata.Name)
		if !ok {
			return fmt.Errorf("admit Repository: generate UID")
		}
		repo := &datastore.Repository{APIVersion: resource.APIVersion, Kind: resource.Kind, UID: uid, ID: uid, RepositoryID: uid, Namespace: namespace.Name, NamespaceID: namespace.Name, Name: resource.Metadata.Name, Labels: cloneStringMap(resource.Metadata.Labels), Annotations: cloneStringMap(resource.Metadata.Annotations), OwnerReferences: ownerReferences, Finalizers: []string{}, Revision: admCtx.Revision, CreationTimestamp: admCtx.Now, CreationActor: admCtx.ActorSubject, UpdateTimestamp: admCtx.Now, UpdateActor: admCtx.ActorSubject, SourcePath: sourcePath, GitCommitSHA: admCtx.CommitSHA, GitRef: admCtx.RefName, Spec: specJSON, Body: string(body), DefaultBranch: resource.Spec.DefaultBranch, StorageClass: resource.Spec.StorageClass}
		datastore.NormalizeRepositoryContract(repo)
		repo.Status = admissionAcceptedStatus(repo.Generation, admCtx.Revision, admCtx.Now)
		if err := s.store.CreateRepositoryInActiveNamespace(ctx, repo); err != nil {
			return fmt.Errorf("admit Repository: create authoritative row: %w", err)
		}
		if err := s.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: namespace.Name, Name: repo.Name, RepositoryID: uid}); err != nil {
			return fmt.Errorf("admit Repository: create namespace mapping: %w", err)
		}
		return nil
	}
	if isRepositoryStorageClassDowngrade(existing.StorageClass, resource.Spec.StorageClass) {
		return nil
	}
	for range maxRepositoryAdmissionUpdateAttempts {
		intent, err := admission.ReadDeletionIntent(existing.Status)
		if err != nil {
			return err
		}
		if existing.DeletionTimestamp != nil || intent != nil {
			return admission.NewError(admission.CodeFailedPrecondition, "REPOSITORY_TERMINATING", "repository deletion is pending")
		}
		if !s.isAdmissionCommitCurrent(ctx, admCtx.RepositoryID, admCtx.RefName, admCtx.CommitSHA) {
			admCtx.markSuperseded()
			return nil
		}
		desiredStateChanged := existing.APIVersion != resource.APIVersion ||
			existing.Kind != resource.Kind ||
			specBodyChanged(existing.Spec, existing.Body, specJSON, body)
		metadataChanged := !stringMapsEqual(existing.Labels, resource.Metadata.Labels) ||
			!stringMapsEqual(existing.Annotations, resource.Metadata.Annotations) ||
			!bytes.Equal(existing.OwnerReferences, ownerReferences)
		provenanceChanged := existing.Revision != admCtx.Revision ||
			existing.SourcePath != sourcePath ||
			existing.GitCommitSHA != admCtx.CommitSHA ||
			existing.GitRef != admCtx.RefName
		if !desiredStateChanged && !metadataChanged && !provenanceChanged {
			return nil
		}
		expected := existing.ResourceVersion
		existing.Labels, existing.Annotations, existing.OwnerReferences = cloneStringMap(resource.Metadata.Labels), cloneStringMap(resource.Metadata.Annotations), ownerReferences
		existing.Spec, existing.Body, existing.DefaultBranch, existing.StorageClass = specJSON, string(body), resource.Spec.DefaultBranch, resource.Spec.StorageClass
		existing.Revision, existing.UpdateTimestamp, existing.UpdateActor, existing.SourcePath, existing.GitCommitSHA, existing.GitRef = admCtx.Revision, admCtx.Now, admCtx.ActorSubject, sourcePath, admCtx.CommitSHA, admCtx.RefName
		if desiredStateChanged {
			datastore.AdvanceRepositorySpecVersion(existing)
		} else {
			datastore.AdvanceRepositorySystemVersion(existing)
		}
		existing.Status = mergeRepositoryAdmissionStatus(existing.Status, existing.Generation, admCtx.Revision, admCtx.Now)
		err = s.store.UpdateRepository(ctx, existing, expected)
		if err == nil {
			return nil
		}
		if !errors.Is(err, datastore.ErrConflict) {
			return fmt.Errorf("admit Repository: update authoritative row: %w", err)
		}
		existing, err = s.store.GetRepository(ctx, existing.UID)
		if err != nil {
			return fmt.Errorf("admit Repository: reload after conflict: %w", err)
		}
		if existing.Name != resource.Metadata.Name || existing.Namespace != resource.Metadata.Namespace {
			return fmt.Errorf("admit Repository: identity changed during conflict retry")
		}
	}
	return fmt.Errorf("admit Repository: update conflict retry budget exhausted: %w", datastore.ErrConflict)
}

// isRepositoryStorageClassDowngrade recognizes the currently documented
// storage tiers. Unknown classes deliberately remain for the later validation
// matrix; treating arbitrary strings lexicographically made unrelated class
// names appear to be upgrades or downgrades.
func isRepositoryStorageClassDowngrade(current, proposed string) bool {
	ranks := map[string]int{"standard": 1, "premium": 2}
	currentRank, knownCurrent := ranks[strings.ToLower(current)]
	proposedRank, knownProposed := ranks[strings.ToLower(proposed)]
	return knownCurrent && knownProposed && proposedRank < currentRank
}

var errCategoryDeletionBlocked = errors.New("category deletion blocked by child categories")

func (s *Server) markPushedInfrastructureDeletion(ctx context.Context, resource any, repositoryID, refName, path, actor string) error {
	current, err := s.git.ResolveRef(ctx, repositoryID, refName)
	if err != nil {
		return fmt.Errorf("resolve deletion ref: %w", err)
	}
	if _, err := s.git.ReadFile(ctx, repositoryID, path, current); grpcstatus.Code(err) != codes.NotFound {
		if err != nil {
			return err
		}
		return fmt.Errorf("deletion superseded: manifest still exists")
	}
	now := s.clock.Now().UTC()
	switch r := resource.(type) {
	case *datastore.Namespace:
		intent, readErr := admission.ReadDeletionIntent(r.Status)
		if readErr != nil {
			return readErr
		}
		if intent == nil {
			intent = &admission.InfrastructureDeletionIntent{UID: r.UID, RepositoryID: repositoryID, Path: path, Ref: refName, ExpectedCommit: r.GitCommitSHA, Actor: actor}
		}
		if intent.UID != r.UID || intent.RepositoryID != repositoryID || intent.Path != path || intent.Ref != refName {
			return fmt.Errorf("pushed removal does not match pending namespace deletion")
		}
		if intent.RemovalCommit == "" {
			intent.RemovalCommit = current
		}
		r.Status, err = admission.WithDeletionIntent(r.Status, intent, r.Generation, now)
		if err == nil && r.DeletionTimestamp != nil {
			if err := admission.CheckInfrastructureDeletion(ctx, s.store, r); err != nil {
				return err
			}
			expected := r.ResourceVersion
			datastore.AdvanceNamespaceSystemVersion(r)
			return s.store.UpdateNamespace(ctx, r, expected)
		}
	case *datastore.Repository:
		intent, readErr := admission.ReadDeletionIntent(r.Status)
		if readErr != nil {
			return readErr
		}
		if intent == nil {
			intent = &admission.InfrastructureDeletionIntent{UID: r.UID, RepositoryID: repositoryID, Path: path, Ref: refName, ExpectedCommit: r.GitCommitSHA, Actor: actor}
		}
		if intent.UID != r.UID || intent.RepositoryID != repositoryID || intent.Path != path || intent.Ref != refName {
			return fmt.Errorf("pushed removal does not match pending repository deletion")
		}
		if intent.RemovalCommit == "" {
			intent.RemovalCommit = current
		}
		r.Status, err = admission.WithDeletionIntent(r.Status, intent, r.Generation, now)
		if err == nil && r.DeletionTimestamp != nil {
			if err := admission.CheckInfrastructureDeletion(ctx, s.store, r); err != nil {
				return err
			}
			expected := r.ResourceVersion
			datastore.AdvanceRepositorySystemVersion(r)
			return s.store.UpdateRepository(ctx, r, expected)
		}
	}
	if err != nil {
		return err
	}
	return admission.MarkInfrastructureDeletion(ctx, s.store, resource, now, actor)
}

func (s *Server) deleteResource(ctx context.Context, id resourceIdentity, repositoryID, refName, sourcePath, actor string, removed ...*parsedEntry) error {
	existing, err := s.lookupResourceByIdentity(ctx, id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			s.log.Info("admit_resources: delete skipped for missing resource",
				zap.String("kind", id.Kind),
				zap.String("namespace", id.Namespace),
				zap.String("name", id.Name))
			return nil
		}
		s.log.Error("admit_resources: delete lookup failed",
			zap.String("kind", id.Kind),
			zap.String("namespace", id.Namespace),
			zap.String("name", id.Name),
			zap.Error(err))
		return fmt.Errorf("delete %s %s/%s: %w", id.Kind, id.Namespace, id.Name, err)
	}

	// A branch deletion's changed_paths covers every path in the deleted
	// ref's final tree (git-service's compute_changed_paths), including
	// files inherited unchanged from another still-live ref (e.g. a
	// feature branch's full tree, which also contains everything already
	// on main). Only actually delete when this resource's own
	// last-admitted repository AND ref match the ones being deleted --
	// otherwise it is still legitimately reachable elsewhere and this is
	// a phantom deletion from the deleted ref's now-irrelevant tree
	// contents. Both must match: resource identity (namespace, kind,
	// name) is not scoped by repository, so two repositories in the same
	// namespace using the same ref name (e.g. both "refs/heads/main")
	// could otherwise collide on the ref check alone.
	if ownRepo, ownRef, ok := resourceOwnership(existing); ok && refName != "" &&
		(ownRepo != repositoryID || ownRef != refName) {
		s.log.Info("admit_resources: delete skipped; resource owned by a different repository/ref",
			zap.String("kind", id.Kind),
			zap.String("namespace", id.Namespace),
			zap.String("name", id.Name),
			zap.String("deleted_repository", repositoryID),
			zap.String("deleted_ref", refName),
			zap.String("owning_repository", ownRepo),
			zap.String("owning_ref", ownRef))
		return nil
	}

	var uid string
	var deleteErr error
	if id.Kind == "Namespace" || id.Kind == "Repository" {
		var commit, namespace string
		switch r := existing.(type) {
		case *datastore.Namespace:
			commit = r.GitCommitSHA
		case *datastore.Repository:
			commit, namespace = r.GitCommitSHA, r.Namespace
		}
		if commit == "" || len(removed) == 0 || removed[0] == nil {
			return fmt.Errorf("infrastructure deletion requires admitted and removed manifest evidence")
		}
		content, err := s.git.ReadFile(ctx, repositoryID, sourcePath, commit)
		if err != nil {
			return fmt.Errorf("read admitted deletion manifest: %w", err)
		}
		parsed, body, err := s.parser.ParseResource(bytes.NewReader(content))
		if err != nil {
			return err
		}
		entry, ok, err := newParsedEntry(sourcePath, parsed, body, namespace)
		if err != nil || !ok || entry.contentHash != removed[0].contentHash {
			return fmt.Errorf("removed infrastructure manifest does not match admitted revision")
		}
	}
	switch r := existing.(type) {
	case *datastore.Product:
		if r.DeletionTimestamp != nil {
			return nil
		}
		owners, ok := s.store.(datastore.OwnerReferenceStore)
		if !ok {
			return fmt.Errorf("product deletion requires owner-reference datastore support")
		}
		lookupStarted := time.Now()
		blocked, checkErr := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{
			Namespace: r.Namespace, RepositoryID: r.RepositoryID,
		}, r.UID)
		productDeletionDependentLookupDuration.Observe(time.Since(lookupStarted).Seconds())
		if checkErr != nil {
			return fmt.Errorf("check product deletion dependents: %w", checkErr)
		}
		if blocked {
			productDeletionBlockedTotal.Inc()
			s.log.Info("Product deletion blocked by ProductVariant owner reference", zap.String("namespace", r.Namespace), zap.String("name", r.Name), zap.String("uid", r.UID))
			return fmt.Errorf("product %s/%s has blocking ProductVariants", r.Namespace, r.Name)
		}
		lifecycle, ok := s.store.(datastore.ProductLifecycleStore)
		if !ok {
			return fmt.Errorf("product deletion requires lifecycle datastore support")
		}
		_, markErr := lifecycle.MarkProductTerminating(ctx, r.UID, r.ResourceVersion, "gitstore.dev/foreground-deletion", s.clock.Now().UTC())
		if markErr != nil {
			return fmt.Errorf("mark Product deletion: %w", markErr)
		}
		return nil
	case *datastore.CategoryTaxonomy:
		owners, ok := s.store.(datastore.OwnerReferenceStore)
		if !ok {
			return fmt.Errorf("category deletion requires owner-reference datastore support")
		}
		lookupStarted := time.Now()
		hasChildren, lookupErr := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{
			Namespace: r.Namespace, RepositoryID: r.RepositoryID,
		}, r.UID)
		categoryDeletionDependentLookupDuration.Observe(time.Since(lookupStarted).Seconds())
		if lookupErr != nil {
			s.log.Warn("category deletion dependent lookup failed",
				zap.String("namespace", r.Namespace),
				zap.String("name", r.Name),
				zap.Error(lookupErr))
			return fmt.Errorf("check category deletion dependents: %w", lookupErr)
		}
		if hasChildren {
			categoryDeletionBlockedTotal.Inc()
			s.log.Info("category deletion blocked by child owner reference",
				zap.String("namespace", r.Namespace),
				zap.String("name", r.Name),
				zap.String("uid", r.UID))
			return fmt.Errorf("%w: %s/%s", errCategoryDeletionBlocked, r.Namespace, r.Name)
		}
		lifecycle, ok := s.store.(datastore.CategoryTaxonomyDeletionStore)
		if !ok {
			return fmt.Errorf("category deletion requires lifecycle datastore support")
		}
		_, markErr := lifecycle.MarkCategoryTaxonomyDeletion(ctx, r.Namespace, r.Name, r.ResourceVersion, s.clock.Now().UTC())
		if markErr != nil {
			return fmt.Errorf("mark category deletion: %w", markErr)
		}
		return nil
	case *datastore.Collection:
		uid = r.UID
		deleteErr = s.store.DeleteCollectionWithResourceVersion(ctx, r.UID, r.ResourceVersion)
	case *datastore.ProductVariant:
		uid = r.UID
		deleteErr = s.store.DeleteProductVariantWithResourceVersion(ctx, r.UID, r.ResourceVersion)
	case *datastore.Namespace:
		if err := s.validateNamespaceAuthoringTarget(ctx, repositoryID, sourcePath, r.Name); err != nil {
			return err
		}
		if r.SourcePath != sourcePath || r.GitRef != refName {
			return fmt.Errorf("namespace deletion provenance mismatch")
		}
		return s.markPushedInfrastructureDeletion(ctx, r, repositoryID, refName, sourcePath, actor)
	case *datastore.Repository:
		if err := s.validateRepositoryAuthoringTarget(ctx, repositoryID, sourcePath, r.Namespace, r.Name); err != nil {
			s.log.Info("admit_resources: Repository deletion skipped; invalid authoring target",
				zap.String("namespace", r.Namespace),
				zap.String("name", r.Name),
				zap.String("repository_id", repositoryID),
				zap.String("source_path", sourcePath),
				zap.Error(err))
			return nil
		}
		if r.GitRef == "" || refName == "" || r.GitRef != refName {
			s.log.Info("admit_resources: Repository deletion skipped; resource owned by a different ref",
				zap.String("namespace", r.Namespace),
				zap.String("name", r.Name),
				zap.String("deleted_ref", refName),
				zap.String("owning_ref", r.GitRef))
			return nil
		}
		return s.markPushedInfrastructureDeletion(ctx, r, repositoryID, refName, sourcePath, actor)
	case *datastore.File:
		uid = r.UID
		deleteErr = s.store.DeleteFileWithResourceVersion(ctx, r.UID, r.ResourceVersion)
	default:
		deleteErr = datastore.ErrNotFound
	}
	if deleteErr != nil {
		s.log.Error("admit_resources: delete resource failed",
			zap.String("kind", id.Kind),
			zap.String("namespace", id.Namespace),
			zap.String("name", id.Name),
			zap.String("uid", uid),
			zap.Error(deleteErr))
		return fmt.Errorf("delete %s %s/%s: %w", id.Kind, id.Namespace, id.Name, deleteErr)
	}
	s.log.Info("admit_resources: resource deleted",
		zap.String("kind", id.Kind),
		zap.String("namespace", id.Namespace),
		zap.String("name", id.Name),
		zap.String("uid", uid))
	return nil
}

// resourceOwnership returns existing's (RepositoryID, GitRef) and true, or
// ("", "", false) for a kind that has no such fields (Namespace isn't
// scoped to a single repository, and deleteResource's Namespace case
// returns before this check runs anyway).
func resourceOwnership(existing any) (string, string, bool) {
	switch r := existing.(type) {
	case *datastore.Product:
		return r.RepositoryID, r.GitRef, true
	case *datastore.CategoryTaxonomy:
		return r.RepositoryID, r.GitRef, true
	case *datastore.Collection:
		return r.RepositoryID, r.GitRef, true
	case *datastore.ProductVariant:
		return r.RepositoryID, r.GitRef, true
	case *datastore.File:
		return r.RepositoryID, r.GitRef, true
	default:
		return "", "", false
	}
}

// detectCycles delegates to the admission/catalog package implementation.
// Kept as a thin wrapper so the batch pre-processing in AdmitResources (which
// still needs topo-sort ordering) does not need to import admcatalog directly.
func detectCycles(parentMap map[string]string) map[string]bool {
	return admcatalog.DetectCycles(parentMap)
}

// topoSortCategories delegates to the admission/catalog package implementation.
func topoSortCategories(parentMap map[string]string, cycleMembers map[string]bool) []string {
	return admcatalog.TopoSortCategories(parentMap, cycleMembers)
}

// isRefNotFound returns true when a gRPC error carries a NotFound status code,
// which is what the git service returns when a ref does not exist.
func isRefNotFound(err error) bool {
	return grpcstatus.Code(err) == codes.NotFound
}

func isZeroOID(sha string) bool {
	if sha == "" {
		return false
	}
	for _, r := range sha {
		if r != '0' {
			return false
		}
	}
	return true
}

func nextResourceVersion(current string) string {
	n, err := strconv.ParseInt(current, 10, 64)
	if err != nil || n < 1 {
		return "1"
	}
	return fmt.Sprintf("%d", n+1)
}

func specBodyChanged(existingSpec []byte, existingBody string, specJSON []byte, body []byte) bool {
	return !bytes.Equal(existingSpec, specJSON) || existingBody != string(body)
}

// resolvedCategoryOwnerReferences writes only controller-managed category
// ownership. Author manifests cannot supply ownerReferences, and unresolved
// references intentionally produce no reverse projection.
func (s *Server) resolvedCategoryOwnerReferences(ctx context.Context, namespace string, reference *catalog.ObjectReference, blockOwnerDeletion bool) json.RawMessage {
	references, err := ResolvedCategoryOwnerReferences(ctx, s.store, namespace, reference, blockOwnerDeletion)
	if err != nil {
		// Preserve this admission path's existing behavior exactly (fail
		// open to an empty owner reference rather than rejecting the push) —
		// only log it, now that the shared function actually reports it.
		s.log.Warn("resolved_category_owner_references: category lookup failed",
			zap.String("namespace", namespace),
			zap.String("name", reference.Name),
			zap.Error(err))
		return json.RawMessage(`[]`)
	}
	return references
}

// ResolvedCategoryOwnerReferences resolves reference (by name, scoped to
// namespace) against store and returns the CategoryTaxonomy-kind
// OwnerReferences JSON payload it implies. It returns an empty array (never
// an error) when reference is absent or genuinely does not resolve to an
// existing, non-terminating category — that is a legitimate, expected
// outcome. It returns a non-nil error only for a real lookup/marshal
// failure, so a transient datastore blip is distinguishable from "no such
// category" by callers that must retry rather than silently commit an empty
// projection (spec 062: a caller that commits CategoryResolved=True without
// actually establishing the owner reference leaves DecoupleCategoryProducts
// unable to find that Product later). Exported so both admission (this
// package) and the updateProductStatus resolver (spec 062) can synthesize
// the exact same owner-reference shape from a single implementation, rather
// than maintaining two independent copies that could drift.
func ResolvedCategoryOwnerReferences(ctx context.Context, store datastore.Datastore, namespace string, reference *catalog.ObjectReference, blockOwnerDeletion bool) (json.RawMessage, error) {
	empty := json.RawMessage(`[]`)
	if reference == nil || reference.Name == "" {
		return empty, nil
	}
	owner, err := store.GetCategoryTaxonomyByName(ctx, namespace, reference.Name)
	if errors.Is(err, datastore.ErrNotFound) {
		return empty, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolved category owner references: look up %s/%s: %w", namespace, reference.Name, err)
	}
	if owner == nil || owner.DeletionTimestamp != nil {
		return empty, nil
	}
	references, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion:         owner.APIVersion,
		Kind:               "CategoryTaxonomy",
		Name:               owner.Name,
		UID:                owner.UID,
		BlockOwnerDeletion: blockOwnerDeletion,
		RepositoryID:       owner.RepositoryID,
	}})
	if err != nil {
		return nil, fmt.Errorf("resolved category owner references: marshal %s/%s: %w", namespace, reference.Name, err)
	}
	return references, nil
}

// resolvedProductVariantOwnerReferences projects an admitted ProductVariant's
// productRef into the datastore's reverse owner index. Product references that
// cannot yet resolve deliberately remain absent: that preserves Git's
// order-independent authoring model. A resolved, terminating Product is not a
// valid target for a new dependent, however, because it could strand that
// dependent behind foreground deletion.
func (s *Server) resolvedProductVariantOwnerReferences(ctx context.Context, namespace string, reference *catalog.ObjectReference) (json.RawMessage, bool) {
	empty := json.RawMessage(`[]`)
	if reference == nil || reference.Name == "" {
		return empty, false
	}
	owner, err := s.store.GetProductByName(ctx, namespace, reference.Name)
	if err != nil || owner == nil {
		return empty, false
	}
	if owner.DeletionTimestamp != nil {
		return empty, true
	}
	references, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion:         owner.APIVersion,
		Kind:               "Product",
		Name:               owner.Name,
		UID:                owner.UID,
		BlockOwnerDeletion: true,
		RepositoryID:       owner.RepositoryID,
	}})
	if err != nil {
		s.log.Warn("admit_resources: marshal product owner reference failed",
			zap.String("namespace", namespace), zap.String("name", reference.Name), zap.Error(err))
		return empty, false
	}
	return references, false
}

func (s *Server) admitNamespace(
	ctx context.Context,
	resource *catalog.NamespaceResource,
	body []byte,
	admCtx AdmissionContext,
	sourcePath string,
	op admission.Operation,
	rawExisting any,
) {
	name := resource.Metadata.Name
	existing, _ := rawExisting.(*datastore.Namespace)
	if op == admission.OperationUpdate && existing == nil {
		s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonNamespaceNotFound, false, namespaceadmission.ErrNamespaceNotFound)
		return
	}
	if namespaceadmission.IsBootstrap(name) {
		s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonBootstrapNamespace, existing != nil, namespaceadmission.ErrBootstrapNamespace)
		return
	}
	if op == admission.OperationCreate && existing != nil {
		s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonNamespaceAlreadyExists, true, namespaceadmission.ErrNamespaceAlreadyExists)
		return
	}
	if existing != nil && existing.DeletionTimestamp != nil {
		s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonNamespaceTerminating, true, namespaceadmission.ErrNamespaceTerminating)
		return
	}
	tier, ok := namespaceadmission.TierFromManifest(resource.Spec.Tier)
	if !ok {
		s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonInvalidTier, existing != nil, fmt.Errorf("unsupported tier %q", resource.Spec.Tier))
		return
	}
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Warn("admit_resources: Namespace rejected",
			zap.String("name", name),
			zap.String("operation", string(op)),
			zap.Error(fmt.Errorf("marshal spec: %w", err)))
		return
	}
	ownerReferences := json.RawMessage(`[]`)
	if len(resource.Metadata.OwnerReferences) > 0 {
		ownerReferences, err = json.Marshal(resource.Metadata.OwnerReferences)
		if err != nil {
			s.log.Warn("admit_resources: Namespace rejected",
				zap.String("name", name),
				zap.String("operation", string(op)),
				zap.Error(fmt.Errorf("marshal owner references: %w", err)))
			return
		}
	}

	for range namespaceadmission.AdmissionWriteAttempts {
		refCheck := func(ctx context.Context) (bool, error) {
			return s.isAdmissionCommitCurrent(ctx, admCtx.RepositoryID, admCtx.RefName, admCtx.CommitSHA), nil
		}
		if namespaceadmission.RecheckAuthoringRef(ctx, refCheck) != nil {
			admCtx.markSuperseded()
			return
		}
		if op == admission.OperationCreate && existing != nil {
			s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonNamespaceAlreadyExists, true, namespaceadmission.ErrNamespaceAlreadyExists)
			return
		}
		if op == admission.OperationUpdate && existing == nil {
			s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonNamespaceNotFound, false, namespaceadmission.ErrNamespaceNotFound)
			return
		}
		if existing != nil && existing.DeletionTimestamp != nil {
			s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonNamespaceTerminating, true, namespaceadmission.ErrNamespaceTerminating)
			return
		}

		created := existing == nil
		namespace := existing
		if created {
			namespace = &datastore.Namespace{
				APIVersion:        resource.APIVersion,
				Kind:              resource.Kind,
				UID:               s.ids.NewID(),
				Name:              name,
				Generation:        datastore.NamespaceInitialGeneration,
				ResourceVersion:   datastore.NamespaceInitialResourceVersion,
				Revision:          admCtx.Revision,
				CreationTimestamp: admCtx.Now,
				CreationActor:     admCtx.ActorSubject,
				UpdateTimestamp:   admCtx.Now,
				UpdateActor:       admCtx.ActorSubject,
				Labels:            cloneStringMap(resource.Metadata.Labels),
				Annotations:       cloneStringMap(resource.Metadata.Annotations),
				OwnerReferences:   ownerReferences,
				Finalizers:        append([]string(nil), resource.Metadata.Finalizers...),
				SourcePath:        sourcePath,
				GitCommitSHA:      admCtx.CommitSHA,
				GitRef:            admCtx.RefName,
				Spec:              specJSON,
				Body:              string(body),
				Title:             resource.Spec.Title,
				Tier:              tier,
			}
			datastore.NormalizeNamespaceContract(namespace)
			namespace.Status = namespaceadmission.AdmissionStatus(namespace.Generation, admCtx.Revision, admCtx.Now)
			if namespaceadmission.RecheckAuthoringRef(ctx, refCheck) != nil {
				admCtx.markSuperseded()
				return
			}
			err = s.store.CreateNamespace(ctx, namespace)
		} else if namespaceadmission.TierRank(tier) < namespaceadmission.TierRank(existing.Tier) {
			err = namespaceadmission.ErrTierDemotion
		} else {
			desiredStateChanged := existing.APIVersion != resource.APIVersion ||
				existing.Kind != resource.Kind ||
				specBodyChanged(existing.Spec, existing.Body, specJSON, body)
			metadataChanged := !stringMapsEqual(existing.Labels, resource.Metadata.Labels) ||
				!stringMapsEqual(existing.Annotations, resource.Metadata.Annotations)
			// Namespace owner references are system-owned and authored manifests
			// are rejected when they contain them. Retain any existing system
			// projection unless a future admission path supplies one explicitly.
			ownerReferencesChanged := len(resource.Metadata.OwnerReferences) > 0 && !bytes.Equal(existing.OwnerReferences, ownerReferences)
			provenanceChanged := existing.Revision != admCtx.Revision ||
				existing.SourcePath != sourcePath ||
				existing.GitCommitSHA != admCtx.CommitSHA ||
				existing.GitRef != admCtx.RefName
			if !desiredStateChanged && !metadataChanged && !ownerReferencesChanged && !provenanceChanged {
				return
			}
			expectedResourceVersion := existing.ResourceVersion
			existing.APIVersion = resource.APIVersion
			existing.Kind = resource.Kind
			existing.Revision = admCtx.Revision
			existing.UpdateTimestamp = admCtx.Now
			existing.UpdateActor = admCtx.ActorSubject
			existing.Labels = cloneStringMap(resource.Metadata.Labels)
			existing.Annotations = cloneStringMap(resource.Metadata.Annotations)
			if ownerReferencesChanged {
				existing.OwnerReferences = ownerReferences
			}
			existing.SourcePath = sourcePath
			existing.GitCommitSHA = admCtx.CommitSHA
			existing.GitRef = admCtx.RefName
			existing.Spec = specJSON
			existing.Body = string(body)
			existing.Title = resource.Spec.Title
			existing.Tier = tier
			if desiredStateChanged {
				datastore.AdvanceNamespaceSpecVersion(existing)
			} else {
				datastore.AdvanceNamespaceSystemVersion(existing)
			}
			existing.Status = namespaceadmission.MergeAdmissionStatus(existing.Status, existing.Generation, admCtx.Revision, admCtx.Now)
			if namespaceadmission.RecheckAuthoringRef(ctx, refCheck) != nil {
				admCtx.markSuperseded()
				return
			}
			err = s.store.UpdateNamespace(ctx, existing, expectedResourceVersion)
		}
		if errors.Is(err, datastore.ErrConflict) || errors.Is(err, datastore.ErrAlreadyExists) {
			existing, err = s.store.GetNamespaceByName(ctx, name)
			if err == nil {
				continue
			}
			if errors.Is(err, datastore.ErrNotFound) {
				if op == admission.OperationUpdate {
					s.recordNamespaceAdmissionRejection(name, op, namespaceadmission.ReasonNamespaceNotFound, false, namespaceadmission.ErrNamespaceNotFound)
					return
				}
				existing = nil
				continue
			}
		}
		if err != nil {
			reason := namespaceadmission.ReasonInvalidEnvelope
			switch {
			case errors.Is(err, namespaceadmission.ErrTierDemotion):
				reason = namespaceadmission.ReasonTierDemotion
			case errors.Is(err, datastore.ErrConflict):
				reason = namespaceadmission.ReasonResourceVersionConflict
			case errors.Is(err, datastore.ErrAlreadyExists):
				reason = namespaceadmission.ReasonNamespaceAlreadyExists
			}
			s.recordNamespaceAdmissionRejection(name, op, reason, existing != nil, err)
			return
		}
		return
	}
	s.log.Warn("admit_resources: Namespace rejected after repeated concurrent updates",
		zap.String("namespace", name),
		zap.String("operation", string(op)),
		zap.String("stage", string(namespaceadmission.StagePolicy)),
		zap.String("reason", string(namespaceadmission.ReasonResourceVersionConflict)),
		zap.Bool("conflict", true),
		zap.Int("attempts", namespaceadmission.AdmissionWriteAttempts))
	s.namespaceMetrics.ObserveRejection(namespaceadmission.ReasonResourceVersionConflict)
}

func (s *Server) recordNamespaceAdmissionRejection(
	name string,
	op admission.Operation,
	reason namespaceadmission.Reason,
	existing bool,
	err error,
) {
	s.namespaceMetrics.ObserveRejection(reason)
	s.log.Warn("admit_resources: Namespace rejected",
		zap.String("namespace", name),
		zap.String("operation", string(op)),
		zap.String("stage", string(namespaceadmission.StagePolicy)),
		zap.String("reason", string(reason)),
		zap.Bool("existing", existing),
		zap.Bool("conflict", errors.Is(err, datastore.ErrConflict)),
		zap.Error(err))
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	output := make(map[string]string, len(input))
	maps.Copy(output, input)
	return output
}

func stringMapsEqual(left, right map[string]string) bool {
	return maps.Equal(left, right)
}

func fileAdmissionStatus(generation int64, revision string, now time.Time) []byte {
	status := catalog.FileStatus{
		ObservedGeneration:  generation,
		LastAppliedRevision: revision,
		Conditions: []catalog.Condition{
			{Type: catalog.ConditionAdmissionAccepted, Status: catalog.ConditionTrue, ObservedGeneration: generation, LastTransitionTime: now},
			{Type: catalog.ConditionReady, Status: catalog.ConditionTrue, ObservedGeneration: generation, LastTransitionTime: now},
		},
	}
	b, _ := json.Marshal(status)
	return b
}

const maxFileAdmissionUpdateAttempts = 3

func (s *Server) admitFile(
	ctx context.Context,
	resource *catalog.FileResource,
	body []byte,
	admCtx AdmissionContext,
	sourcePath string,
	op admission.Operation,
	rawExisting any,
) {
	if resource == nil {
		return
	}
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal file spec failed", zap.Error(err))
		return
	}
	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = admCtx.Namespace
	}
	existing, _ := rawExisting.(*datastore.File)
	if existing == nil || op == admission.OperationCreate {
		uid, ok := s.newUID(resource.Kind, resource.Metadata.Name)
		if !ok {
			return
		}
		f := &datastore.File{
			UID: uid, Namespace: namespace, Name: resource.Metadata.Name,
			APIVersion: resource.APIVersion, Kind: resource.Kind,
			Labels: cloneStringMap(resource.Metadata.Labels), Annotations: cloneStringMap(resource.Metadata.Annotations),
			Generation: 1, ResourceVersion: "1", CreationTimestamp: admCtx.Now,
			Revision: admCtx.Revision, RepositoryID: admCtx.RepositoryID, SourcePath: sourcePath,
			GitCommitSHA: admCtx.CommitSHA, GitRef: admCtx.RefName, Spec: specJSON, Body: string(body),
		}
		f.Status = fileAdmissionStatus(1, admCtx.Revision, admCtx.Now)
		if err := s.store.CreateFile(ctx, f); err == nil {
			return
		} else if !errors.Is(err, datastore.ErrAlreadyExists) {
			s.log.Error("admit_resources: create file failed", zap.Error(err))
			return
		}
		// Another replica may have created this identity after operationForEntry
		// observed it as absent. If this admission still owns the ref tip, reload
		// the winner and converge through the resource-version-guarded update
		// path below. A stale admission must leave the winner untouched.
		if !s.isAdmissionCommitCurrent(ctx, admCtx.RepositoryID, admCtx.RefName, admCtx.CommitSHA) {
			return
		}
		existing, err = s.store.GetFileByName(ctx, namespace, resource.Metadata.Name)
		if err != nil {
			s.log.Error("admit_resources: reload File after create conflict failed", zap.Error(err))
			return
		}
	}
	for range maxFileAdmissionUpdateAttempts {
		changedSpecBody := specBodyChanged(existing.Spec, existing.Body, specJSON, body)
		changedMetadata := existing.APIVersion != resource.APIVersion || existing.Kind != resource.Kind ||
			!reflect.DeepEqual(existing.Labels, cloneStringMap(resource.Metadata.Labels)) ||
			!reflect.DeepEqual(existing.Annotations, cloneStringMap(resource.Metadata.Annotations))
		changedProvenance := existing.RepositoryID != admCtx.RepositoryID || existing.SourcePath != sourcePath ||
			existing.GitCommitSHA != admCtx.CommitSHA || existing.GitRef != admCtx.RefName
		if !changedSpecBody && !changedMetadata && !changedProvenance {
			return
		}
		if existingSpecContentType(existing.Spec) != resource.Spec.ContentType {
			s.log.Warn("admit_resources: File contentType is immutable", zap.String("name", resource.Metadata.Name))
			return
		}
		expectedResourceVersion := existing.ResourceVersion
		gen := existing.Generation
		if changedSpecBody {
			gen++
		}
		existing.APIVersion, existing.Kind = resource.APIVersion, resource.Kind
		existing.Labels, existing.Annotations = cloneStringMap(resource.Metadata.Labels), cloneStringMap(resource.Metadata.Annotations)
		existing.Generation, existing.ResourceVersion = gen, nextResourceVersion(expectedResourceVersion)
		existing.Revision, existing.RepositoryID, existing.SourcePath = admCtx.Revision, admCtx.RepositoryID, sourcePath
		existing.GitCommitSHA, existing.GitRef, existing.Spec, existing.Body = admCtx.CommitSHA, admCtx.RefName, specJSON, string(body)
		existing.Status = fileAdmissionStatus(gen, admCtx.Revision, admCtx.Now)
		err := s.store.UpdateFile(ctx, existing, expectedResourceVersion)
		if err == nil {
			return
		}
		if !errors.Is(err, datastore.ErrConflict) {
			s.log.Error("admit_resources: update file failed", zap.Error(err))
			return
		}
		if !s.isAdmissionCommitCurrent(ctx, admCtx.RepositoryID, admCtx.RefName, admCtx.CommitSHA) {
			return
		}
		existing, err = s.store.GetFileByName(ctx, namespace, resource.Metadata.Name)
		if err != nil {
			s.log.Error("admit_resources: reload File after update conflict failed", zap.Error(err))
			return
		}
	}
	s.log.Error("admit_resources: update File conflict retry budget exhausted",
		zap.String("namespace", namespace), zap.String("name", resource.Metadata.Name),
		zap.String("commit_sha", admCtx.CommitSHA))
}

func existingSpecContentType(raw []byte) string {
	var spec catalog.FileSpec
	if json.Unmarshal(raw, &spec) != nil {
		return ""
	}
	return spec.ContentType
}

func (s *Server) admitProduct(
	ctx context.Context,
	resource *catalog.ProductResource,
	body []byte,
	admCtx AdmissionContext,
	sourcePath string,
	op admission.Operation,
	rawExisting any,
) admission.EntryDecision {
	decision := admission.EntryDecision{Kind: "Product", Path: sourcePath}
	if resource != nil {
		decision.Name, decision.Namespace = resource.Metadata.Name, resource.Metadata.Namespace
	}
	failed := func(err error) admission.EntryDecision {
		decision.Outcome, decision.Err = admission.EntryFailed, err
		return decision
	}
	// Lifecycle is author-owned desired state. Persist an explicit default so
	// Git-authored Products and GraphQL-authored Products have identical
	// admitted representations.
	if resource.Spec.Lifecycle.State == "" {
		resource.Spec.Lifecycle.State = "ACTIVE"
	}
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal product spec failed",
			zap.String("name", resource.Metadata.Name), zap.Error(err))
		return failed(fmt.Errorf("marshal product spec: %w", err))
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = admCtx.Namespace
	}
	decision.Namespace = namespace

	existing, _ := rawExisting.(*datastore.Product)
	var oldObject any
	if existing != nil {
		oldObject = existing
		if op == admission.OperationCreate {
			op = admission.OperationUpdate
		}
	} else {
		if op == admission.OperationUpdate {
			s.log.Warn("admit_resources: product update missing stored identity; creating resource",
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace))
		}
		op = admission.OperationCreate
	}
	ownerReferences := s.resolvedCategoryOwnerReferences(ctx, namespace, resource.Spec.CategoryRef, false)

	if d, denied := s.chain.Admit(ctx, admission.AdmissionRequest{
		Object:    resource,
		OldObject: oldObject,
		Kind:      resource.Kind,
		Name:      resource.Metadata.Name,
		Namespace: namespace,
		Operation: op,
		Trigger:   admission.TriggerGitPush,
		Now:       admCtx.Now,
		GitContext: &admission.GitAdmissionContext{
			RepositoryID: admCtx.RepositoryID,
			CommitSHA:    admCtx.CommitSHA,
			RefName:      admCtx.RefName,
			Revision:     admCtx.Revision,
		},
	}).(admission.Denied); denied {
		s.log.Warn("admit_resources: product denied by admission chain",
			zap.String("name", resource.Metadata.Name),
			zap.String("namespace", namespace),
			zap.String("reason", d.Reason))
		decision.Outcome = admission.EntryDenied
		decision.Diagnostics = []admission.Diagnostic{{Reason: "POLICY_DENIED", Message: d.Reason, Level: admission.LevelFailure, File: sourcePath, Field: d.Field}}
		ObserveAdmissionRejection("Product", admission.PhasePostReceive)
		if existing != nil {
			s.recordProductAdmissionDenied(ctx, existing, decision.Diagnostics, admCtx.Now)
		}
		return decision
	}

	if op == admission.OperationCreate {
		uid, ok := s.newUID(resource.Kind, resource.Metadata.Name)
		if !ok {
			return failed(fmt.Errorf("generate product UID"))
		}
		p := &datastore.Product{
			UID:               uid,
			Namespace:         namespace,
			Name:              resource.Metadata.Name,
			APIVersion:        resource.APIVersion,
			Kind:              resource.Kind,
			Labels:            resource.Metadata.Labels,
			Annotations:       resource.Metadata.Annotations,
			OwnerReferences:   ownerReferences,
			Generation:        1,
			ResourceVersion:   "1",
			CreationTimestamp: admCtx.Now,
			CreationActor:     admCtx.ActorSubject,
			UpdateTimestamp:   admCtx.Now,
			UpdateActor:       admCtx.ActorSubject,
			Revision:          admCtx.Revision,
			RepositoryID:      admCtx.RepositoryID,
			SourcePath:        sourcePath,
			GitCommitSHA:      admCtx.CommitSHA,
			GitRef:            admCtx.RefName,
			Spec:              specJSON,
			Body:              string(body),
		}
		p.Status = admissionAcceptedStatus(1, admCtx.Revision, admCtx.Now)
		if cerr := s.store.CreateProduct(ctx, p); cerr != nil {
			s.log.Error("admit_resources: create product failed",
				zap.String("name", resource.Metadata.Name), zap.Error(cerr))
			return failed(fmt.Errorf("create product: %w", cerr))
		}
	} else {
		changedSpecBody := specBodyChanged(existing.Spec, existing.Body, specJSON, body)
		changedMetadata := existing.APIVersion != resource.APIVersion ||
			existing.Kind != resource.Kind ||
			!reflect.DeepEqual(existing.Labels, resource.Metadata.Labels) ||
			!reflect.DeepEqual(existing.Annotations, resource.Metadata.Annotations)
		changedProvenance := existing.RepositoryID != admCtx.RepositoryID ||
			existing.SourcePath != sourcePath
		changedOwnerReferences := !bytes.Equal(existing.OwnerReferences, ownerReferences)
		changedAdmission := !productAdmissionAccepted(existing.Status)
		if !changedSpecBody && !changedMetadata && !changedProvenance && !changedOwnerReferences && !changedAdmission {
			decision.Outcome = admission.EntryNoOp
			return decision
		}
		gen := existing.Generation
		if changedSpecBody {
			gen++
		}
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.OwnerReferences = ownerReferences
		existing.Generation = gen
		existing.ResourceVersion = nextResourceVersion(existing.ResourceVersion)
		existing.UpdateTimestamp = admCtx.Now
		existing.UpdateActor = admCtx.ActorSubject
		existing.Revision = admCtx.Revision
		existing.RepositoryID = admCtx.RepositoryID
		existing.SourcePath = sourcePath
		existing.GitCommitSHA = admCtx.CommitSHA
		existing.GitRef = admCtx.RefName
		existing.Spec = specJSON
		existing.Body = string(body)
		existing.Status = admissionAcceptedStatus(gen, admCtx.Revision, admCtx.Now)
		if uerr := s.store.UpdateProduct(ctx, existing); uerr != nil {
			s.log.Error("admit_resources: update product failed",
				zap.String("name", resource.Metadata.Name), zap.Error(uerr))
			return failed(fmt.Errorf("update product: %w", uerr))
		}
	}
	decision.Outcome = admission.EntryAccepted
	return decision
}

func (s *Server) recordProductAdmissionDenied(ctx context.Context, existing *datastore.Product, diagnostics []admission.Diagnostic, now time.Time) {
	current := existing
	for range maxRepositoryAdmissionUpdateAttempts {
		var status catalog.ProductStatus
		if len(current.Status) > 0 && json.Unmarshal(current.Status, &status) != nil {
			return
		}
		denied := catalog.Condition{Type: catalog.ConditionAdmissionAccepted, Status: catalog.ConditionFalse, ObservedGeneration: current.Generation, LastTransitionTime: now, Reason: "AdmissionReportFailed", Message: admission.FormatRejection(diagnostics)}
		conditions, replaced := make([]catalog.Condition, 0, len(status.Conditions)+1), false
		for _, condition := range status.Conditions {
			if condition.Type == catalog.ConditionAdmissionAccepted {
				condition, replaced = denied, true
			}
			conditions = append(conditions, condition)
		}
		if !replaced {
			conditions = append(conditions, denied)
		}
		_, err := s.store.UpdateProductStatus(ctx, current.Namespace, current.Name, datastore.ProductStatusPatch{ResourceVersion: current.ResourceVersion, Conditions: conditions})
		if err == nil || !errors.Is(err, datastore.ErrConflict) {
			return
		}
		current, err = s.store.GetProductByName(ctx, current.Namespace, current.Name)
		if err != nil {
			return
		}
	}
}

func productAdmissionAccepted(raw []byte) bool {
	var status catalog.ProductStatus
	if len(raw) == 0 || json.Unmarshal(raw, &status) != nil {
		return true
	}
	for _, condition := range status.Conditions {
		if condition.Type == catalog.ConditionAdmissionAccepted {
			return condition.Status != catalog.ConditionFalse
		}
	}
	return true
}

func (s *Server) admitCollection(
	ctx context.Context,
	resource *catalog.CollectionResource,
	body []byte,
	admCtx AdmissionContext,
	sourcePath string,
	op admission.Operation,
	rawExisting any,
) {
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal collection spec failed",
			zap.String("name", resource.Metadata.Name), zap.Error(err))
		return
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = admCtx.Namespace
	}

	existing, _ := rawExisting.(*datastore.Collection)
	var oldObject any
	if existing != nil {
		oldObject = existing
		if op == admission.OperationCreate {
			op = admission.OperationUpdate
		}
	} else {
		if op == admission.OperationUpdate {
			s.log.Warn("admit_resources: collection update missing stored identity; creating resource",
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace))
		}
		op = admission.OperationCreate
	}

	if d, denied := s.chain.Admit(ctx, admission.AdmissionRequest{
		Object:    resource,
		OldObject: oldObject,
		Kind:      resource.Kind,
		Name:      resource.Metadata.Name,
		Namespace: namespace,
		Operation: op,
		Trigger:   admission.TriggerGitPush,
		Now:       admCtx.Now,
		GitContext: &admission.GitAdmissionContext{
			RepositoryID: admCtx.RepositoryID,
			CommitSHA:    admCtx.CommitSHA,
			RefName:      admCtx.RefName,
			Revision:     admCtx.Revision,
		},
	}).(admission.Denied); denied {
		s.log.Warn("admit_resources: collection denied by admission chain",
			zap.String("name", resource.Metadata.Name),
			zap.String("namespace", namespace),
			zap.String("reason", d.Reason))
		return
	}

	if op == admission.OperationCreate {
		uid, ok := s.newUID(resource.Kind, resource.Metadata.Name)
		if !ok {
			return
		}
		c := &datastore.Collection{
			UID:               uid,
			Namespace:         namespace,
			Name:              resource.Metadata.Name,
			APIVersion:        resource.APIVersion,
			Kind:              resource.Kind,
			Labels:            resource.Metadata.Labels,
			Annotations:       resource.Metadata.Annotations,
			Generation:        1,
			ResourceVersion:   "1",
			CreationTimestamp: admCtx.Now,
			Revision:          admCtx.Revision,
			RepositoryID:      admCtx.RepositoryID,
			SourcePath:        sourcePath,
			GitCommitSHA:      admCtx.CommitSHA,
			GitRef:            admCtx.RefName,
			Spec:              specJSON,
			Body:              string(body),
		}
		c.Status = admissionAcceptedStatus(1, admCtx.Revision, admCtx.Now)
		if cerr := s.store.CreateCollection(ctx, c); cerr != nil {
			s.log.Error("admit_resources: create collection failed",
				zap.String("name", resource.Metadata.Name), zap.Error(cerr))
		}
	} else {
		changedSpecBody := specBodyChanged(existing.Spec, existing.Body, specJSON, body)
		changedMetadata := existing.APIVersion != resource.APIVersion ||
			existing.Kind != resource.Kind ||
			!reflect.DeepEqual(existing.Labels, resource.Metadata.Labels) ||
			!reflect.DeepEqual(existing.Annotations, resource.Metadata.Annotations)
		changedProvenance := existing.RepositoryID != admCtx.RepositoryID ||
			existing.SourcePath != sourcePath
		if !changedSpecBody && !changedMetadata && !changedProvenance {
			return
		}
		gen := existing.Generation
		if changedSpecBody {
			gen++
		}
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.Generation = gen
		existing.ResourceVersion = nextResourceVersion(existing.ResourceVersion)
		existing.Revision = admCtx.Revision
		existing.RepositoryID = admCtx.RepositoryID
		existing.SourcePath = sourcePath
		existing.GitCommitSHA = admCtx.CommitSHA
		existing.GitRef = admCtx.RefName
		existing.Spec = specJSON
		existing.Body = string(body)
		existing.Status = admissionAcceptedStatus(gen, admCtx.Revision, admCtx.Now)
		if uerr := s.store.UpdateCollection(ctx, existing); uerr != nil {
			s.log.Error("admit_resources: update collection failed",
				zap.String("name", resource.Metadata.Name), zap.Error(uerr))
		}
	}
}

// admitProductVariant stores a ProductVariant after admission checks.
// Product existence is not required at admit time; the controller resolves
// the productRef asynchronously (single-pass catalog authoring support).
func (s *Server) admitProductVariant(
	ctx context.Context,
	resource *catalog.ProductVariantResource,
	body []byte,
	admCtx AdmissionContext,
	sourcePath string,
	op admission.Operation,
	rawExisting any,
) {
	if resource == nil {
		return
	}
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal product_variant spec failed",
			zap.String("name", resource.Metadata.Name), zap.Error(err))
		return
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = admCtx.Namespace
	}

	existing, _ := rawExisting.(*datastore.ProductVariant)
	var oldObject any
	if existing != nil {
		oldObject = existing
		if op == admission.OperationCreate {
			op = admission.OperationUpdate
		}
	} else {
		if op == admission.OperationUpdate {
			s.log.Warn("admit_resources: product_variant update missing stored identity; creating resource",
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace))
		}
		op = admission.OperationCreate
	}
	ownerReferences, parentTerminating := s.resolvedProductVariantOwnerReferences(ctx, namespace, resource.Spec.ProductRef)
	if parentTerminating {
		s.log.Warn("admit_resources: product_variant targets terminating product",
			zap.String("name", resource.Metadata.Name),
			zap.String("namespace", namespace),
			zap.String("product_ref", resource.Spec.ProductRef.Name))
		return
	}

	// Run admission chain; map resulting conditions back to variantAdmitResult.
	admitResult := variantAdmitResult{
		OptionsAccepted: true,
		PricingAccepted: true,
	}
	admReq := admission.AdmissionRequest{
		Object:    resource,
		OldObject: oldObject,
		Kind:      resource.Kind,
		Name:      resource.Metadata.Name,
		Namespace: namespace,
		Operation: op,
		Trigger:   admission.TriggerGitPush,
		Now:       admCtx.Now,
		GitContext: &admission.GitAdmissionContext{
			RepositoryID: admCtx.RepositoryID,
			CommitSHA:    admCtx.CommitSHA,
			RefName:      admCtx.RefName,
			Revision:     admCtx.Revision,
		},
	}
	switch dec := s.chain.Admit(ctx, admReq).(type) {
	case admission.Denied:
		s.log.Warn("admit_resources: product_variant denied by admission chain",
			zap.String("name", resource.Metadata.Name),
			zap.String("namespace", namespace),
			zap.String("reason", dec.Reason))
		return
	case admission.Allowed:
		for _, c := range dec.Conditions {
			switch c.Type {
			case catalog.ConditionProductResolved:
				admitResult.ProductResolved = c.Status
			case catalog.ConditionOptionsAccepted:
				admitResult.OptionsAccepted = c.Status
				admitResult.OptionsMsg = c.Message
			case catalog.ConditionPricingAccepted:
				admitResult.PricingAccepted = c.Status
				admitResult.PricingMsg = c.Message
			}
		}
	}

	productRefName := ""
	if resource.Spec.ProductRef != nil {
		productRefName = resource.Spec.ProductRef.Name
	}

	// Compute resolved summaries.
	admitResult.Resolved = &catalog.ResolvedProductVariantDefinition{
		PriceSet:  computeResolvedPriceSet(s.celEnv, resource.Spec),
		Inventory: computeResolvedInventory(resource.Spec),
	}

	if op == admission.OperationCreate || existing == nil {
		// SKU uniqueness check: only enforced on create so that an update can correct a
		// conflicted variant (e.g. change its SKU away from the conflicting value).
		// On update the resource already owns its identity; a different variant holding
		// the same SKU is a pre-existing data issue that must remain fixable via push.
		if skuOwner, skuErr := s.store.GetProductVariantBySKU(ctx, namespace, resource.Spec.SKU); skuErr == nil && skuOwner != nil && skuOwner.Name != resource.Metadata.Name {
			s.log.Warn("admit_resources: product_variant SKU conflict; incoming resource skipped",
				zap.String("operation", string(op)),
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace),
				zap.String("sku", resource.Spec.SKU),
				zap.String("conflict_name", skuOwner.Name),
				zap.String("conflict_uid", skuOwner.UID))
			return
		} else if skuErr != nil && !errors.Is(skuErr, datastore.ErrNotFound) {
			s.log.Error("admit_resources: product_variant SKU lookup failed",
				zap.String("operation", string(op)),
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace),
				zap.String("sku", resource.Spec.SKU),
				zap.Error(skuErr))
			return
		}
		statusJSON := variantAdmissionStatus(1, admCtx.Revision, admCtx.Now, admitResult)
		uid, ok := s.newUID(resource.Kind, resource.Metadata.Name)
		if !ok {
			return
		}
		v := &datastore.ProductVariant{
			UID:               uid,
			Namespace:         namespace,
			Name:              resource.Metadata.Name,
			APIVersion:        resource.APIVersion,
			Kind:              resource.Kind,
			Labels:            resource.Metadata.Labels,
			Annotations:       resource.Metadata.Annotations,
			Generation:        1,
			ResourceVersion:   "1",
			CreationTimestamp: admCtx.Now,
			Revision:          admCtx.Revision,
			RepositoryID:      admCtx.RepositoryID,
			SourcePath:        sourcePath,
			GitCommitSHA:      admCtx.CommitSHA,
			GitRef:            admCtx.RefName,
			SKU:               resource.Spec.SKU,
			ProductRefName:    productRefName,
			OwnerReferences:   ownerReferences,
			Spec:              specJSON,
			Body:              string(body),
			Status:            statusJSON,
		}
		if cerr := s.store.CreateProductVariant(ctx, v); cerr != nil {
			s.log.Error("admit_resources: create product_variant failed",
				zap.String("name", resource.Metadata.Name), zap.Error(cerr))
		} else {
			s.log.Info("admit_resources: product_variant created",
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace),
				zap.String("sku", resource.Spec.SKU),
				zap.String("uid", v.UID),
				zap.Bool("product_resolved", admitResult.ProductResolved),
				zap.Bool("options_accepted", admitResult.OptionsAccepted),
				zap.Bool("pricing_accepted", admitResult.PricingAccepted))
		}
	} else {
		changedSpecBody := specBodyChanged(existing.Spec, existing.Body, specJSON, body)
		changedMetadata := existing.APIVersion != resource.APIVersion ||
			existing.Kind != resource.Kind ||
			!reflect.DeepEqual(existing.Labels, resource.Metadata.Labels) ||
			!reflect.DeepEqual(existing.Annotations, resource.Metadata.Annotations)
		changedProvenance := existing.RepositoryID != admCtx.RepositoryID ||
			existing.SourcePath != sourcePath
		changedDenorm := existing.SKU != resource.Spec.SKU || existing.ProductRefName != productRefName
		changedOwnerReferences := !bytes.Equal(existing.OwnerReferences, ownerReferences)
		if !changedSpecBody && !changedMetadata && !changedProvenance && !changedDenorm && !changedOwnerReferences {
			return
		}
		gen := existing.Generation
		if changedSpecBody {
			gen++
		}
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.Generation = gen
		existing.ResourceVersion = nextResourceVersion(existing.ResourceVersion)
		existing.Revision = admCtx.Revision
		existing.RepositoryID = admCtx.RepositoryID
		existing.SourcePath = sourcePath
		existing.GitCommitSHA = admCtx.CommitSHA
		existing.GitRef = admCtx.RefName
		existing.SKU = resource.Spec.SKU
		existing.ProductRefName = productRefName
		existing.OwnerReferences = ownerReferences
		existing.Spec = specJSON
		existing.Body = string(body)
		existing.Status = variantAdmissionStatus(gen, admCtx.Revision, admCtx.Now, admitResult)
		if uerr := s.store.UpdateProductVariant(ctx, existing); uerr != nil {
			s.log.Error("admit_resources: update product_variant failed",
				zap.String("name", resource.Metadata.Name), zap.Error(uerr))
		} else {
			s.log.Info("admit_resources: product_variant updated",
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace),
				zap.String("sku", resource.Spec.SKU),
				zap.String("uid", existing.UID),
				zap.Int64("generation", gen),
				zap.Bool("product_resolved", admitResult.ProductResolved),
				zap.Bool("options_accepted", admitResult.OptionsAccepted),
				zap.Bool("pricing_accepted", admitResult.PricingAccepted))
		}
	}
}

// admitCategoryTaxonomyWithContext stores a CategoryTaxonomy with hierarchy context.
// inPushAncestorPaths maps category names that have already been admitted in this
// push to their computed AncestorPath; populated as each category is stored so
// that later categories see the full paths of co-created parents.
// catPushSet is the full set of CategoryTaxonomy AdmissionRequests in this push,
// passed to the chain policy for cross-resource cycle and parent resolution.
func (s *Server) admitCategoryTaxonomyWithContext(
	ctx context.Context,
	resource *catalog.CategoryTaxonomyResource,
	body []byte,
	admCtx AdmissionContext,
	sourcePath string,
	op admission.Operation,
	rawExisting any,
	inPushAncestorPaths map[string]string,
	catPushSet []admission.AdmissionRequest,
) admission.EntryDecision {
	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = admCtx.Namespace
	}
	name := resource.Metadata.Name
	decision := admission.EntryDecision{Kind: "CategoryTaxonomy", Namespace: namespace, Name: name, Path: sourcePath}
	failed := func(err error) admission.EntryDecision {
		decision.Outcome, decision.Err = admission.EntryFailed, err
		return decision
	}

	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal category spec failed",
			zap.String("name", resource.Metadata.Name), zap.Error(err))
		return failed(fmt.Errorf("marshal category spec: %w", err))
	}

	existing, _ := rawExisting.(*datastore.CategoryTaxonomy)
	var oldObject any
	if existing != nil {
		oldObject = existing
		if op == admission.OperationCreate {
			op = admission.OperationUpdate
		}
	} else {
		if op == admission.OperationUpdate {
			s.log.Warn("admit_resources: category update missing stored identity; creating resource",
				zap.String("name", name),
				zap.String("namespace", namespace))
		}
		op = admission.OperationCreate
	}

	// Run admission chain to determine ParentResolved and Acyclic conditions.
	parentResolved := false
	inCycle := false
	admReq := admission.AdmissionRequest{
		Object:    resource,
		OldObject: oldObject,
		Kind:      resource.Kind,
		Name:      name,
		Namespace: namespace,
		Operation: op,
		Trigger:   admission.TriggerGitPush,
		Now:       admCtx.Now,
		GitContext: &admission.GitAdmissionContext{
			RepositoryID: admCtx.RepositoryID,
			CommitSHA:    admCtx.CommitSHA,
			RefName:      admCtx.RefName,
			Revision:     admCtx.Revision,
		},
		PushSet: catPushSet,
	}
	switch dec := s.chain.Admit(ctx, admReq).(type) {
	case admission.Denied:
		s.log.Warn("admit_resources: category denied by admission chain",
			zap.String("name", name),
			zap.String("namespace", namespace),
			zap.String("reason", dec.Reason))
		decision.Outcome = admission.EntryDenied
		decision.Diagnostics = []admission.Diagnostic{{
			Reason: "POLICY_DENIED", Message: dec.Reason, Level: admission.LevelFailure, File: sourcePath, Field: dec.Field,
		}}
		ObserveAdmissionRejection("CategoryTaxonomy", admission.PhasePostReceive)
		if existing != nil {
			s.recordCategoryAdmissionDenied(ctx, existing, decision.Diagnostics, admCtx.Now)
		}
		return decision
	case admission.Allowed:
		for _, c := range dec.Conditions {
			switch c.Type {
			case catalog.ConditionParentResolved:
				parentResolved = c.Status
			case catalog.ConditionAcyclic:
				inCycle = !c.Status
			}
		}
	}

	// Compute parent name and ancestor path.
	parentName := ""
	ancestorPath := name

	if resource.Spec.ParentRef != nil && resource.Spec.ParentRef.Name != "" {
		parentName = resource.Spec.ParentRef.Name

		// Check if parent was already admitted in this push (co-creation).
		// inPushAncestorPaths is populated in topological order so the parent's
		// full computed path is available here even for deep chains (root→child→grandchild).
		if parentPath, inPush := inPushAncestorPaths[parentName]; inPush {
			ancestorPath = parentPath + "/" + name
		} else if parentResolved {
			// Look up parent in DB for its ancestor path.
			parent, perr := s.store.GetCategoryTaxonomyByName(ctx, namespace, parentName)
			if perr == nil && parent != nil {
				ancestorPath = parent.AncestorPath + "/" + name
			}
		}
		// If parent not found: tentative root path stays as `name`.
	}
	ownerReferences := s.resolvedCategoryOwnerReferences(ctx, namespace, resource.Spec.ParentRef, true)

	// Record the computed path immediately so that any sibling category later in
	// this push's topological order sees the correct full path for this node,
	// regardless of whether the DB write below succeeds.
	inPushAncestorPaths[name] = ancestorPath

	if op == admission.OperationCreate || existing == nil {
		statusJSON := categoryAdmissionStatusFull(1, admCtx.Revision, admCtx.Now, parentResolved, inCycle)
		uid, ok := s.newUID(resource.Kind, name)
		if !ok {
			return failed(fmt.Errorf("generate category UID"))
		}
		c := &datastore.CategoryTaxonomy{
			UID:               uid,
			Namespace:         namespace,
			Name:              name,
			APIVersion:        resource.APIVersion,
			Kind:              resource.Kind,
			Labels:            resource.Metadata.Labels,
			Annotations:       resource.Metadata.Annotations,
			OwnerReferences:   ownerReferences,
			Generation:        1,
			ResourceVersion:   "1",
			CreationTimestamp: admCtx.Now,
			Revision:          admCtx.Revision,
			RepositoryID:      admCtx.RepositoryID,
			SourcePath:        sourcePath,
			GitCommitSHA:      admCtx.CommitSHA,
			GitRef:            admCtx.RefName,
			ParentName:        parentName,
			AncestorPath:      ancestorPath,
			Spec:              specJSON,
			Body:              string(body),
			Status:            statusJSON,
		}
		if cerr := s.store.CreateCategoryTaxonomy(ctx, c); cerr != nil {
			s.log.Error("admit_resources: create category failed",
				zap.String("name", name), zap.Error(cerr))
			return failed(fmt.Errorf("create category: %w", cerr))
		}
		s.log.Info("admit_resources: category created",
			zap.String("kind", resource.Kind),
			zap.String("namespace", namespace),
			zap.String("name", name),
			zap.String("ancestor_path", ancestorPath),
			zap.Bool("parent_resolved", parentResolved))
	} else {
		changedSpecBody := specBodyChanged(existing.Spec, existing.Body, specJSON, body)
		changedMetadata := existing.APIVersion != resource.APIVersion ||
			existing.Kind != resource.Kind ||
			!reflect.DeepEqual(existing.Labels, resource.Metadata.Labels) ||
			!reflect.DeepEqual(existing.Annotations, resource.Metadata.Annotations)
		changedProvenance := existing.RepositoryID != admCtx.RepositoryID ||
			existing.SourcePath != sourcePath
		changedHierarchy := existing.ParentName != parentName || existing.AncestorPath != ancestorPath
		changedOwnerReferences := !bytes.Equal(existing.OwnerReferences, ownerReferences)
		// A previously denied update leaves AdmissionAccepted=False; re-admitting
		// the accepted content must clear it.
		changedAdmission := !categoryAdmissionAccepted(existing.Status)
		if !changedSpecBody && !changedMetadata && !changedProvenance && !changedHierarchy && !changedOwnerReferences && !changedAdmission {
			decision.Outcome = admission.EntryNoOp
			return decision
		}
		gen := existing.Generation
		if changedSpecBody {
			gen++
		}
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.OwnerReferences = ownerReferences
		existing.Generation = gen
		existing.ResourceVersion = nextResourceVersion(existing.ResourceVersion)
		existing.Revision = admCtx.Revision
		existing.RepositoryID = admCtx.RepositoryID
		existing.SourcePath = sourcePath
		existing.GitCommitSHA = admCtx.CommitSHA
		existing.GitRef = admCtx.RefName
		existing.ParentName = parentName
		existing.AncestorPath = ancestorPath
		existing.Spec = specJSON
		existing.Body = string(body)
		existing.Status = categoryAdmissionStatusFull(gen, admCtx.Revision, admCtx.Now, parentResolved, inCycle)
		if uerr := s.store.UpdateCategoryTaxonomy(ctx, existing); uerr != nil {
			s.log.Error("admit_resources: update category failed",
				zap.String("name", name), zap.Error(uerr))
			return failed(fmt.Errorf("update category: %w", uerr))
		}
		s.log.Info("admit_resources: category updated",
			zap.String("kind", resource.Kind),
			zap.String("namespace", namespace),
			zap.String("name", name),
			zap.String("ancestor_path", ancestorPath))
	}
	decision.Outcome = admission.EntryAccepted
	return decision
}

// recordCategoryAdmissionDenied marks an existing category's AdmissionAccepted
// condition False without touching spec, body or generation, so the last
// accepted generation stays served while the denial is visible.
func (s *Server) recordCategoryAdmissionDenied(ctx context.Context, existing *datastore.CategoryTaxonomy, diagnostics []admission.Diagnostic, now time.Time) {
	current := existing
	for range maxRepositoryAdmissionUpdateAttempts {
		var status catalog.CategoryTaxonomyStatus
		if len(current.Status) > 0 {
			if err := json.Unmarshal(current.Status, &status); err != nil {
				s.log.Error("admit_resources: decode category status failed", zap.String("name", current.Name), zap.Error(err))
				return
			}
		}
		denied := catalog.Condition{
			Type:               catalog.ConditionAdmissionAccepted,
			Status:             catalog.ConditionFalse,
			ObservedGeneration: current.Generation,
			LastTransitionTime: now,
			Reason:             "AdmissionReportFailed",
			Message:            admission.FormatRejection(diagnostics),
		}
		conditions := make([]catalog.Condition, 0, len(status.Conditions)+1)
		replaced := false
		for _, condition := range status.Conditions {
			if condition.Type == catalog.ConditionAdmissionAccepted {
				condition, replaced = denied, true
			}
			conditions = append(conditions, condition)
		}
		if !replaced {
			conditions = append(conditions, denied)
		}
		_, err := s.store.UpdateCategoryTaxonomyStatus(ctx, current.Namespace, current.Name, datastore.CategoryTaxonomyStatusPatch{
			ResourceVersion: current.ResourceVersion,
			Conditions:      conditions,
		})
		if err == nil {
			return
		}
		if !errors.Is(err, datastore.ErrConflict) {
			s.log.Error("admit_resources: record category denial failed", zap.String("name", current.Name), zap.Error(err))
			return
		}
		current, err = s.store.GetCategoryTaxonomyByName(ctx, current.Namespace, current.Name)
		if err != nil {
			s.log.Error("admit_resources: reload category after conflict failed", zap.String("name", existing.Name), zap.Error(err))
			return
		}
	}
	s.log.Error("admit_resources: record category denial retry budget exhausted", zap.String("name", existing.Name))
}

// categoryAdmissionAccepted reports whether a stored status has no
// AdmissionAccepted=False condition.
func categoryAdmissionAccepted(raw []byte) bool {
	if len(raw) == 0 {
		return true
	}
	var status catalog.CategoryTaxonomyStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return true
	}
	for _, condition := range status.Conditions {
		if condition.Type == catalog.ConditionAdmissionAccepted {
			return condition.Status != catalog.ConditionFalse
		}
	}
	return true
}

// admissionAcceptedStatus builds the initial status JSON with AdmissionAccepted: True (FR-009).
func admissionAcceptedStatus(generation int64, revision string, now time.Time) []byte {
	status := catalog.ProductStatus{
		ObservedGeneration:  generation,
		LastAppliedRevision: revision,
		Conditions: []catalog.Condition{
			{
				Type:               catalog.ConditionAdmissionAccepted,
				Status:             catalog.ConditionTrue,
				ObservedGeneration: generation,
				LastTransitionTime: now,
				Reason:             "AdmittedByHookPipeline",
				Message:            "Resource admitted via the post-receive hook pipeline.",
			},
		},
	}
	b, _ := json.Marshal(status)
	return b
}

// mergeRepositoryAdmissionStatus updates admission-owned status without
// discarding controller-owned observation, conditions, or resolved storage.
func mergeRepositoryAdmissionStatus(raw []byte, generation int64, revision string, now time.Time) []byte {
	var status catalog.RepositoryStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return admissionAcceptedStatus(generation, revision, now)
	}
	status.LastAppliedRevision = revision
	accepted := catalog.Condition{
		Type:               catalog.ConditionAdmissionAccepted,
		Status:             catalog.ConditionTrue,
		ObservedGeneration: generation,
		LastTransitionTime: now,
		Reason:             "AdmittedByHookPipeline",
		Message:            "Resource admitted via the post-receive hook pipeline.",
	}
	// Do not preallocate from untrusted persisted data: a corrupt or hostile
	// condition count could overflow len+1 before allocation.
	var conditions []catalog.Condition
	for _, condition := range status.Conditions {
		if condition.Type != catalog.ConditionAdmissionAccepted {
			conditions = append(conditions, condition)
		}
	}
	status.Conditions = append([]catalog.Condition{accepted}, conditions...)
	b, err := json.Marshal(status)
	if err != nil {
		return admissionAcceptedStatus(generation, revision, now)
	}
	return b
}

// variantAdmitResult carries the results of all admission checks for a ProductVariant.
type variantAdmitResult struct {
	ProductResolved bool
	OptionsAccepted bool
	OptionsMsg      string
	PricingAccepted bool
	PricingMsg      string
	Resolved        *catalog.ResolvedProductVariantDefinition
}

// variantAdmissionStatus builds the status JSON for a ProductVariant from admission results.
func variantAdmissionStatus(generation int64, revision string, now time.Time, r variantAdmitResult) []byte {
	condBool := func(b bool) catalog.ConditionStatus {
		if b {
			return catalog.ConditionTrue
		}
		return catalog.ConditionFalse
	}
	optionsCond := catalog.Condition{
		Type:               catalog.ConditionOptionsAccepted,
		Status:             condBool(r.OptionsAccepted),
		ObservedGeneration: generation,
		LastTransitionTime: now,
	}
	if !r.OptionsAccepted && r.OptionsMsg != "" {
		optionsCond.Reason = "IncompatibleOptions"
		optionsCond.Message = r.OptionsMsg
	}
	pricingCond := catalog.Condition{
		Type:               catalog.ConditionPricingAccepted,
		Status:             condBool(r.PricingAccepted),
		ObservedGeneration: generation,
		LastTransitionTime: now,
	}
	if !r.PricingAccepted && r.PricingMsg != "" {
		pricingCond.Reason = "InvalidCELExpression"
		pricingCond.Message = r.PricingMsg
	}
	status := catalog.ProductVariantStatus{
		ObservedGeneration:  generation,
		LastAppliedRevision: revision,
		Conditions: []catalog.Condition{
			{
				Type:               catalog.ConditionAdmissionAccepted,
				Status:             catalog.ConditionTrue,
				ObservedGeneration: generation,
				LastTransitionTime: now,
				Reason:             "AdmittedByHookPipeline",
				Message:            "Resource admitted via the post-receive hook pipeline.",
			},
			{
				Type:               catalog.ConditionProductResolved,
				Status:             condBool(r.ProductResolved),
				ObservedGeneration: generation,
				LastTransitionTime: now,
			},
			optionsCond,
			pricingCond,
		},
		Resolved: r.Resolved,
	}
	b, _ := json.Marshal(status)
	return b
}

// computeResolvedPriceSet builds a ResolvedPriceSetDefinition summary from the spec.
// compiledExpressions counts CEL expressions that parse without error.
// env may be nil; in that case compiledExpressions is always 0.
func computeResolvedPriceSet(env *cel.Env, spec catalog.ProductVariantSpec) *catalog.ResolvedPriceSetDefinition {
	if spec.Pricing == nil || spec.Pricing.PriceSet == nil {
		return nil
	}
	ps := spec.Pricing.PriceSet
	currencySet := make(map[string]struct{})
	strategySet := make(map[string]struct{})
	var compiled int32
	for _, pt := range ps.Prices {
		if pt.CurrencyCode != "" {
			currencySet[pt.CurrencyCode] = struct{}{}
		}
		if pt.Strategy != nil && pt.Strategy.Type != "" {
			strategySet[pt.Strategy.Type] = struct{}{}
		}
		if env != nil && pt.Eligibility != nil {
			for _, c := range pt.Eligibility.Constraints {
				if _, iss := env.Parse(c.Expression); iss == nil || iss.Err() == nil {
					compiled++
				}
			}
		}
	}
	currencies := make([]string, 0, len(currencySet))
	for c := range currencySet {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	strategies := make([]string, 0, len(strategySet))
	for s := range strategySet {
		strategies = append(strategies, s)
	}
	sort.Strings(strategies)
	return &catalog.ResolvedPriceSetDefinition{
		Name:                ps.Name,
		PriceCount:          int64(len(ps.Prices)),
		Currencies:          currencies,
		Strategies:          strategies,
		CompiledExpressions: compiled,
	}
}

// computeResolvedInventory builds a ResolvedInventoryDefinition from the spec.
func computeResolvedInventory(spec catalog.ProductVariantSpec) *catalog.ResolvedInventoryDefinition {
	if spec.Inventory == nil {
		return nil
	}
	return &catalog.ResolvedInventoryDefinition{
		Managed: spec.Inventory.Managed,
		Policy:  spec.Inventory.Policy,
	}
}

// categoryAdmissionStatusFull builds the initial status JSON for a CategoryTaxonomy,
// including Acyclic condition (T032) and ParentResolved based on actual resolution (T033).
func categoryAdmissionStatusFull(generation int64, revision string, now time.Time, parentResolved bool, inCycle bool) []byte {
	parentStatus := catalog.ConditionFalse
	if parentResolved {
		parentStatus = catalog.ConditionTrue
	}
	acyclicStatus := catalog.ConditionTrue
	if inCycle {
		acyclicStatus = catalog.ConditionFalse
	}
	status := catalog.CategoryTaxonomyStatus{
		ObservedGeneration:  generation,
		LastAppliedRevision: revision,
		Conditions: []catalog.Condition{
			{
				Type:               catalog.ConditionAdmissionAccepted,
				Status:             catalog.ConditionTrue,
				ObservedGeneration: generation,
				LastTransitionTime: now,
				Reason:             "AdmittedByHookPipeline",
				Message:            "Resource admitted via the post-receive hook pipeline.",
			},
			{
				Type:               catalog.ConditionParentResolved,
				Status:             parentStatus,
				ObservedGeneration: generation,
				LastTransitionTime: now,
			},
			{
				Type:               catalog.ConditionAcyclic,
				Status:             acyclicStatus,
				ObservedGeneration: generation,
				LastTransitionTime: now,
			},
		},
	}
	b, _ := json.Marshal(status)
	return b
}
