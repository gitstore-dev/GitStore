// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

type lifecycleFixture struct {
	mu        sync.Mutex
	resources map[types.WorkItemKey]Resource
	writes    []StatusUpdate
	completed []string
	err       error
}

type lostLifecycleReply struct {
	*lifecycleFixture
}

func (f lostLifecycleReply) UpdateStatus(ctx context.Context, resource Resource, update StatusUpdate) error {
	if err := f.lifecycleFixture.UpdateStatus(ctx, resource, update); err != nil {
		return err
	}
	return errors.New("committed status reply lost")
}

func (f lostLifecycleReply) CompleteDeletion(ctx context.Context, resource Resource) error {
	if err := f.lifecycleFixture.CompleteDeletion(ctx, resource); err != nil {
		return err
	}
	return errors.New("committed deletion reply lost")
}

func (f *lifecycleFixture) lookup(_ context.Context, key types.WorkItemKey) (Resource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resource, ok := f.resources[key]
	return resource, ok, f.err
}

func (f *lifecycleFixture) UpdateStatus(_ context.Context, resource Resource, update StatusUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	current, found := f.resources[resource.Key()]
	if !found || current.Metadata.UID != resource.Metadata.UID || current.Metadata.ResourceVersion != resource.Metadata.ResourceVersion {
		return types.ErrConflict
	}
	f.writes = append(f.writes, update)
	resource.Metadata.ResourceVersion += "-next"
	resource.Status.ObservedGeneration = resource.Metadata.Generation
	resource.Status.Resolved = update.Resolved
	resource.Status.Conditions = update.Conditions
	if update.Finalizers != nil {
		resource.Metadata.Finalizers = update.Finalizers
	}
	f.resources[resource.Key()] = resource
	return nil
}

func (f *lifecycleFixture) CompleteDeletion(_ context.Context, resource Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	current, found := f.resources[resource.Key()]
	if found && (current.Metadata.UID != resource.Metadata.UID || current.Metadata.ResourceVersion != resource.Metadata.ResourceVersion) {
		return types.ErrConflict
	}
	f.completed = append(f.completed, resource.Metadata.UID)
	delete(f.resources, resource.Key())
	return nil
}

func TestReconcilerResolvesOnceAndNeverReattachesRecreatedRole(t *testing.T) {
	binding := Resource{Kind: "RoleBinding", Metadata: Metadata{UID: "binding-uid", Name: "access", Namespace: "shop", Generation: 1, ResourceVersion: "1",
		Finalizers: []string{LifecycleFinalizer}}, Spec: Spec{RoleRef: &RoleRef{Kind: "Role", Name: "reader"}}}
	role := Resource{Kind: "Role", Metadata: Metadata{UID: "first-role", Name: "reader", Namespace: "shop", Generation: 1}}
	fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{binding.Key(): binding, role.Key(): role}}
	first := NewReconciler(fixture.lookup, fixture)
	second := NewReconciler(fixture.lookup, fixture)
	first.Reconcile(t.Context(), binding.Key())
	second.Reconcile(t.Context(), binding.Key())
	require.Len(t, fixture.writes, 1, "duplicate controllers compute the same status")
	require.Equal(t, "first-role", fixture.writes[0].Resolved.RoleUID)
	role.Metadata.UID = "recreated-role"
	fixture.resources[role.Key()] = role
	second.Reconcile(t.Context(), binding.Key())
	require.Equal(t, "first-role", fixture.resources[binding.Key()].Status.Resolved.RoleUID, "a same-name role must not acquire the old grant")
	require.Equal(t, "FALSE", fixture.resources[binding.Key()].Status.Conditions[0].Status)
}

func TestReconcilerWaitsForResolutionAndClearsOnlyItsFinalizer(t *testing.T) {
	binding := Resource{Kind: "RoleBinding", Metadata: Metadata{UID: "binding-uid", Name: "access", Namespace: "shop", Generation: 1,
		ResourceVersion: "1", Finalizers: []string{LifecycleFinalizer, "other/finalizer"}},
		Spec: Spec{RoleRef: &RoleRef{Kind: "Role", Name: "missing"}}}
	fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{binding.Key(): binding}}
	reconciler := NewReconciler(fixture.lookup, fixture)
	reconciler.Reconcile(t.Context(), binding.Key())
	require.Nil(t, fixture.resources[binding.Key()].Status.Resolved)
	require.Equal(t, "RoleNotFound", fixture.writes[0].Conditions[0].Reason)
	binding = fixture.resources[binding.Key()]
	now := time.Now()
	binding.Metadata.DeletionTimestamp = &now
	fixture.resources[binding.Key()] = binding
	reconciler.Reconcile(t.Context(), binding.Key())
	require.Equal(t, []string{"other/finalizer"}, fixture.resources[binding.Key()].Metadata.Finalizers)
	require.Empty(t, fixture.completed)
	binding = fixture.resources[binding.Key()]
	binding.Metadata.Finalizers = []string{}
	fixture.resources[binding.Key()] = binding
	reconciler.Reconcile(t.Context(), binding.Key())
	require.Equal(t, []string{"binding-uid"}, fixture.completed)
}

