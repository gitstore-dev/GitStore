// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
)

type RBACManifestChange struct {
	Path                    string
	Content                 []byte
	Delete                  bool
	ExpectedUID             string
	ExpectedResourceVersion string
	Operation               Operation
}

type RBACCandidate struct {
	Scope               rbac.RepositoryScope
	RepositoryID        string
	GitRef              string
	ExpectedGitRevision string
	Changes             []RBACManifestChange
	FinalTree           map[string][]byte
}

type RBACValidatedCandidate struct {
	Validation *rbac.Validation
	Snapshot   rbac.Snapshot
	Write      rbac.TierWrite
	Canonical  bool
}

func (c *RBACValidatedCandidate) Close() { c.Validation.Close() }

type RBACValidator struct {
	Capture           rbac.ValidationCapture
	Builtins          *rbac.BuiltinSet
	Clock             apiruntime.Clock
	IDGenerator       apiruntime.IDGenerator
	Ready             func(context.Context) error
	ManagedStore      datastore.Datastore
	ControllerSubject string
}

func rbacRejected(path, reason string) error {
	return Rejected(PhasePreReceive, "", []Diagnostic{{Level: LevelFailure, File: path, Reason: reason, Message: "RBAC candidate violates " + reason}})
}

func rbacPathKey(tier, path string) (rbac.ResourceKey, error) {
	if len(path) > 1024 {
		return rbac.ResourceKey{}, rbac.ErrInvalid
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] != "rbac" || !strings.HasSuffix(parts[2], ".yaml") {
		return rbac.ResourceKey{}, rbac.ErrInvalid
	}
	kind := map[string]rbac.Kind{"roles": rbac.Role, "clusterroles": rbac.ClusterRole,
		"rolebindings": rbac.RoleBinding, "clusterrolebindings": rbac.ClusterRoleBinding}[parts[1]]
	key := rbac.ResourceKey{TierID: tier, Kind: kind, Name: strings.TrimSuffix(parts[2], ".yaml")}
	if err := rbac.ValidateKey(key); err != nil {
		return key, err
	}
	canonical, err := rbac.CanonicalPath(key.Kind, key.Name)
	if err != nil || canonical != path {
		return key, rbac.ErrInvalid
	}
	return key, nil
}

func rbacResourceContext(resource *rbac.Resource) auth.ResourceContext {
	kind := string(resource.Kind)
	result := auth.ResourceContext{Kind: strings.ToLower(kind[:1]) + kind[1:], Name: resource.Metadata.Name}
	if resource.Kind.Namespaced() {
		result.Attrs = map[string]any{"namespace": resource.Metadata.Namespace}
	}
	return result
}

func rbacRequire(ctx context.Context, validation *rbac.Validation, resource *rbac.Resource, verb string) error {
	target := rbacResourceContext(resource)
	decision, err := validation.Authorize(ctx, target.Kind+"."+verb, target)
	if err != nil {
		return err
	}
	if decision.Outcome != auth.OutcomeAllow {
		return NewError(CodeForbidden, "RBAC_PERMISSION_REQUIRED", "RBAC operation is not authorized")
	}
	return nil
}

func (v RBACValidator) Validate(ctx context.Context, principal *auth.Principal, candidate RBACCandidate) (*RBACValidatedCandidate, error) {
	if v.Ready != nil {
		if err := v.Ready(ctx); err != nil {
			return nil, rbac.ErrUnavailable
		}
	}
	return v.validate(ctx, principal, candidate, nil, "")
}

