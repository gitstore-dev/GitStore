// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/admissionreport"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	categoryAPIVersion = "catalog.gitstore.dev/v1beta1"
	categoryKind       = "CategoryTaxonomy"
	categoryRefName    = "refs/heads/main"
)

// CategoryManifestInput is the author-controlled part of a category mutation.
type CategoryManifestInput struct {
	APIVersion string
	Kind       string
	Metadata   *model.ObjectMetaInput
	Spec       *model.CategorySpecInput
	// Body is the Markdown body. Nil keeps the current body on update and
	// means an empty body on create.
	Body *string
}

// CommitCategoryManifest authors a category through Git: it renders the
// manifest, runs the push pre-receive checks in-process (API commits skip the
// Git hooks), commits, admits the commit synchronously and returns the record
// only when it came from that commit. Errors are *admission.Error.
func (s *Service) CommitCategoryManifest(ctx context.Context, input CategoryManifestInput, caller string, create bool) (*datastore.CategoryTaxonomy, error) {
	operation := "update"
	if create {
		operation = "create"
	}
	started := time.Now()
	category, commitSHA, err := s.commitCategoryManifest(ctx, input, caller, create)
	s.observeCategoryMutation(operation, input.Metadata, commitSHA, started, err)
	return category, err
}

func (s *Service) commitCategoryManifest(ctx context.Context, input CategoryManifestInput, caller string, create bool) (*datastore.CategoryTaxonomy, string, error) {
	if s.gitWriter == nil || s.committedAdmitter == nil || s.manifestValidator == nil {
		return nil, "", fmt.Errorf("category admission runtime is unavailable")
	}
	metadata, spec := input.Metadata, input.Spec
	if metadata == nil || spec == nil || metadata.Name == "" || metadata.Namespace == "" {
		return nil, "", admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{
			Reason: "INVALID_ENVELOPE", Message: "metadata.name, metadata.namespace and spec are required", Level: admission.LevelFailure,
		}})
	}
	path := fmt.Sprintf("categories/%s.md", metadata.Name)
	if input.APIVersion != categoryAPIVersion || input.Kind != categoryKind {
		return nil, "", admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{
			Reason: "INVALID_ENVELOPE", Message: fmt.Sprintf("apiVersion must be %q and kind must be %q", categoryAPIVersion, categoryKind),
			Level: admission.LevelFailure, File: path,
		}})
	}
	labels, err := stringMap(metadata.Labels)
	if err != nil {
		return nil, "", invalidCategoryField(path, "metadata.labels", err)
	}
	annotations, err := stringMap(metadata.Annotations)
	if err != nil {
		return nil, "", invalidCategoryField(path, "metadata.annotations", err)
	}

	var repositoryID string
	existing, lookupErr := s.store.GetCategoryTaxonomyByName(ctx, metadata.Namespace, metadata.Name)
	switch {
	case lookupErr != nil && !errors.Is(lookupErr, datastore.ErrNotFound):
		return nil, "", fmt.Errorf("look up category: %w", lookupErr)
	case create && lookupErr == nil:
		return nil, "", admission.NewError(admission.CodeAlreadyExists, "CATEGORY_ALREADY_EXISTS",
			fmt.Sprintf("category %q already exists", metadata.Name))
	case !create && lookupErr != nil:
		return nil, "", admission.NewError(admission.CodeNotFound, "CATEGORY_NOT_FOUND",
			fmt.Sprintf("category %q not found", metadata.Name))
	}
	if create {
		mapping, err := s.store.LookupRepository(ctx, metadata.Namespace, SystemRepositoryName)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				return nil, "", admission.NewError(admission.CodeNotFound, "NAMESPACE_NOT_FOUND",
					fmt.Sprintf("namespace %q not found", metadata.Namespace))
			}
			return nil, "", fmt.Errorf("look up namespace system repository: %w", err)
		}
		repositoryID = mapping.RepositoryID
	} else {
		if existing.DeletionTimestamp != nil {
			return nil, "", admission.NewError(admission.CodeFailedPrecondition, "CATEGORY_TERMINATING",
				fmt.Sprintf("category %q is terminating", existing.Name))
		}
		if existing.RepositoryID == "" || existing.SourcePath == "" {
			return nil, "", admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_UNAVAILABLE",
				fmt.Sprintf("category %q has no recorded source repository and path", existing.Name))
		}
		if err := requireDefaultBranchProvenance(existing); err != nil {
			return nil, "", err
		}
		if err := guardOwnerAnnotationUnchanged(existing.Annotations, annotations); err != nil {
			return nil, "", admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{
				Reason: "IMMUTABLE_OWNER", Message: err.Error(), Level: admission.LevelFailure,
				File: existing.SourcePath, Field: "metadata.annotations",
			}})
		}
		repositoryID, path = existing.RepositoryID, existing.SourcePath
	}

	current, err := s.gitWriter.ReadFileForRepo(ctx, repositoryID, path, categoryRefName)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, "", fmt.Errorf("read current category manifest: %w", err)
		}
		current = nil
	}
	var body []byte
	switch {
	case input.Body != nil:
		body = []byte(*input.Body)
	case !create:
		body = markdownBody(current)
	}
	content, err := renderManifest(categoryManifestEnvelope(metadata, labels, annotations, spec), body)
	if err != nil {
		return nil, "", invalidCategoryField(path, "", err)
	}

	commitSHA := ""
	if current != nil && bytes.Equal(current, content) {
		// The manifest is already at HEAD (an identical re-submit, or a retry
		// after a commit whose admission did not finish): admit HEAD again
		// without a new commit, so no new generation or cascade is produced.
		commitSHA, err = s.gitWriter.ResolveRefForRepo(ctx, repositoryID, categoryRefName)
		if err != nil {
			return nil, "", fmt.Errorf("resolve category repository head: %w", err)
		}
	} else {
		diagnostics, err := s.manifestValidator.ValidateManifest(ctx, admission.ManifestValidationRequest{
			RepositoryID: repositoryID, Path: path, OldContent: current, NewContent: content,
		})
		if err != nil {
			return nil, "", fmt.Errorf("validate category manifest: %w", err)
		}
		if len(diagnostics) > 0 {
			return nil, "", admission.Rejected(admission.PhasePreReceive, "", diagnostics)
		}
		verb := "Update"
		if create {
			verb = "Create"
		}
		commitSHA, err = s.gitWriter.CommitFileForRepo(ctx, repositoryID, gitclient.CommitFileParams{
			Path: path, Content: content, CommitMessage: fmt.Sprintf("%s CategoryTaxonomy %s", verb, metadata.Name), AuthorName: caller,
		})
		if err != nil {
			return nil, "", fmt.Errorf("commit category manifest: %w", err)
		}
	}

	op := admission.OperationUpdate
	if create {
		op = admission.OperationCreate
	}
	result, err := s.committedAdmitter.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
		RepositoryID: repositoryID, Namespace: metadata.Namespace, ActorSubject: caller,
		CommitSHA: commitSHA, RefName: categoryRefName, Path: path, Content: content, Operation: op,
	})
	if err != nil {
		return nil, commitSHA, categoryAdmissionError(err, commitSHA)
	}
	category, err := convergeCommittedResource(result, func() (*datastore.CategoryTaxonomy, string, error) {
		record, err := s.store.GetCategoryTaxonomyByName(ctx, metadata.Namespace, metadata.Name)
		if err != nil {
			return nil, "", err
		}
		return record, record.GitCommitSHA, nil
	})
	if err != nil {
		return nil, commitSHA, categoryAdmissionError(err, commitSHA)
	}
	admissionreport.Report(ctx, commitSHA, result.Warnings)
	return category, commitSHA, nil
}

