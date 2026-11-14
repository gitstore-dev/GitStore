// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	gomemdb "github.com/hashicorp/go-memdb"
)

type rbacRecord struct {
	ID, UID, TierID, RoleUID string
	Resource                 *rbac.Resource
}

func (m *memdbDatastore) RBACStore() datastore.RBACStore { return m }

func rbacRecordID(key rbac.ResourceKey) string {
	return key.TierID + "\x00" + string(key.Kind) + "/" + key.Name
}

func readRBACTier(tx *gomemdb.Txn, tier string) (rbac.TierState, error) {
	raw, err := tx.First("rbac_tier", "id", tier)
	if err != nil {
		return rbac.TierState{}, err
	}
	if raw == nil {
		return rbac.TierState{TierID: tier}, nil
	}
	return *raw.(*rbac.TierState), nil
}

func (m *memdbDatastore) ReadRBACTier(ctx context.Context, tier string) (rbac.TierState, error) {
	if err := ctx.Err(); err != nil {
		return rbac.TierState{}, err
	}
	if !rbac.ValidTier(tier) {
		return rbac.TierState{}, datastore.RBACError(rbac.ErrInvalid)
	}
	tx := m.db.Txn(false)
	defer tx.Abort()
	return readRBACTier(tx, tier)
}

func (m *memdbDatastore) GetRBAC(ctx context.Context, key rbac.ResourceKey) (*rbac.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := rbac.ValidateKey(key); err != nil {
		return nil, datastore.RBACError(err)
	}
	tx := m.db.Txn(false)
	defer tx.Abort()
	raw, err := tx.First("rbac_resource", "id", rbacRecordID(key))
	return cloneRBACRecord(raw, err)
}

func (m *memdbDatastore) GetRBACByUID(ctx context.Context, uid string) (*rbac.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if uid == "" || len(uid) > 64 {
		return nil, datastore.RBACError(rbac.ErrInvalid)
	}
	tx := m.db.Txn(false)
	defer tx.Abort()
	raw, err := tx.First("rbac_resource", "uid", uid)
	return cloneRBACRecord(raw, err)
}

func cloneRBACRecord(raw any, err error) (*rbac.Resource, error) {
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, datastore.RBACError(rbac.ErrNotFound)
	}
	resource, err := rbac.CloneResource(raw.(*rbacRecord).Resource)
	return resource, datastore.RBACError(err)
}

func (m *memdbDatastore) ListRBAC(ctx context.Context, request rbac.ListRequest) (rbac.Page, error) {
	scope := request.TierID + "/" + string(request.Kind)
	after, err := rbac.DecodePageCursor(scope, request.After, request.Limit)
	if err != nil || !rbac.ValidTier(request.TierID) {
		return rbac.Page{}, datastore.RBACError(rbac.ErrInvalid)
	}
	if request.Kind != "" && rbac.ValidateKey(rbac.ResourceKey{TierID: request.TierID, Kind: request.Kind, Name: "probe"}) != nil {
		return rbac.Page{}, datastore.RBACError(rbac.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return rbac.Page{}, err
	}
	tx := m.db.Txn(false)
	defer tx.Abort()
	state, err := readRBACTier(tx, request.TierID)
	if err != nil {
		return rbac.Page{}, err
	}
	if request.ExpectedRevision != nil && *request.ExpectedRevision != state.Revision {
		return rbac.Page{}, datastore.RBACError(rbac.ErrConflict)
	}
	page := rbac.Page{Revision: state.Revision}
	iter, err := tx.LowerBound("rbac_resource", "id", request.TierID+"\x00"+after)
	if err != nil {
		return page, err
	}
	last := ""
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		record := raw.(*rbacRecord)
		if record.TierID != request.TierID {
			break
		}
		key := strings.TrimPrefix(record.ID, request.TierID+"\x00")
		if key <= after || (request.Kind != "" && record.Resource.Kind != request.Kind) {
			continue
		}
		if len(page.Resources) == request.Limit {
			page.Next = rbac.EncodePageCursor(scope, last)
			break
		}
		resource, err := rbac.CloneResource(record.Resource)
		if err != nil {
			return rbac.Page{}, datastore.RBACError(err)
		}
		page.Resources = append(page.Resources, resource)
		last = key
	}
	return page, nil
}