// ValidateInitialAdministrator is not a request option: it constructs exactly
// one seed from the system-owned, still-pending root Namespace receipt.
func (v RBACValidator) ValidateInitialAdministrator(ctx context.Context, store datastore.NamespaceStore) (*RBACValidatedCandidate, error) {
	if store == nil {
		return nil, rbac.ErrUnavailable
	}
	namespace, err := store.GetNamespaceByName(ctx, "gitstore-system")
	if err != nil || namespace == nil || namespace.DeletionTimestamp != nil {
		return nil, rbac.ErrUnavailable
	}
	receipt, err := datastore.ReadNamespaceRBACBootstrap(namespace)
	if err != nil || receipt == nil || receipt.Commit != "" ||
		!auth.ValidTrustedPrincipal(&auth.Principal{Subject: receipt.Subject, AuthMethod: "bootstrap"}) {
		return nil, rbac.ErrUnavailable
	}
	spec, err := json.Marshal(rbac.BindingSpec{
		RoleRef:  rbac.RoleRef{Kind: rbac.ClusterRole, Name: "cluster-admin"},
		Subjects: []rbac.Subject{{Kind: "User", Name: receipt.Subject}},
	})
	if err != nil {
		return nil, err
	}
	resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRoleBinding,
		Metadata: rbac.Metadata{Name: "initial-admin"}, Spec: spec}
	content, err := rbac.CanonicalManifest(resource)
	if err != nil {
		return nil, err
	}
	candidate := RBACCandidate{
		Scope:        rbac.RepositoryScope{Namespace: namespace.Name, NamespaceUID: namespace.UID, Root: true},
		RepositoryID: receipt.RepositoryID, GitRef: receipt.GitRef,
		ExpectedGitRevision: strings.TrimPrefix(receipt.GitRef, "refs/heads/") + "@sha1:" + receipt.ExpectedCommit,
		Changes:             []RBACManifestChange{{Path: "rbac/clusterrolebindings/initial-admin.yaml", Content: content, Operation: OperationCreate}},
	}
	principal := &auth.Principal{Subject: "system:api", Issuer: "gitstore-api", AuthMethod: "internal"}
	return v.validate(ctx, principal, candidate, receipt, namespace.ResourceVersion)
}

