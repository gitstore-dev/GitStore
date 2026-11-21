// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNamespaceCleanupRetiresOnlyTheDeletedUIDAndReleasesClaims(t *testing.T) {
	store, bindingKey, role, binding := referenceFixture(t)
	_, err := (rbac.References{Store: store}).Resolve(t.Context(), bindingKey, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	job := rbac.NamespaceCleanup{NamespaceUID: bindingKey.TierID, Namespace: "shop"}
	require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
	cleanup := rbac.NamespaceCleaner{Store: store, Lookup: func(_ context.Context, uid string) (rbac.CleanupNamespace, error) {
		require.Equal(t, job.NamespaceUID, uid)
		return rbac.CleanupNamespace{UID: uid, Name: "shop", Terminating: false}, nil
	}}
	_, err = cleanup.Reconcile(t.Context(), job)
	require.ErrorIs(t, err, rbac.ErrConflict, "a live parent cannot authorize collection")
	cleanup.Lookup = func(context.Context, string) (rbac.CleanupNamespace, error) {
		return rbac.CleanupNamespace{}, errors.New("namespace storage unavailable")
	}
	_, err = cleanup.Reconcile(t.Context(), job)
	require.Error(t, err, "uncertain parent absence must block cleanup")
	cleanup.Lookup = func(context.Context, string) (rbac.CleanupNamespace, error) {
		return rbac.CleanupNamespace{}, rbac.ErrNotFound
	}
	done := false
	for range 5 {
		done, err = cleanup.Reconcile(t.Context(), job)
		require.NoError(t, err)
		if done {
			break
		}
	}
	require.True(t, done)
	guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	require.EqualValues(t, 0, guard.Claims)
	require.Equal(t, rbac.GuardFinalized, guard.State)
	state, err := store.ReadRBACTier(t.Context(), job.NamespaceUID)
	require.NoError(t, err)
	require.True(t, state.NamespaceRetired)
	require.Zero(t, state.RoleCount+state.BindingCount)
	identity := state.Projection
	identity.GitRevision += "-late"
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: job.NamespaceUID, ExpectedRevision: state.Revision,
		ExpectedGitRevision: state.Projection.GitRevision, Projection: &identity})
	require.ErrorIs(t, err, rbac.ErrConflict, "late projection cannot resurrect a collected namespace tier")
	identity = state.Projection
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: job.NamespaceUID, ExpectedRevision: state.Revision,
		Projection: &identity})
	require.ErrorIs(t, err, rbac.ErrConflict, "even an identical historical receipt cannot reopen the tier")
}

type cleanupLostReply struct {
	rbac.Store
	apply, complete bool
}

func (s *cleanupLostReply) ApplyRBAC(ctx context.Context, write rbac.TierWrite) (rbac.TierState, error) {
	state, err := s.Store.ApplyRBAC(ctx, write)
	if err == nil && s.apply {
		s.apply = false
		return rbac.TierState{}, rbac.ErrUnavailable
	}
	return state, err
}

func (s *cleanupLostReply) CompleteRBACNamespaceCleanup(ctx context.Context, job rbac.NamespaceCleanup) error {
	err := s.Store.CompleteRBACNamespaceCleanup(ctx, job)
	if err == nil && s.complete {
		s.complete = false
		return rbac.ErrUnavailable
	}
	return err
}