func (m *memdbDatastore) ApplyRBAC(ctx context.Context, write rbac.TierWrite) (rbac.TierState, error) {
	write.Actor = datastore.RBACAuditActorFromContext(ctx)
	if err := ctx.Err(); err != nil {
		return rbac.TierState{}, err
	}
	if err := rbac.ValidateTierWriteBounds(write); err != nil {
		return rbac.TierState{}, datastore.RBACError(err)
	}
	tx := m.db.Txn(true)
	defer tx.Abort()
	state, err := readRBACTier(tx, write.TierID)
	if err != nil {
		return state, err
	}
	current := make(map[rbac.ResourceKey]*rbac.Resource, len(write.Changes))
	for _, change := range write.Changes {
		if change.Resource == nil {
			return state, datastore.RBACError(rbac.ErrInvalid)
		}
		key := rbac.ResourceKey{TierID: write.TierID, Kind: change.Resource.Kind, Name: change.Resource.Metadata.Name}
		raw, err := tx.First("rbac_resource", "id", rbacRecordID(key))
		if err != nil {
			return state, err
		}
		if raw != nil {
			current[key] = raw.(*rbacRecord).Resource
		}
	}
	plan, err := rbac.PlanTierWrite(state, current, write, time.Now())
	if err != nil || plan.Replay {
		return state, datastore.RBACError(err)
	}
	for _, change := range plan.Changes {
		id := rbacRecordID(change.Key)
		if change.After == nil {
			if change.Before.Kind.IsRole() {
				guard, err := readRBACGuard(tx, change.Before.Metadata.UID)
				if err != nil {
					return state, err
				}
				if guard.State != rbac.GuardFinalized || guard.Claims != 0 {
					return state, datastore.RBACError(rbac.ErrConflict)
				}
			} else {
				roleUID, err := rbac.ResolvedRoleUID(change.Before)
				if err != nil {
					return state, datastore.RBACError(err)
				}
				if roleUID != "" {
					claim, err := tx.First("rbac_claim", "id", roleUID+"/"+change.Before.Metadata.UID)
					if err != nil || claim != nil {
						return state, datastore.RBACError(rbac.ErrConflict)
					}
				}
			}
			if _, err := tx.DeleteAll("rbac_resource", "id", id); err != nil {
				return state, err
			}
			continue
		}
		roleUID, err := rbac.ResolvedRoleUID(change.After)
		if err != nil {
			return state, datastore.RBACError(err)
		}
		if change.Before != nil {
			beforeUID, err := rbac.ResolvedRoleUID(change.Before)
			if err != nil || (beforeUID != "" && beforeUID != roleUID) {
				return state, datastore.RBACError(rbac.ErrInvalid)
			}
		}
		if roleUID != "" {
			claim, err := tx.First("rbac_claim", "id", roleUID+"/"+change.After.Metadata.UID)
			if err != nil || claim == nil {
				return state, datastore.RBACError(rbac.ErrConflict)
			}
			if claim.(*rbacClaimRecord).Claim.Binding != change.Key {
				return state, datastore.RBACError(rbac.ErrConflict)
			}
		}
		existing, err := tx.First("rbac_resource", "uid", change.After.Metadata.UID)
		if err != nil {
			return state, err
		}
		if existing != nil && existing.(*rbacRecord).ID != id {
			return state, datastore.RBACError(rbac.ErrConflict)
		}
		if err := tx.Insert("rbac_resource", &rbacRecord{
			ID: id, UID: change.After.Metadata.UID, TierID: write.TierID, RoleUID: roleUID, Resource: change.After,
		}); err != nil {
			return state, err
		}
	}
	if err := tx.Insert("rbac_tier", &plan.State); err != nil {
		return state, err
	}
	events, err := rbacJournalEvents(plan)
	if err != nil {
		return state, err
	}
	// Acquire before Commit releases the memdb writer lock, keeping candidate
	// events and tier progress contiguous and in authoritative commit order.
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return state, err
	}
	tx.Commit()
	now := time.Now().UTC()
	m.pruneLocked(now.Add(-m.namespaceWatchRetention))
	for _, event := range events {
		m.namespaceWatchSequence++
		event.Epoch, event.Sequence, event.At = m.namespaceWatchEpoch, m.namespaceWatchSequence, now
		m.namespaceWatchEvents = append(m.namespaceWatchEvents, event)
	}
	return plan.State, nil
}

func rbacJournalEvents(plan rbac.TierPlan) ([]datastore.ResourceWatchEvent, error) {
	events := make([]datastore.ResourceWatchEvent, 0, len(plan.Changes)+1)
	for _, row := range plan.Changes {
		change, err := watchjournal.RBACChange(row.Before, row.After)
		if err != nil {
			return nil, err
		}
		change.DeduplicationKey = fmt.Sprintf("memdb:rbac:%s:%d:%s", plan.State.TierID, plan.State.Revision, rbacRecordID(row.Key))
		event, ok := watchjournal.ClassifyResource(change, nil)
		if ok {
			events = append(events, event)
		}
	}
	progress, _ := watchjournal.ClassifyResource(watchjournal.Change{
		DeduplicationKey: fmt.Sprintf("memdb:rbac:%s:%d", plan.State.TierID, plan.State.Revision),
		TierProgress:     &datastore.RBACTierProgress{TierID: plan.State.TierID, Revision: plan.State.Revision},
	}, nil)
	return append(events, progress), nil
}

func (m *memdbDatastore) RepairRBACProjection(ctx context.Context, tier string, revision uint64) error {
	state, err := m.ReadRBACTier(ctx, tier)
	if err != nil {
		return err
	}
	if state.Revision != revision {
		return datastore.RBACError(rbac.ErrConflict)
	}
	return nil
}

func (m *memdbDatastore) ListRBACBindingsByRole(ctx context.Context, roleUID, after string, limit int) (rbac.Page, error) {
	key, err := rbac.DecodePageCursor(roleUID, after, limit)
	if err != nil {
		return rbac.Page{}, datastore.RBACError(err)
	}
	if err := ctx.Err(); err != nil {
		return rbac.Page{}, err
	}
	tx := m.db.Txn(false)
	defer tx.Abort()
	iter, err := tx.Get("rbac_resource", "role_uid", roleUID)
	if err != nil {
		return rbac.Page{}, err
	}
	page := rbac.Page{}
	last := ""
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		record := raw.(*rbacRecord)
		if record.ID <= key {
			continue
		}
		if len(page.Resources) == limit {
			page.Next = rbac.EncodePageCursor(roleUID, last)
			break
		}
		resource, err := rbac.CloneResource(record.Resource)
		if err != nil {
			return rbac.Page{}, datastore.RBACError(err)
		}
		page.Resources = append(page.Resources, resource)
		last = record.ID
	}
	return page, nil
}
