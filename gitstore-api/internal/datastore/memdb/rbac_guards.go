// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	gomemdb "github.com/hashicorp/go-memdb"
)

type rbacClaimRecord struct {
	ID, RoleUID string
	Claim       rbac.RoleClaim
}

func readRBACGuard(tx *gomemdb.Txn, uid string) (rbac.RoleGuard, error) {
	raw, err := tx.First("rbac_guard", "id", uid)
	if err != nil {
		return rbac.RoleGuard{}, err
	}
	if raw == nil {
		return rbac.RoleGuard{RoleUID: uid}, nil
	}
	return *raw.(*rbac.RoleGuard), nil
}

func (m *memdbDatastore) ReadRBACRoleGuard(ctx context.Context, uid string) (rbac.RoleGuard, error) {
	if err := ctx.Err(); err != nil {
		return rbac.RoleGuard{}, err
	}
	if !rbac.ValidTier(uid) || uid == rbac.ClusterTierID {
		return rbac.RoleGuard{}, datastore.RBACError(rbac.ErrInvalid)
	}
	tx := m.db.Txn(false)
	defer tx.Abort()
	return readRBACGuard(tx, uid)
}

func (m *memdbDatastore) MutateRBACRoleGuard(ctx context.Context, mutation rbac.GuardMutation) (rbac.RoleGuard, error) {
	if err := ctx.Err(); err != nil {
		return rbac.RoleGuard{}, err
	}
	tx := m.db.Txn(true)
	defer tx.Abort()
	current, err := readRBACGuard(tx, mutation.RoleUID)
	if err != nil {
		return current, err
	}
	var claim *rbac.RoleClaim
	if mutation.Claim != nil {
		raw, err := tx.First("rbac_claim", "id", mutation.RoleUID+"/"+mutation.Claim.BindingUID)
		if err != nil {
			return current, err
		}
		if raw != nil {
			value := raw.(*rbacClaimRecord).Claim
			claim = &value
		}
	}
	next, changed, err := rbac.PlanGuardMutation(current, claim, mutation)
	if err != nil || !changed {
		return current, datastore.RBACError(err)
	}
	if mutation.Action == rbac.GuardAcquire {
		if err := tx.Insert("rbac_claim", &rbacClaimRecord{
			ID: mutation.RoleUID + "/" + mutation.Claim.BindingUID, RoleUID: mutation.RoleUID, Claim: *mutation.Claim,
		}); err != nil {
			return current, err
		}
	} else if mutation.Action == rbac.GuardRelease {
		if _, err := tx.DeleteAll("rbac_claim", "id", mutation.RoleUID+"/"+mutation.Claim.BindingUID); err != nil {
			return current, err
		}
	}
	if err := tx.Insert("rbac_guard", &next); err != nil {
		return current, err
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	tx.Commit()
	return next, nil
}

func (m *memdbDatastore) ListRBACRoleClaims(ctx context.Context, uid, after string, limit int) (rbac.ClaimPage, error) {
	cursor, err := rbac.DecodePageCursor(uid, after, limit)
	if err != nil || !rbac.ValidTier(uid) || uid == rbac.ClusterTierID {
		return rbac.ClaimPage{}, datastore.RBACError(rbac.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return rbac.ClaimPage{}, err
	}
	tx := m.db.Txn(false)
	defer tx.Abort()
	iter, err := tx.LowerBound("rbac_claim", "id", uid+"/"+cursor)
	if err != nil {
		return rbac.ClaimPage{}, err
	}
	page := rbac.ClaimPage{}
	last := ""
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		record := raw.(*rbacClaimRecord)
		if record.RoleUID != uid {
			break
		}
		if record.Claim.BindingUID <= cursor {
			continue
		}
		if len(page.Claims) == limit {
			page.Next = rbac.EncodePageCursor(uid, last)
			break
		}
		page.Claims = append(page.Claims, record.Claim)
		last = record.Claim.BindingUID
	}
	return page, nil
}