func categoryManifestEnvelope(metadata *model.ObjectMetaInput, labels, annotations map[string]string, spec *model.CategorySpecInput) map[string]any {
	meta := map[string]any{"name": metadata.Name, "namespace": metadata.Namespace}
	if len(labels) > 0 {
		meta["labels"] = labels
	}
	if len(annotations) > 0 {
		meta["annotations"] = annotations
	}
	manifestSpec := map[string]any{"title": spec.Title}
	if spec.ParentRef != nil {
		ref := map[string]any{"name": spec.ParentRef.Name}
		if spec.ParentRef.Kind != nil && *spec.ParentRef.Kind != "" {
			ref["kind"] = *spec.ParentRef.Kind
		}
		if spec.ParentRef.APIVersion != nil && *spec.ParentRef.APIVersion != "" {
			ref["apiVersion"] = *spec.ParentRef.APIVersion
		}
		if spec.ParentRef.Namespace != nil && *spec.ParentRef.Namespace != "" {
			ref["namespace"] = *spec.ParentRef.Namespace
		}
		manifestSpec["parentRef"] = ref
	}
	if len(spec.Media) > 0 {
		media := make([]any, 0, len(spec.Media))
		for _, item := range spec.Media {
			if item == nil || item.FileRef == nil {
				continue
			}
			fileRef := map[string]any{"name": item.FileRef.Name, "kind": item.FileRef.Kind}
			if item.FileRef.Optional != nil {
				fileRef["optional"] = *item.FileRef.Optional
			}
			media = append(media, map[string]any{"fileRef": fileRef})
		}
		manifestSpec["media"] = media
	}
	return map[string]any{"apiVersion": categoryAPIVersion, "kind": categoryKind, "metadata": meta, "spec": manifestSpec}
}

