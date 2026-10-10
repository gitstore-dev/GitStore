// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// deleteInfrastructureManifest records authorization before the cross-service
// write. The existing resource watch drives controller recovery of that record.
func (s *Service) deleteInfrastructureManifest(ctx context.Context, resource any, caller string) error {
	if s.gitWriter == nil || s.committedAdmitter == nil {
		return fmt.Errorf("git deletion admission runtime is unavailable")
	}
	var kind, name, namespace, uid, path, ref, commit, authorNamespace string
	var intent *admission.InfrastructureDeletionIntent
	var err error
	switch r := resource.(type) {
	case *datastore.Namespace:
		kind, name, uid, path, ref, commit = "Namespace", r.Name, r.UID, r.SourcePath, r.GitRef, r.GitCommitSHA
		authorNamespace = SystemRepositoryName
		intent, err = admission.ReadDeletionIntent(r.Status)
	case *datastore.Repository:
		kind, name, namespace, uid, path, ref, commit = "Repository", r.Name, r.Namespace, r.UID, r.SourcePath, r.GitRef, r.GitCommitSHA
		authorNamespace = namespace
		intent, err = admission.ReadDeletionIntent(r.Status)
	default:
		return fmt.Errorf("unsupported deletion resource %T", resource)
	}
	if err != nil {
		return err
	}
	expectedPath := "repositories/" + name + ".md"
	if kind == "Namespace" {
		expectedPath = "namespaces/" + name + ".md"
	}
	if path != expectedPath || ref != "refs/heads/main" || commit == "" {
		return admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_UNAVAILABLE", "deletion requires canonical admitted manifest provenance on refs/heads/main")
	}
	mapping, err := s.store.LookupRepository(ctx, authorNamespace, SystemRepositoryName)
	if err != nil {
		return fmt.Errorf("resolve authoring repository: %w", err)
	}
	authoring, err := s.store.GetRepository(ctx, mapping.RepositoryID)
	if err != nil {
		return err
	}
	if authoring.Namespace != authorNamespace || authoring.Name != SystemRepositoryName {
		return admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_MISMATCH", "authoring repository identity does not match")
	}
	if intent == nil {
		if err := admission.CheckInfrastructureDeletion(ctx, s.store, resource); err != nil {
			return err
		}
		head, err := s.gitWriter.ResolveRefForRepo(ctx, mapping.RepositoryID, ref)
		if err != nil {
			return err
		}
		content, err := s.gitWriter.ReadFileForRepo(ctx, mapping.RepositoryID, path, head)
		if err != nil {
			return fmt.Errorf("read deletion manifest: %w", err)
		}
		admitted, err := s.gitWriter.ReadFileForRepo(ctx, mapping.RepositoryID, path, commit)
		if err != nil {
			return fmt.Errorf("read admitted deletion manifest: %w", err)
		}
		if !bytes.Equal(content, admitted) {
			return admission.NewError(admission.CodeConflict, "SUPERSEDED", "manifest has changed since admission")
		}
		if err := validateDeletionIdentity(content, kind, namespace, name); err != nil {
			return err
		}
		intent = &admission.InfrastructureDeletionIntent{UID: uid, RepositoryID: mapping.RepositoryID, Path: path, Ref: ref, ExpectedCommit: head, Actor: caller}
		if err := s.saveDeletionIntent(ctx, resource, intent); err != nil {
			return err
		}
	}
	if intent.UID != uid || intent.Path != path || intent.Ref != ref || intent.RepositoryID != mapping.RepositoryID || intent.ExpectedCommit == "" {
		return admission.NewError(admission.CodeFailedPrecondition, "DELETION_INTENT_MISMATCH", "deletion intent does not match resource provenance")
	}
	head, err := s.gitWriter.ResolveRefForRepo(ctx, intent.RepositoryID, intent.Ref)
	if err != nil {
		return err
	}
	content, readErr := s.gitWriter.ReadFileForRepo(ctx, intent.RepositoryID, intent.Path, head)
	switch {
	case readErr == nil:
		if intent.RemovalCommit != "" {
			return admission.NewError(admission.CodeConflict, "SUPERSEDED", "manifest was restored after removal; operator reconciliation is required")
		}
	case status.Code(readErr) != codes.NotFound:
		return fmt.Errorf("verify pending deletion: %w", readErr)
	}
	removal, err := s.gitWriter.DeleteFileForRepo(ctx, intent.RepositoryID, gitclient.DeleteFileParams{
		Path: intent.Path, CommitMessage: fmt.Sprintf("Delete %s %s", kind, name), AuthorName: intent.Actor,
		RefName: intent.Ref, ExpectedCommitSHA: intent.ExpectedCommit,
	})
	if err != nil {
		if status.Code(err) == codes.Aborted && readErr == nil && head != intent.ExpectedCommit {
			original, readErr := s.gitWriter.ReadFileForRepo(ctx, intent.RepositoryID, intent.Path, intent.ExpectedCommit)
			if readErr != nil {
				return readErr
			}
			if !bytes.Equal(original, content) {
				return admission.NewError(admission.CodeConflict, "SUPERSEDED", "manifest changed before removal; operator reconciliation is required")
			}
			intent.ExpectedCommit = head
			if saveErr := s.saveDeletionIntent(ctx, resource, intent); saveErr != nil {
				return saveErr
			}
		}
		return fmt.Errorf("remove %s manifest: %w", kind, err)
	}
	intent.RemovalCommit = removal
	if err := s.saveDeletionIntent(ctx, resource, intent); err != nil {
		return err
	}
	head, err = s.gitWriter.ResolveRefForRepo(ctx, intent.RepositoryID, intent.Ref)
	if err != nil {
		return err
	}
	_, err = s.committedAdmitter.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
		RepositoryID: intent.RepositoryID, Namespace: namespace, ActorSubject: intent.Actor,
		CommitSHA: head, RefName: intent.Ref, Path: intent.Path, Kind: kind, Name: name,
		Operation: admission.OperationDelete, ExpectedUID: uid,
	})
	if err != nil {
		return committedAdmissionError(err, head, kind)
	}
	return nil
}

