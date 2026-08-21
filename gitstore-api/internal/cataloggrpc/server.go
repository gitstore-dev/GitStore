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
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
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

const namespaceAdmissionWriteAttempts = 4

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
	store    datastore.Datastore
	git      GitReader
	log      *zap.Logger
	eventBus *eventbus.Bus // nil-safe: publish is skipped if unset (e.g. in older tests)

	parser ResourceParser
	clock  apiruntime.Clock
	ids    apiruntime.IDGenerator
	celEnv *cel.Env // shared, constructed once; nil means CEL unavailable (skip rather than reject)
	chain  *admission.Chain
}

// ServerDeps contains dependencies for the CatalogService gRPC server.
type ServerDeps struct {
	Store                   datastore.Datastore
	GitReader               GitReader
	GitClient               *gitclient.Client
	Logger                  *zap.Logger
	Parser                  ResourceParser
	Clock                   apiruntime.Clock
	IDGenerator             apiruntime.IDGenerator
	CELEnv                  *cel.Env
	ExtraValidatingPolicies []admission.ValidatingAdmissionPolicy
	// EventBus receives a change notification after every successful
	// CategoryTaxonomy create/update/delete, fanning out to GraphQL watch
	// subscriptions (spec 040, research.md R2). Optional — nil disables
	// publishing (e.g. in tests that don't exercise the watch API).
	EventBus *eventbus.Bus
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
	return &Server{
		store:    deps.Store,
		git:      git,
		log:      deps.Logger,
		eventBus: deps.EventBus,
		parser:   parser,
		clock:    clock,
		ids:      ids,
		celEnv:   celEnv,
		chain:    chain,
	}, nil
}

// publishCategoryTaxonomyEvent fans out a change notification for the
// EventBus subscribers of GraphQL watch queries (spec 040). No-op when
// eventBus is nil (e.g. tests that construct Server directly).
func (s *Server) publishCategoryTaxonomyEvent(evType eventbus.EventType, c *datastore.CategoryTaxonomy) {
	if s.eventBus == nil || c == nil {
		return
	}
	s.eventBus.Publish(eventbus.Event{
		Type:            evType,
		Kind:            "CategoryTaxonomy",
		Namespace:       c.Namespace,
		Name:            c.Name,
		ResourceVersion: c.ResourceVersion,
		Object:          c,
	})
}

// publishProductEvent fans out a change notification for the EventBus
// subscribers of the watchProducts GraphQL subscription (spec 042). No-op
// when eventBus is nil (e.g. tests that construct Server directly).
func (s *Server) publishProductEvent(evType eventbus.EventType, p *datastore.Product) {
	if s.eventBus == nil || p == nil {
		return
	}
	s.eventBus.Publish(eventbus.Event{
		Type:            evType,
		Kind:            "Product",
		Namespace:       p.Namespace,
		Name:            p.Name,
		ResourceVersion: p.ResourceVersion,
		Object:          p,
	})
}

