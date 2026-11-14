// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gocql/gocql"
)

func (s *scyllaDatastore) ReadRBACRoleGuard(ctx context.Context, uid string) (rbac.RoleGuard, error) {
	state := rbac.RoleGuard{RoleUID: uid}
	if !rbac.ValidTier(uid) || uid == rbac.ClusterTierID {
		return state, datastore.RBACError(rbac.ErrInvalid)
	}
	err := s.session.Session.Query("SELECT version,state,claims FROM rbac_role_guards_by_id WHERE role_uid=? AND entry=?", uid, "").
		WithContext(ctx).Consistency(gocql.Quorum).Scan(&state.Version, &state.State, &state.Claims)
	if errors.Is(err, gocql.ErrNotFound) {
		return state, nil
	}
	return state, err
}

func (s *scyllaDatastore) readRBACClaim(ctx context.Context, uid, bindingUID string) (*rbac.RoleClaim, error) {
	var data []byte
	err := s.session.Session.Query("SELECT payload FROM rbac_role_guards_by_id WHERE role_uid=? AND entry=?", uid, bindingUID).
		WithContext(ctx).Consistency(gocql.Quorum).Scan(&data)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var claim rbac.RoleClaim
	if json.Unmarshal(data, &claim) != nil {
		return nil, datastore.RBACError(rbac.ErrUnavailable)
	}
	return &claim, nil
}

func (s *scyllaDatastore) MutateRBACRoleGuard(ctx context.Context, mutation rbac.GuardMutation) (rbac.RoleGuard, error) {
	current, err := s.ReadRBACRoleGuard(ctx, mutation.RoleUID)
	if err != nil {
		return current, err
	}
	var claim *rbac.RoleClaim
	if mutation.Claim != nil {
		claim, err = s.readRBACClaim(ctx, mutation.RoleUID, mutation.Claim.BindingUID)
		if err != nil {
			return current, err
		}
		after, err := s.ReadRBACRoleGuard(ctx, mutation.RoleUID)
		if err != nil {
			return current, err
		}
		if after.Version != current.Version {
			return current, datastore.RBACError(rbac.ErrConflict)
		}
	}
	next, changed, err := rbac.PlanGuardMutation(current, claim, mutation)
	if err != nil || !changed {
		return current, datastore.RBACError(err)
	}
	batch := s.session.Batch(gocql.LoggedBatch)
	batch.Batch = batch.Batch.WithContext(ctx).SerialConsistency(gocql.Serial)
	batch.SetConsistency(gocql.Quorum)
	if mutation.Action == rbac.GuardAcquire {
		data, _ := json.Marshal(mutation.Claim)
		batch.Entries = append(batch.Entries, gocql.BatchEntry{
			Stmt: "INSERT INTO rbac_role_guards_by_id (role_uid,entry,payload) VALUES (?,?,?)",
			Args: []any{mutation.RoleUID, mutation.Claim.BindingUID, data},
		})
	} else if mutation.Action == rbac.GuardRelease {
		batch.Entries = append(batch.Entries, gocql.BatchEntry{
			Stmt: "DELETE FROM rbac_role_guards_by_id WHERE role_uid=? AND entry=?",
			Args: []any{mutation.RoleUID, mutation.Claim.BindingUID},
		})
	}
	statement := "UPDATE rbac_role_guards_by_id SET version=?,state=?,claims=? WHERE role_uid=? AND entry=? IF version=?"
	args := []any{next.Version, string(next.State), next.Claims, mutation.RoleUID, ""}
	if current.Version == 0 {
		statement = "INSERT INTO rbac_role_guards_by_id (version,state,claims,role_uid,entry) VALUES (?,?,?,?,?) IF NOT EXISTS"
	} else {
		args = append(args, current.Version)
	}
	batch.Entries = append(batch.Entries, gocql.BatchEntry{Stmt: statement, Args: args})
	if err := s.executeRBACBatch(batch); err != nil {
		return current, err
	}
	return next, nil
}

func (s *scyllaDatastore) ListRBACRoleClaims(ctx context.Context, uid, after string, limit int) (rbac.ClaimPage, error) {
	cursor, err := rbac.DecodePageCursor(uid, after, limit)
	if err != nil || !rbac.ValidTier(uid) || uid == rbac.ClusterTierID {
		return rbac.ClaimPage{}, datastore.RBACError(rbac.ErrInvalid)
	}
	iter := s.session.Session.Query("SELECT entry,payload FROM rbac_role_guards_by_id WHERE role_uid=? AND entry>? LIMIT ?",
		uid, cursor, limit+1).WithContext(ctx).Consistency(gocql.Quorum).Iter()
	page := rbac.ClaimPage{}
	var entry, last string
	var data []byte
	for iter.Scan(&entry, &data) {
		if len(page.Claims) == limit {
			page.Next = rbac.EncodePageCursor(uid, last)
			break
		}
		var claim rbac.RoleClaim
		if json.Unmarshal(data, &claim) != nil {
			_ = iter.Close()
			return rbac.ClaimPage{}, datastore.RBACError(rbac.ErrUnavailable)
		}
		page.Claims = append(page.Claims, claim)
		last = entry
	}
	if err := iter.Close(); err != nil {
		return rbac.ClaimPage{}, err
	}
	return page, nil
}
