// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func statusConflictError(kind, namespace, name, currentResourceVersion string) error {
	identifier := name
	if namespace != "" {
		identifier = namespace + "/" + name
	}
	return (&admission.Error{
		Code: admission.CodeConflict,
		Diagnostics: []admission.Diagnostic{{
			Reason:  "RESOURCE_VERSION_CONFLICT",
			Message: fmt.Sprintf("current resourceVersion is %s", currentResourceVersion),
			Level:   admission.LevelFailure,
		}},
		Message: fmt.Sprintf("%s %s status update conflict", kind, identifier),
	}).ToGQLError()
}

// updateCategoryTaxonomyStatusGeneric backs updateResourceStatus for
// kind "CategoryTaxonomy", reusing the same datastore write path as the
// dedicated updateCategoryStatus mutation.
func (r *mutationResolver) updateCategoryTaxonomyStatusGeneric(ctx context.Context, input model.UpdateResourceStatusInput) (*model.UpdateResourceStatusPayload, error) {
	patch := datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion: input.ResourceVersion,
	}
	if input.ObservedGeneration != nil {
		gen := int64(*input.ObservedGeneration)
		patch.ObservedGeneration = &gen
	}
	patch.LastAppliedRevision = input.LastAppliedRevision
	if input.Conditions != nil {
		patch.Conditions = toConditions(input.Conditions)
	}
	if input.Resolved != nil {
		patch.Resolved = resolvedFromJSONMap(input.Resolved)
	}

	updated, err := r.store.UpdateCategoryTaxonomyStatus(ctx, input.Namespace, input.Name, patch)
	if err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			StatusWriteConflictsTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("kind", input.Kind)))
			r.logger.Info("status write conflict",
				zap.String("kind", input.Kind),
				zap.String("namespace", input.Namespace),
				zap.String("name", input.Name))
			current, getErr := r.store.GetCategoryTaxonomyByName(ctx, input.Namespace, input.Name)
			if getErr != nil {
				return nil, gqlerror.Errorf("status update conflict, and could not re-fetch current version: %v", getErr)
			}
			return nil, statusConflictError(input.Kind, input.Namespace, input.Name, current.ResourceVersion)
		}
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("%s %s/%s not found", input.Kind, input.Namespace, input.Name),
				Extensions: map[string]any{"code": "NOT_FOUND"},
			}
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	return &model.UpdateResourceStatusPayload{Object: categoryTaxonomyToJSONMap(updated)}, nil
}

func (r *mutationResolver) updateNamespaceStatusGeneric(ctx context.Context, input model.UpdateResourceStatusInput) (*model.UpdateResourceStatusPayload, error) {
	namespace, err := r.store.GetNamespaceByName(ctx, input.Name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("Namespace %s not found", input.Name),
				Extensions: map[string]any{"code": "NOT_FOUND"},
			}

		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}

	patch := datastore.NamespaceStatusPatch{ResourceVersion: input.ResourceVersion}
	if input.ObservedGeneration != nil {
		generation := int64(*input.ObservedGeneration)
		patch.ObservedGeneration = &generation
	}
	patch.LastAppliedRevision = input.LastAppliedRevision
	if input.Conditions != nil {
		patch.Conditions = toConditions(input.Conditions)
	}
	if err := datastore.ApplyNamespaceStatusPatch(namespace, patch); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			return nil, statusConflictError(input.Kind, input.Namespace, input.Name, namespace.ResourceVersion)
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	if err := r.store.UpdateNamespace(ctx, namespace, input.ResourceVersion); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			current, getErr := r.store.GetNamespaceByName(ctx, input.Name)
			if getErr != nil {
				return nil, gqlerror.Errorf("status update conflict, and could not re-fetch current version: %v", getErr)
			}
			return nil, statusConflictError(input.Kind, input.Namespace, input.Name, current.ResourceVersion)
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	return &model.UpdateResourceStatusPayload{Object: namespaceToJSONMap(namespace)}, nil
}

