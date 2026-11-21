// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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

func (s *scyllaDatastore) RBACOriginFactsReader() datastore.RBACOriginFactsReader {
	return s.readRBACOriginFacts
}

func (s *scyllaDatastore) readRBACOriginFacts(ctx context.Context, lookups []datastore.RBACOriginLookup) ([]datastore.RBACOriginFacts, error) {
	if len(lookups) < 1 || len(lookups) > datastore.MaxRBACOriginLookups {
		return nil, datastore.RBACError(rbac.ErrInvalid)
	}
	namespaceIDs := make([]gocql.UUID, len(lookups))
	repositoryIDs := make([]gocql.UUID, len(lookups))
	names := make([]string, len(lookups))
	for i, lookup := range lookups {
		var err error
		namespaceIDs[i], err = gocql.ParseUUID(lookup.NamespaceUID)
		if err != nil || lookup.Namespace == "" {
			return nil, datastore.RBACError(rbac.ErrInvalid)
		}
		repositoryIDs[i], err = gocql.ParseUUID(lookup.RepositoryID)
		if err != nil {
			return nil, datastore.RBACError(rbac.ErrInvalid)
		}
		names[i] = lookup.Namespace
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var namespaces []namespaceRow
	var repositories []repositoryRow
	var forward, reverse []namespaceMappingRow
	reads := []func() error{
		func() error {
			return s.session.Query("SELECT * FROM namespaces_by_uid WHERE uid IN ?", nil).
				WithContext(ctx).Bind(namespaceIDs).SelectRelease(&namespaces)
		},
		func() error {
			return s.session.Query("SELECT * FROM repositories_by_uid WHERE uid IN ?", nil).
				WithContext(ctx).Bind(repositoryIDs).SelectRelease(&repositories)
		},
		func() error {
			return s.session.Query("SELECT namespace,name,repository_id FROM namespace_mappings WHERE namespace IN ? AND name=?", nil).
				WithContext(ctx).Bind(names, "gitstore-system").SelectRelease(&forward)
		},
		func() error {
			return s.session.Query("SELECT repository_id,namespace,name FROM namespace_mappings_by_repository WHERE repository_id IN ?", nil).
				WithContext(ctx).Bind(repositoryIDs).SelectRelease(&reverse)
		},
	}
	readErrors := make([]error, len(reads))
	var workers sync.WaitGroup
	for i, read := range reads {
		workers.Go(func() {
			readErrors[i] = read()
			if readErrors[i] != nil {
				cancel()
			}
		})
	}
	workers.Wait()
	if err := errors.Join(append(readErrors, ctx.Err())...); err != nil {
		return nil, err
	}
	namespaceByUID := make(map[string]*datastore.Namespace, len(namespaces))
	for _, row := range namespaces {
		if row.WatchCommitted == nil || *row.WatchCommitted {
			namespaceByUID[row.UID.String()] = fromNamespaceRow(&row)
		}
	}
	repositoryByUID := make(map[string]*datastore.Repository, len(repositories))
	for _, row := range repositories {
		repositoryByUID[row.UID.String()] = fromRepositoryRow(&row)
	}
	mappingByNamespace := make(map[string]*datastore.NamespaceMapping, len(forward))
	for _, row := range forward {
		mappingByNamespace[row.Namespace] = fromNamespaceMappingRow(&row)
	}
	reverseByRepository := make(map[string]*datastore.NamespaceMapping, len(reverse))
	for _, row := range reverse {
		reverseByRepository[row.RepositoryID.String()] = fromNamespaceMappingRow(&row)
	}
	facts := make([]datastore.RBACOriginFacts, len(lookups))
	for i, lookup := range lookups {
		namespace := namespaceByUID[lookup.NamespaceUID]
		repository := repositoryByUID[lookup.RepositoryID]
		mapping := mappingByNamespace[lookup.Namespace]
		if namespace == nil || repository == nil || mapping == nil || mapping.RepositoryID != lookup.RepositoryID {
			continue
		}
		path := repositoryPath{namespace: lookup.Namespace, name: "gitstore-system"}
		if repository.Namespace != path.namespace || repository.Name != path.name {
			_ = s.repositoryMappingRepairError("lookup_repository", lookup.RepositoryID, "namespace_mappings",
				path, datastore.FindingStale, fmt.Errorf("authoritative repository path is %s/%s", repository.Namespace, repository.Name))
			continue
		}
		reverse := reverseByRepository[lookup.RepositoryID]
		if reverse == nil {
			_ = s.repositoryMappingRepairError("lookup_repository", lookup.RepositoryID, "namespace_mappings_by_repository",
				path, datastore.FindingMissing, datastore.ErrNotFound)
			continue
		}
		if reverse.Namespace != path.namespace || reverse.Name != path.name {
			_ = s.repositoryMappingRepairError("lookup_repository", lookup.RepositoryID, "namespace_mappings_by_repository",
				path, datastore.FindingStale, fmt.Errorf("reverse mapping points to %s/%s", reverse.Namespace, reverse.Name))
			continue
		}
		facts[i] = datastore.RBACOriginFacts{Namespace: namespace, Mapping: mapping, Repository: repository}
	}
	return facts, nil
}

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
	return decodeRBACTier(state, projection, repair)
}

