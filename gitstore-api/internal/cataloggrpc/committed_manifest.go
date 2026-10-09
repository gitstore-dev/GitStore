// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"go.uber.org/zap"
)

// ErrCommittedManifestSuperseded is retained as an alias for callers that
// historically depend on cataloggrpc. New callers use admission's shared
// sentinel, avoiding a GraphQL-to-gRPC package dependency.
var ErrCommittedManifestSuperseded = admission.ErrCommittedManifestSuperseded

// AdmitCommittedManifest materializes one already-committed manifest through
// the same parsed-entry and admission operations used by AdmitResources. It is
// the synchronous counterpart of the post-receive batch path; neither callers
// nor GraphQL resolvers may write an author-controlled resource directly.
func (s *Server) AdmitCommittedManifest(ctx context.Context, req admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
	result, err := s.admitCommittedManifest(ctx, req)
	// Constructor dependencies and request shape are not post-commit failures;
	// attempting repair for either can dereference an unavailable Git reader.
	if err == nil || isCommittedAdmissionDenied(err) || s.git == nil || s.store == nil ||
		req.RepositoryID == "" || req.CommitSHA == "" || req.RefName == "" || req.Path == "" {
		return result, err
	}

	// A Git write is already durable when this method is called.  Keep the
	// caller-visible result of that write, but make a small, synchronous effort
	// to converge its tip.  The repair context deliberately survives a canceled
	// request: cancellation after commit must not strand the manifest.
	repairCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	repaired := s.repairCommittedManifest(repairCtx, req)
	s.log.Info("committed manifest admission repair completed",
		zap.String("repository_id", req.RepositoryID), zap.String("ref_name", req.RefName),
		zap.String("path", req.Path), zap.String("commit_sha", req.CommitSHA),
		zap.String("outcome", repaired))
	return result, err
}

func isCommittedAdmissionDenied(err error) bool {
	var admissionErr *admission.Error
	if !errors.As(err, &admissionErr) || admissionErr.Code != admission.CodeAdmissionRejected {
		return false
	}
	// EntryFailed is also represented as a post-receive rejection so callers
	// receive the normal wire envelope.  Only an actual policy/content denial
	// is terminal for repair; a write failure is worth retrying at the same tip.
	for _, diagnostic := range admissionErr.Diagnostics {
		if diagnostic.Reason == "ADMISSION_FAILED" {
			return false
		}
	}
	return true
}