func TestReconcilerConcurrentReplicasRetryCASAndOldUIDWorkCannotPublish(t *testing.T) {
	binding := Resource{Kind: "RoleBinding", Metadata: Metadata{UID: "binding-uid", Name: "access", Namespace: "shop",
		ResourceVersion: "uid-bound-version", Generation: 1}, Spec: Spec{RoleRef: &RoleRef{Kind: "Role", Name: "reader"}}}
	role := Resource{Kind: "Role", Metadata: Metadata{UID: "role-uid", Name: "reader", Namespace: "shop"}}
	fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{binding.Key(): binding, role.Key(): role}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ready, release := make(chan struct{}, 2), make(chan struct{})
	lookup := func(ctx context.Context, key types.WorkItemKey) (Resource, bool, error) {
		value, found, err := fixture.lookup(ctx, key)
		if key == binding.Key() {
			ready <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return Resource{}, false, ctx.Err()
			}
		}
		return value, found, err
	}
	results := make(chan types.ReconcileResult, 2)
	for range 2 {
		go func() { results <- NewReconciler(lookup, fixture).Reconcile(ctx, binding.Key()) }()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("replicas did not capture the same old version")
		}
	}
	close(release)
	successes, conflicts := 0, 0
	for range 2 {
		select {
		case result := <-results:
			switch result := result.(type) {
			case types.Success:
				successes++
			case types.TransientFailure:
				require.ErrorIs(t, result.Err, types.ErrConflict)
				conflicts++
			default:
				t.Fatalf("unexpected result %T", result)
			}
		case <-ctx.Done():
			t.Fatal("concurrent reconciliation did not finish")
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).Reconcile(ctx, binding.Key()))
	require.Len(t, fixture.writes, 1, "retry recomputes from the winning observation")
	replacement := binding
	replacement.Metadata.UID = "new-binding-uid"
	fixture.resources[binding.Key()] = replacement
	staleLookup := func(context.Context, types.WorkItemKey) (Resource, bool, error) { return binding, true, nil }
	now := time.Now()
	binding.Metadata.DeletionTimestamp = &now
	result := NewReconciler(staleLookup, fixture).Reconcile(ctx, binding.Key())
	require.IsType(t, types.TransientFailure{}, result)
	require.ErrorIs(t, result.(types.TransientFailure).Err, types.ErrConflict)
	require.Empty(t, fixture.completed)
	require.Equal(t, "new-binding-uid", fixture.resources[binding.Key()].Metadata.UID)
}

func TestReconcilerDoesNotTreatLookupErrorsAsAbsence(t *testing.T) {
	fixture := &lifecycleFixture{err: errors.New("checkpoint unavailable")}
	NewReconciler(fixture.lookup, fixture).Reconcile(t.Context(), types.WorkItemKey{Kind: "Role", Namespace: "shop", Name: "reader"})
	require.Empty(t, fixture.writes)
	require.Empty(t, fixture.completed)
}

func TestReconcilerRecoversUnresolvedAndLostLifecycleRepliesAcrossReplacement(t *testing.T) {
	binding := Resource{Kind: "RoleBinding", Metadata: Metadata{UID: "binding-uid", Name: "access", Namespace: "shop",
		ResourceVersion: "1", Generation: 1, Finalizers: []string{LifecycleFinalizer}},
		Spec: Spec{RoleRef: &RoleRef{Kind: "Role", Name: "reader"}}}
	fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{binding.Key(): binding}}
	require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).Reconcile(t.Context(), binding.Key()))
	require.Equal(t, "RoleNotFound", fixture.resources[binding.Key()].Status.Conditions[0].Reason)
	role := Resource{Kind: "Role", Metadata: Metadata{UID: "role-uid", Name: "reader", Namespace: "shop"}}
	fixture.resources[role.Key()] = role
	require.IsType(t, types.TransientFailure{}, NewReconciler(fixture.lookup, lostLifecycleReply{fixture}).Reconcile(t.Context(), binding.Key()))
	require.Equal(t, role.Metadata.UID, fixture.resources[binding.Key()].Status.Resolved.RoleUID)
	writes := len(fixture.writes)
	require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).Reconcile(t.Context(), binding.Key()))
	require.Len(t, fixture.writes, writes, "replacement observes committed resolution instead of resubmitting it")

	now := time.Now()
	role.Metadata.DeletionTimestamp = &now
	fixture.resources[role.Key()] = role
	require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).Reconcile(t.Context(), binding.Key()))
	current := fixture.resources[binding.Key()]
	require.Equal(t, "RoleTerminating", current.Status.Conditions[0].Reason)
	require.Equal(t, role.Metadata.UID, current.Status.Resolved.RoleUID)
	current.Metadata.DeletionTimestamp = &now
	fixture.resources[binding.Key()] = current
	require.IsType(t, types.TransientFailure{}, NewReconciler(fixture.lookup, lostLifecycleReply{fixture}).Reconcile(t.Context(), binding.Key()))
	require.Empty(t, fixture.resources[binding.Key()].Metadata.Finalizers)
	require.Empty(t, fixture.completed, "lost finalizer reply cannot lead to completion with the old version")
	require.IsType(t, types.TransientFailure{}, NewReconciler(fixture.lookup, lostLifecycleReply{fixture}).Reconcile(t.Context(), binding.Key()))
	require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).Reconcile(t.Context(), binding.Key()))
	require.Equal(t, []string{binding.Metadata.UID}, fixture.completed, "completion remains effect-free after response loss")
}