func (s *Server) publishNamespaceEvent(evType eventbus.EventType, namespace *datastore.Namespace) {
	if s.eventBus == nil || namespace == nil {
		return
	}
	s.eventBus.Publish(eventbus.Event{
		Type:            evType,
		Kind:            "Namespace",
		Name:            namespace.Name,
		ResourceVersion: namespace.ResourceVersion,
		Object:          namespace,
	})
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
	var allErrors []*catalogv1.ValidationError

	for _, blob := range req.Blobs {
		// Opt-in: blobs not starting with `---` are not product resources.
		trimmed := bytes.TrimLeft(blob.Content, " \t\r\n")
		if !bytes.HasPrefix(trimmed, []byte("---")) {
			continue
		}

		parsed, _, err := s.parser.ParseResource(bytes.NewReader(blob.Content))
		if err == nil && parsed != nil && parsed.Namespace != nil {
			err = s.validateNamespaceAuthoringTarget(ctx, req.RepositoryId, blob.Path, parsed.Namespace.Metadata.Name)
		}
		if err == nil && parsed != nil && parsed.CategoryTaxonomy != nil {
			category := parsed.CategoryTaxonomy
			if category.Spec.ParentRef != nil && category.Spec.ParentRef.Name != "" {
				namespace, resolveErr := s.resolveNamespaceIdentifier(ctx, req.RepositoryId)
				if resolveErr != nil {
					err = fmt.Errorf("resolve category namespace: %w", resolveErr)
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

		s.log.Warn("validate_resources: pre-receive rejection",
			zap.String("path", blob.Path),
			zap.Error(err))

		// Convert the error string into ValidationError messages.
		// validate.ParseResource returns a joined error string; split on "; " and "\n".
		msgs := splitValidationErrors(err.Error())
		for _, msg := range msgs {
			ve := errorToValidationError(blob.Path, msg)
			allErrors = append(allErrors, ve)
		}
	}

	if len(allErrors) == 0 {
		return &catalogv1.ValidateResourcesResponse{Accepted: true}, nil
	}
	return &catalogv1.ValidateResourcesResponse{
		Accepted: false,
		Errors:   allErrors,
	}, nil
}

// ValidateCategoryTaxonomyDeletion checks proposed trees without changing
// datastore state. The complete old and proposed resource sets allow an atomic
// child deletion or reparenting to satisfy a parent deletion precondition.
func (s *Server) ValidateCategoryTaxonomyDeletion(
	ctx context.Context,
	req *catalogv1.ValidateCategoryTaxonomyDeletionRequest,
) (*catalogv1.ValidateCategoryTaxonomyDeletionResponse, error) {
	if req.GetRepositoryId() == "" {
		return nil, grpcstatus.Error(codes.InvalidArgument, "repository_id is required")
	}
	namespace, err := s.resolveNamespaceIdentifier(ctx, req.GetRepositoryId())
	if err != nil {
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

		deletedCategories := make(map[string]struct{})
		for _, operation := range deriveResourceAdmissionOperations(oldEntries, proposedEntries, nil) {
			if operation.operation == admission.OperationDelete &&
				operation.identity.Kind == "CategoryTaxonomy" {
				deletedCategories[operation.identity.key()] = struct{}{}
			}
		}
		if len(deletedCategories) == 0 {
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
				return &catalogv1.ValidateCategoryTaxonomyDeletionResponse{
					Accepted: false,
					Reason:   "child categories present",
				}, nil
			}
		}
	}

	return &catalogv1.ValidateCategoryTaxonomyDeletionResponse{Accepted: true}, nil
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
	} else if strings.HasPrefix(trimmed, "spec.title exceeds") {
		field = "spec.title"
		constraint = "max=200"
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
	if refName == "" {
		return true
	}

	current, err := s.git.ResolveRef(ctx, repositoryID, refName)
	if err != nil {
		if isRefNotFound(err) {
			if !isZeroOID(commitSHA) {
				s.log.Info("admit_resources: ref no longer exists; stale admission skipped",
					zap.String("repository_id", repositoryID),
					zap.String("ref_name", refName),
					zap.String("new_commit_sha", commitSHA))
				return false
			}
			return true
		}
		s.log.Error("admit_resources: resolve ref failed",
			zap.String("repository_id", repositoryID),
			zap.String("ref_name", refName),
			zap.String("new_commit_sha", commitSHA),
			zap.Error(err))
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
		s.log.Info("admit_resources: stale admission skipped",
			zap.String("repository_id", repositoryID),
			zap.String("ref_name", refName),
			zap.String("admitted_commit_sha", commitSHA),
			zap.String("current_commit_sha", current))
		return false
	}
	return true
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
		newCommit = req.GetCommitSha()
	}
	if newCommit == "" {
		s.log.Warn("admit_resources: missing new commit sha",
			zap.String("repository_id", req.RepositoryId),
			zap.String("ref_name", req.RefName))
		return &catalogv1.AdmitResourcesResponse{}, nil
	}

	if !s.isAdmissionCommitCurrent(ctx, req.RepositoryId, req.RefName, newCommit) {
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

	// Extract branch name from ref: "refs/heads/main" → "main"
	branch := strings.TrimPrefix(req.RefName, "refs/heads/")
	revision := branch + "@sha1:" + newCommit
	now := s.clock.Now().UTC()
	actorSubject := strings.TrimSpace(req.GetActorSubject())
	if actorSubject == "" {
		// Preserve compatibility with git-service replicas that predate actor_subject.
		actorSubject = "admission"
	}

	// Build the admission context once so every per-file admit helper can read
	// namespace, commit SHA, ref, and wall-clock time without re-querying the DB.
	admCtx := AdmissionContext{
		RepositoryID: req.RepositoryId,
		Namespace:    repoNamespace,
		ActorSubject: actorSubject,
		CommitSHA:    newCommit,
		RefName:      req.RefName,
		Revision:     revision,
		Now:          now,
	}

	oldEntries := s.loadParsedEntries(ctx, req.RepositoryId, req.GetOldCommitSha(), admCtx.Namespace, req.GetChangedPaths())
	newEntries := s.loadParsedEntries(ctx, req.RepositoryId, newCommit, admCtx.Namespace, req.GetChangedPaths())
	ops := deriveResourceAdmissionOperations(oldEntries, newEntries, req.GetChangedPaths())
	if err := s.applyResourceOperations(ctx, ops, admCtx); err != nil {
		s.log.Error("admit_resources: apply operations failed",
			zap.String("repository_id", req.RepositoryId),
			zap.String("commit_sha", newCommit),
			zap.Error(err))
		if errors.Is(err, errCategoryDeletionBlocked) {
			return nil, grpcstatus.Error(codes.FailedPrecondition, "child categories present")
		}
		return nil, grpcstatus.Errorf(codes.Internal, "admit_resources: %v", err)
	}

	return &catalogv1.AdmitResourcesResponse{}, nil
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
	for _, op := range ops {
		switch op.operation {
		case admission.OperationDelete:
			if err := s.deleteResource(ctx, op.identity); err != nil {
				return err
			}
		case admission.OperationCreate, admission.OperationUpdate:
			if op.newEntry == nil {
				continue
			}
			upsertOps[op.identity.key()] = op
			upsertEntries = append(upsertEntries, op.newEntry)
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
	return s.admitParsedEntries(ctx, upsertEntries, admCtx, upsertOps)
}

func (s *Server) admitParsedEntries(
	ctx context.Context,
	entries []*parsedEntry,
	admCtx AdmissionContext,
	explicitOps map[string]resourceAdmissionOperation,
) error {
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
	for _, name := range topoOrder {
		e := categoryEntries[name]
		op, existing, err := s.operationForEntry(ctx, e, explicitOps)
		if err != nil {
			return err
		}
		s.admitCategoryTaxonomyWithContext(ctx, e.parsed.CategoryTaxonomy, e.body, admCtx, e.path, op, existing, inPushAncestorPaths, catPushSet)
	}
	for _, e := range entries {
		if e.parsed.Kind == "CategoryTaxonomy" {
			continue // handled in the topo loop above
		}
		op, existing, err := s.operationForEntry(ctx, e, explicitOps)
		if err != nil {
			return err
		}
		switch e.parsed.Kind {
		case "Product":
			s.admitProduct(ctx, e.parsed.Product, e.body, admCtx, e.path, op, existing)
		case "Collection":
			s.admitCollection(ctx, e.parsed.Collection, e.body, admCtx, e.path, op, existing)
		case "ProductVariant":
			s.admitProductVariant(ctx, e.parsed.ProductVariant, e.body, admCtx, e.path, op, existing)
		case "Namespace":
			s.admitNamespace(ctx, e.parsed.Namespace, e.body, admCtx, e.path, op, existing)
		}
	}
	return nil
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
	default:
		return nil, datastore.ErrNotFound
	}
}

var errCategoryDeletionBlocked = errors.New("category deletion blocked by child categories")

func (s *Server) deleteResource(ctx context.Context, id resourceIdentity) error {
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

	var uid string
	var deleteErr error
	switch r := existing.(type) {
	case *datastore.Product:
		uid = r.UID
		deleteErr = s.store.DeleteProduct(ctx, r.UID)
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
		terminating, markErr := lifecycle.MarkCategoryTaxonomyDeletion(ctx, r.Namespace, r.Name, r.ResourceVersion, s.clock.Now().UTC())
		if markErr != nil {
			return fmt.Errorf("mark category deletion: %w", markErr)
		}
		s.publishCategoryTaxonomyEvent(eventbus.Modified, terminating)
		return nil
	case *datastore.Collection:
		uid = r.UID
		deleteErr = s.store.DeleteCollection(ctx, r.UID)
	case *datastore.ProductVariant:
		uid = r.UID
		deleteErr = s.store.DeleteProductVariant(ctx, r.UID)
	case *datastore.Namespace:
		s.log.Info("admit_resources: Namespace manifest deletion ignored; use deleteNamespace",
			zap.String("name", r.Name))
		return nil
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
	if catTaxonomy, ok := existing.(*datastore.CategoryTaxonomy); ok {
		s.publishCategoryTaxonomyEvent(eventbus.Deleted, catTaxonomy)
	}
	if product, ok := existing.(*datastore.Product); ok {
		s.publishProductEvent(eventbus.Deleted, product)
	}
	s.log.Info("admit_resources: resource deleted",
		zap.String("kind", id.Kind),
		zap.String("namespace", id.Namespace),
		zap.String("name", id.Name),
		zap.String("uid", uid))
	return nil
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

// productCategoryRefName extracts spec.categoryRef.name from a marshaled
// ProductSpec, returning "" for no categoryRef (or on any parse failure —
// unmarshal-once-marshaled-by-us specJSON is never expected to fail, but a
// zero value is the safe default either way).
func productCategoryRefName(specJSON []byte) string {
	var spec catalog.ProductSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil || spec.CategoryRef == nil {
		return ""
	}
	return spec.CategoryRef.Name
}

// resolvedCategoryOwnerReferences writes only controller-managed category
// ownership. Author manifests cannot supply ownerReferences, and unresolved
// references intentionally produce no reverse projection.
func (s *Server) resolvedCategoryOwnerReferences(ctx context.Context, namespace string, reference *catalog.ObjectReference, blockOwnerDeletion bool) json.RawMessage {
	empty := json.RawMessage(`[]`)
	if reference == nil || reference.Name == "" {
		return empty
	}
	owner, err := s.store.GetCategoryTaxonomyByName(ctx, namespace, reference.Name)
	if err != nil || owner == nil || owner.DeletionTimestamp != nil {
		return empty
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
		s.log.Warn("admit_resources: marshal category owner reference failed",
			zap.String("namespace", namespace),
			zap.String("name", reference.Name),
			zap.Error(err))
		return empty
	}
	return references
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
	if namespaceadmission.IsBootstrap(name) {
		s.log.Warn("admit_resources: Namespace rejected",
			zap.String("name", name),
			zap.String("operation", string(op)),
			zap.Bool("existing", existing != nil),
			zap.Error(namespaceadmission.ErrBootstrapNamespace))
		return
	}
	tier, ok := namespaceadmission.TierFromManifest(resource.Spec.Tier)
	if !ok {
		s.log.Warn("admit_resources: Namespace rejected",
			zap.String("name", name),
			zap.String("operation", string(op)),
			zap.Bool("existing", existing != nil),
			zap.Error(fmt.Errorf("unsupported tier %q", resource.Spec.Tier)))
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

	for attempt := 0; attempt < namespaceAdmissionWriteAttempts; attempt++ {
		if !s.isAdmissionCommitCurrent(ctx, admCtx.RepositoryID, admCtx.RefName, admCtx.CommitSHA) {
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
			err = s.store.CreateNamespace(ctx, namespace)
		} else if namespaceadmission.TierRank(tier) < namespaceadmission.TierRank(existing.Tier) {
			err = namespaceadmission.ErrTierDemotion
		} else {
			authorChanged := existing.APIVersion != resource.APIVersion ||
				existing.Kind != resource.Kind ||
				!reflect.DeepEqual(existing.Labels, resource.Metadata.Labels) ||
				!reflect.DeepEqual(existing.Annotations, resource.Metadata.Annotations) ||
				specBodyChanged(existing.Spec, existing.Body, specJSON, body)
			systemChanged := existing.Revision != admCtx.Revision ||
				existing.SourcePath != sourcePath ||
				existing.GitCommitSHA != admCtx.CommitSHA ||
				existing.GitRef != admCtx.RefName
			if !authorChanged && !systemChanged {
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
			existing.SourcePath = sourcePath
			existing.GitCommitSHA = admCtx.CommitSHA
			existing.GitRef = admCtx.RefName
			existing.Spec = specJSON
			existing.Body = string(body)
			existing.Title = resource.Spec.Title
			existing.Tier = tier
			if authorChanged {
				datastore.AdvanceNamespaceSpecVersion(existing)
			} else {
				datastore.AdvanceNamespaceSystemVersion(existing)
			}
			existing.Status = namespaceadmission.AdmissionStatus(existing.Generation, admCtx.Revision, admCtx.Now)
			err = s.store.UpdateNamespace(ctx, existing, expectedResourceVersion)
		}
		if errors.Is(err, datastore.ErrConflict) || errors.Is(err, datastore.ErrAlreadyExists) {
			existing, err = s.store.GetNamespaceByName(ctx, name)
			if err == nil {
				continue
			}
			if errors.Is(err, datastore.ErrNotFound) {
				existing = nil
				continue
			}
		}
		if err != nil {
			s.log.Warn("admit_resources: Namespace rejected",
				zap.String("name", name),
				zap.String("operation", string(op)),
				zap.Bool("existing", existing != nil),
				zap.Error(err))
			return
		}
		eventType := eventbus.Modified
		if created {
			eventType = eventbus.Added
		}
		s.publishNamespaceEvent(eventType, namespace)
		return
	}
	s.log.Warn("admit_resources: Namespace rejected after repeated concurrent updates",
		zap.String("name", name),
		zap.String("operation", string(op)),
		zap.Int("attempts", namespaceAdmissionWriteAttempts))
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (s *Server) admitProduct(
	ctx context.Context,
	resource *catalog.ProductResource,
	body []byte,
	admCtx AdmissionContext,
	sourcePath string,
	op admission.Operation,
	rawExisting any,
) {
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal product spec failed",
			zap.String("name", resource.Metadata.Name), zap.Error(err))
		return
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = admCtx.Namespace
	}

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
		return
	}

	if op == admission.OperationCreate || existing == nil {
		uid, ok := s.newUID(resource.Kind, resource.Metadata.Name)
		if !ok {
			return
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
		} else {
			s.publishProductEvent(eventbus.Added, p)
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
		if !changedSpecBody && !changedMetadata && !changedProvenance && !changedOwnerReferences {
			return
		}
		// Diff categoryRef before existing.Spec is overwritten below, so the
		// watchProducts event only fires when the field CategoryTaxonomy
		// reconciliation actually cares about changed (spec 042,
		// contracts/product-watch-contract.md call site 2) — a spec change
		// that is not a categoryRef change (e.g. price/description) still
		// persists via the write below but must not publish an event.
		categoryRefChanged := productCategoryRefName(existing.Spec) != productCategoryRefName(specJSON)
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
		existing.Spec = specJSON
		existing.Body = string(body)
		existing.Status = admissionAcceptedStatus(gen, admCtx.Revision, admCtx.Now)
		if uerr := s.store.UpdateProduct(ctx, existing); uerr != nil {
			s.log.Error("admit_resources: update product failed",
				zap.String("name", resource.Metadata.Name), zap.Error(uerr))
		} else if categoryRefChanged {
			s.publishProductEvent(eventbus.Modified, existing)
		}
	}
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

	if op == admission.OperationCreate || existing == nil {
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
			switch catalog.ConditionType(c.Type) {
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
		if !changedSpecBody && !changedMetadata && !changedProvenance && !changedDenorm {
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
) {
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal category spec failed",
			zap.String("name", resource.Metadata.Name), zap.Error(err))
		return
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = admCtx.Namespace
	}

	name := resource.Metadata.Name

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
		return
	case admission.Allowed:
		for _, c := range dec.Conditions {
			switch catalog.ConditionType(c.Type) {
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
			return
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
			return
		}
		s.publishCategoryTaxonomyEvent(eventbus.Added, c)
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
		if !changedSpecBody && !changedMetadata && !changedProvenance && !changedHierarchy && !changedOwnerReferences {
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
			return
		}
		s.publishCategoryTaxonomyEvent(eventbus.Modified, existing)
		s.log.Info("admit_resources: category updated",
			zap.String("kind", resource.Kind),
			zap.String("namespace", namespace),
			zap.String("name", name),
			zap.String("ancestor_path", ancestorPath))
	}
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