// admitCommittedManifest performs one admission attempt.  Its public wrapper
// is responsible for best-effort repair while preserving this attempt's error.
func (s *Server) admitCommittedManifest(ctx context.Context, req admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
	if s.git == nil || s.store == nil {
		return nil, fmt.Errorf("committed admission is unavailable")
	}
	if req.RepositoryID == "" || req.CommitSHA == "" || req.RefName == "" || req.Path == "" {
		return nil, fmt.Errorf("committed admission requires repository, ref, commit, and path")
	}
	if req.Operation == admission.OperationDelete {
		if req.Kind == "CategoryTaxonomy" {
			return s.admitCommittedCategoryDeletion(ctx, req)
		}
		return s.admitCommittedProductDeletion(ctx, req)
	}
	if req.Operation != admission.OperationCreate && req.Operation != admission.OperationUpdate {
		return nil, fmt.Errorf("committed admission requires create, update, or delete operation")
	}

	content := append([]byte(nil), req.Content...)
	commitSHA := req.CommitSHA
	current, ok := s.currentAdmissionCommit(ctx, req.RepositoryID, req.RefName, req.CommitSHA)
	if !ok {
		return nil, ErrCommittedManifestSuperseded
	}
	if current != req.CommitSHA {
		return nil, ErrCommittedManifestSuperseded
	}

	parsed, body, err := s.parser.ParseResource(bytes.NewReader(content))
	if err != nil || parsed == nil {
		if err == nil {
			err = errors.New("empty resource")
		}
		return nil, fmt.Errorf("parse committed manifest: %w", err)
	}
	entry, accepted, err := newParsedEntry(req.Path, parsed, body, req.Namespace)
	if err != nil {
		return nil, fmt.Errorf("committed manifest identity: %w", err)
	}
	if !accepted {
		return nil, fmt.Errorf("committed manifest has no supported resource")
	}
	if err := s.validateCommittedAuthoringTarget(ctx, req.RepositoryID, entry); err != nil {
		return nil, err
	}
	if err := s.validateCommittedOperation(ctx, entry, req.Operation); err != nil {
		return nil, err
	}

	actor := req.ActorSubject
	if actor == "" {
		actor = "admission"
	}
	now := s.clock.Now().UTC()
	superseded := false
	admCtx := AdmissionContext{
		RepositoryID: req.RepositoryID,
		Namespace:    req.Namespace,
		ActorSubject: actor,
		CommitSHA:    commitSHA,
		RefName:      req.RefName,
		Revision:     strings.TrimPrefix(req.RefName, "refs/heads/") + "@sha1:" + commitSHA,
		Now:          now,
		superseded:   &superseded,
	}
	ops := map[string]resourceAdmissionOperation{entry.identity.key(): {
		operation: req.Operation,
		newEntry:  entry,
		identity:  entry.identity,
	}}
	decisions, err := s.admitParsedEntries(ctx, []*parsedEntry{entry}, admCtx, ops)
	if err != nil {
		return nil, err
	}
	if admCtx.wasSuperseded() || !s.isAdmissionCommitCurrent(ctx, req.RepositoryID, req.RefName, commitSHA) {
		return nil, ErrCommittedManifestSuperseded
	}
	result := &admission.CommittedManifestResult{Kind: entry.identity.Kind, Namespace: entry.identity.Namespace, Name: entry.identity.Name, CommitSHA: commitSHA}
	for _, decision := range decisions {
		if decision.Kind != entry.identity.Kind || decision.Name != entry.identity.Name {
			continue
		}
		switch decision.Outcome {
		case admission.EntryDenied:
			return nil, admission.Rejected(admission.PhasePostReceive, commitSHA, decision.Diagnostics)
		case admission.EntryFailed:
			if decision.Kind == "CategoryTaxonomy" && errors.Is(decision.Err, datastore.ErrAlreadyExists) {
				// A concurrent create of the same name won the datastore write.
				return nil, admission.NewError(admission.CodeAlreadyExists, "CATEGORY_ALREADY_EXISTS",
					fmt.Sprintf("category %q already exists", decision.Name))
			}
			return nil, admission.Rejected(admission.PhasePostReceive, commitSHA, []admission.Diagnostic{{
				Reason: "ADMISSION_FAILED", Message: "admission failed: " + errorText(decision.Err), Level: admission.LevelFailure, File: entry.path,
			}})
		case admission.EntryNoOp:
			result.NoOp = true
		}
		result.Warnings = append(result.Warnings, decision.Warnings...)
	}
	stored, err := s.lookupResourceByIdentity(ctx, entry.identity)
	if err != nil || stored == nil {
		if err == nil {
			err = datastore.ErrNotFound
		}
		return nil, fmt.Errorf("committed admission did not materialize %s %s/%s: %w", entry.identity.Kind, entry.identity.Namespace, entry.identity.Name, err)
	}
	if !committedResourceMatches(stored, req, entry, result.NoOp) {
		// Product historically represents an identical manifest as a silent
		// no-op. Treat that as success only after proving the stored body and
		// provenance are identical; otherwise this is a real failed admission.
		if committedResourceMatches(stored, req, entry, true) {
			result.NoOp = true
			return result, nil
		}
		return nil, fmt.Errorf("committed admission did not materialize current content and provenance for %s %s/%s", entry.identity.Kind, entry.identity.Namespace, entry.identity.Name)
	}
	return result, nil
}

