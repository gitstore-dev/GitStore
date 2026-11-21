// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"errors"
	"strings"
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
	if update.LastAppliedRevision != "" {
		resource.Status.LastAppliedRevision = update.LastAppliedRevision
	}
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

func TestReconcilerAcknowledgesGenerationAndRevisionWithoutRepeatedWrites(t *testing.T) {
	for _, kind := range Kinds {
		t.Run(kind, func(t *testing.T) {
			role := Resource{Kind: kind, Metadata: Metadata{
				UID: "role-uid", Name: "reader", Generation: 2, ResourceVersion: "2",
				Revision:   "main@sha1:1111111111111111111111111111111111111111",
				Finalizers: []string{LifecycleFinalizer},
			}}
			if namespaced(kind) {
				role.Metadata.Namespace = "shop"
			}
			if strings.HasSuffix(kind, "Binding") {
				role.Spec.RoleRef = &RoleRef{Kind: "ClusterRole", Name: "missing"}
			}
			role.Status.ObservedGeneration = 1
			fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{role.Key(): role}}
			for range 2 {
				reconciler := NewReconciler(fixture.lookup, fixture)
				require.IsType(t, types.Success{}, reconciler.Reconcile(t.Context(), role.Key()))
			}
			require.Len(t, fixture.writes, 1)
			current := fixture.resources[role.Key()]
			require.EqualValues(t, 2, current.Status.ObservedGeneration)
			require.EqualValues(t, 2, current.Status.Conditions[0].ObservedGeneration)
			require.Equal(t, role.Metadata.Revision, current.Status.LastAppliedRevision)
			current.Metadata.Revision = "main@sha1:2222222222222222222222222222222222222222"
			fixture.resources[role.Key()] = current
			require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).Reconcile(t.Context(), role.Key()))
			require.Len(t, fixture.writes, 2, "same-generation authored revisions still require acknowledgement")
			require.Equal(t, current.Metadata.Revision, fixture.resources[role.Key()].Status.LastAppliedRevision)
			require.Empty(t, fixture.completed)
		})
	}
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
			case types.RequeueAfter:
				require.Equal(t, conflictRequeueDelay, result.After)
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
	require.IsType(t, types.RequeueAfter{}, result)
	require.Equal(t, conflictRequeueDelay, result.(types.RequeueAfter).After)
	require.Empty(t, fixture.completed)
	require.Equal(t, "new-binding-uid", fixture.resources[binding.Key()].Metadata.UID)
}

func TestReconcilerDoesNotTreatLookupErrorsAsAbsence(t *testing.T) {
	fixture := &lifecycleFixture{err: errors.New("checkpoint unavailable")}
	NewReconciler(fixture.lookup, fixture).Reconcile(t.Context(), types.WorkItemKey{Kind: "Role", Namespace: "shop", Name: "reader"})
	require.Empty(t, fixture.writes)
	require.Empty(t, fixture.completed)
}

func TestReconcilerChecksAuthorityWhenRoleWatchLags(t *testing.T) {
	for _, kind := range []string{"Role", "ClusterRole"} {
		t.Run(kind, func(t *testing.T) {
			namespace := ""
			if kind == "Role" {
				namespace = "shop"
			}
			role := Resource{Kind: kind, Metadata: Metadata{UID: "role-uid", Name: "reader", Namespace: namespace}}
			binding := Resource{Kind: kind + "Binding", Metadata: Metadata{UID: "binding-uid", Name: "access",
				Namespace: namespace, Generation: 1, ResourceVersion: "1"}, Spec: Spec{RoleRef: &RoleRef{Kind: kind, Name: "reader"}}}
			fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{binding.Key(): binding}}
			reads := 0
			authority := func(_ context.Context, key types.WorkItemKey) (Resource, bool, error) {
				reads++
				require.Equal(t, role.Key(), key)
				return role, true, nil
			}
			for range 2 {
				require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).
					WithRoleLookup(authority).Reconcile(t.Context(), binding.Key()))
			}
			require.Len(t, fixture.writes, 1, "independent controllers converge before the Role watch catches up")
			require.Equal(t, role.Metadata.UID, fixture.writes[0].Resolved.RoleUID)
			require.Equal(t, "TRUE", fixture.writes[0].Conditions[0].Status)
			require.Equal(t, 2, reads)
			role.Metadata.UID = "replacement-role"
			require.IsType(t, types.Success{}, NewReconciler(fixture.lookup, fixture).
				WithRoleLookup(authority).Reconcile(t.Context(), binding.Key()))
			current := fixture.resources[binding.Key()]
			require.Equal(t, "role-uid", current.Status.Resolved.RoleUID)
			require.Equal(t, "RoleUIDChanged", current.Status.Conditions[0].Reason)
		})
	}
}

