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
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
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
	chain.RegisterValidatingPolicy(admcatalog.NewProductValidatingPolicy(deps.Logger))
	chain.RegisterValidatingPolicy(admcatalog.NewCollectionValidatingPolicy(deps.Logger))
	chain.RegisterValidatingPolicy(admcatalog.NewProductVariantValidatingPolicy(deps.Store, celEnv, deps.Logger))
	chain.RegisterValidatingPolicy(admcatalog.NewCategoryTaxonomyValidatingPolicy(deps.Store, deps.Logger))
	for _, p := range deps.ExtraValidatingPolicies {
		chain.RegisterValidatingPolicy(p)
	}
	return &Server{
		store:  deps.Store,
		git:    git,
		log:    deps.Logger,
		parser: parser,
		clock:  clock,
		ids:    ids,
		celEnv: celEnv,
		chain:  chain,
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
	_ context.Context,
	req *catalogv1.ValidateResourcesRequest,
) (*catalogv1.ValidateResourcesResponse, error) {
	var allErrors []*catalogv1.ValidationError

	for _, blob := range req.Blobs {
		// Opt-in: blobs not starting with `---` are not product resources.
		trimmed := bytes.TrimLeft(blob.Content, " \t\r\n")
		if !bytes.HasPrefix(trimmed, []byte("---")) {
			continue
		}

		_, _, err := s.parser.ParseResource(bytes.NewReader(blob.Content))
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
	ns, err := s.store.GetNamespace(ctx, repo.NamespaceID)
	if err != nil || ns == nil {
		return "", fmt.Errorf("admit_resources: namespace %s not found for repository %s: %w", repo.NamespaceID, repositoryID, err)
	}
	return ns.Identifier, nil
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

	if req.RefName != "" {
		current, err := s.git.ResolveRef(ctx, req.RepositoryId, req.RefName)
		if err != nil {
			if isRefNotFound(err) {
				if !isZeroOID(newCommit) {
					// Ref gone but we were not a delete push — skip.
					s.log.Info("admit_resources: ref no longer exists; stale admission skipped",
						zap.String("repository_id", req.RepositoryId),
						zap.String("ref_name", req.RefName),
						zap.String("new_commit_sha", newCommit))
					return &catalogv1.AdmitResourcesResponse{}, nil
				}
				// Zero-OID delete and ref is truly gone — proceed.
			} else {
				s.log.Error("admit_resources: resolve ref failed",
					zap.String("repository_id", req.RepositoryId),
					zap.String("ref_name", req.RefName),
					zap.String("new_commit_sha", newCommit),
					zap.Error(err))
				return &catalogv1.AdmitResourcesResponse{}, nil
			}
		} else if isZeroOID(newCommit) {
			// Delete push but the ref still exists (branch was recreated) — skip.
			// Only act when current is non-empty: an empty SHA means the git service
			// does not support ResolveRef and we cannot determine staleness.
			if current != "" {
				s.log.Info("admit_resources: branch delete is stale; ref was recreated — skipping",
					zap.String("repository_id", req.RepositoryId),
					zap.String("ref_name", req.RefName),
					zap.String("current_commit_sha", current))
				return &catalogv1.AdmitResourcesResponse{}, nil
			}
		} else if current != "" && current != newCommit {
			// current == "" means the git service returned no SHA — skip the staleness
			// check rather than silently admitting. A properly implemented service always
			// returns a non-empty SHA (see ResolveRefForRepo), so an empty value here
			// indicates an unimplemented or degraded backend.
			s.log.Info("admit_resources: stale admission skipped",
				zap.String("repository_id", req.RepositoryId),
				zap.String("ref_name", req.RefName),
				zap.String("admitted_commit_sha", newCommit),
				zap.String("current_commit_sha", current))
			return &catalogv1.AdmitResourcesResponse{}, nil
		}
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

	// Build the admission context once so every per-file admit helper can read
	// namespace, commit SHA, ref, and wall-clock time without re-querying the DB.
	admCtx := AdmissionContext{
		RepositoryID: req.RepositoryId,
		Namespace:    repoNamespace,
		CommitSHA:    newCommit,
		RefName:      req.RefName,
		Revision:     revision,
		Now:          now,
	}

	oldEntries := s.loadParsedEntries(ctx, req.RepositoryId, req.GetOldCommitSha(), admCtx.Namespace, req.GetChangedPaths())
	newEntries := s.loadParsedEntries(ctx, req.RepositoryId, newCommit, admCtx.Namespace, req.GetChangedPaths())
	ops := deriveResourceAdmissionOperations(oldEntries, newEntries, req.GetChangedPaths())
	s.applyResourceOperations(ctx, ops, admCtx)

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

func (s *Server) applyResourceOperations(ctx context.Context, ops []resourceAdmissionOperation, admCtx AdmissionContext) {
	upsertOps := make(map[string]resourceAdmissionOperation)
	var upsertEntries []*parsedEntry
	for _, op := range ops {
		switch op.operation {
		case admission.OperationDelete:
			s.deleteResource(ctx, op.identity)
		case admission.OperationCreate, admission.OperationUpdate:
			if op.newEntry == nil {
				continue
			}
			upsertOps[op.identity.key()] = op
			upsertEntries = append(upsertEntries, op.newEntry)
		}
	}
	s.admitParsedEntries(ctx, upsertEntries, admCtx, upsertOps)
	// TODO(CategoryTaxonomy controller): when a CategoryTaxonomy is deleted or
	// its parentRef changes, descendants already stored in the datastore are left
	// with a stale AncestorPath. Admission only processes the files that changed
	// in this push; unchanged children are never re-admitted here.
	// The CategoryTaxonomy controller must reconcile this: on any category
	// create/update/delete event it should walk all direct and transitive children
	// (by ParentName pointer) and recompute AncestorPath in topological order.
	// The ParentResolved=False condition on a child is the observable signal that
	// its path may be stale and reconciliation is needed.
}

func (s *Server) admitParsedEntries(
	ctx context.Context,
	entries []*parsedEntry,
	admCtx AdmissionContext,
	explicitOps map[string]resourceAdmissionOperation,
) {
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
			siblingOp = admission.OperationCreate
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
		op, existing, ok := s.operationForEntry(ctx, e, explicitOps)
		if !ok {
			continue
		}
		s.admitCategoryTaxonomyWithContext(ctx, e.parsed.CategoryTaxonomy, e.body, admCtx, e.path, op, existing, inPushAncestorPaths, catPushSet)
	}
	for _, e := range entries {
		op, existing, ok := s.operationForEntry(ctx, e, explicitOps)
		if !ok {
			continue
		}
		switch e.parsed.Kind {
		case "Product":
			s.admitProduct(ctx, e.parsed.Product, e.body, admCtx, e.path, op, existing)
		case "Collection":
			s.admitCollection(ctx, e.parsed.Collection, e.body, admCtx, e.path, op, existing)
		case "ProductVariant":
			s.admitProductVariant(ctx, e.parsed.ProductVariant, e.body, admCtx, e.path, op, existing)
		}
	}
}

func (s *Server) operationForEntry(
	ctx context.Context,
	e *parsedEntry,
	explicitOps map[string]resourceAdmissionOperation,
) (admission.Operation, any, bool) {
	if e == nil {
		return "", nil, false
	}
	if op, ok := explicitOps[e.identity.key()]; ok {
		existing, err := s.lookupResourceByIdentity(ctx, e.identity)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			s.log.Error("admit_resources: lookup resource failed",
				zap.String("kind", e.identity.Kind),
				zap.String("namespace", e.identity.Namespace),
				zap.String("name", e.identity.Name),
				zap.Error(err))
			return "", nil, false
		}
		return op.operation, existing, true
	}
	existing, err := s.lookupResourceByIdentity(ctx, e.identity)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return admission.OperationCreate, nil, true
		}
		s.log.Error("admit_resources: lookup resource failed",
			zap.String("kind", e.identity.Kind),
			zap.String("namespace", e.identity.Namespace),
			zap.String("name", e.identity.Name),
			zap.Error(err))
		return "", nil, false
	}
	if existing != nil {
		return admission.OperationUpdate, existing, true
	}
	return admission.OperationCreate, nil, true
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
	default:
		return nil, datastore.ErrNotFound
	}
}