// repairCommittedManifest retries only while this caller's commit remains the
// ref tip.  It reads the authoritative tree again and derives the operation
// from the current projection, preventing a stale caller from admitting a
// later writer's content under its own identity.
func (s *Server) repairCommittedManifest(ctx context.Context, req admission.CommittedManifestRequest) string {
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return "budget_exhausted"
		}
		current, ok := s.currentAdmissionCommit(ctx, req.RepositoryID, req.RefName, req.CommitSHA)
		if !ok || current != req.CommitSHA {
			return "superseded"
		}
		retry := req
		if req.Operation == admission.OperationDelete {
			// Deletion admission validates stored provenance itself.
			if _, err := s.admitCommittedManifest(ctx, retry); err == nil {
				return "repaired"
			} else if isCommittedAdmissionDenied(err) {
				return "denied"
			}
			continue
		}

		content, err := s.git.ReadFile(ctx, req.RepositoryID, req.Path, current)
		if err != nil {
			return "read_failed"
		}
		parsed, body, err := s.parser.ParseResource(bytes.NewReader(content))
		if err != nil || parsed == nil {
			return "invalid_content"
		}
		entry, accepted, err := newParsedEntry(req.Path, parsed, body, req.Namespace)
		if err != nil || !accepted {
			return "identity_mismatch"
		}
		// The request normally has no Kind/Name for create/update, so establish
		// identity from its original content before accepting the reread file.
		original, originalBody, originalErr := s.parser.ParseResource(bytes.NewReader(req.Content))
		if originalErr != nil || original == nil {
			return "identity_mismatch"
		}
		originalEntry, originalAccepted, identityErr := newParsedEntry(req.Path, original, originalBody, req.Namespace)
		if identityErr != nil || !originalAccepted || originalEntry.identity != entry.identity {
			return "identity_mismatch"
		}
		retry.Content = content
		// The mutation was authorized for req.Operation. Do not turn a failed
		// create into an update (or vice versa) here: this boundary has no
		// principal authorizer and must not broaden the caller's authority.
		retry.Operation = req.Operation
		if _, err := s.admitCommittedManifest(ctx, retry); err == nil {
			return "repaired"
		} else if isCommittedAdmissionDenied(err) {
			return "denied"
		}
	}
	return "attempts_exhausted"
}

func committedResourceMatches(resource any, req admission.CommittedManifestRequest, entry *parsedEntry, noOp bool) bool {
	if entry == nil {
		return false
	}
	var repositoryID, sourcePath, gitRef, commitSHA string
	var apiVersion, kind, namespace, name, body string
	var labels, annotations map[string]string
	var storedSpec []byte
	switch v := resource.(type) {
	case *datastore.CategoryTaxonomy:
		repositoryID, sourcePath, gitRef, commitSHA = v.RepositoryID, v.SourcePath, v.GitRef, v.GitCommitSHA
		apiVersion, kind, namespace, name, labels, annotations, body, storedSpec = v.APIVersion, v.Kind, v.Namespace, v.Name, v.Labels, v.Annotations, v.Body, v.Spec
	case *datastore.Product:
		repositoryID, sourcePath, gitRef, commitSHA = v.RepositoryID, v.SourcePath, v.GitRef, v.GitCommitSHA
		apiVersion, kind, namespace, name, labels, annotations, body, storedSpec = v.APIVersion, v.Kind, v.Namespace, v.Name, v.Labels, v.Annotations, v.Body, v.Spec
	case *datastore.Namespace:
		// Namespace provenance is its configured authoring target rather than a
		// stored repository ID (Namespace is cluster-scoped).
		sourcePath, gitRef, commitSHA = v.SourcePath, v.GitRef, v.GitCommitSHA
		apiVersion, kind, name, labels, annotations, body, storedSpec = v.APIVersion, v.Kind, v.Name, v.Labels, v.Annotations, v.Body, v.Spec
	case *datastore.Repository:
		// Repository.RepositoryID is the ID being declared, not the system
		// repository that authored this manifest.
		sourcePath, gitRef, commitSHA = v.SourcePath, v.GitRef, v.GitCommitSHA
		apiVersion, kind, namespace, name, labels, annotations, body, storedSpec = v.APIVersion, v.Kind, v.Namespace, v.Name, v.Labels, v.Annotations, v.Body, v.Spec
	case *datastore.File:
		repositoryID, sourcePath, gitRef, commitSHA = v.RepositoryID, v.SourcePath, v.GitRef, v.GitCommitSHA
		apiVersion, kind, namespace, name, labels, annotations, body, storedSpec = v.APIVersion, v.Kind, v.Namespace, v.Name, v.Labels, v.Annotations, v.Body, v.Spec
	case *datastore.Collection:
		repositoryID, sourcePath, gitRef, commitSHA = v.RepositoryID, v.SourcePath, v.GitRef, v.GitCommitSHA
		apiVersion, kind, namespace, name, labels, annotations, body, storedSpec = v.APIVersion, v.Kind, v.Namespace, v.Name, v.Labels, v.Annotations, v.Body, v.Spec
	case *datastore.ProductVariant:
		repositoryID, sourcePath, gitRef, commitSHA = v.RepositoryID, v.SourcePath, v.GitRef, v.GitCommitSHA
		apiVersion, kind, namespace, name, labels, annotations, body, storedSpec = v.APIVersion, v.Kind, v.Namespace, v.Name, v.Labels, v.Annotations, v.Body, v.Spec
	default:
		return false
	}
	if (repositoryID != "" && repositoryID != req.RepositoryID) || sourcePath != req.Path || gitRef != req.RefName {
		return false
	}
	expected, ok := comparableForParsed(entry.parsed, entry.body, req.Namespace)
	if !ok {
		return false
	}
	expectedSpec, expectedErr := json.Marshal(expected.Spec)
	var expectedValue, storedValue any
	if expectedErr != nil || json.Unmarshal(expectedSpec, &expectedValue) != nil || json.Unmarshal(storedSpec, &storedValue) != nil ||
		apiVersion != expected.APIVersion || kind != expected.Kind || namespace != expected.Namespace || name != expected.Name ||
		!maps.Equal(labels, expected.Labels) || !maps.Equal(annotations, expected.Annotations) || body != expected.Body || !reflect.DeepEqual(storedValue, expectedValue) {
		return false
	}
	// A verified no-op intentionally retains its earlier commit, but it must
	// still prove that the admitted body and provenance are the same.
	return noOp || commitSHA == req.CommitSHA
}

