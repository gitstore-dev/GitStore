// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
)

type References struct {
	Store    Store
	Builtins *BuiltinSet
}

func (r References) mutateGuard(ctx context.Context, uid string, action GuardAction, claim *RoleClaim) (RoleGuard, error) {
	for range 3 {
		current, err := r.Store.ReadRBACRoleGuard(ctx, uid)
		if err != nil {
			return current, err
		}
		if current.Version == 0 && action != GuardRegister {
			_, err = r.Store.MutateRBACRoleGuard(ctx, GuardMutation{RoleUID: uid, Action: GuardRegister})
			if err != nil && !errors.Is(err, ErrConflict) {
				return current, err
			}
			continue
		}
		if action == GuardClose && (current.State == GuardTerminating || current.State == GuardFinalized) {
			return current, nil
		}
		result, err := r.Store.MutateRBACRoleGuard(ctx, GuardMutation{
			RoleUID: uid, ExpectedVersion: current.Version, Action: action, Claim: claim,
		})
		if !errors.Is(err, ErrConflict) {
			return result, err
		}
	}
	return RoleGuard{}, ErrConflict
}

func (r References) RecoverRoleClaims(ctx context.Context, uid string) error {
	if r.Store == nil || !validUUID(uid) {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	guard, err := r.Store.ReadRBACRoleGuard(ctx, uid)
	if err != nil {
		return err
	}
	if guard.State != GuardTerminating && guard.State != GuardFinalized {
		return ErrConflict
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := r.Store.ListRBACRoleClaims(ctx, uid, after, 1000)
		if err != nil {
			return err
		}
		if len(page.Claims) > 1000 || page.Next != "" && (page.Next == after || len(page.Claims) == 0) {
			return ErrUnavailable
		}
		for _, claim := range page.Claims {
			if claim.RoleUID != uid {
				return ErrUnavailable
			}
			if _, err := r.RecoverClaim(ctx, claim); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		after = page.Next
	}
}

func (r References) CloseRole(ctx context.Context, key ResourceKey, uid string) error {
	if r.Store == nil || !key.Kind.IsRole() || ValidateKey(key) != nil {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	role, err := r.Store.GetRBAC(ctx, key)
	if err != nil {
		return err
	}
	if role.Metadata.UID != uid {
		return ErrConflict
	}
	_, err = r.mutateGuard(ctx, uid, GuardClose, nil)
	return err
}

// PrepareResolution acquires a durable claim before status/owner-reference publication.
// Absent or terminating targets remain unresolved. It never switches an old UID
// link to a recreated role with the same name.
func (r References) PrepareResolution(ctx context.Context, key ResourceKey, uid, version string) (*Resource, error) {
	if r.Store == nil || key.Kind.IsRole() || ValidateKey(key) != nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	binding, err := r.Store.GetRBAC(ctx, key)
	if err != nil {
		return nil, err
	}
	if binding.Metadata.UID != uid || binding.Metadata.ResourceVersion != version || binding.Metadata.DeletionTimestamp != nil {
		return nil, ErrConflict
	}
	spec, err := DecodeBindingSpec(binding)
	if err != nil {
		return nil, err
	}
	targetKey := ResourceKey{TierID: key.TierID, Kind: spec.RoleRef.Kind, Name: spec.RoleRef.Name}
	if targetKey.Kind == ClusterRole {
		targetKey.TierID = ClusterTierID
	}
	if !targetKey.Kind.IsRole() || ValidateKey(targetKey) != nil {
		return nil, ErrInvalid
	}
	var role *Resource
	virtual := false
	if r.Builtins != nil && targetKey.Kind == ClusterRole {
		role, err = r.Builtins.Get(targetKey.Kind, targetKey.Name)
		virtual = err == nil
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if !virtual {
		role, err = r.Store.GetRBAC(ctx, targetKey)
	}
	if errors.Is(err, ErrNotFound) || err == nil && role.Metadata.DeletionTimestamp != nil {
		return binding, nil
	}
	if err != nil {
		return nil, err
	}
	previous, err := ResolvedRoleUID(binding)
	if err != nil || previous != "" && previous != role.Metadata.UID {
		return nil, ErrConflict
	}
	claim := RoleClaim{RoleUID: role.Metadata.UID, BindingUID: uid, Binding: key}
	if _, err := r.mutateGuard(ctx, claim.RoleUID, GuardAcquire, &claim); err != nil {
		return nil, err
	}
	if !virtual {
		latest, err := r.Store.GetRBAC(ctx, targetKey)
		if err != nil {
			return nil, err
		}
		if latest.Metadata.UID != role.Metadata.UID || latest.Metadata.DeletionTimestamp != nil {
			return binding, nil
		}
		role = latest
	}
	guard, err := r.Store.ReadRBACRoleGuard(ctx, claim.RoleUID)
	if err != nil {
		return nil, err
	}
	if guard.State != GuardActive {
		return binding, nil
	}
	updated, err := CloneResource(binding)
	if err != nil {
		return nil, err
	}
	status := map[string]json.RawMessage{}
	if len(updated.Status) > 0 && json.Unmarshal(updated.Status, &status) != nil {
		return nil, ErrUnavailable
	}
	resolved := map[string]json.RawMessage{}
	if raw := status["resolved"]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &resolved) != nil {
		return nil, ErrUnavailable
	}
	resolved["roleRef"], _ = json.Marshal(map[string]any{
		"uid": role.Metadata.UID, "kind": role.Kind, "name": role.Metadata.Name, "observedGeneration": role.Metadata.Generation,
	})
	status["resolved"], _ = json.Marshal(resolved)
	updated.Status, err = json.Marshal(status)
	if err != nil {
		return nil, err
	}
	var owners []map[string]any
	if len(updated.Metadata.OwnerReferences) > 0 && json.Unmarshal(updated.Metadata.OwnerReferences, &owners) != nil {
		return nil, ErrUnavailable
	}
	found := false
	for _, owner := range owners {
		if owner["uid"] == role.Metadata.UID {
			owner["blockOwnerDeletion"] = true
			found = true
		}
	}
	if !found {
		owners = append(owners, map[string]any{"apiVersion": APIVersion, "kind": role.Kind,
			"name": role.Metadata.Name, "uid": role.Metadata.UID, "blockOwnerDeletion": true})
	}
	updated.Metadata.OwnerReferences, err = json.Marshal(owners)
	if err != nil {
		return nil, err
	}
	if string(updated.Status) == string(binding.Status) && string(updated.Metadata.OwnerReferences) == string(binding.Metadata.OwnerReferences) {
		return binding, nil
	}
	return updated, nil
}

func (r References) Resolve(ctx context.Context, key ResourceKey, uid, version string) (*Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	updated, err := r.PrepareResolution(ctx, key, uid, version)
	if err != nil {
		return nil, err
	}
	current, err := r.Store.GetRBAC(ctx, key)
	if err != nil {
		return nil, err
	}
	if current.Metadata.UID != uid || current.Metadata.ResourceVersion != version {
		return nil, ErrConflict
	}
	if bytes.Equal(updated.Status, current.Status) && bytes.Equal(updated.Metadata.OwnerReferences, current.Metadata.OwnerReferences) {
		return current, nil
	}
	state, err := r.Store.ReadRBACTier(ctx, key.TierID)
	if err != nil {
		return nil, err
	}
	_, err = r.Store.ApplyRBAC(ctx, TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
		Changes: []Change{{Resource: updated, ExpectedUID: uid, ExpectedResourceVersion: version}}})
	if err != nil {
		return nil, err
	}
	return r.Store.GetRBAC(ctx, key)
}

// RecoverClaim retains uncertain/live claims. An unresolved binding cannot be
// fenced while acquisition remains open: a new resolver could join after the
// fence. After closure, a binding CAS invalidates every pre-closure publisher
// before releasing the claim, even when it already read the old claim.
func (r References) RecoverClaim(ctx context.Context, claim RoleClaim) (bool, error) {
	if r.Store == nil || !validUUID(claim.RoleUID) || !validUUID(claim.BindingUID) ||
		ValidateKey(claim.Binding) != nil || claim.Binding.Kind.IsRole() {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	binding, err := r.Store.GetRBAC(ctx, claim.Binding)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err == nil && binding.Metadata.UID == claim.BindingUID && binding.Metadata.DeletionTimestamp == nil {
		resolved, err := ResolvedRoleUID(binding)
		if err != nil {
			return false, err
		}
		if resolved == claim.RoleUID {
			return false, nil
		}
		guard, err := r.Store.ReadRBACRoleGuard(ctx, claim.RoleUID)
		if err != nil {
			return false, err
		}
		if guard.State == GuardActive || guard.State == "" {
			return false, nil
		}
		state, err := r.Store.ReadRBACTier(ctx, claim.Binding.TierID)
		if err != nil {
			return false, err
		}
		_, err = r.Store.ApplyRBAC(ctx, TierWrite{TierID: claim.Binding.TierID, ExpectedRevision: state.Revision,
			Changes: []Change{{Resource: binding, ExpectedUID: claim.BindingUID, ExpectedResourceVersion: binding.Metadata.ResourceVersion}}})
		if err != nil {
			return false, err
		}
	}
	_, err = r.mutateGuard(ctx, claim.RoleUID, GuardRelease, &claim)
	return err == nil, err
}