func invalidCategoryField(path, field string, err error) *admission.Error {
	return admission.Rejected(admission.PhasePreReceive, "", []admission.Diagnostic{{
		Reason: "INVALID_FIELD", Message: err.Error(), Level: admission.LevelFailure, File: path, Field: field,
	}})
}

// categoryAdmissionError maps a committed-admission failure onto the shared
// envelope. Admission errors pass through; a superseded commit is a CONFLICT.
func committedAdmissionError(err error, commitSHA, resource string) error {
	var admissionErr *admission.Error
	if errors.As(err, &admissionErr) {
		return admissionErr
	}
	if errors.Is(err, admission.ErrCommittedManifestSuperseded) {
		return admission.NewError(admission.CodeConflict, "SUPERSEDED",
			"the "+resource+" manifest was changed by a concurrent commit")
	}
	return admission.Rejected(admission.PhasePostReceive, commitSHA, []admission.Diagnostic{{
		Reason: "ADMISSION_FAILED", Message: "admission failed: " + err.Error(), Level: admission.LevelFailure,
	}})
}

func categoryAdmissionError(err error, commitSHA string) error {
	return committedAdmissionError(err, commitSHA, "category")
}

func (s *Service) observeCategoryMutation(operation string, metadata *model.ObjectMetaInput, commitSHA string, started time.Time, err error) {
	outcome, phase, diagnosticCount := "success", "", 0
	var admissionErr *admission.Error
	switch {
	case err == nil:
	case errors.As(err, &admissionErr):
		outcome, phase, diagnosticCount = string(admissionErr.Code), string(admissionErr.Phase), len(admissionErr.Diagnostics)
	default:
		outcome = "error"
	}
	categoryMutationTotal.WithLabelValues(operation, outcome).Inc()
	categoryMutationDuration.WithLabelValues(operation).Observe(time.Since(started).Seconds())
	fields := []zap.Field{
		zap.String("operation", operation), zap.String("outcome", outcome),
		zap.String("commit", commitSHA), zap.String("phase", phase), zap.Int("diagnostic_count", diagnosticCount),
	}
	if metadata != nil {
		fields = append(fields, zap.String("namespace", metadata.Namespace), zap.String("name", metadata.Name))
	}
	if err != nil {
		s.logger.Warn("category mutation rejected", append(fields, zap.Error(err))...)
		return
	}
	s.logger.Info("category mutation committed", fields...)
}

// categoryMutationGraphQLError renders shared-envelope errors as GraphQL
// errors; any other error is internal and carries no envelope.
func admissionMutationGraphQLError(err error) error {
	var admissionErr *admission.Error
	if errors.As(err, &admissionErr) {
		return admissionErr.ToGQLError()
	}
	return err
}

func categoryMutationGraphQLError(err error) error { return admissionMutationGraphQLError(err) }

// DeleteCategoryManifest starts foreground deletion by removing the category's
// manifest from Git at its stored provenance and admitting that removal, so
// Git and the datastore cannot diverge. Errors are *admission.Error.
func (s *Service) DeleteCategoryManifest(ctx context.Context, uid, caller string) (*datastore.CategoryTaxonomy, model.ResourceDeletionOutcome, error) {
	started := time.Now()
	category, outcome, commitSHA, err := s.deleteCategoryManifest(ctx, uid, caller)
	var metadata *model.ObjectMetaInput
	if category != nil {
		metadata = &model.ObjectMetaInput{Namespace: category.Namespace, Name: category.Name}
	}
	s.observeCategoryMutation("delete", metadata, commitSHA, started, err)
	return category, outcome, err
}