func errorText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func (s *Server) admitCommittedProductDeletion(ctx context.Context, req admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
	if req.Kind != "Product" || req.Name == "" {
		return nil, fmt.Errorf("committed deletion requires a Product identity")
	}
	current, ok := s.currentAdmissionCommit(ctx, req.RepositoryID, req.RefName, req.CommitSHA)
	if !ok || current != req.CommitSHA {
		return nil, ErrCommittedManifestSuperseded
	}
	product, err := s.store.GetProductByName(ctx, req.Namespace, req.Name)
	if err != nil {
		return nil, err
	}
	if product.RepositoryID != req.RepositoryID || product.SourcePath != req.Path {
		return nil, fmt.Errorf("committed Product deletion provenance does not match")
	}
	lifecycle, ok := s.store.(datastore.ProductLifecycleStore)
	if !ok {
		return nil, fmt.Errorf("product lifecycle datastore is unavailable")
	}
	if _, err := lifecycle.MarkProductTerminating(ctx, product.UID, product.ResourceVersion, "gitstore.dev/foreground-deletion", s.clock.Now().UTC()); err != nil {
		return nil, err
	}
	return &admission.CommittedManifestResult{Kind: "Product", Namespace: product.Namespace, Name: product.Name, CommitSHA: req.CommitSHA}, nil
}

// admitCommittedCategoryDeletion admits a committed removal of a category
// manifest through the same deletion path a push uses: the blocking-child
// check and the foreground-deletion mark.
func (s *Server) admitCommittedCategoryDeletion(ctx context.Context, req admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("committed deletion requires a CategoryTaxonomy name")
	}
	current, ok := s.currentAdmissionCommit(ctx, req.RepositoryID, req.RefName, req.CommitSHA)
	if !ok || current != req.CommitSHA {
		return nil, ErrCommittedManifestSuperseded
	}
	category, err := s.store.GetCategoryTaxonomyByName(ctx, req.Namespace, req.Name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, admission.NewError(admission.CodeNotFound, "CATEGORY_NOT_FOUND", fmt.Sprintf("category %q not found", req.Name))
		}
		return nil, err
	}
	if category.RepositoryID != req.RepositoryID || category.SourcePath != req.Path {
		return nil, admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_UNAVAILABLE",
			fmt.Sprintf("category %q was not admitted from %s", req.Name, req.Path))
	}
	identity := resourceIdentity{Kind: "CategoryTaxonomy", Namespace: category.Namespace, Name: category.Name}
	actor := req.ActorSubject
	if actor == "" {
		actor = "admission"
	}
	if err := s.deleteResource(ctx, identity, req.RepositoryID, category.GitRef, req.Path, actor); err != nil {
		if errors.Is(err, errCategoryDeletionBlocked) {
			return nil, admission.Rejected(admission.PhasePostReceive, req.CommitSHA, []admission.Diagnostic{{
				Reason: "CHILD_CATEGORIES_PRESENT", Message: fmt.Sprintf("category %q has child categories", req.Name),
				Level: admission.LevelFailure, File: req.Path,
			}})
		}
		return nil, err
	}
	return &admission.CommittedManifestResult{Kind: "CategoryTaxonomy", Namespace: category.Namespace, Name: category.Name, CommitSHA: req.CommitSHA}, nil
}