func (r *mutationResolver) updateRepositoryStatusGeneric(ctx context.Context, input model.UpdateResourceStatusInput) (*model.UpdateResourceStatusPayload, error) {
	mapping, err := r.store.LookupRepository(ctx, input.Namespace, input.Name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("Repository %s/%s not found", input.Namespace, input.Name), Extensions: map[string]any{"code": "NOT_FOUND"}}
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	repository, err := r.store.GetRepository(ctx, mapping.RepositoryID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("Repository %s/%s not found", input.Namespace, input.Name), Extensions: map[string]any{"code": "NOT_FOUND"}}
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}

	patch := datastore.RepositoryStatusPatch{
		ResourceVersion:     input.ResourceVersion,
		LastAppliedRevision: input.LastAppliedRevision,
	}
	if input.ObservedGeneration != nil {
		generation := int64(*input.ObservedGeneration)
		patch.ObservedGeneration = &generation
	}
	if input.Conditions != nil {
		patch.Conditions = toConditions(input.Conditions)
	}
	if input.Resolved != nil {
		resolved := &catalog.ResolvedRepositoryDefinition{}
		data, marshalErr := json.Marshal(input.Resolved)
		if marshalErr != nil {
			return nil, gqlerror.Errorf("update resource status: encode resolved Repository status: %v", marshalErr)
		}
		if unmarshalErr := json.Unmarshal(data, resolved); unmarshalErr != nil {
			return nil, gqlerror.Errorf("update resource status: decode resolved Repository status: %v", unmarshalErr)
		}
		patch.Resolved = resolved
	}
	if err := datastore.ApplyRepositoryStatusPatch(repository, patch); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			return nil, statusConflictError(input.Kind, input.Namespace, input.Name, repository.ResourceVersion)
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	if err := r.store.UpdateRepository(ctx, repository, input.ResourceVersion); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			current, getErr := r.store.GetRepository(ctx, mapping.RepositoryID)
			if getErr != nil {
				return nil, gqlerror.Errorf("status update conflict, and could not re-fetch current version: %v", getErr)
			}
			return nil, statusConflictError(input.Kind, input.Namespace, input.Name, current.ResourceVersion)
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	return &model.UpdateResourceStatusPayload{Object: repositoryToJSONMap(repository)}, nil
}

func (r *mutationResolver) updateFileStatusGeneric(ctx context.Context, input model.UpdateResourceStatusInput) (*model.UpdateResourceStatusPayload, error) {
	patch := datastore.FileStatusPatch{ResourceVersion: input.ResourceVersion, LastAppliedRevision: input.LastAppliedRevision}
	if input.ObservedGeneration != nil {
		gen := int64(*input.ObservedGeneration)
		patch.ObservedGeneration = &gen
	}
	if input.Conditions != nil {
		patch.Conditions = toConditions(input.Conditions)
	}
	if input.Resolved != nil {
		patch.Resolved = fileResolvedFromJSONMap(input.Resolved)
	}
	updated, err := r.store.UpdateFileStatus(ctx, input.Namespace, input.Name, patch)
	if err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			current, getErr := r.store.GetFileByName(ctx, input.Namespace, input.Name)
			if getErr != nil {
				return nil, gqlerror.Errorf("status update conflict: %v", getErr)
			}
			return nil, statusConflictError(input.Kind, input.Namespace, input.Name, current.ResourceVersion)
		}
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("File %s/%s not found", input.Namespace, input.Name), Extensions: map[string]any{"code": "NOT_FOUND"}}
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	return &model.UpdateResourceStatusPayload{Object: fileToJSONMap(updated)}, nil
}

func rbacLifecycleError(err error) error {
	switch {
	case errors.Is(err, rbac.ErrConflict), errors.Is(err, datastore.ErrConflict):
		return admission.NewError(admission.CodeConflict, "RESOURCE_VERSION_CONFLICT", "RBAC resource changed or still has blocking references").ToGQLError()
	case errors.Is(err, rbac.ErrInvalid):
		return admission.NewError(admission.CodeBadUserInput, "INVALID_RBAC_STATUS", "invalid RBAC lifecycle change").ToGQLError()
	case errors.Is(err, rbac.ErrNotFound), errors.Is(err, datastore.ErrNotFound):
		return admission.ResourceNotFound("RBAC", "RBAC resource not found")
	default:
		return rbacAuthorError(err)
	}
}

