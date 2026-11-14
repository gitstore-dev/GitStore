// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3"
)

type rbacRepairStep struct {
	Key           rbac.ResourceKey
	UID           string
	BeforeRoleUID string
	AfterRoleUID  string
	Removed       bool
}

func (s *scyllaDatastore) RBACStore() datastore.RBACStore { return s }

func rbacEntry(key rbac.ResourceKey) string { return string(key.Kind) + "/" + key.Name }

func decodeRBACResource(tier string, data []byte) (*rbac.Resource, error) {
	var resource rbac.Resource
	if json.Unmarshal(data, &resource) != nil {
		return nil, datastore.RBACError(rbac.ErrUnavailable)
	}
	if resource.Kind.Namespaced() {
		resource.NamespaceUID = tier
	}
	return &resource, nil
}

func (s *scyllaDatastore) readRBACTier(ctx context.Context, tier string) (rbac.TierState, []rbacRepairStep, error) {
	state := rbac.TierState{TierID: tier}
	var projection, repair []byte
	err := s.session.Session.Query(
		"SELECT revision,role_count,binding_count,payload_bytes,projection,repair,namespace_retired FROM rbac_resources_by_scope WHERE tier=? AND entry=?",
		tier, "",
	).WithContext(ctx).Consistency(gocql.Quorum).Scan(&state.Revision, &state.RoleCount, &state.BindingCount, &state.PayloadBytes, &projection, &repair, &state.NamespaceRetired)
	if errors.Is(err, gocql.ErrNotFound) {
		return state, nil, nil
	}
	if err != nil {
		return state, nil, err
	}
	var steps []rbacRepairStep
	if len(projection) > 4096 || len(repair) > (rbac.MaxRolesPerTier+rbac.MaxBindingsPerTier)*4096 ||
		json.Unmarshal(projection, &state.Projection) != nil || (len(repair) > 0 && json.Unmarshal(repair, &steps) != nil) ||
		len(steps) > rbac.MaxRolesPerTier+rbac.MaxBindingsPerTier {
		return state, nil, datastore.RBACError(rbac.ErrUnavailable)
	}
	state.ProjectionPending = len(steps) > 0
	return state, steps, nil
}

func (s *scyllaDatastore) ReadRBACTier(ctx context.Context, tier string) (rbac.TierState, error) {
	if !rbac.ValidTier(tier) {
		return rbac.TierState{}, datastore.RBACError(rbac.ErrInvalid)
	}
	state, _, err := s.readRBACTier(ctx, tier)
	return state, err
}