func (s *Service) saveDeletionIntent(ctx context.Context, resource any, intent *admission.InfrastructureDeletionIntent) error {
	now := s.clock.Now().UTC()
	switch r := resource.(type) {
	case *datastore.Namespace:
		expected := r.ResourceVersion
		raw, err := admission.WithDeletionIntent(r.Status, intent, r.Generation, now)
		if err != nil {
			return err
		}
		r.Status = raw
		datastore.AdvanceNamespaceSystemVersion(r)
		return s.store.UpdateNamespace(ctx, r, expected)
	case *datastore.Repository:
		expected := r.ResourceVersion
		raw, err := admission.WithDeletionIntent(r.Status, intent, r.Generation, now)
		if err != nil {
			return err
		}
		r.Status = raw
		datastore.AdvanceRepositorySystemVersion(r)
		return s.store.UpdateRepository(ctx, r, expected)
	}
	return fmt.Errorf("unsupported deletion intent resource %T", resource)
}

func validateDeletionIdentity(content []byte, kind, namespace, name string) error {
	parsed, _, err := validate.NewParser().ParseResource(bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("parse deletion manifest: %w", err)
	}
	if parsed != nil {
		if kind == "Namespace" && parsed.Namespace != nil && parsed.Namespace.Metadata.Name == name {
			return nil
		}
		if kind == "Repository" && parsed.Repository != nil && parsed.Repository.Metadata.Name == name && parsed.Repository.Metadata.Namespace == namespace {
			return nil
		}
	}
	return admission.NewError(admission.CodeFailedPrecondition, "PROVENANCE_MISMATCH", "manifest identity does not match deletion target")
}

func deletionActor(ctx context.Context) string {
	if principal := auth.PrincipalFromContext(ctx); principal != nil {
		return principal.Subject
	}
	return "system"
}

func (s *Service) verifyInfrastructureRemoval(ctx context.Context, raw []byte, uid string) error {
	intent, err := admission.ReadDeletionIntent(raw)
	if err != nil {
		return err
	}
	if intent == nil || intent.UID != uid || intent.RemovalCommit == "" || s.gitWriter == nil {
		return admission.NewError(admission.CodeFailedPrecondition, "DELETION_REPAIR_REQUIRED", "finalization requires recorded manifest removal; reconcile legacy deletion first")
	}
	_, err = s.gitWriter.ReadFileForRepo(ctx, intent.RepositoryID, intent.Path, intent.Ref)
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify manifest removal: %w", err)
	}
	return admission.NewError(admission.CodeFailedPrecondition, "MANIFEST_PRESENT", "manifest still exists; finalization is blocked")
}

func (s *Service) finalizeNamespaceSystemRepository(ctx context.Context, ns *datastore.Namespace) error {
	blocked, err := datastore.NamespaceDeletionBlocked(ctx, s.store, ns)
	if err != nil {
		return err
	}
	if blocked {
		return admission.NewError(admission.CodeFailedPrecondition, "NAMESPACE_NOT_EMPTY", "namespace still has repositories or catalog resources")
	}
	mapping, err := s.store.LookupRepository(ctx, ns.Name, SystemRepositoryName)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	repo, err := s.store.GetRepository(ctx, mapping.RepositoryID)
	if err != nil {
		return err
	}
	if repo.Name != SystemRepositoryName || repo.Namespace != ns.Name {
		return fmt.Errorf("system repository identity mismatch")
	}
	if repo.DeletionTimestamp == nil {
		expected := repo.ResourceVersion
		now := s.clock.Now().UTC()
		repo.DeletionTimestamp = &now
		repo.Finalizers = append(repo.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
		datastore.AdvanceRepositorySystemVersion(repo)
		if err := s.store.UpdateRepository(ctx, repo, expected); err != nil {
			return err
		}
	}
	_, err = s.CompleteRepositoryDeletion(ctx, ns.Name, repo.Name, repo.ResourceVersion, repo.UID)
	return err
}