func (s *Server) deleteResource(ctx context.Context, id resourceIdentity) {
	existing, err := s.lookupResourceByIdentity(ctx, id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			s.log.Info("admit_resources: delete skipped for missing resource",
				zap.String("kind", id.Kind),
				zap.String("namespace", id.Namespace),
				zap.String("name", id.Name))
			return
		}
		s.log.Error("admit_resources: delete lookup failed",
			zap.String("kind", id.Kind),
			zap.String("namespace", id.Namespace),
			zap.String("name", id.Name),
			zap.Error(err))
		return
	}

	var uid string
	var deleteErr error
	switch r := existing.(type) {
	case *datastore.Product:
		uid = r.UID
		deleteErr = s.store.DeleteProduct(ctx, r.UID)
	case *datastore.CategoryTaxonomy:
		uid = r.UID
		deleteErr = s.store.DeleteCategoryTaxonomy(ctx, r.UID)
	case *datastore.Collection:
		uid = r.UID
		deleteErr = s.store.DeleteCollection(ctx, r.UID)
	case *datastore.ProductVariant:
		uid = r.UID
		deleteErr = s.store.DeleteProductVariant(ctx, r.UID)
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
		return
	}
	s.log.Info("admit_resources: resource deleted",
		zap.String("kind", id.Kind),
		zap.String("namespace", id.Namespace),
		zap.String("name", id.Name),
		zap.String("uid", uid))
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
		if uerr := s.store.UpdateProduct(ctx, existing); uerr != nil {
			s.log.Error("admit_resources: update product failed",
				zap.String("name", resource.Metadata.Name), zap.Error(uerr))
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
		if !changedSpecBody && !changedMetadata && !changedProvenance && !changedHierarchy {
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
