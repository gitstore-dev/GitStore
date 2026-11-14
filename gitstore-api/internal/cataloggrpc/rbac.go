// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) projectRBACReceive(ctx context.Context, req *catalogv1.AdmitResourcesRequest) error {
	if s.rbacValidator == nil {
		return nil
	}
	// The ref is already committed. Request cancellation or actor revocation
	// cannot undo it or become a fresh authorization veto.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	repository, err := s.store.GetRepository(ctx, req.GetRepositoryId())
	if err != nil {
		return err
	}
	datastore.NormalizeRepositoryContract(repository)
	if repository.Name != "gitstore-system" {
		return nil
	}
	if req.GetRefName() != "refs/heads/"+repository.DefaultBranch {
		return nil
	}
	namespace, err := s.store.GetNamespaceByName(ctx, repository.Namespace)
	if err != nil || namespace == nil {
		return rbac.ErrUnavailable
	}
	tier := namespace.UID
	if namespace.Name == "gitstore-system" {
		tier = rbac.ClusterTierID
	}
	source := rbac.OriginReader{Store: s.store, Git: s.rbacGit, MaxBytes: s.rbacValidator.Capture.Reader.MaxBytes}
	origin, resources, err := source.ReadTree(ctx, tier)
	if err != nil {
		return err
	}
	if origin.RepositoryID != req.RepositoryId || origin.GitRevision != repository.DefaultBranch+"@sha1:"+req.NewCommitSha {
		// A delayed callback must not roll a newer projection back or attribute
		// an intervening commit to this receive's actor.
		return nil
	}
	return s.projectRBACOrigin(ctx, tier, origin, resources, auth.PrincipalFromContext(ctx).Subject)
}