func (r *Resolver) rbacLifecycleResource(ctx context.Context, kind rbac.Kind, namespace, name, verb, version string) (rbac.Store, rbac.ResourceKey, *rbac.Resource, error) {
	if r.rbacValidator == nil {
		return nil, rbac.ResourceKey{}, nil, rbacUnavailableError()
	}
	if err := r.authorizeRBACRead(ctx, kind, verb, namespace, name); err != nil {
		return nil, rbac.ResourceKey{}, nil, err
	}
	invalid := func() (rbac.Store, rbac.ResourceKey, *rbac.Resource, error) {
		return nil, rbac.ResourceKey{}, nil, rbacLifecycleError(rbac.ErrInvalid)
	}
	if kind.Namespaced() != (namespace != "") {
		return invalid()
	}
	uid, nativeVersion, err := decodeRBACResourceVersion(version)
	if err != nil {
		return nil, rbac.ResourceKey{}, nil, rbacAuthorError(err)
	}
	key := rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: kind, Name: name}
	if kind.Namespaced() {
		parent, err := r.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return nil, key, nil, rbacLifecycleError(err)
		}
		if parent == nil {
			return nil, key, nil, rbacLifecycleError(rbac.ErrUnavailable)
		}
		key.TierID = parent.UID
	}
	if rbac.ValidateKey(key) != nil {
		return invalid()
	}
	store, err := r.rbacStore()
	if err != nil {
		return nil, key, nil, err
	}
	before, err := store.GetRBAC(ctx, key)
	if err != nil {
		return nil, key, nil, rbacLifecycleError(err)
	}
	if before.Metadata.UID != uid || before.Metadata.ResourceVersion != nativeVersion ||
		before.Metadata.Namespace != namespace || kind.Namespaced() && before.NamespaceUID != key.TierID {
		return nil, key, nil, rbacLifecycleError(rbac.ErrConflict)
	}
	protection, err := r.rbacLifecycleProtection(ctx, before)
	if err != nil {
		return nil, key, nil, rbacLifecycleError(err)
	}
	if err := protection.StatusChange(before, before); err != nil {
		return nil, key, nil, rbacLifecycleError(err)
	}
	return store, key, before, nil
}

func (r *Resolver) rbacLifecycleProtection(ctx context.Context, resource *rbac.Resource) (rbac.Protection, error) {
	protection := rbac.Protection{Builtins: r.rbacValidator.Builtins}
	if resource.Kind != rbac.RoleBinding || resource.Metadata.Name != rbac.ManagedBindingName {
		return protection, nil
	}
	if err := r.rbacValidator.RequireController(ctx, auth.PrincipalFromContext(ctx)); err != nil {
		return protection, err
	}
	return r.rbacValidator.ManagedProjectionProtection(ctx, resource.Metadata.Namespace, resource.NamespaceUID)
}

