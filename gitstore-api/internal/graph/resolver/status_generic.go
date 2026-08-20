// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

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
			return &model.UpdateResourceStatusPayload{
				Conflict: &model.StatusConflict{CurrentResourceVersion: current.ResourceVersion},
			}, nil
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
			return &model.UpdateResourceStatusPayload{
				Conflict: &model.StatusConflict{CurrentResourceVersion: namespace.ResourceVersion},
			}, nil
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	if err := r.store.UpdateNamespace(ctx, namespace, input.ResourceVersion); err != nil {
		if errors.Is(err, datastore.ErrConflict) {
			current, getErr := r.store.GetNamespaceByName(ctx, input.Name)
			if getErr != nil {
				return nil, gqlerror.Errorf("status update conflict, and could not re-fetch current version: %v", getErr)
			}
			return &model.UpdateResourceStatusPayload{
				Conflict: &model.StatusConflict{CurrentResourceVersion: current.ResourceVersion},
			}, nil
		}
		return nil, gqlerror.Errorf("update resource status: %v", err)
	}
	r.publishNamespaceStatusEvent(namespace)
	return &model.UpdateResourceStatusPayload{Object: namespaceToJSONMap(namespace)}, nil
}