// ReconcileRBACTier repairs committed Git after a wholly lost callback. The
// repair writer is explicit; original Git authorship remains at the pinned OID.
func (s *Server) ReconcileRBACTier(ctx context.Context, tier string) error {
	if s.rbacValidator == nil {
		return rbac.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	origin, resources, err := (rbac.OriginReader{Store: s.store, Git: s.rbacGit, MaxBytes: s.rbacValidator.Capture.Reader.MaxBytes}).ReadTree(ctx, tier)
	if err != nil {
		return err
	}
	return s.projectRBACOrigin(ctx, tier, origin, resources, "system:rbac-projection-repair")
}

func (s *Server) projectRBACOrigin(ctx context.Context, tier string, origin rbac.SnapshotOrigin, resources []*rbac.Resource, actor string) error {
	capable, ok := s.store.(datastore.RBACCapable)
	if !ok || capable.RBACStore() == nil {
		return rbac.ErrUnavailable
	}
	store := capable.RBACStore()
	identity := rbac.ProjectionIdentity{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
		GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: actor}
	_, commit, _ := strings.Cut(identity.GitRevision, "@sha1:")
	for range 3 {
		state, err := store.ReadRBACTier(ctx, tier)
		if err != nil {
			return err
		}
		if state.Projection.GitRevision == identity.GitRevision {
			if state.Projection.RepositoryID != identity.RepositoryID || state.Projection.GitRef != identity.GitRef ||
				state.Projection.TreeDigest != identity.TreeDigest {
				return rbac.ErrUnavailable
			}
			if state.ProjectionPending {
				return store.RepairRBACProjection(ctx, tier, state.Revision)
			}
			return nil
		}
		if state.ProjectionPending {
			if err := store.RepairRBACProjection(ctx, tier, state.Revision); err != nil {
				return err
			}
			continue
		}
		snapshot, retry, err := datastore.ReadRBACSnapshotAttempt(ctx, store, tier)
		if err != nil {
			return err
		}
		if retry || snapshot.State.Revision != state.Revision {
			continue
		}
		write, err := s.committedRBACWrite(ctx, snapshot, resources, identity)
		if err != nil {
			return err
		}
		current, err := s.rbacGit.ResolveRef(ctx, identity.RepositoryID, identity.GitRef)
		if err != nil {
			return err
		}
		if current != commit {
			return nil
		}
		_, err = admission.ProjectCommittedRBAC(ctx, store, write)
		if err == nil {
			return nil
		}
		if !errors.Is(err, rbac.ErrConflict) && !errors.Is(err, admission.ErrCommittedManifestSuperseded) {
			return err
		}
	}
	return rbac.ErrUnavailable
}

func (s *Server) committedRBACWrite(ctx context.Context, snapshot datastore.RBACSnapshot, resources []*rbac.Resource, identity rbac.ProjectionIdentity) (rbac.TierWrite, error) {
	write := rbac.TierWrite{TierID: snapshot.State.TierID, ExpectedRevision: snapshot.State.Revision,
		ExpectedGitRevision: snapshot.State.Projection.GitRevision, Projection: &identity}
	previous := make(map[string]*rbac.Resource, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		path, err := rbac.CanonicalPath(resource.Kind, resource.Metadata.Name)
		if err != nil {
			return write, err
		}
		previous[path] = resource
	}
	protection := rbac.Protection{Builtins: s.rbacValidator.Builtins}
	ids := s.rbacValidator.IDGenerator
	if ids == nil {
		ids = apiruntime.UUIDGenerator{}
	}
	for _, resource := range resources {
		resource, err := rbac.CloneResource(resource)
		if err != nil {
			return write, err
		}
		path, _ := rbac.CanonicalPath(resource.Kind, resource.Metadata.Name)
		before := previous[path]
		delete(previous, path)
		authorProtection := protection
		if resource.Kind == rbac.RoleBinding && resource.Metadata.Name == rbac.ManagedBindingName {
			authorProtection, err = s.rbacValidator.ManagedProjectionProtection(ctx, resource.Metadata.Namespace, resource.NamespaceUID)
			if err != nil {
				return write, rbac.ErrUnavailable
			}
			if authorProtection.Managed.OrdinaryCollision(before) {
				oldManifest, oldErr := rbac.CanonicalManifest(before)
				newManifest, newErr := rbac.CanonicalManifest(resource)
				if before.Metadata.DeletionTimestamp == nil && oldErr == nil && newErr == nil && bytes.Equal(oldManifest, newManifest) {
					continue
				}
				return write, rbac.ErrUnavailable
			}
			if authorProtection.Managed.Check(resource) != nil ||
				before != nil && authorProtection.Managed.Check(before) != nil {
				return write, rbac.ErrUnavailable
			}
		}
		if before != nil {
			oldManifest, err := rbac.CanonicalManifest(before)
			if err != nil {
				return write, err
			}
			newManifest, err := rbac.CanonicalManifest(resource)
			if err != nil {
				return write, err
			}
			if before.Metadata.DeletionTimestamp == nil && bytes.Equal(oldManifest, newManifest) {
				continue
			}
		}
		if err := authorProtection.AuthorChange(before, resource); err != nil {
			return write, err
		}
		change := rbac.Change{Resource: resource}
		if before != nil {
			change.ExpectedUID, change.ExpectedResourceVersion = before.Metadata.UID, before.Metadata.ResourceVersion
			labels, annotations := resource.Metadata.Labels, resource.Metadata.Annotations
			resource.Metadata = before.Metadata
			resource.Metadata.Labels, resource.Metadata.Annotations = labels, annotations
			resource.Status = before.Status
		} else {
			if authorProtection.Managed != nil {
				resource.Metadata.UID = authorProtection.Managed.UID()
			} else {
				resource.Metadata.UID, err = ids.NewV7ID()
			}
			if err != nil {
				return write, err
			}
			resource.Metadata.Finalizers = []string{rbac.LifecycleFinalizer}
			if resource.Kind.Namespaced() {
				resource.Metadata.OwnerReferences, err = json.Marshal([]map[string]any{{
					"apiVersion": "gitstore.dev/v1beta1", "kind": "Namespace", "name": resource.Metadata.Namespace,
					"uid": resource.NamespaceUID, "blockOwnerDeletion": false,
				}})
				if err != nil {
					return write, err
				}
			}
		}
		write.Changes = append(write.Changes, change)
	}
	paths := make([]string, 0, len(previous))
	for path := range previous {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		resource := previous[path]
		if resource.Metadata.DeletionTimestamp != nil {
			continue
		}
		authorProtection := protection
		if resource.Kind == rbac.RoleBinding && resource.Metadata.Name == rbac.ManagedBindingName {
			var err error
			authorProtection, err = s.rbacValidator.ManagedProjectionProtection(ctx, resource.Metadata.Namespace, resource.NamespaceUID)
			if err != nil {
				return write, err
			}
		}
		if err := authorProtection.AuthorChange(resource, nil); err != nil {
			return write, err
		}
		write.Changes = append(write.Changes, rbac.Change{Resource: resource, ExpectedUID: resource.Metadata.UID,
			ExpectedResourceVersion: resource.Metadata.ResourceVersion, Terminate: true})
	}
	return write, nil
}

func isRBACPath(path string) bool { return path == "rbac" || strings.HasPrefix(path, "rbac/") }

func validRBACObjectID(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRBACBlob(blob *catalogv1.ResourceBlob) bool {
	if len(blob.GetPath()) > 1024 || len(blob.GetContent()) > rbac.MaxManifestBytes || !validRBACObjectID(blob.GetBlobOid()) {
		return false
	}
	digest := sha1.New()
	_, _ = fmt.Fprintf(digest, "blob %d\x00", len(blob.Content))
	_, _ = digest.Write(blob.Content)
	return hex.EncodeToString(digest.Sum(nil)) == blob.BlobOid
}

func rbacValidationErrors(err error) []*catalogv1.ValidationError {
	var rejected *admission.Error
	if !errors.As(err, &rejected) {
		return nil
	}
	result := make([]*catalogv1.ValidationError, 0, len(rejected.Diagnostics))
	for _, diagnostic := range rejected.Diagnostics {
		message := diagnostic.Message
		if rejected.Code == admission.CodeForbidden {
			message = rejected.Error()
		}
		result = append(result, &catalogv1.ValidationError{FilePath: diagnostic.File, Field: diagnostic.Field,
			Constraint: diagnostic.Reason, Message: message})
	}
	return result
}

func (s *Server) validateRBACChanges(ctx context.Context, req *catalogv1.ValidateResourcesRequest) (*catalogv1.ValidateResourcesRequest, []*catalogv1.ValidationError, error) {
	hasRBAC := false
	for _, tree := range req.GetTrees() {
		hasRBAC = hasRBAC || tree.GetRbacTree() != nil
		for _, blobs := range [][]*catalogv1.ResourceBlob{tree.GetOldBlobs(), tree.GetProposedBlobs()} {
			for _, blob := range blobs {
				hasRBAC = hasRBAC || isRBACPath(blob.GetPath())
			}
		}
	}
	if !hasRBAC {
		return req, nil, nil
	}
	reject := func(reason string) (*catalogv1.ValidateResourcesRequest, []*catalogv1.ValidationError, error) {
		return req, []*catalogv1.ValidationError{{Constraint: reason, Message: "RBAC candidate violates " + reason}}, nil
	}
	if len(req.Trees) != 1 {
		return reject("RBAC_MULTI_REF_UNSUPPORTED")
	}
	if s.rbacValidator == nil {
		return nil, nil, status.Error(codes.Unavailable, "RBAC admission is not activated")
	}
	tree := req.Trees[0]
	if tree.GetRbacTree() == nil {
		return reject("RBAC_COMPLETE_TREE_REQUIRED")
	}
	ref := tree.GetRef()
	branch, ok := strings.CutPrefix(ref.GetRefName(), "refs/heads/")
	if !ok || branch == "" || len(ref.GetRefName()) > 256 || !validRBACObjectID(ref.GetOldCommitSha()) || !validRBACObjectID(ref.GetNewCommitSha()) {
		return reject("RBAC_RECEIVE_IDENTITY_INVALID")
	}
	repository, err := s.store.GetRepository(ctx, req.RepositoryId)
	if err != nil || repository == nil || repository.DeletionTimestamp != nil {
		return nil, nil, status.Error(codes.Unavailable, "RBAC repository facts unavailable")
	}
	datastore.NormalizeRepositoryContract(repository)
	if repository.Name != "gitstore-system" || repository.UID != req.RepositoryId {
		return reject("RBAC_REPOSITORY_SCOPE_MISMATCH")
	}
	namespace, err := s.store.GetNamespaceByName(ctx, repository.Namespace)
	if err != nil || namespace == nil || namespace.DeletionTimestamp != nil {
		return nil, nil, status.Error(codes.Unavailable, "RBAC namespace facts unavailable")
	}
	candidate := admission.RBACCandidate{RepositoryID: req.RepositoryId, GitRef: ref.RefName,
		ExpectedGitRevision: branch + "@sha1:" + ref.OldCommitSha,
		FinalTree:           map[string][]byte{},
		Scope:               rbac.RepositoryScope{Namespace: namespace.Name, NamespaceUID: namespace.UID, Root: namespace.Name == "gitstore-system"}}
	total := 0
	if len(tree.RbacTree.Blobs) > rbac.MaxRolesPerTier+rbac.MaxBindingsPerTier {
		return reject("RBAC_TREE_INVALID")
	}
	for _, blob := range tree.RbacTree.Blobs {
		if !isRBACPath(blob.GetPath()) || !validRBACBlob(blob) ||
			candidate.FinalTree[blob.Path] != nil || len(blob.Content) > rbac.MaxSnapshotBytes-total {
			return reject("RBAC_TREE_INVALID")
		}
		total += len(blob.Content)
		candidate.FinalTree[blob.Path] = blob.Content
	}
	old := map[string]*catalogv1.ResourceBlob{}
	proposed := map[string]*catalogv1.ResourceBlob{}
	filteredTree := &catalogv1.ResourceValidationTree{Ref: ref}
	for side, blobs := range [][]*catalogv1.ResourceBlob{tree.OldBlobs, tree.ProposedBlobs} {
		target := old
		if side == 1 {
			target = proposed
		}
		for _, blob := range blobs {
			if !isRBACPath(blob.GetPath()) {
				if side == 0 {
					filteredTree.OldBlobs = append(filteredTree.OldBlobs, blob)
				} else {
					filteredTree.ProposedBlobs = append(filteredTree.ProposedBlobs, blob)
				}
				continue
			}
			if target[blob.Path] != nil || !validRBACBlob(blob) || len(target) >= rbac.MaxCandidateResources {
				return reject("RBAC_CANDIDATE_INVALID")
			}
			target[blob.Path] = blob
		}
	}
	for _, blob := range tree.ProposedBlobs {
		if isRBACPath(blob.GetPath()) {
			candidate.Changes = append(candidate.Changes, admission.RBACManifestChange{Path: blob.Path, Content: blob.Content})
		}
	}
	for _, blob := range tree.OldBlobs {
		if isRBACPath(blob.GetPath()) && proposed[blob.Path] == nil {
			candidate.Changes = append(candidate.Changes, admission.RBACManifestChange{Path: blob.Path, Delete: true})
		}
	}
	result, err := s.rbacValidator.Validate(ctx, auth.PrincipalFromContext(ctx), candidate)
	if err != nil {
		if diagnostics := rbacValidationErrors(err); len(diagnostics) != 0 {
			return req, diagnostics, nil
		}
		return nil, nil, status.Error(codes.Unavailable, "authoritative RBAC validation unavailable")
	}
	result.Close()
	return &catalogv1.ValidateResourcesRequest{RepositoryId: req.RepositoryId, Authorization: req.Authorization,
		Trees: []*catalogv1.ResourceValidationTree{filteredTree}}, nil, nil
}
