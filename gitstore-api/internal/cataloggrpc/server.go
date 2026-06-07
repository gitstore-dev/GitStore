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
	"strings"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// GitReader is the read subset of gitclient.Client used by AdmitResources.
// Abstracted here so it can be mocked in tests.
type GitReader interface {
	ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error)
	ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
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
}

// NewServer creates a new CatalogService gRPC server.
// store and gitClient may be nil for unit tests that only call ValidateResources.
func NewServer(store datastore.Datastore, gitClient *gitclient.Client) *Server {
	var git GitReader
	if gitClient != nil {
		git = &gitClientReader{c: gitClient}
	}
	return &Server{store: store, git: git, log: zap.NewNop()}
}

// NewServerWithLogger creates a new server with a custom logger.
func NewServerWithLogger(store datastore.Datastore, gitClient *gitclient.Client, log *zap.Logger) *Server {
	var git GitReader
	if gitClient != nil {
		git = &gitClientReader{c: gitClient}
	}
	return &Server{store: store, git: git, log: log}
}

// NewServerForTest creates a server with a mock GitReader for unit tests.
func NewServerForTest(store datastore.Datastore, git GitReader) *Server {
	return &Server{store: store, git: git, log: zap.NewNop()}
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

		_, _, err := validate.ParseResource(bytes.NewReader(blob.Content))
		if err == nil {
			continue
		}

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
		parsed, body, err := validate.ParseResource(bytes.NewReader(content))
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

	now := time.Now().UTC()

	// inPushAncestorPaths is populated as each category is admitted so that
	// children later in the same push see their parent's full computed path.
	inPushAncestorPaths := make(map[string]string, len(topoOrder))
	for _, name := range topoOrder {
		e := categoryEntries[name]
		s.admitCategoryTaxonomyWithContext(ctx, e.parsed.CategoryTaxonomy, e.body, req, repoNamespace, revision, now, inPushAncestorPaths, cycleMembers[name])
	}
	for _, e := range entries {
		if e.parsed.Kind == "Product" {
			s.admitProduct(ctx, e.parsed.Product, e.body, req, repoNamespace, revision, now)
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
	req *catalogv1.AdmitResourcesRequest,
	repoNamespace string,
	revision string,
	now time.Time,
) {
	specJSON, err := json.Marshal(resource.Spec)
	if err != nil {
		s.log.Error("admit_resources: marshal product spec failed",
			zap.String("name", resource.Metadata.Name), zap.Error(err))
		return
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = repoNamespace
	}

	existing, getErr := s.store.GetProductByName(ctx, namespace, resource.Metadata.Name)

	if getErr != nil || existing == nil {
		p := &datastore.Product{
			UID:               uuid.Must(uuid.NewV7()).String(),
			Namespace:         namespace,
			Name:              resource.Metadata.Name,
			APIVersion:        resource.APIVersion,
			Kind:              resource.Kind,
			Labels:            resource.Metadata.Labels,
			Annotations:       resource.Metadata.Annotations,
			Generation:        1,
			ResourceVersion:   "1",
			CreationTimestamp: now,
			Revision:          revision,
			GitCommitSHA:      req.CommitSha,
			GitRef:            req.RefName,
			Spec:              specJSON,
			Body:              string(body),
		}
		p.Status = admissionAcceptedStatus(1, revision, now)
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
		existing.Revision = revision
		existing.GitCommitSHA = req.CommitSha
		existing.GitRef = req.RefName
		existing.Spec = specJSON
		existing.Body = string(body)
		existing.Status = admissionAcceptedStatus(gen, revision, now)
		if uerr := s.store.UpdateProduct(ctx, existing); uerr != nil {
			s.log.Error("admit_resources: update product failed",
				zap.String("name", resource.Metadata.Name), zap.Error(uerr))
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
	req *catalogv1.AdmitResourcesRequest,
	repoNamespace string,
	revision string,
	now time.Time,
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
		namespace = repoNamespace
	}

	name := resource.Metadata.Name

	// Compute parent name and ancestor path.
	parentName := ""
	ancestorPath := name
	parentResolved := false

	if resource.Spec.ParentRef != nil && resource.Spec.ParentRef.Name != "" {
		parentName = resource.Spec.ParentRef.Name

		// T033: check if parent was already admitted in this push (co-creation).
		// inPushAncestorPaths is populated in topological order so the parent's
		// full computed path is available here even for deep chains (root→child→grandchild).
		if parentPath, inPush := inPushAncestorPaths[parentName]; inPush {
			ancestorPath = parentPath + "/" + name
			parentResolved = true
		} else {
			// T023: look up parent in DB.
			parent, perr := s.store.GetCategoryTaxonomyByName(ctx, namespace, parentName)
			if perr == nil && parent != nil {
				ancestorPath = parent.AncestorPath + "/" + name
				parentResolved = true
			}
			// If parent not found: tentative root, ParentResolved=False.
		}
	}

	existing, getErr := s.store.GetCategoryTaxonomyByName(ctx, namespace, name)

	statusJSON := categoryAdmissionStatusFull(1, revision, now, parentResolved, inCycle)

	if getErr != nil || existing == nil {
		c := &datastore.CategoryTaxonomy{
			UID:               uuid.Must(uuid.NewV7()).String(),
			Namespace:         namespace,
			Name:              name,
			APIVersion:        resource.APIVersion,
			Kind:              resource.Kind,
			Labels:            resource.Metadata.Labels,
			Annotations:       resource.Metadata.Annotations,
			Generation:        1,
			ResourceVersion:   "1",
			CreationTimestamp: now,
			Revision:          revision,
			GitCommitSHA:      req.CommitSha,
			GitRef:            req.RefName,
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
		existing.Revision = revision
		existing.GitCommitSHA = req.CommitSha
		existing.GitRef = req.RefName
		existing.ParentName = parentName
		existing.AncestorPath = ancestorPath
		existing.Spec = specJSON
		existing.Body = string(body)
		existing.Status = categoryAdmissionStatusFull(gen, revision, now, parentResolved, inCycle)
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
