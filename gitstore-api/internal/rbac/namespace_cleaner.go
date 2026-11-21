// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"errors"
	"slices"
	"time"
)

type CleanupNamespace struct {
	UID, Name   string
	Terminating bool
}

type NamespaceCleaner struct {
	Store  Store
	Lookup func(context.Context, string) (CleanupNamespace, error)
}

// Reconcile processes at most one child page. The durable handoff survives
// parent removal; a retired tier prevents late projection from reviving it.
func (c NamespaceCleaner) Reconcile(ctx context.Context, job NamespaceCleanup) (bool, error) {
	if ValidateNamespaceCleanup(job) != nil || c.Store == nil || c.Lookup == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parent, err := c.Lookup(ctx, job.NamespaceUID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err == nil && (parent.UID != job.NamespaceUID || parent.Name != job.Namespace || !parent.Terminating) {
		return false, ErrConflict
	}
	state, err := c.Store.ReadRBACTier(ctx, job.NamespaceUID)
	if err != nil {
		return false, err
	}
	if state.ProjectionPending {
		return false, ErrUnavailable
	}
	if !state.NamespaceRetired {
		state, err = c.Store.ApplyRBAC(ctx, TierWrite{TierID: job.NamespaceUID,
			ExpectedRevision: state.Revision, RetireNamespace: true})
		if err != nil {
			return false, err
		}
	}
	for _, kind := range []Kind{RoleBinding, Role} {
		page, err := c.Store.ListRBAC(ctx, ListRequest{TierID: job.NamespaceUID, Kind: kind, Limit: 100})
		if err != nil {
			return false, err
		}
		if len(page.Resources) > 100 {
			return false, ErrUnavailable
		}
		if len(page.Resources) == 0 {
			continue
		}
		for _, resource := range page.Resources {
			if resource.Kind != kind || resource.NamespaceUID != job.NamespaceUID || resource.Metadata.Namespace != job.Namespace {
				return false, ErrUnavailable
			}
			if err := c.removeChild(ctx, job.NamespaceUID, resource); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	state, err = c.Store.ReadRBACTier(ctx, job.NamespaceUID)
	if err != nil {
		return false, err
	}
	if !state.NamespaceRetired || state.ProjectionPending || state.RoleCount != 0 || state.BindingCount != 0 {
		return false, ErrUnavailable
	}
	if err := c.Store.CompleteRBACNamespaceCleanup(ctx, job); err != nil {
		return false, err
	}
	return true, nil
}

func (c NamespaceCleaner) removeChild(ctx context.Context, tier string, before *Resource) error {
	after, err := CloneResource(before)
	if err != nil {
		return err
	}
	changed := false
	if after.Metadata.DeletionTimestamp == nil {
		now := time.Now().UTC()
		after.Metadata.DeletionTimestamp = &now
		changed = true
	}
	if slices.Contains(after.Metadata.Finalizers, LifecycleFinalizer) {
		after.Metadata.Finalizers = slices.DeleteFunc(after.Metadata.Finalizers, func(value string) bool { return value == LifecycleFinalizer })
		changed = true
	}
	if changed {
		state, err := c.Store.ReadRBACTier(ctx, tier)
		if err != nil {
			return err
		}
		_, err = c.Store.ApplyRBAC(ctx, TierWrite{TierID: tier, ExpectedRevision: state.Revision, Changes: []Change{{
			Resource: after, ExpectedUID: before.Metadata.UID, ExpectedResourceVersion: before.Metadata.ResourceVersion,
		}}})
		if err != nil {
			return err
		}
		after, err = c.Store.GetRBAC(ctx, ResourceKey{TierID: tier, Kind: after.Kind, Name: after.Metadata.Name})
		if err != nil {
			return err
		}
	}
	if len(after.Metadata.Finalizers) != 0 {
		return nil
	}
	key := ResourceKey{TierID: tier, Kind: after.Kind, Name: after.Metadata.Name}
	references := References{Store: c.Store}
	if after.Kind.IsRole() {
		if err := references.CloseRole(ctx, key, after.Metadata.UID); err != nil {
			return err
		}
		if err := references.RecoverRoleClaims(ctx, after.Metadata.UID); err != nil {
			return err
		}
		guard, err := c.Store.ReadRBACRoleGuard(ctx, after.Metadata.UID)
		if err != nil {
			return err
		}
		if guard.State != GuardFinalized {
			_, err = c.Store.MutateRBACRoleGuard(ctx, GuardMutation{RoleUID: after.Metadata.UID,
				ExpectedVersion: guard.Version, Action: GuardFinalize})
			if err != nil {
				return err
			}
		}
	} else {
		roleUID, err := ResolvedRoleUID(after)
		if err != nil {
			return err
		}
		if roleUID != "" {
			if _, err := references.RecoverClaim(ctx, RoleClaim{RoleUID: roleUID, BindingUID: after.Metadata.UID, Binding: key}); err != nil {
				return err
			}
		}
	}
	state, err := c.Store.ReadRBACTier(ctx, tier)
	if err != nil {
		return err
	}
	_, err = c.Store.ApplyRBAC(ctx, TierWrite{TierID: tier, ExpectedRevision: state.Revision, Changes: []Change{{
		Resource: after, ExpectedUID: after.Metadata.UID, ExpectedResourceVersion: after.Metadata.ResourceVersion, Remove: true,
	}}})
	return err
}
