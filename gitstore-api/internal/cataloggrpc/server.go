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
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/google/cel-go/cel"
	"go.uber.org/zap"
)

// GitReader is the read subset of gitclient.Client used by AdmitResources.
// Abstracted here so it can be mocked in tests.
type GitReader interface {
	ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error)
	ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
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
}

// ServerDeps contains dependencies for the CatalogService gRPC server.
type ServerDeps struct {
	Store       datastore.Datastore
	GitReader   GitReader
	GitClient   *gitclient.Client
	Logger      *zap.Logger
	Parser      ResourceParser
	Clock       apiruntime.Clock
	IDGenerator apiruntime.IDGenerator
	CELEnv      *cel.Env
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
	return &Server{
		store:  deps.Store,
		git:    git,
		log:    deps.Logger,
		parser: parser,
		clock:  clock,
		ids:    ids,
		celEnv: celEnv,
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

	paths, err := s.git.ListFiles(ctx, req.RepositoryId, "", req.CommitSha)
	if err != nil {
		s.log.Error("admit_resources: list files failed",
			zap.String("repository_id", req.RepositoryId),
			zap.String("commit_sha", req.CommitSha),
			zap.Error(err))
		return &catalogv1.AdmitResourcesResponse{}, nil
	}

	// Extract branch name from ref: "refs/heads/main" → "main"
	branch := strings.TrimPrefix(req.RefName, "refs/heads/")
	revision := branch + "@sha1:" + req.CommitSha
	now := s.clock.Now().UTC()

	// Build the admission context once so every per-file admit helper can read
	// namespace, commit SHA, ref, and wall-clock time without re-querying the DB.
	admCtx := AdmissionContext{
		RepositoryID: req.RepositoryId,
		Namespace:    repoNamespace,
		CommitSHA:    req.CommitSha,
		RefName:      req.RefName,
		Revision:     revision,
		Now:          now,
	}

	// Two-pass: collect all parsed resources first so CategoryTaxonomy
	// can detect intra-push cycles and resolve in-push parents.
	type parsedEntry struct {
		parsed *validate.ParsedResource
		body   []byte
	}
	var entries []parsedEntry

	for _, path := range paths {
		content, err := s.git.ReadFile(ctx, req.RepositoryId, path, req.CommitSha)
		if err != nil {
			s.log.Error("admit_resources: read file failed",
				zap.String("path", path), zap.Error(err))
			continue
		}
		parsed, body, err := s.parser.ParseResource(bytes.NewReader(content))
		if err != nil || parsed == nil {
			if err != nil {
				s.log.Error("admit_resources: parse failed",
					zap.String("path", path), zap.Error(err))
			}
			continue
		}
		entries = append(entries, parsedEntry{parsed, body})
	}

	// Build intra-push category graph: name → parentName
	pushCategoryParents := make(map[string]string)
	categoryEntries := make(map[string]*parsedEntry)
	for i := range entries {
		e := &entries[i]
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

	// inPushAncestorPaths is populated as each category is admitted so that
	// children later in the same push see their parent's full computed path.
	inPushAncestorPaths := make(map[string]string, len(topoOrder))
	for _, name := range topoOrder {
		e := categoryEntries[name]
		s.admitCategoryTaxonomyWithContext(ctx, e.parsed.CategoryTaxonomy, e.body, admCtx, inPushAncestorPaths, cycleMembers[name])
	}
	for _, e := range entries {
		switch e.parsed.Kind {
		case "Product":
			s.admitProduct(ctx, e.parsed.Product, e.body, admCtx)
		case "Collection":
			s.admitCollection(ctx, e.parsed.Collection, e.body, admCtx)
		case "ProductVariant":
			s.admitProductVariant(ctx, e.parsed.ProductVariant, e.body, admCtx)
		}
	}

	return &catalogv1.AdmitResourcesResponse{}, nil
}

// detectCycles returns the set of category names involved in intra-push cycles.
// Uses DFS with three-color marking (0=white, 1=gray, 2=black). When a back
// edge is found, every node on the current gray path is marked as in-cycle —
// not just the node that triggered the back edge — so chains like A→B→C→B
// correctly flag both B and C.
func detectCycles(parentMap map[string]string) map[string]bool {
	inCycle := make(map[string]bool)
	color := make(map[string]int, len(parentMap))
	// grayStack tracks the DFS path currently being explored.
	var grayStack []string
	var visit func(name string)
	visit = func(name string) {
		if color[name] == 2 {
			return
		}
		if color[name] == 1 {
			// Back edge to `name`. The cycle runs from `name`'s position in the
			// gray stack to the top — nodes before it are mere ancestors of the
			// cycle entry point and must not be flagged.
			for i, n := range grayStack {
				if n == name {
					for _, m := range grayStack[i:] {
						inCycle[m] = true
					}
					break
				}
			}
			return
		}
		parent, inPush := parentMap[name]
		if !inPush || parent == "" {
			color[name] = 2
			return
		}
		color[name] = 1
		grayStack = append(grayStack, name)
		visit(parent)
		grayStack = grayStack[:len(grayStack)-1]
		color[name] = 2
	}
	for name := range parentMap {
		visit(name)
	}
	return inCycle
}

// topoSortCategories returns category names from parentMap in topological order
// (roots first, leaves last). Nodes in a cycle are appended at the end in
// insertion order so they are still admitted (with Acyclic=False status).
func topoSortCategories(parentMap map[string]string, cycleMembers map[string]bool) []string {
	visited := make(map[string]bool, len(parentMap))
	order := make([]string, 0, len(parentMap))
	var visit func(name string)
	visit = func(name string) {
		if visited[name] || cycleMembers[name] {
			return
		}
		visited[name] = true
		parent := parentMap[name]
		// Only recurse if the parent is also part of this push; external parents
		// (already in DB) are not in parentMap and must not be added to order.
		if parent != "" {
			if _, inPush := parentMap[parent]; inPush {
				visit(parent)
			}
		}
		order = append(order, name)
	}
	for name := range parentMap {
		visit(name)
	}
	// Append cycle members after the acyclic nodes, in stable order.
	for name := range parentMap {
		if cycleMembers[name] {
			order = append(order, name)
		}
	}
	return order
}

func (s *Server) admitProduct(
	ctx context.Context,
	resource *catalog.ProductResource,
	body []byte,
	admCtx AdmissionContext,
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

	existing, getErr := s.store.GetProductByName(ctx, namespace, resource.Metadata.Name)

	if getErr != nil || existing == nil {
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
		gen := existing.Generation + 1
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.Generation = gen
		existing.ResourceVersion = fmt.Sprintf("%d", gen)
		existing.Revision = admCtx.Revision
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

	existing, getErr := s.store.GetCollectionByName(ctx, namespace, resource.Metadata.Name)

	if getErr != nil || existing == nil {
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
		gen := existing.Generation + 1
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.Generation = gen
		existing.ResourceVersion = fmt.Sprintf("%d", gen)
		existing.Revision = admCtx.Revision
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

	// Run all admission checks, collecting results into admitResult.
	admitResult := variantAdmitResult{
		OptionsAccepted: true,
		PricingAccepted: true,
	}

	productRefName := ""
	if resource.Spec.ProductRef != nil {
		productRefName = resource.Spec.ProductRef.Name
		if productRefName != "" {
			if parent, perr := s.store.GetProductByName(ctx, namespace, productRefName); perr == nil {
				admitResult.ProductResolved = true
				if len(resource.Spec.SelectedOptions) > 0 {
					admitResult.OptionsAccepted, admitResult.OptionsMsg = validateSelectedOptions(resource.Spec.SelectedOptions, parent.Spec)
					if !admitResult.OptionsAccepted {
						s.log.Warn("admit_resources: product_variant option incompatibility",
							zap.String("name", resource.Metadata.Name),
							zap.String("namespace", namespace),
							zap.String("product_ref", productRefName),
							zap.String("reason", admitResult.OptionsMsg))
					}
				}
			} else {
				s.log.Info("admit_resources: product_variant productRef deferred — product not yet in datastore",
					zap.String("name", resource.Metadata.Name),
					zap.String("namespace", namespace),
					zap.String("product_ref", productRefName))
			}
		}
	}

	// CEL syntax validation (admission-phase; no runtime evaluation).
	admitResult.PricingAccepted, admitResult.PricingMsg = celValidateExpressions(s.celEnv, resource.Spec)
	if !admitResult.PricingAccepted {
		s.log.Warn("admit_resources: product_variant CEL syntax error",
			zap.String("name", resource.Metadata.Name),
			zap.String("namespace", namespace),
			zap.String("reason", admitResult.PricingMsg))
	}

	// Compute resolved summaries.
	admitResult.Resolved = &catalog.ResolvedProductVariantDefinition{
		PriceSet:  computeResolvedPriceSet(s.celEnv, resource.Spec),
		Inventory: computeResolvedInventory(resource.Spec),
	}

	existing, getErr := s.store.GetProductVariantByName(ctx, namespace, resource.Metadata.Name)

	if getErr != nil || existing == nil {
		// SKU uniqueness check: another variant in this namespace may already hold the SKU.
		if skuOwner, skuErr := s.store.GetProductVariantBySKU(ctx, namespace, resource.Spec.SKU); skuErr == nil && skuOwner != nil && skuOwner.Name != resource.Metadata.Name {
			s.log.Warn("admit_resources: product_variant SKU conflict — SKU already claimed by another variant",
				zap.String("name", resource.Metadata.Name),
				zap.String("namespace", namespace),
				zap.String("sku", resource.Spec.SKU),
				zap.String("conflict_name", skuOwner.Name))
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
		gen := existing.Generation + 1
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.Generation = gen
		existing.ResourceVersion = fmt.Sprintf("%d", gen)
		existing.Revision = admCtx.Revision
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
// inCycle indicates this category is part of a detected intra-push cycle.
func (s *Server) admitCategoryTaxonomyWithContext(
	ctx context.Context,
	resource *catalog.CategoryTaxonomyResource,
	body []byte,
	admCtx AdmissionContext,
	inPushAncestorPaths map[string]string,
	inCycle bool,
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

	// Compute parent name and ancestor path.
	parentName := ""
	ancestorPath := name
	parentResolved := false

	if resource.Spec.ParentRef != nil && resource.Spec.ParentRef.Name != "" {
		parentName = resource.Spec.ParentRef.Name

		// Check if parent was already admitted in this push (co-creation).
		// inPushAncestorPaths is populated in topological order so the parent's
		// full computed path is available here even for deep chains (root→child→grandchild).
		if parentPath, inPush := inPushAncestorPaths[parentName]; inPush {
			ancestorPath = parentPath + "/" + name
			parentResolved = true
		} else {
			// Look up parent in DB.
			parent, perr := s.store.GetCategoryTaxonomyByName(ctx, namespace, parentName)
			if perr == nil && parent != nil {
				ancestorPath = parent.AncestorPath + "/" + name
				parentResolved = true
			}
			// If parent not found: tentative root, ParentResolved=False.
		}
	}

	existing, getErr := s.store.GetCategoryTaxonomyByName(ctx, namespace, name)

	statusJSON := categoryAdmissionStatusFull(1, admCtx.Revision, admCtx.Now, parentResolved, inCycle)

	if getErr != nil || existing == nil {
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
		gen := existing.Generation + 1
		existing.APIVersion = resource.APIVersion
		existing.Kind = resource.Kind
		existing.Labels = resource.Metadata.Labels
		existing.Annotations = resource.Metadata.Annotations
		existing.Generation = gen
		existing.ResourceVersion = fmt.Sprintf("%d", gen)
		existing.Revision = admCtx.Revision
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
	// Record the computed path so children later in this push see the correct full path.
	inPushAncestorPaths[name] = ancestorPath
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

// celValidateExpressions parses each CEL expression for syntax only (no evaluation).
// env may be nil (CEL unavailable); in that case all expressions are considered valid.
// Returns (true, "") if all are valid, or (false, message) on the first syntax error.
func celValidateExpressions(env *cel.Env, spec catalog.ProductVariantSpec) (bool, string) {
	if env == nil || spec.Pricing == nil || spec.Pricing.PriceSet == nil {
		return true, ""
	}
	for i, pt := range spec.Pricing.PriceSet.Prices {
		if pt.Eligibility == nil {
			continue
		}
		for j, c := range pt.Eligibility.Constraints {
			if _, iss := env.Parse(c.Expression); iss != nil && iss.Err() != nil {
				return false, fmt.Sprintf("pricing.priceSet.prices[%d].eligibility.constraints[%d]: invalid CEL expression %q: %s",
					i, j, c.Expression, iss.Err())
			}
		}
	}
	return true, ""
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

// validateSelectedOptions checks that every selected option name exists in the
// parent product and, when the parent option declares allowed values, that the
// selected value is one of them.
// Returns (true, "") on success, or (false, descriptive message) on first
// mismatch.
func validateSelectedOptions(selected []catalog.SelectedOptionDefinition, parentSpec []byte) (bool, string) {
	// Parse the parent product spec to extract declared option names and values.
	var spec struct {
		Options []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		} `json:"options"`
	}
	if err := json.Unmarshal(parentSpec, &spec); err != nil {
		// Cannot parse parent spec; skip option validation rather than false-reject.
		return true, ""
	}
	declared := make(map[string]map[string]struct{}, len(spec.Options))
	for _, o := range spec.Options {
		allowedValues := make(map[string]struct{}, len(o.Values))
		for _, v := range o.Values {
			allowedValues[v] = struct{}{}
		}
		declared[o.Name] = allowedValues
	}
	for _, so := range selected {
		allowedValues, ok := declared[so.Name]
		if !ok {
			return false, fmt.Sprintf("selectedOptions: name %q not found in parent product options", so.Name)
		}
		if len(allowedValues) > 0 {
			if _, ok := allowedValues[so.Value]; !ok {
				return false, fmt.Sprintf("selectedOptions: value %q for option %q not found in parent product option values", so.Value, so.Name)
			}
		}
	}
	return true, ""
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