// validateCommittedOperation makes synchronous admission strict. The batch
// pipeline deliberately logs and continues for one bad file; GraphQL has a
// single object and must return that rejection to its caller instead.
func (s *Server) validateCommittedOperation(ctx context.Context, entry *parsedEntry, operation admission.Operation) error {
	existing, err := s.lookupResourceByIdentity(ctx, entry.identity)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return err
	}
	if errors.Is(err, datastore.ErrNotFound) {
		existing = nil
	}
	if operation == admission.OperationCreate && existing != nil {
		if entry.identity.Kind == "Namespace" {
			return namespaceadmission.ErrNamespaceAlreadyExists
		}
		if entry.identity.Kind == "CategoryTaxonomy" {
			return admission.NewError(admission.CodeAlreadyExists, "CATEGORY_ALREADY_EXISTS", fmt.Sprintf("category %q already exists", entry.identity.Name))
		}
		return fmt.Errorf("%s %s/%s already exists", entry.identity.Kind, entry.identity.Namespace, entry.identity.Name)
	}
	if operation == admission.OperationUpdate && existing == nil {
		if entry.identity.Kind == "Namespace" {
			return namespaceadmission.ErrNamespaceNotFound
		}
		if entry.identity.Kind == "CategoryTaxonomy" {
			return admission.NewError(admission.CodeNotFound, "CATEGORY_NOT_FOUND", fmt.Sprintf("category %q not found", entry.identity.Name))
		}
		return fmt.Errorf("%s %s/%s not found", entry.identity.Kind, entry.identity.Namespace, entry.identity.Name)
	}
	switch resource := existing.(type) {
	case *datastore.Namespace:
		if namespaceadmission.IsBootstrap(entry.identity.Name) {
			return namespaceadmission.ErrBootstrapNamespace
		}
		if resource != nil && resource.DeletionTimestamp != nil {
			return namespaceadmission.ErrNamespaceTerminating
		}
		if resource != nil && entry.parsed.Namespace != nil {
			tier, ok := namespaceadmission.TierFromManifest(entry.parsed.Namespace.Spec.Tier)
			if !ok {
				return fmt.Errorf("unsupported tier %q", entry.parsed.Namespace.Spec.Tier)
			}
			if namespaceadmission.TierRank(tier) < namespaceadmission.TierRank(resource.Tier) {
				return namespaceadmission.ErrTierDemotion
			}
		}
	case *datastore.Repository:
		if resource != nil && entry.parsed.Repository != nil && isRepositoryStorageClassDowngrade(resource.StorageClass, entry.parsed.Repository.Spec.StorageClass) {
			return fmt.Errorf("repository storageClass downgrade is not allowed")
		}
	}
	if entry.identity.Kind == "Namespace" && namespaceadmission.IsBootstrap(entry.identity.Name) {
		return namespaceadmission.ErrBootstrapNamespace
	}
	return nil
}

func (s *Server) validateCommittedAuthoringTarget(ctx context.Context, repositoryID string, entry *parsedEntry) error {
	switch entry.identity.Kind {
	case "Namespace":
		return s.validateNamespaceAuthoringTarget(ctx, repositoryID, entry.path, entry.identity.Name)
	case "Repository":
		return s.validateRepositoryAuthoringTarget(ctx, repositoryID, entry.path, entry.identity.Namespace, entry.identity.Name)
	default:
		return nil
	}
}
