// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func statusConflictError(kind, namespace, name, currentResourceVersion string) error {
	identifier := name
	if namespace != "" {
		identifier = namespace + "/" + name
	}
	return &gqlerror.Error{
		Message: fmt.Sprintf("%s %s status update conflict", kind, identifier),
		Extensions: map[string]any{
			"code":            "RESOURCE_VERSION_CONFLICT",
			"resourceVersion": currentResourceVersion,
		},
	}
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
			StatusWriteConflictsTotal.WithLabelValues(input.Kind).Inc()
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
	r.publishCategoryTaxonomyStatusEvent(updated)
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
	if r.eventBus != nil {
		r.eventBus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "File", Namespace: updated.Namespace, Name: updated.Name, ResourceVersion: updated.ResourceVersion, Object: updated})
	}
	return &model.UpdateResourceStatusPayload{Object: fileToJSONMap(updated)}, nil
}
