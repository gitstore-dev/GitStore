// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"encoding/json"
	"math"

	"github.com/google/uuid"
)

func PlanGuardMutation(current RoleGuard, existing *RoleClaim, mutation GuardMutation) (RoleGuard, bool, error) {
	id, err := uuid.Parse(mutation.RoleUID)
	if err != nil || id.String() != mutation.RoleUID || current.RoleUID != mutation.RoleUID || current.Claims < 0 {
		return current, false, ErrInvalid
	}
	if mutation.Action == GuardAcquire || mutation.Action == GuardRelease {
		claim := mutation.Claim
		if claim == nil || claim.RoleUID != mutation.RoleUID ||
			(claim.Binding.Kind != RoleBinding && claim.Binding.Kind != ClusterRoleBinding) || ValidateKey(claim.Binding) != nil {
			return current, false, ErrInvalid
		}
		bindingUID, err := uuid.Parse(claim.BindingUID)
		if err != nil || bindingUID.String() != claim.BindingUID {
			return current, false, ErrInvalid
		}
	}
	if mutation.Action == GuardRegister && current.State == GuardActive {
		return current, false, nil
	}
	if mutation.Action == GuardRelease && existing == nil {
		return current, false, nil
	}
	if mutation.Action == GuardFinalize && current.State == GuardFinalized {
		return current, false, nil
	}
	if mutation.Action == GuardAcquire && current.State == GuardActive && existing != nil &&
		mutation.Claim != nil && *existing == *mutation.Claim {
		return current, false, nil
	}
	if current.Version != mutation.ExpectedVersion || current.Version >= math.MaxInt64 {
		return current, false, ErrConflict
	}
	next := current
	switch mutation.Action {
	case GuardRegister:
		if current.Version != 0 || current.State != "" {
			return current, false, ErrConflict
		}
		next.State = GuardActive
	case GuardAcquire, GuardRelease:
		claim := mutation.Claim
		if claim == nil || claim.RoleUID != mutation.RoleUID || claim.BindingUID == "" ||
			(claim.Binding.Kind != RoleBinding && claim.Binding.Kind != ClusterRoleBinding) || ValidateKey(claim.Binding) != nil {
			return current, false, ErrInvalid
		}
		if mutation.Action == GuardAcquire {
			if current.State != GuardActive || existing != nil || current.Claims == math.MaxInt64 {
				return current, false, ErrConflict
			}
			next.Claims++
		} else {
			if *existing != *claim || current.Claims < 1 || current.State == GuardFinalized {
				return current, false, ErrConflict
			}
			next.Claims--
		}
	case GuardClose:
		if current.State != GuardActive && current.State != GuardTerminating {
			return current, false, ErrConflict
		}
		next.State = GuardTerminating
	case GuardFinalize:
		if current.State != GuardTerminating || current.Claims != 0 {
			return current, false, ErrConflict
		}
		next.State = GuardFinalized
	default:
		return current, false, ErrInvalid
	}
	next.Version++
	return next, true, nil
}

func ResolvedRoleUID(resource *Resource) (string, error) {
	if resource.Kind.IsRole() || len(resource.Status) == 0 {
		return "", nil
	}
	var status struct {
		Resolved struct {
			RoleRef struct {
				UID string `json:"uid"`
			} `json:"roleRef"`
		} `json:"resolved"`
	}
	if json.Unmarshal(resource.Status, &status) != nil {
		return "", ErrInvalid
	}
	return status.Resolved.RoleRef.UID, nil
}