func (s *Service) deleteCategoryManifest(ctx context.Context, uid, caller string) (*datastore.CategoryTaxonomy, model.ResourceDeletionOutcome, string, error) {
	category, err := s.store.GetCategoryTaxonomy(ctx, uid)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, "", "", admission.NewError(admission.CodeNotFound, "CATEGORY_NOT_FOUND", "category not found")
		}
		return nil, "", "", fmt.Errorf("look up category: %w", err)
	}
	if category.DeletionTimestamp != nil {
		return category, model.ResourceDeletionOutcomeAlreadyTerminating, "", nil
	}
	if category.RepositoryID == "" || category.SourcePath == "" {
		return category, "", "", admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_UNAVAILABLE",
			fmt.Sprintf("category %q has no recorded source repository and path", category.Name))
	}
	if err := requireDefaultBranchProvenance(category); err != nil {
		return category, "", "", err
	}
	owners, ok := s.store.(datastore.OwnerReferenceStore)
	if !ok {
		return nil, "", "", fmt.Errorf("category deletion is unavailable while owner-reference indexing is disabled")
	}
	hasChildren, err := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{
		Namespace: category.Namespace, RepositoryID: category.RepositoryID,
	}, category.UID)
	if err != nil {
		return nil, "", "", fmt.Errorf("check category deletion dependents: %w", err)
	}
	if hasChildren {
		return category, "", "", admission.NewError(admission.CodeFailedPrecondition, "CHILD_CATEGORIES_PRESENT",
			fmt.Sprintf("category %q has child categories", category.Name))
	}
	if s.gitWriter == nil || s.committedAdmitter == nil {
		return nil, "", "", fmt.Errorf("category admission runtime is unavailable")
	}
	commitSHA, err := s.gitWriter.DeleteFileForRepo(ctx, category.RepositoryID, gitclient.DeleteFileParams{
		Path: category.SourcePath, CommitMessage: fmt.Sprintf("Delete CategoryTaxonomy %s", category.Name), AuthorName: caller,
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("delete category manifest: %w", err)
	}
	refName := categoryRefName
	if _, err := s.committedAdmitter.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
		RepositoryID: category.RepositoryID, Namespace: category.Namespace, ActorSubject: caller,
		CommitSHA: commitSHA, RefName: refName, Path: category.SourcePath,
		Operation: admission.OperationDelete, Kind: categoryKind, Name: category.Name,
	}); err != nil {
		return nil, "", commitSHA, categoryAdmissionError(err, commitSHA)
	}
	terminating, err := s.store.GetCategoryTaxonomy(ctx, uid)
	if err != nil {
		return nil, "", commitSHA, fmt.Errorf("re-read terminating category: %w", err)
	}
	return terminating, model.ResourceDeletionOutcomeTerminationStarted, commitSHA, nil
}

// completeCategoryDeletion backs the completeCategoryDeletion mutation.
func (r *mutationResolver) completeCategoryDeletion(ctx context.Context, namespace, name, resourceVersion string) (*datastore.CategoryTaxonomy, error) {
	deleted, err := r.service.CompleteCategoryDeletion(ctx, namespace, name, resourceVersion)
	if errors.Is(err, datastore.ErrConflict) {
		if deleted == nil {
			return nil, fmt.Errorf("complete category deletion conflict, and current version could not be read")
		}
		return nil, statusConflictError(categoryKind, namespace, name, deleted.ResourceVersion)
	}
	if err != nil {
		return nil, categoryMutationGraphQLError(err)
	}
	return deleted, nil
}

const (
	// categoryFilterMaxDepthArgument is the documented upper bound of the
	// maxDepth argument.
	categoryFilterMaxDepthArgument = 128
	categoryFilterMaxPage          = datastore.DefaultPageSize
	categoryFilterFetchFanout      = 16
)

func badCategoryListArgument(message string) error {
	return admission.NewError(admission.CodeBadUserInput, "INVALID_ARGUMENT", message).ToGQLError()
}

