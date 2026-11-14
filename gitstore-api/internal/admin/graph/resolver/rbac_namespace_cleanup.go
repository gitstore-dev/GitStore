// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"go.uber.org/zap"
)

func (r *Resolver) reconcileNamespaceCleanup(ctx context.Context, after *string) (*model.ReconcileRBACNamespaceCleanupsPayload, error) {
	if r.rbacValidator == nil || r.registry == nil || r.registry.AuthZ() == nil {
		return nil, rbacUnavailableError()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	principal := auth.PrincipalFromContext(ctx)
	if err := r.rbacValidator.RequireController(ctx, principal); err != nil {
		return nil, rbacAuthorError(err)
	}
	decision, err := r.registry.AuthZ().Authorize(ctx, principal, auth.ActionNamespacePurge, auth.ResourceContext{Kind: "namespace"})
	if err != nil {
		return nil, rbacAuthorError(err)
	}
	if decision.Outcome != auth.OutcomeAllow {
		return nil, admission.NewError(admission.CodeForbidden, "RBAC_PERMISSION_REQUIRED", "global Namespace purge permission is required").ToGQLError()
	}
	store, err := r.rbacStore()
	if err != nil {
		return nil, err
	}
	cursor := ""
	if after != nil {
		cursor = *after
	}
	page, err := store.ListRBACNamespaceCleanups(ctx, cursor, 1)
	if err != nil {
		return nil, rbacLifecycleError(err)
	}
	result := &model.ReconcileRBACNamespaceCleanupsPayload{}
	if page.Next != "" {
		result.NextCursor = &page.Next
	}
	cleaner := rbac.NamespaceCleaner{Store: store, Lookup: func(ctx context.Context, uid string) (rbac.CleanupNamespace, error) {
		parent, err := r.store.GetNamespace(ctx, uid)
		if errors.Is(err, datastore.ErrNotFound) {
			return rbac.CleanupNamespace{}, rbac.ErrNotFound
		}
		if err != nil {
			return rbac.CleanupNamespace{}, err
		}
		if parent == nil {
			return rbac.CleanupNamespace{}, rbac.ErrUnavailable
		}
		return rbac.CleanupNamespace{UID: parent.UID, Name: parent.Name, Terminating: parent.DeletionTimestamp != nil}, nil
	}}
	for _, job := range page.Jobs {
		done, err := cleaner.Reconcile(ctx, job)
		result.Pending = !done || err != nil
		if err != nil {
			// Retain the job, but advance discovery so one blocked parent cannot
			// starve cleanup for unrelated Namespace incarnations.
			r.logger.Warn("RBAC namespace cleanup retained for retry", zap.Error(err))
		}
	}
	return result, nil
}