func TestReconcilerRoleAuthorityPreservesAbsenceAndFailures(t *testing.T) {
	for _, outcome := range []string{"absent", "unavailable", "terminating", "wrong-identity", "cached", "checkpoint-error"} {
		t.Run(outcome, func(t *testing.T) {
			binding := Resource{Kind: "ClusterRoleBinding", Metadata: Metadata{UID: "binding-uid", Name: "access",
				Generation: 1, ResourceVersion: "1"}, Spec: Spec{RoleRef: &RoleRef{Kind: "ClusterRole", Name: "reader"}}}
			role := Resource{Kind: "ClusterRole", Metadata: Metadata{UID: "role-uid", Name: "reader"}}
			fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{binding.Key(): binding}}
			if outcome == "cached" {
				fixture.resources[role.Key()] = role
			}
			lookup := func(ctx context.Context, key types.WorkItemKey) (Resource, bool, error) {
				if key == role.Key() && outcome == "checkpoint-error" {
					return Resource{}, false, errors.New("checkpoint unavailable")
				}
				return fixture.lookup(ctx, key)
			}
			reads := 0
			authority := func(context.Context, types.WorkItemKey) (Resource, bool, error) {
				reads++
				switch outcome {
				case "absent":
					return Resource{}, false, nil
				case "unavailable":
					return Resource{}, false, errors.New("authority unavailable")
				case "terminating":
					now := time.Now()
					role.Metadata.DeletionTimestamp = &now
				case "wrong-identity":
					role.Metadata.Name = "other"
				default:
					t.Fatal("authority must not replace a checkpoint hit or hide a checkpoint error")
				}
				return role, true, nil
			}
			result := NewReconciler(lookup, fixture).WithRoleLookup(authority).Reconcile(t.Context(), binding.Key())
			switch outcome {
			case "unavailable", "wrong-identity", "checkpoint-error":
				require.IsType(t, types.TransientFailure{}, result)
				require.Empty(t, fixture.writes)
			default:
				require.IsType(t, types.Success{}, result)
				require.Len(t, fixture.writes, 1)
				switch outcome {
				case "absent":
					require.Equal(t, "RoleNotFound", fixture.writes[0].Conditions[0].Reason)
				case "terminating":
					require.Equal(t, "RoleTerminating", fixture.writes[0].Conditions[0].Reason)
				case "cached":
					require.Equal(t, "role-uid", fixture.writes[0].Resolved.RoleUID)
				}
			}
			expectedReads := 1
			if outcome == "cached" || outcome == "checkpoint-error" {
				expectedReads = 0
			}
			require.Equal(t, expectedReads, reads)
		})
	}
}

func TestReconcilerDefersConcurrentMutationConflictsWithoutRetryExhaustion(t *testing.T) {
	for _, stage := range []string{"status", "finalizer", "completion"} {
		t.Run(stage, func(t *testing.T) {
			resource := Resource{Kind: "Role", Metadata: Metadata{
				UID: "role-uid", Name: "reader", Namespace: "shop", Generation: 1, ResourceVersion: "1",
			}}
			if stage != "status" {
				now := time.Now()
				resource.Metadata.DeletionTimestamp = &now
			}
			if stage == "finalizer" {
				resource.Metadata.Finalizers = []string{LifecycleFinalizer}
			}
			fixture := &lifecycleFixture{resources: map[types.WorkItemKey]Resource{resource.Key(): resource},
				err: errors.Join(errors.New("another replica won"), types.ErrConflict)}
			lookup := func(context.Context, types.WorkItemKey) (Resource, bool, error) {
				return resource, true, nil
			}
			reconciler := NewReconciler(lookup, fixture)
			for range 8 {
				result := reconciler.Reconcile(t.Context(), resource.Key())
				require.IsType(t, types.RequeueAfter{}, result)
				require.Equal(t, conflictRequeueDelay, result.(types.RequeueAfter).After)
			}
			require.Empty(t, fixture.writes)
			require.Empty(t, fixture.completed)
			fixture.err = nil
			require.IsType(t, types.Success{}, reconciler.Reconcile(t.Context(), resource.Key()))
			if stage == "completion" {
				require.Equal(t, []string{resource.Metadata.UID}, fixture.completed)
			} else {
				require.Len(t, fixture.writes, 1)
			}
		})
	}
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