func TestNamespaceCleanupRecoversLostRepliesAndConcurrentWorkers(t *testing.T) {
	store, key, role, binding := referenceFixture(t)
	_, err := (rbac.References{Store: store}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	job := rbac.NamespaceCleanup{NamespaceUID: key.TierID, Namespace: "shop"}
	require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
	lost := &cleanupLostReply{Store: store, apply: true, complete: true}
	cleaner := rbac.NamespaceCleaner{Store: lost, Lookup: func(context.Context, string) (rbac.CleanupNamespace, error) {
		return rbac.CleanupNamespace{}, rbac.ErrNotFound
	}}
	_, err = cleaner.Reconcile(t.Context(), job)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	cleaner.Store = store
	var workers sync.WaitGroup
	failures := make(chan error, 2)
	for range 2 {
		workers.Go(func() {
			for range 10 {
				done, err := cleaner.Reconcile(t.Context(), job)
				if err != nil && !errors.Is(err, rbac.ErrConflict) && !errors.Is(err, rbac.ErrNotFound) {
					failures <- err
					return
				}
				if done {
					return
				}
			}
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	cleaner.Store = lost
	_, err = cleaner.Reconcile(t.Context(), job)
	require.ErrorIs(t, err, rbac.ErrUnavailable, "completed job with lost reply is recoverable")
	done, err := cleaner.Reconcile(t.Context(), job)
	require.NoError(t, err)
	require.True(t, done)
	guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	require.Equal(t, rbac.GuardFinalized, guard.State)
	require.Zero(t, guard.Claims)
}

func TestNamespaceCleanupBoundsPagesPreservesForeignFinalizersAndReplacement(t *testing.T) {
	store, key, _, binding := referenceFixture(t)
	state, err := store.ReadRBACTier(t.Context(), key.TierID)
	require.NoError(t, err)
	projection := state.Projection
	projection.GitRevision += "-bindings"
	var changes []rbac.Change
	for index := range 101 {
		child, err := rbac.CloneResource(binding)
		require.NoError(t, err)
		child.Metadata.Name = fmt.Sprintf("binding-%03d", index)
		child.Metadata.UID = uuid.NewString()
		if index == 0 {
			child.Metadata.Finalizers = []string{rbac.LifecycleFinalizer, "external.example/retain"}
		}
		changes = append(changes, rbac.Change{Resource: child})
	}
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
		ExpectedGitRevision: state.Projection.GitRevision, Projection: &projection, Changes: changes})
	require.NoError(t, err)
	replacement := uuid.NewString()
	child, err := rbac.CloneResource(binding)
	require.NoError(t, err)
	child.NamespaceUID, child.Metadata.UID = replacement, uuid.NewString()
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: replacement, Projection: &projection,
		Changes: []rbac.Change{{Resource: child}}})
	require.NoError(t, err)
	job := rbac.NamespaceCleanup{NamespaceUID: key.TierID, Namespace: "shop"}
	require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
	cleaner := rbac.NamespaceCleaner{Store: store, Lookup: func(context.Context, string) (rbac.CleanupNamespace, error) {
		return rbac.CleanupNamespace{}, rbac.ErrNotFound
	}}
	done, err := cleaner.Reconcile(t.Context(), job)
	require.NoError(t, err)
	require.False(t, done)
	state, err = store.ReadRBACTier(t.Context(), key.TierID)
	require.NoError(t, err)
	require.EqualValues(t, 3, state.BindingCount, "at most 100 children visited; retained finalizer consumes one slot")
	for range 2 {
		done, err = cleaner.Reconcile(t.Context(), job)
		require.NoError(t, err)
		require.False(t, done)
	}
	retainedKey := rbac.ResourceKey{TierID: key.TierID, Kind: rbac.RoleBinding, Name: "binding-000"}
	retained, err := store.GetRBAC(t.Context(), retainedKey)
	require.NoError(t, err)
	require.Equal(t, []string{"external.example/retain"}, retained.Metadata.Finalizers)
	require.NotNil(t, retained.Metadata.DeletionTimestamp)
	recreated, err := store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: replacement, Kind: binding.Kind, Name: binding.Metadata.Name})
	require.NoError(t, err)
	require.Nil(t, recreated.Metadata.DeletionTimestamp)
	require.Equal(t, child.Metadata.UID, recreated.Metadata.UID)
}

func TestNamespaceCleanupHandoffIsImmutableIdempotentAndPaged(t *testing.T) {
	store, _, _, _ := referenceFixture(t)
	var jobs []rbac.NamespaceCleanup
	for range 3 {
		job := rbac.NamespaceCleanup{NamespaceUID: uuid.NewString(), Namespace: "shop"}
		require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
		require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
		conflicting := job
		conflicting.Namespace = "other"
		require.ErrorIs(t, store.ScheduleRBACNamespaceCleanup(t.Context(), conflicting), rbac.ErrConflict)
		jobs = append(jobs, job)
	}
	seen := map[string]bool{}
	after := ""
	for {
		page, err := store.ListRBACNamespaceCleanups(t.Context(), after, 1)
		require.NoError(t, err)
		require.Len(t, page.Jobs, 1)
		require.False(t, seen[page.Jobs[0].NamespaceUID])
		seen[page.Jobs[0].NamespaceUID] = true
		require.NoError(t, store.CompleteRBACNamespaceCleanup(t.Context(), page.Jobs[0]))
		if page.Next == "" {
			break
		}
		require.NotEqual(t, after, page.Next)
		after = page.Next
	}
	require.Len(t, seen, len(jobs))
	for _, job := range jobs {
		require.NoError(t, store.CompleteRBACNamespaceCleanup(t.Context(), job))
	}
	page, err := store.ListRBACNamespaceCleanups(t.Context(), "", 100)
	require.NoError(t, err)
	require.Empty(t, page.Jobs)
}