func (v RBACValidator) validate(ctx context.Context, principal *auth.Principal, candidate RBACCandidate,
	initializing *datastore.NamespaceRBACBootstrap, namespaceVersion string) (*RBACValidatedCandidate, error) {
	if v.Builtins == nil || v.Builtins.Digest() != v.Capture.Reader.BuiltinDigest {
		return nil, rbac.ErrUnavailable
	}
	if len(candidate.Changes) == 0 || len(candidate.Changes) > rbac.MaxCandidateResources ||
		!strings.HasPrefix(candidate.GitRef, "refs/heads/") || len(candidate.GitRef) > 256 {
		return nil, rbacRejected("", "RBAC_CANDIDATE_INVALID")
	}
	tier, namespaceUID := candidate.Scope.NamespaceUID, candidate.Scope.NamespaceUID
	if candidate.Scope.Root {
		tier, namespaceUID = rbac.ClusterTierID, ""
	}
	seen := map[rbac.ResourceKey]bool{}
	keys := make([]rbac.ResourceKey, 0, len(candidate.Changes))
	total := 0
	for _, change := range candidate.Changes {
		key, err := rbacPathKey(tier, change.Path)
		if err != nil || seen[key] || change.Delete && len(change.Content) != 0 ||
			change.Operation != "" && change.Operation != OperationCreate && change.Operation != OperationUpdate && change.Operation != OperationDelete ||
			change.Operation == OperationDelete && !change.Delete ||
			change.Delete && change.Operation != "" && change.Operation != OperationDelete ||
			len(change.Content) > rbac.MaxManifestBytes || len(change.Content) > rbac.MaxCandidateBytes-total {
			return nil, rbacRejected("", "RBAC_CANDIDATE_INVALID")
		}
		seen[key] = true
		keys = append(keys, key)
		total += len(change.Content)
	}
	validation, err := v.Capture.Begin(ctx, principal, namespaceUID)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*RBACValidatedCandidate, error) {
		validation.Close()
		return nil, err
	}
	snapshot, err := validation.Snapshot(tier)
	if err != nil {
		return fail(err)
	}
	if initializing != nil && (snapshot.State.RoleCount != 0 || snapshot.State.BindingCount != 0 ||
		!snapshot.Origin.Root || snapshot.Origin.NamespaceUID != initializing.NamespaceUID ||
		snapshot.Origin.NamespaceVersion != namespaceVersion ||
		snapshot.Origin.RepositoryID != initializing.RepositoryID || snapshot.Origin.GitRef != initializing.GitRef ||
		snapshot.Origin.GitRevision != candidate.ExpectedGitRevision) {
		return fail(rbac.ErrUnavailable)
	}
	if snapshot.Origin.RepositoryID != candidate.RepositoryID || snapshot.Origin.NamespaceUID != candidate.Scope.NamespaceUID ||
		snapshot.Origin.Namespace != candidate.Scope.Namespace || snapshot.Origin.Root != candidate.Scope.Root {
		return fail(rbacRejected("", "RBAC_REPOSITORY_SCOPE_MISMATCH"))
	}
	canonical := snapshot.Origin.GitRef == candidate.GitRef
	if canonical && snapshot.State.Projection.GitRevision != candidate.ExpectedGitRevision {
		return fail(NewError(CodeConflict, "RBAC_GIT_CONFLICT", "canonical Git revision changed"))
	}
	if snapshot.State.ProjectionPending {
		return fail(rbac.ErrUnavailable)
	}
	clock, ids := v.Clock, v.IDGenerator
	if clock == nil {
		clock = apiruntime.SystemClock{}
	}
	if ids == nil {
		ids = apiruntime.UUIDGenerator{}
	}
	result := &RBACValidatedCandidate{Validation: validation, Snapshot: snapshot, Canonical: canonical,
		Write: rbac.TierWrite{TierID: tier, ExpectedRevision: snapshot.State.Revision, ExpectedGitRevision: snapshot.State.Projection.GitRevision}}
	beforeRows := map[rbac.ResourceKey]*rbac.Resource{}
	final := map[rbac.ResourceKey]*rbac.Resource{}
	protection := rbac.Protection{Builtins: v.Builtins}
	if candidate.FinalTree != nil {
		if len(candidate.FinalTree) > rbac.MaxRolesPerTier+rbac.MaxBindingsPerTier {
			return fail(rbacRejected("", "RBAC_QUOTA_EXCEEDED"))
		}
		paths := make([]string, 0, len(candidate.FinalTree))
		for path := range candidate.FinalTree {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		total, roles, bindings := 0, 0, 0
		for _, path := range paths {
			content := candidate.FinalTree[path]
			if len(content) > rbac.MaxManifestBytes || len(content) > rbac.MaxSnapshotBytes-total {
				return fail(rbacRejected(path, "RBAC_TREE_INVALID"))
			}
			total += len(content)
			resource, err := rbac.ParseManifest(content, path, candidate.Scope)
			if err != nil {
				return fail(rbacRejected(path, "RBAC_MANIFEST_INVALID"))
			}
			if resource.Kind.IsRole() {
				roles++
			} else {
				bindings++
			}
			if roles > rbac.MaxRolesPerTier || bindings > rbac.MaxBindingsPerTier {
				return fail(rbacRejected(path, "RBAC_QUOTA_EXCEEDED"))
			}
			current, err := validation.Lookup(tier, resource.Kind, resource.Metadata.Name)
			if err != nil && !errors.Is(err, rbac.ErrNotFound) {
				return fail(err)
			}
			if current != nil && current.Metadata.DeletionTimestamp == nil {
				resource.Metadata.UID = current.Metadata.UID
			}
			if managedBinding(resource) {
				if current == nil || current.Metadata.DeletionTimestamp != nil {
					if !seen[rbac.ResourceKey{TierID: tier, Kind: resource.Kind, Name: resource.Metadata.Name}] {
						return fail(rbacRejected(path, "RBAC_RESOURCE_PROTECTED"))
					}
				} else if !seen[rbac.ResourceKey{TierID: tier, Kind: resource.Kind, Name: resource.Metadata.Name}] {
					currentProtection, err := v.ManagedProjectionProtection(ctx, snapshot.Origin.Namespace, snapshot.Origin.NamespaceUID)
					if err != nil || !currentProtection.Managed.OrdinaryCollision(current) && currentProtection.Managed.Check(current) != nil {
						return fail(rbacRejected(path, "RBAC_RESOURCE_PROTECTED"))
					}
					oldBody, oldErr := rbac.CanonicalManifest(current)
					newBody, newErr := rbac.CanonicalManifest(resource)
					if oldErr != nil || newErr != nil || !bytes.Equal(oldBody, newBody) {
						return fail(rbacRejected(path, "RBAC_RESOURCE_PROTECTED"))
					}
				}
			} else if err := protection.AuthorChange(nil, resource); err != nil {
				return fail(rbacRejected(path, "RBAC_RESOURCE_PROTECTED"))
			}
			final[rbac.ResourceKey{TierID: tier, Kind: resource.Kind, Name: resource.Metadata.Name}] = resource
		}
	}
	for index, change := range candidate.Changes {
		key := keys[index]
		before, err := validation.Lookup(tier, key.Kind, key.Name)
		if err != nil && !errors.Is(err, rbac.ErrNotFound) {
			return fail(err)
		}
		verb := "create"
		var after *rbac.Resource
		if change.Delete {
			verb = "delete"
			after = before
			if after == nil {
				after = &rbac.Resource{Kind: key.Kind, Metadata: rbac.Metadata{Name: key.Name, Namespace: candidate.Scope.Namespace}}
			}
		} else {
			after, err = rbac.ParseManifest(change.Content, change.Path, candidate.Scope)
			if err != nil {
				return fail(rbacRejected(change.Path, "RBAC_MANIFEST_INVALID"))
			}
			if before != nil {
				verb = "update"
			}
		}
		if change.Operation != "" {
			verb = strings.ToLower(string(change.Operation))
		}
		if initializing == nil {
			if err := rbacRequire(ctx, validation, after, verb); err != nil {
				return fail(err)
			}
		}
		if change.Operation == OperationCreate && before != nil {
			return fail(NewError(CodeAlreadyExists, "RBAC_ALREADY_EXISTS", "RBAC resource already exists"))
		}
		if change.Operation == OperationUpdate && before == nil {
			return fail(NewError(CodeNotFound, "RBAC_NOT_FOUND", "RBAC resource not found"))
		}
		if change.ExpectedUID != "" && (before == nil || change.ExpectedUID != before.Metadata.UID) ||
			change.ExpectedResourceVersion != "" && (before == nil || change.ExpectedResourceVersion != before.Metadata.ResourceVersion) {
			return fail(NewError(CodeConflict, "RBAC_RESOURCE_CONFLICT", "RBAC resource version changed"))
		}
		if before == nil && verb == "delete" {
			return fail(NewError(CodeNotFound, "RBAC_NOT_FOUND", "RBAC resource not found"))
		}
		desired := after
		if verb == "delete" {
			desired = nil
		}
		if candidate.FinalTree != nil {
			proposed := final[key]
			if desired == nil && proposed != nil || desired != nil && proposed == nil {
				return fail(rbacRejected(change.Path, "RBAC_TREE_MISMATCH"))
			}
			if desired != nil {
				expected, expectedErr := rbac.CanonicalManifest(proposed)
				actual, actualErr := rbac.CanonicalManifest(desired)
				if expectedErr != nil || actualErr != nil || !bytes.Equal(expected, actual) {
					return fail(rbacRejected(change.Path, "RBAC_TREE_MISMATCH"))
				}
			}
		}
		authorProtection := protection
		if managedBinding(before) || managedBinding(desired) {
			authorProtection, err = v.ManagedProjectionProtection(ctx, snapshot.Origin.Namespace, snapshot.Origin.NamespaceUID)
			if err != nil {
				return fail(err)
			}
			if desired != nil || !authorProtection.Managed.OrdinaryCollision(before) {
				authorProtection, err = v.managedAuthorProtection(ctx, principal, snapshot.Origin)
				if err != nil {
					return fail(err)
				}
			}
		}
		if err := authorProtection.AuthorChange(before, desired); err != nil {
			if errors.Is(err, rbac.ErrUnavailable) {
				return fail(err)
			}
			return fail(rbacRejected(change.Path, "RBAC_RESOURCE_PROTECTED"))
		}
		write := rbac.Change{Resource: after, Terminate: verb == "delete"}
		if before != nil {
			beforeRows[key] = before
			write.ExpectedUID, write.ExpectedResourceVersion = before.Metadata.UID, before.Metadata.ResourceVersion
			if verb != "delete" {
				labels, annotations := after.Metadata.Labels, after.Metadata.Annotations
				after.Metadata = before.Metadata
				after.Metadata.Labels, after.Metadata.Annotations = labels, annotations
				after.Status = before.Status
			}
		} else {
			if authorProtection.Managed != nil {
				after.Metadata.UID = authorProtection.Managed.UID()
			} else {
				after.Metadata.UID, err = ids.NewV7ID()
			}
			if err != nil {
				return fail(rbac.ErrUnavailable)
			}
			after.Metadata.Finalizers = []string{rbac.LifecycleFinalizer}
			if key.Kind.Namespaced() {
				after.Metadata.OwnerReferences, err = json.Marshal([]map[string]any{{
					"apiVersion": "gitstore.dev/v1beta1", "kind": "Namespace", "name": snapshot.Origin.Namespace,
					"uid": snapshot.Origin.NamespaceUID, "blockOwnerDeletion": false,
				}})
				if err != nil {
					return fail(rbac.ErrUnavailable)
				}
			}
		}
		final[key] = desired
		result.Write.Changes = append(result.Write.Changes, write)
	}
	for index, write := range result.Write.Changes {
		if write.Terminate || initializing != nil {
			continue
		}
		resource := write.Resource
		role := resource
		explicit := "escalate"
		if !resource.Kind.IsRole() {
			explicit = "bind"
			spec, err := rbac.DecodeBindingSpec(resource)
			if err != nil {
				return fail(rbacRejected(candidate.Changes[index].Path, "RBAC_MANIFEST_INVALID"))
			}
			roleTier := tier
			if spec.RoleRef.Kind == rbac.ClusterRole {
				roleTier = rbac.ClusterTierID
			}
			key := rbac.ResourceKey{TierID: roleTier, Kind: spec.RoleRef.Kind, Name: spec.RoleRef.Name}
			var present bool
			role, present = final[key]
			if !present {
				if key.Kind == rbac.ClusterRole {
					role, err = v.Builtins.Get(key.Kind, key.Name)
				}
				if role == nil && (canonical || roleTier != tier) {
					role, err = validation.Lookup(roleTier, key.Kind, key.Name)
				}
				if err != nil && !errors.Is(err, rbac.ErrNotFound) {
					return fail(err)
				}
			}
			if role != nil && role.Metadata.DeletionTimestamp != nil {
				role = nil
			}
			resolvedUID, err := rbac.ResolvedRoleUID(resource)
			if err != nil {
				return fail(rbac.ErrUnavailable)
			}
			if role != nil && resolvedUID != "" && resolvedUID != role.Metadata.UID {
				role = nil
			}
		}
		covered := false
		if role != nil {
			proof, err := rbacCoverageRequest(role, resource)
			if err != nil {
				return fail(rbacRejected(candidate.Changes[index].Path, "RBAC_MANIFEST_INVALID"))
			}
			evidence, err := validation.Covers(ctx, proof)
			if err != nil && !errors.Is(err, auth.ErrNotSupported) {
				return fail(err)
			}
			covered = err == nil && evidence.Covered
		}
		if !covered {
			if err := rbacRequire(ctx, validation, resource, explicit); err != nil {
				return fail(err)
			}
		}
	}
	if _, err := rbac.PlanAuthorCandidate(snapshot.State, beforeRows, result.Write, clock.Now()); err != nil {
		if errors.Is(err, rbac.ErrQuota) {
			return fail(rbacRejected("", "RBAC_QUOTA_EXCEEDED"))
		}
		return fail(err)
	}
	if canonical && candidate.FinalTree != nil {
		resources := make([]*rbac.Resource, 0, len(final))
		for _, resource := range final {
			if resource != nil {
				resources = append(resources, resource)
			}
		}
		actual, err := rbac.ManifestTreeDigest(resources)
		if err != nil {
			return fail(err)
		}
		expected, err := validation.ProposedTreeDigest(tier, result.Write.Changes)
		if err != nil {
			return fail(err)
		}
		if actual != expected {
			return fail(rbacRejected("", "RBAC_TREE_MISMATCH"))
		}
	}
	if err := validation.Check(ctx); err != nil {
		return fail(err)
	}
	return result, nil
}

func rbacCoverageRequest(role, binding *rbac.Resource) (auth.AuthorizationCoverageRequest, error) {
	spec, err := rbac.DecodeRoleSpec(role)
	if err != nil {
		return auth.AuthorizationCoverageRequest{}, err
	}
	result := auth.AuthorizationCoverageRequest{}
	if binding.Kind.Namespaced() {
		result.Namespace, result.NamespaceUID = binding.Metadata.Namespace, binding.NamespaceUID
	}
	for _, rule := range spec.Rules {
		actions, err := rbaclocal.ExpandActions(rule.Resources, rule.Verbs)
		if err != nil {
			return result, err
		}
		for _, action := range actions {
			result.Permissions = append(result.Permissions, auth.AuthorizationPermission{Action: action.Action, ResourceNames: rule.ResourceNames})
		}
	}
	return result, nil
}