func (s *scyllaDatastore) GetRBAC(ctx context.Context, key rbac.ResourceKey) (*rbac.Resource, error) {
	if err := rbac.ValidateKey(key); err != nil {
		return nil, datastore.RBACError(err)
	}
	var data []byte
	err := s.session.Session.Query("SELECT payload FROM rbac_resources_by_scope WHERE tier=? AND entry=?", key.TierID, rbacEntry(key)).
		WithContext(ctx).Consistency(gocql.Quorum).Scan(&data)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, datastore.RBACError(rbac.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return decodeRBACResource(key.TierID, data)
}

func (s *scyllaDatastore) GetRBACByUID(ctx context.Context, uid string) (*rbac.Resource, error) {
	if uid == "" || len(uid) > 64 {
		return nil, datastore.RBACError(rbac.ErrInvalid)
	}
	var tier, entry string
	var deleted bool
	err := s.session.Session.Query("SELECT tier,entry,deleted FROM rbac_resources_by_id WHERE uid=?", uid).
		WithContext(ctx).Consistency(gocql.Quorum).Scan(&tier, &entry, &deleted)
	if errors.Is(err, gocql.ErrNotFound) || (err == nil && deleted) {
		return nil, datastore.RBACError(rbac.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	kind, name, ok := strings.Cut(entry, "/")
	if !ok {
		return nil, datastore.RBACError(rbac.ErrUnavailable)
	}
	resource, err := s.GetRBAC(ctx, rbac.ResourceKey{TierID: tier, Kind: rbac.Kind(kind), Name: name})
	if err == nil && resource.Metadata.UID != uid {
		return nil, datastore.RBACError(rbac.ErrNotFound)
	}
	return resource, err
}

func (s *scyllaDatastore) ListRBAC(ctx context.Context, request rbac.ListRequest) (rbac.Page, error) {
	scope := request.TierID + "/" + string(request.Kind)
	after, err := rbac.DecodePageCursor(scope, request.After, request.Limit)
	if err != nil || !rbac.ValidTier(request.TierID) {
		return rbac.Page{}, datastore.RBACError(rbac.ErrInvalid)
	}
	before, err := s.ReadRBACTier(ctx, request.TierID)
	if err != nil {
		return rbac.Page{}, err
	}
	if request.ExpectedRevision != nil && *request.ExpectedRevision != before.Revision {
		return rbac.Page{}, datastore.RBACError(rbac.ErrConflict)
	}
	statement := "SELECT entry,payload FROM rbac_resources_by_scope WHERE tier=? AND entry>? LIMIT ?"
	args := []any{request.TierID, after, request.Limit + 1}
	if request.Kind != "" {
		if rbac.ValidateKey(rbac.ResourceKey{TierID: request.TierID, Kind: request.Kind, Name: "probe"}) != nil {
			return rbac.Page{}, datastore.RBACError(rbac.ErrInvalid)
		}
		start := max(after, string(request.Kind)+"/")
		statement = "SELECT entry,payload FROM rbac_resources_by_scope WHERE tier=? AND entry>? AND entry<? LIMIT ?"
		args = []any{request.TierID, start, string(request.Kind) + "0", request.Limit + 1}
	}
	iter := s.session.Session.Query(statement, args...).WithContext(ctx).Consistency(gocql.Quorum).Iter()
	page := rbac.Page{Revision: before.Revision}
	var entry, last string
	var data []byte
	for iter.Scan(&entry, &data) {
		if len(page.Resources) == request.Limit {
			page.Next = rbac.EncodePageCursor(scope, last)
			break
		}
		resource, err := decodeRBACResource(request.TierID, data)
		if err != nil {
			_ = iter.Close()
			return rbac.Page{}, err
		}
		page.Resources = append(page.Resources, resource)
		last = entry
	}
	if err := iter.Close(); err != nil {
		return rbac.Page{}, err
	}
	afterState, err := s.ReadRBACTier(ctx, request.TierID)
	if err != nil {
		return rbac.Page{}, err
	}
	if afterState.Revision != before.Revision {
		return rbac.Page{}, datastore.RBACError(rbac.ErrConflict)
	}
	return page, nil
}

func (s *scyllaDatastore) ApplyRBAC(ctx context.Context, write rbac.TierWrite) (rbac.TierState, error) {
	write.Actor = datastore.RBACAuditActorFromContext(ctx)
	if err := rbac.ValidateTierWriteBounds(write); err != nil {
		return rbac.TierState{}, datastore.RBACError(err)
	}
	state, _, err := s.readRBACTier(ctx, write.TierID)
	if err != nil {
		return state, err
	}
	current := make(map[rbac.ResourceKey]*rbac.Resource, len(write.Changes))
	for _, change := range write.Changes {
		if change.Resource == nil {
			return state, datastore.RBACError(rbac.ErrInvalid)
		}
		key := rbac.ResourceKey{TierID: write.TierID, Kind: change.Resource.Kind, Name: change.Resource.Metadata.Name}
		resource, err := s.GetRBAC(ctx, key)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return state, err
		}
		current[key] = resource
	}
	plan, err := rbac.PlanTierWrite(state, current, write, time.Now())
	if err != nil {
		return state, datastore.RBACError(err)
	}
	if plan.Replay {
		if err := s.RepairRBACProjection(ctx, state.TierID, state.Revision); err != nil {
			return state, err
		}
		state.ProjectionPending = false
		return state, nil
	}
	batch := s.session.Batch(gocql.LoggedBatch)
	batch.Batch = batch.Batch.WithContext(ctx).SerialConsistency(gocql.Serial)
	batch.SetConsistency(gocql.Quorum)
	steps := make([]rbacRepairStep, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		step := rbacRepairStep{Key: change.Key, Removed: change.After == nil}
		if change.Before != nil {
			step.UID = change.Before.Metadata.UID
			step.BeforeRoleUID, err = rbac.ResolvedRoleUID(change.Before)
			if err != nil {
				return state, datastore.RBACError(err)
			}
		}
		if change.After == nil {
			if err := s.verifyRBACRemoval(ctx, change.Before, step.BeforeRoleUID); err != nil {
				return state, err
			}
			batch.Entries = append(batch.Entries, gocql.BatchEntry{
				Stmt: "DELETE FROM rbac_resources_by_scope WHERE tier=? AND entry=?",
				Args: []any{write.TierID, rbacEntry(change.Key)},
			})
		} else {
			step.UID = change.After.Metadata.UID
			step.AfterRoleUID, err = rbac.ResolvedRoleUID(change.After)
			if err != nil || (step.BeforeRoleUID != "" && step.BeforeRoleUID != step.AfterRoleUID) {
				return state, datastore.RBACError(rbac.ErrInvalid)
			}
			if step.AfterRoleUID != "" {
				claim, err := s.readRBACClaim(ctx, step.AfterRoleUID, step.UID)
				if err != nil {
					return state, err
				}
				if claim == nil || claim.Binding != change.Key {
					return state, datastore.RBACError(rbac.ErrConflict)
				}
			}
			data, err := json.Marshal(change.After)
			if err != nil {
				return state, datastore.RBACError(rbac.ErrInvalid)
			}
			batch.Entries = append(batch.Entries, gocql.BatchEntry{
				Stmt: "INSERT INTO rbac_resources_by_scope (tier,entry,uid,payload,revision,creation_timestamp,creation_actor,update_timestamp,update_actor) VALUES (?,?,?,?,?,?,?,?,?)",
				Args: []any{write.TierID, rbacEntry(change.Key), step.UID, data, plan.State.Revision,
					change.After.Metadata.CreationTimestamp, change.After.Metadata.CreationActor,
					change.After.Metadata.UpdateTimestamp, change.After.Metadata.UpdateActor},
			})
		}
		steps = append(steps, step)
	}
	projection, _ := json.Marshal(plan.State.Projection)
	repair, _ := json.Marshal(steps)
	args := []any{plan.State.Revision, plan.State.RoleCount, plan.State.BindingCount, plan.State.PayloadBytes, projection, repair, plan.State.NamespaceRetired, write.TierID, ""}
	statement := "UPDATE rbac_resources_by_scope SET revision=?,role_count=?,binding_count=?,payload_bytes=?,projection=?,repair=?,namespace_retired=? WHERE tier=? AND entry=? IF revision=?"
	if state.Revision == 0 {
		statement = "INSERT INTO rbac_resources_by_scope (revision,role_count,binding_count,payload_bytes,projection,repair,namespace_retired,tier,entry) VALUES (?,?,?,?,?,?,?,?,?) IF NOT EXISTS"
	} else {
		args = append(args, state.Revision)
	}
	batch.Entries = append(batch.Entries, gocql.BatchEntry{Stmt: statement, Args: args})
	if err := s.executeRBACBatch(batch); err != nil {
		return state, err
	}
	if err := s.RepairRBACProjection(ctx, write.TierID, plan.State.Revision); err != nil {
		plan.State.ProjectionPending = true
		return plan.State, fmt.Errorf("scylla: RBAC committed with pending index repair: %w", err)
	}
	return plan.State, nil
}

func (s *scyllaDatastore) executeRBACBatch(batch *gocqlx.Batch) error {
	applied, iter, err := s.session.MapExecuteBatchCAS(batch, make(map[string]any))
	if iter != nil {
		if closeErr := iter.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return fmt.Errorf("scylla: RBAC transaction outcome uncertain: %w", err)
	}
	if !applied {
		return datastore.RBACError(rbac.ErrConflict)
	}
	return nil
}

func (s *scyllaDatastore) verifyRBACRemoval(ctx context.Context, before *rbac.Resource, roleUID string) error {
	if before.Kind.IsRole() {
		guard, err := s.ReadRBACRoleGuard(ctx, before.Metadata.UID)
		if err != nil {
			return err
		}
		if guard.State != rbac.GuardFinalized || guard.Claims != 0 {
			return datastore.RBACError(rbac.ErrConflict)
		}
	} else if roleUID != "" {
		claim, err := s.readRBACClaim(ctx, roleUID, before.Metadata.UID)
		if err != nil {
			return err
		}
		if claim != nil {
			return datastore.RBACError(rbac.ErrConflict)
		}
	}
	return nil
}

func (s *scyllaDatastore) ListRBACBindingsByRole(ctx context.Context, roleUID, after string, limit int) (rbac.Page, error) {
	cursor, err := rbac.DecodePageCursor(roleUID, after, limit)
	if err != nil {
		return rbac.Page{}, datastore.RBACError(err)
	}
	iter := s.session.Session.Query("SELECT binding_uid,tier,entry,deleted FROM rbac_bindings_by_role WHERE role_uid=? AND binding_uid>? LIMIT ?",
		roleUID, cursor, limit+1).WithContext(ctx).Consistency(gocql.Quorum).Iter()
	page := rbac.Page{}
	var uid, tier, entry, last string
	var deleted bool
	seen := 0
	for iter.Scan(&uid, &tier, &entry, &deleted) {
		if seen == limit {
			page.Next = rbac.EncodePageCursor(roleUID, last)
			break
		}
		seen++
		last = uid
		if deleted {
			continue
		}
		kind, name, ok := strings.Cut(entry, "/")
		if !ok {
			_ = iter.Close()
			return rbac.Page{}, datastore.RBACError(rbac.ErrUnavailable)
		}
		resource, err := s.GetRBAC(ctx, rbac.ResourceKey{TierID: tier, Kind: rbac.Kind(kind), Name: name})
		if errors.Is(err, datastore.ErrNotFound) {
			continue
		}
		if err != nil {
			_ = iter.Close()
			return rbac.Page{}, err
		}
		resolved, err := rbac.ResolvedRoleUID(resource)
		if err != nil {
			_ = iter.Close()
			return rbac.Page{}, datastore.RBACError(err)
		}
		if resource.Metadata.UID == uid && resolved == roleUID {
			page.Resources = append(page.Resources, resource)
		}
	}
	if err := iter.Close(); err != nil {
		return rbac.Page{}, err
	}
	return page, nil
}
