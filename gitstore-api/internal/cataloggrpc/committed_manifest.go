// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
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
	if s.git == nil || s.store == nil {
		return nil, fmt.Errorf("committed admission is unavailable")
	}
	if req.RepositoryID == "" || req.CommitSHA == "" || req.RefName == "" || req.Path == "" {
		return nil, fmt.Errorf("committed admission requires repository, ref, commit, and path")
	}
	if req.Operation == admission.OperationDelete {
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
		latest, err := s.git.ReadFile(ctx, req.RepositoryID, req.Path, current)
		if err != nil || !bytes.Equal(latest, content) {
			return nil, ErrCommittedManifestSuperseded
		}
		content = latest
		commitSHA = current
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
	if err := s.admitParsedEntries(ctx, []*parsedEntry{entry}, admCtx, ops); err != nil {
		return nil, err
	}
	if admCtx.wasSuperseded() || !s.isAdmissionCommitCurrent(ctx, req.RepositoryID, req.RefName, commitSHA) {
		return nil, ErrCommittedManifestSuperseded
	}
	stored, err := s.lookupResourceByIdentity(ctx, entry.identity)
	if err != nil || stored == nil {
		if err == nil {
			err = datastore.ErrNotFound
		}
		return nil, fmt.Errorf("committed admission did not materialize %s %s/%s: %w", entry.identity.Kind, entry.identity.Namespace, entry.identity.Name, err)
	}
	return &admission.CommittedManifestResult{Kind: entry.identity.Kind, Namespace: entry.identity.Namespace, Name: entry.identity.Name, CommitSHA: commitSHA}, nil
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
		return fmt.Errorf("%s %s/%s already exists", entry.identity.Kind, entry.identity.Namespace, entry.identity.Name)
	}
	if operation == admission.OperationUpdate && existing == nil {
		if entry.identity.Kind == "Namespace" {
			return namespaceadmission.ErrNamespaceNotFound
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