func decodeRBACTier(state rbac.TierState, projection, repair []byte) (rbac.TierState, []rbacRepairStep, error) {
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

func (s *scyllaDatastore) CaptureRBACSnapshot(ctx context.Context, tier string, maxBytes int64) (rbac.TierState, [][]byte, error) {
	if !rbac.ValidTier(tier) || maxBytes < 1 || maxBytes > rbac.MaxSnapshotBytes {
		return rbac.TierState{}, nil, datastore.RBACError(rbac.ErrInvalid)
	}
	// Full-size captures are bounded by the tier's stored byte/count limits.
	// Keep them unpaged so server byte thresholds cannot split the SERIAL read.
	// Smaller caller budgets retain paging and its before/after revision fence.
	limit := rbac.MaxRolesPerTier + rbac.MaxBindingsPerTier
	pageSize := limit + 2
	if maxBytes == rbac.MaxSnapshotBytes {
		pageSize = 0
	}
	iter := s.session.Session.Query(
		"SELECT entry,payload,revision,role_count,binding_count,payload_bytes,projection,repair,namespace_retired FROM rbac_resources_by_scope WHERE tier=? LIMIT ?", tier, limit+2,
	).WithContext(ctx).Consistency(gocql.Serial).PageSize(pageSize).PageState(nil).Iter()
	state := rbac.TierState{TierID: tier}
	var payloads [][]byte
	var size int64
	header := false
	for {
		var entry string
		var payload, projection, repair []byte
		row := rbac.TierState{TierID: tier}
		if !iter.Scan(&entry, &payload, &row.Revision, &row.RoleCount, &row.BindingCount, &row.PayloadBytes, &projection, &repair, &row.NamespaceRetired) {
			break
		}
		if entry == "" {
			var err error
			state, _, err = decodeRBACTier(row, projection, repair)
			if err != nil {
				_ = iter.Close()
				return state, nil, err
			}
			if header || state.RoleCount < 0 || state.RoleCount > rbac.MaxRolesPerTier ||
				state.BindingCount < 0 || state.BindingCount > rbac.MaxBindingsPerTier ||
				state.PayloadBytes < 0 || state.PayloadBytes > maxBytes {
				_ = iter.Close()
				return state, nil, datastore.RBACError(rbac.ErrUnavailable)
			}
			header = true
			continue
		}
		size += int64(len(payload))
		if !header || len(payloads) == limit || size > maxBytes {
			_ = iter.Close()
			return state, nil, datastore.RBACError(rbac.ErrUnavailable)
		}
		payloads = append(payloads, payload)
	}
	paged := len(iter.PageState()) > 0
	if err := iter.Close(); err != nil {
		return state, nil, err
	}
	if paged {
		return s.capturePagedRBACSnapshot(ctx, tier, maxBytes)
	}
	return state, payloads, nil
}

func (s *scyllaDatastore) capturePagedRBACSnapshot(ctx context.Context, tier string, maxBytes int64) (rbac.TierState, [][]byte, error) {
	before, err := s.ReadRBACTier(ctx, tier)
	if err != nil {
		return before, nil, err
	}
	if before.RoleCount < 0 || before.RoleCount > rbac.MaxRolesPerTier ||
		before.BindingCount < 0 || before.BindingCount > rbac.MaxBindingsPerTier ||
		before.PayloadBytes < 0 || before.PayloadBytes > maxBytes {
		return before, nil, datastore.RBACError(rbac.ErrUnavailable)
	}
	limit := rbac.MaxRolesPerTier + rbac.MaxBindingsPerTier
	iter := s.session.Session.Query(
		"SELECT payload FROM rbac_resources_by_scope WHERE tier=? AND entry>? LIMIT ?", tier, "", limit+1,
	).WithContext(ctx).Consistency(gocql.Quorum).Iter()
	var payloads [][]byte
	var payload []byte
	var size int64
	for iter.Scan(&payload) {
		size += int64(len(payload))
		if len(payloads) == limit || size > maxBytes {
			_ = iter.Close()
			return before, nil, datastore.RBACError(rbac.ErrUnavailable)
		}
		payloads = append(payloads, payload)
		payload = nil
	}
	if err := iter.Close(); err != nil {
		return before, nil, err
	}
	after, err := s.ReadRBACTier(ctx, tier)
	if err != nil {
		return before, nil, err
	}
	if before != after {
		return before, nil, datastore.RBACError(rbac.ErrConflict)
	}
	return before, payloads, nil
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

const rbacWriteReadBatchSize = 32

func (s *scyllaDatastore) readRBACResources(ctx context.Context, keys []rbac.ResourceKey) (map[rbac.ResourceKey]*rbac.Resource, error) {
	if len(keys) < 1 || len(keys) > rbacWriteReadBatchSize {
		return nil, datastore.RBACError(rbac.ErrInvalid)
	}
	tier := keys[0].TierID
	entries := make([]string, len(keys))
	requested := make(map[string]rbac.ResourceKey, len(keys))
	for index, key := range keys {
		if err := rbac.ValidateKey(key); err != nil || key.TierID != tier {
			return nil, datastore.RBACError(rbac.ErrInvalid)
		}
		entries[index] = rbacEntry(key)
		requested[entries[index]] = key
	}
	iter := s.session.Session.Query("SELECT entry,payload FROM rbac_resources_by_scope WHERE tier=? AND entry IN ?", tier, entries).
		WithContext(ctx).Consistency(gocql.Quorum).PageSize(0).Iter()
	resources := make(map[rbac.ResourceKey]*rbac.Resource, len(keys))
	var entry string
	var data []byte
	for iter.Scan(&entry, &data) {
		key, ok := requested[entry]
		resource, err := decodeRBACResource(tier, data)
		if !ok || err != nil || resources[key] != nil ||
			resource.Kind != key.Kind || resource.Metadata.Name != key.Name {
			_ = iter.Close()
			return nil, datastore.RBACError(rbac.ErrUnavailable)
		}
		resources[key] = resource
		data = nil
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return resources, nil
}

func readRBACWriteResources(ctx context.Context, write rbac.TierWrite, read func(context.Context, []rbac.ResourceKey) (map[rbac.ResourceKey]*rbac.Resource, error)) (map[rbac.ResourceKey]*rbac.Resource, error) {
	keys := make([]rbac.ResourceKey, len(write.Changes))
	current := make(map[rbac.ResourceKey]*rbac.Resource, len(keys))
	for index, change := range write.Changes {
		if change.Resource == nil {
			return nil, datastore.RBACError(rbac.ErrInvalid)
		}
		key := rbac.ResourceKey{TierID: write.TierID, Kind: change.Resource.Kind, Name: change.Resource.Metadata.Name}
		if err := rbac.ValidateKey(key); err != nil {
			return nil, datastore.RBACError(err)
		}
		if _, exists := current[key]; exists {
			return nil, datastore.RBACError(rbac.ErrInvalid)
		}
		keys[index], current[key] = key, nil
	}
	batches := slices.Collect(slices.Chunk(keys, rbacWriteReadBatchSize))
	var mu sync.Mutex
	err := runRBACSteps(ctx, batches, func(ctx context.Context, batch []rbac.ResourceKey) error {
		resources, err := read(ctx, batch)
		if err != nil {
			return err
		}
		for key, resource := range resources {
			if !slices.Contains(batch, key) || resource == nil ||
				resource.Kind != key.Kind || resource.Metadata.Name != key.Name {
				return datastore.RBACError(rbac.ErrUnavailable)
			}
		}
		mu.Lock()
		for key, resource := range resources {
			current[key] = resource
		}
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return current, nil
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
	current, err := readRBACWriteResources(ctx, write, s.readRBACResources)
	if err != nil {
		return state, err
	}
	plan, err := rbac.PlanTierWrite(state, current, write, time.Now())
	if err != nil {
		return state, datastore.RBACError(err)
	}
	if plan.Replay {
		if state.ProjectionPending {
			if err := s.RepairRBACProjection(ctx, state.TierID, state.Revision); err != nil {
				return state, err
			}
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
		// Completed prior repair already established unchanged lookup links.
		// Status/finalizer updates must not create another index-repair gap.
		if change.Before == nil || change.After == nil ||
			change.Before.Metadata.UID != change.After.Metadata.UID ||
			step.BeforeRoleUID != step.AfterRoleUID {
			steps = append(steps, step)
		}
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
	if len(steps) > 0 {
		if err := s.RepairRBACProjection(ctx, write.TierID, plan.State.Revision); err != nil {
			plan.State.ProjectionPending = true
			return plan.State, fmt.Errorf("scylla: RBAC committed with pending index repair: %w", err)
		}
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