func (r *mutationResolver) updateRBACStatusGeneric(ctx context.Context, input model.UpdateResourceStatusInput) (*model.UpdateResourceStatusPayload, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	kind := rbac.Kind(input.Kind)
	store, key, before, err := r.rbacLifecycleResource(ctx, kind, input.Namespace, input.Name, "status.write", input.ResourceVersion)
	if err != nil {
		return nil, err
	}
	if input.LastAppliedRevision != nil || input.ObservedGeneration != nil &&
		(*input.ObservedGeneration < 0 || int64(*input.ObservedGeneration) > before.Metadata.Generation) ||
		kind.IsRole() && input.Resolved != nil {
		return nil, rbacLifecycleError(rbac.ErrInvalid)
	}
	after, err := rbac.CloneResource(before)
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	if input.Resolved != nil {
		after, err = (rbac.References{Store: store, Builtins: r.rbacValidator.Builtins}).PrepareResolution(ctx, key, before.Metadata.UID, before.Metadata.ResourceVersion)
		if err != nil {
			return nil, rbacLifecycleError(err)
		}
		resolvedUID, err := rbac.ResolvedRoleUID(after)
		if err != nil {
			return nil, rbacLifecycleError(err)
		}
		expected := map[string]any{}
		if resolvedUID != "" {
			spec, err := rbac.DecodeBindingSpec(after)
			if err != nil {
				return nil, rbacLifecycleError(err)
			}
			expected = map[string]any{"roleUID": resolvedUID, "roleKind": string(spec.RoleRef.Kind), "roleName": spec.RoleRef.Name}
		}
		if !reflect.DeepEqual(input.Resolved, expected) {
			return nil, rbacLifecycleError(rbac.ErrInvalid)
		}
	}
	status := map[string]json.RawMessage{}
	if len(after.Status) > 0 && json.Unmarshal(after.Status, &status) != nil {
		return nil, rbacLifecycleError(rbac.ErrUnavailable)
	}
	if input.ObservedGeneration != nil {
		status["observedGeneration"], _ = json.Marshal(*input.ObservedGeneration)
	}
	if input.Conditions != nil {
		status["conditions"], _ = json.Marshal(toConditions(input.Conditions))
	}
	after.Status, err = json.Marshal(status)
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	if input.Finalizers != nil {
		after.Metadata.Finalizers = append([]string{}, input.Finalizers...)
	}
	protection, err := r.rbacLifecycleProtection(ctx, before)
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	if err := protection.StatusChange(before, after); err != nil {
		return nil, rbacLifecycleError(err)
	}
	state, err := store.ReadRBACTier(ctx, key.TierID)
	if err == nil {
		_, err = store.ApplyRBAC(ctx, rbac.TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
			Changes: []rbac.Change{{Resource: after, ExpectedUID: before.Metadata.UID, ExpectedResourceVersion: before.Metadata.ResourceVersion}}})
	}
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	current, err := store.GetRBAC(ctx, key)
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	node, err := rbacResourceToGraphQL(current)
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	data, err := json.Marshal(node)
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, rbacLifecycleError(err)
	}
	return &model.UpdateResourceStatusPayload{Object: object}, nil
}

func (r *Resolver) completeRBACDeletion(ctx context.Context, kind rbac.Kind, id, namespace, name, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uid, err := decodeNodeIDAs(string(kind), id)
	if err != nil {
		return err
	}
	versionUID, _, err := decodeRBACResourceVersion(version)
	if err != nil {
		return rbacAuthorError(err)
	}
	if versionUID != uid {
		return rbacLifecycleError(rbac.ErrConflict)
	}
	store, key, before, err := r.rbacLifecycleResource(ctx, kind, namespace, name, "purge", version)
	if err != nil {
		var structured *gqlerror.Error
		if errors.As(err, &structured) && structured.Extensions["code"] == "NOT_FOUND" {
			return nil
		}
		return err
	}
	if before.Metadata.DeletionTimestamp == nil || len(before.Metadata.Finalizers) != 0 {
		return rbacLifecycleError(rbac.ErrConflict)
	}
	references := rbac.References{Store: store, Builtins: r.rbacValidator.Builtins}
	if kind.IsRole() {
		if err := references.CloseRole(ctx, key, uid); err != nil {
			return rbacLifecycleError(err)
		}
		if err := references.RecoverRoleClaims(ctx, uid); err != nil {
			return rbacLifecycleError(err)
		}
		guard, err := store.ReadRBACRoleGuard(ctx, uid)
		if err != nil {
			return rbacLifecycleError(err)
		}
		if guard.State != rbac.GuardFinalized {
			if _, err := store.MutateRBACRoleGuard(ctx, rbac.GuardMutation{
				RoleUID: uid, ExpectedVersion: guard.Version, Action: rbac.GuardFinalize,
			}); err != nil {
				return rbacLifecycleError(err)
			}
		}
	} else {
		roleUID, err := rbac.ResolvedRoleUID(before)
		if err != nil {
			return rbacLifecycleError(err)
		}
		if roleUID != "" {
			if _, err := references.RecoverClaim(ctx, rbac.RoleClaim{RoleUID: roleUID, BindingUID: uid, Binding: key}); err != nil {
				return rbacLifecycleError(err)
			}
		}
	}
	state, err := store.ReadRBACTier(ctx, key.TierID)
	if err == nil {
		_, err = store.ApplyRBAC(ctx, rbac.TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
			Changes: []rbac.Change{{Resource: before, ExpectedUID: uid, ExpectedResourceVersion: before.Metadata.ResourceVersion, Remove: true}}})
	}
	if err != nil {
		return rbacLifecycleError(err)
	}
	return nil
}