// listCategoryDescendants serves categories(filter:) from the ancestor index:
// one bounded slice of a single ancestor's partition, never a namespace scan.
// Rows are hydrated by UID with bounded concurrency; rows whose record no
// longer matches (removed or recreated, pending repair) are dropped.
func (s *Service) listCategoryDescendants(ctx context.Context, namespace string, filter *model.CategoryFilterInput, page datastore.PageParams) (*model.CategoryConnection, error) {
	maxDepth := 0
	if filter.MaxDepth != nil {
		if *filter.MaxDepth < 1 || *filter.MaxDepth > categoryFilterMaxDepthArgument {
			return nil, badCategoryListArgument(fmt.Sprintf("maxDepth must be between 1 and %d", categoryFilterMaxDepthArgument))
		}
		// No row is deeper than the index can hold, so a larger bound is the
		// whole subtree.
		maxDepth = min(int(*filter.MaxDepth), datastore.MaxCategoryHierarchyDepth)
	}
	if page.First > categoryFilterMaxPage {
		page.First = categoryFilterMaxPage
	}
	if page.Last > categoryFilterMaxPage {
		page.Last = categoryFilterMaxPage
	}
	index, ok := s.store.(datastore.CategoryAncestorIndex)
	if !ok {
		return nil, fmt.Errorf("category subtree filtering is unavailable")
	}
	rows, err := index.ListCategoryDescendants(ctx, datastore.CategoryDescendantQuery{
		Namespace: namespace, Ancestor: filter.DescendantOf,
		IncludeSelf: filter.IncludeSelf != nil && *filter.IncludeSelf, MaxDepth: maxDepth, Page: page,
	})
	if err != nil {
		if errors.Is(err, datastore.ErrInvalidArgument) {
			return nil, badCategoryListArgument("cursor does not belong to a filtered category list")
		}
		return nil, fmt.Errorf("list category descendants: %w", err)
	}

	records := make([]*datastore.CategoryTaxonomy, len(rows.Items))
	errs := make([]error, len(rows.Items))
	sem := make(chan struct{}, categoryFilterFetchFanout)
	var wg sync.WaitGroup
	for i, row := range rows.Items {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			record, err := s.store.GetCategoryTaxonomy(ctx, row.UID)
			switch {
			case errors.Is(err, datastore.ErrNotFound):
			case err != nil:
				errs[i] = err
			case record.Namespace == namespace && record.Name == row.Name:
				records[i] = record
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("load category descendants: %w", err)
	}

	edges := make([]*model.CategoryEdge, 0, len(rows.Items))
	for i, row := range rows.Items {
		if records[i] == nil {
			continue
		}
		edges = append(edges, &model.CategoryEdge{
			Cursor: datastore.EncodeClosureCursor(row.Depth, row.Name),
			Node:   DatastoreCategoryTaxonomyToGraphQL(records[i]),
		})
	}
	pageInfo := &model.PageInfo{HasNextPage: rows.HasNext, HasPreviousPage: rows.HasPrevious}
	if len(rows.Items) > 0 {
		// Cursors span every returned row, including dropped ones, so paging
		// past stale rows still advances.
		first, last := rows.Items[0], rows.Items[len(rows.Items)-1]
		start, end := datastore.EncodeClosureCursor(first.Depth, first.Name), datastore.EncodeClosureCursor(last.Depth, last.Name)
		pageInfo.StartCursor, pageInfo.EndCursor = &start, &end
	}
	return &model.CategoryConnection{Edges: edges, PageInfo: pageInfo}, nil
}

func categoryConditionTrue(conditions []*model.Condition, conditionType string) bool {
	for _, condition := range conditions {
		if condition != nil && condition.Type == conditionType {
			return condition.Status == model.ConditionStatusTrue
		}
	}
	return false
}

// requireDefaultBranchProvenance rejects mutating a category admitted from a
// ref other than the default branch: the Git writer commits only to the
// default branch, so writing there would fork the manifest from the ref the
// category was admitted from.
func requireDefaultBranchProvenance(category *datastore.CategoryTaxonomy) error {
	if category.GitRef == "" || category.GitRef == categoryRefName {
		return nil
	}
	return admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_UNAVAILABLE",
		fmt.Sprintf("category %q was admitted from %s; API mutations write only %s", category.Name, category.GitRef, categoryRefName))
}
