// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type LifecycleClient interface {
	UpdateStatus(context.Context, Resource, StatusUpdate) error
	CompleteDeletion(context.Context, Resource) error
}

type Reconciler struct {
	lookup cache.LookupFunc[Resource]
	client LifecycleClient
}

func NewReconciler(lookup cache.LookupFunc[Resource], client LifecycleClient) *Reconciler {
	return &Reconciler{lookup: lookup, client: client}
}

func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	resource, found, err := r.lookup(ctx, key)
	if err != nil {
		return types.ResultTransient(err)
	}
	if !found {
		return types.ResultOK()
	}
	if resource.Key() != key || resource.Metadata.UID == "" {
		return types.ResultTransient(errors.New("rbac: inconsistent checkpoint identity"))
	}
	// Release-owned virtual resources have a digest, not a Git revision.
	if len(resource.Metadata.Revision) == 64 && !strings.Contains(resource.Metadata.Revision, "@sha1:") {
		return types.ResultOK()
	}
	if resource.Metadata.DeletionTimestamp != nil {
		return r.finalize(ctx, resource)
	}
	update := StatusUpdate{Resolved: resource.Status.Resolved}
	condition := &status.Condition{Type: "RoleResolved", Status: "TRUE", ObservedGeneration: resource.Metadata.Generation,
		LastTransitionTime: time.Now().UTC(), Reason: "RoleReady", Message: "role is active"}
	if strings.HasSuffix(resource.Kind, "Binding") {
		if resource.Spec.RoleRef == nil {
			return types.ResultTransient(errors.New("rbac: binding lacks a role reference"))
		}
		ref := resource.Spec.RoleRef
		target := types.WorkItemKey{Kind: ref.Kind, Name: ref.Name}
		if namespaced(ref.Kind) {
			target.Namespace = resource.Metadata.Namespace
		}
		role, exists, err := r.lookup(ctx, target)
		if err != nil {
			return types.ResultTransient(err)
		}
		switch {
		case !exists:
			condition.Status, condition.Reason, condition.Message = "FALSE", "RoleNotFound", "the referenced role is absent"
		case role.Metadata.DeletionTimestamp != nil:
			condition.Status, condition.Reason, condition.Message = "FALSE", "RoleTerminating", "the referenced role is terminating"
		case update.Resolved != nil && update.Resolved.RoleUID != role.Metadata.UID:
			condition.Status, condition.Reason, condition.Message = "FALSE", "RoleUIDChanged", "the original role incarnation is no longer active"
		case role.Metadata.UID == "":
			return types.ResultTransient(errors.New("rbac: referenced role lacks a UID"))
		default:
			update.Resolved = &ResolvedRole{RoleUID: role.Metadata.UID, RoleKind: ref.Kind, RoleName: ref.Name}
			condition.Message = "the binding's role UID is resolved"
		}
	}
	update.Conditions = mergeCondition(resource.Status.Conditions, condition)
	if resource.Status.ObservedGeneration == resource.Metadata.Generation && reflect.DeepEqual(resource.Status.Resolved, update.Resolved) &&
		reflect.DeepEqual(resource.Status.Conditions, update.Conditions) {
		return types.ResultOK()
	}
	if err := r.client.UpdateStatus(ctx, resource, update); err != nil {
		return types.ResultTransient(err)
	}
	return types.ResultOK()
}

func (r *Reconciler) finalize(ctx context.Context, resource Resource) types.ReconcileResult {
	if slices.Contains(resource.Metadata.Finalizers, LifecycleFinalizer) {
		finalizers := make([]string, 0, len(resource.Metadata.Finalizers))
		for _, finalizer := range resource.Metadata.Finalizers {
			if finalizer != LifecycleFinalizer {
				finalizers = append(finalizers, finalizer)
			}
		}
		if err := r.client.UpdateStatus(ctx, resource, StatusUpdate{Finalizers: finalizers}); err != nil {
			return types.ResultTransient(err)
		}
		// Completion must use the new UID-bound version delivered by the watch.
		return types.ResultOK()
	}
	if len(resource.Metadata.Finalizers) > 0 {
		return types.ResultOK()
	}
	if err := r.client.CompleteDeletion(ctx, resource); err != nil {
		return types.ResultTransient(err)
	}
	return types.ResultOK()
}

func mergeCondition(previous []*status.Condition, fresh *status.Condition) []*status.Condition {
	result := make([]*status.Condition, 0, len(previous)+1)
	for _, current := range previous {
		if current == nil {
			continue
		}
		if current.Type == fresh.Type {
			if current.Status == fresh.Status {
				fresh.LastTransitionTime = current.LastTransitionTime
			}
			continue
		}
		copy := *current
		result = append(result, &copy)
	}
	return append(result, fresh)
}
