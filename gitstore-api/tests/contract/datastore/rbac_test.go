// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func rbacStore(t *testing.T, ds datastore.Datastore) rbac.Store {
	t.Helper()
	capable, ok := ds.(datastore.RBACCapable)
	require.True(t, ok, "backend must expose the RBAC store")
	store := capable.RBACStore()
	require.NotNil(t, store)
	return store
}

func rbacRole(tier, name string) *rbac.Resource {
	return &rbac.Resource{
		APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: tier,
		Metadata: rbac.Metadata{Name: name, Namespace: "contract", UID: uuid.NewString()},
		Spec:     json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`),
	}
}

func rbacProjection(sequence int) *rbac.ProjectionIdentity {
	return &rbac.ProjectionIdentity{
		RepositoryID: "11111111-1111-4111-8111-111111111111",
		GitRef:       "refs/heads/main", GitRevision: fmt.Sprintf("main@sha1:%040x", sequence),
		TreeDigest: fmt.Sprintf("%064x", sequence), Actor: "contract-writer",
	}
}

func rbacCreate(tier string, state rbac.TierState, sequence int, resources ...*rbac.Resource) rbac.TierWrite {
	write := rbac.TierWrite{
		TierID: tier, ExpectedRevision: state.Revision,
		ExpectedGitRevision: state.Projection.GitRevision, Projection: rbacProjection(sequence),
	}
	for _, resource := range resources {
		write.Changes = append(write.Changes, rbac.Change{Resource: resource})
	}
	return write
}

type inconsistentRBACPages struct {
	rbac.Store
	calls int
}

func (s *inconsistentRBACPages) ListRBAC(ctx context.Context, request rbac.ListRequest) (rbac.Page, error) {
	s.calls++
	page, err := s.Store.ListRBAC(ctx, request)
	page.Revision++
	return page, err
}

func runRBACStoreContract(t *testing.T, ds datastore.Datastore) {
	store := rbacStore(t, ds)
	t.Run("operation audit uses authenticated subjects and preserves immutable creation facts", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "audited")
		forged := time.Unix(1, 0).UTC()
		role.Metadata.CreationTimestamp, role.Metadata.UpdateTimestamp = &forged, &forged
		role.Metadata.CreationActor, role.Metadata.UpdateActor = "forged-author", "forged-author"
		write := rbacCreate(tier, rbac.TierState{}, 1, role)
		write.Actor = "forged-write-actor"
		ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "authenticated-creator", AuthMethod: "oidc"})
		state, err := store.ApplyRBAC(ctx, write)
		require.NoError(t, err)
		created, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Equal(t, "authenticated-creator", created.Metadata.CreationActor)
		require.Equal(t, "authenticated-creator", created.Metadata.UpdateActor)
		require.NotNil(t, created.Metadata.CreationTimestamp)
		require.NotEqual(t, forged, *created.Metadata.CreationTimestamp)
		require.Equal(t, created.Metadata.CreationTimestamp, created.Metadata.UpdateTimestamp)
		after, err := rbac.CloneResource(created)
		require.NoError(t, err)
		after.Metadata.CreationTimestamp, after.Metadata.CreationActor = &forged, "forged-updater"
		after.Metadata.UpdateTimestamp, after.Metadata.UpdateActor = &forged, "forged-updater"
		after.Status = json.RawMessage(`{"observedGeneration":1}`)
		ctx = auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "system:serviceaccount:controller", AuthMethod: "service-account"})
		update := rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision,
			Changes: []rbac.Change{{Resource: after, ExpectedUID: after.Metadata.UID, ExpectedResourceVersion: after.Metadata.ResourceVersion}}}
		_, err = store.ApplyRBAC(ctx, update)
		require.NoError(t, err)
		updated, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Equal(t, created.Metadata.CreationActor, updated.Metadata.CreationActor)
		require.Equal(t, created.Metadata.CreationTimestamp, updated.Metadata.CreationTimestamp)
		require.Equal(t, "system:serviceaccount:controller", updated.Metadata.UpdateActor)
		require.False(t, updated.Metadata.UpdateTimestamp.Before(*created.Metadata.UpdateTimestamp))
		require.Equal(t, created.Metadata.Generation, updated.Metadata.Generation)
		require.Equal(t, created.Metadata.Revision, updated.Metadata.Revision)
		_, err = store.ApplyRBAC(ctx, write)
		require.NoError(t, err, "projection replay must not restamp audit metadata")
		_, err = store.ApplyRBAC(ctx, update)
		require.ErrorIs(t, err, rbac.ErrConflict)
		retained, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Equal(t, updated, retained, "replay and rejected CAS must preserve audit facts")
	})
	t.Run("unknown legacy creators are not guessed from publication actors", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "legacy-audit")
		state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, role))
		require.NoError(t, err)
		current, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Empty(t, current.Metadata.CreationActor)
		current.Status = json.RawMessage(`{"observedGeneration":1}`)
		ctx := datastore.ContextWithRBACAuditActor(t.Context(), "system:rbac-projection-repair")
		_, err = store.ApplyRBAC(ctx, rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision,
			Changes: []rbac.Change{{Resource: current, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}}})
		require.NoError(t, err)
		current, err = store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Empty(t, current.Metadata.CreationActor)
		require.Equal(t, "system:rbac-projection-repair", current.Metadata.UpdateActor)
	})
	t.Run("namespace cleanup releases resolved claims before child removal", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "cleanup-reader")
		binding := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.RoleBinding, NamespaceUID: tier,
			Metadata: rbac.Metadata{UID: uuid.NewString(), Namespace: "contract", Name: "cleanup-binding",
				Finalizers: []string{rbac.LifecycleFinalizer}},
			Spec: json.RawMessage(`{"roleRef":{"kind":"Role","name":"cleanup-reader"},"subjects":[{"kind":"User","name":"alice"}]}`)}
		_, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, role, binding))
		require.NoError(t, err)
		key := rbac.ResourceKey{TierID: tier, Kind: binding.Kind, Name: binding.Metadata.Name}
		current, err := store.GetRBAC(t.Context(), key)
		require.NoError(t, err)
		_, err = (rbac.References{Store: store}).Resolve(t.Context(), key, current.Metadata.UID, current.Metadata.ResourceVersion)
		require.NoError(t, err)
		job := rbac.NamespaceCleanup{NamespaceUID: tier, Namespace: "contract"}
		require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
		cleaner := rbac.NamespaceCleaner{Store: store, Lookup: func(context.Context, string) (rbac.CleanupNamespace, error) {
			return rbac.CleanupNamespace{}, rbac.ErrNotFound
		}}
		done := false
		for range 5 {
			done, err = cleaner.Reconcile(t.Context(), job)
			require.NoError(t, err)
			if done {
				break
			}
		}
		require.True(t, done)
		state, err := store.ReadRBACTier(t.Context(), tier)
		require.NoError(t, err)
		require.True(t, state.NamespaceRetired)
		require.Zero(t, state.RoleCount+state.BindingCount)
		guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Equal(t, rbac.GuardFinalized, guard.State)
		require.Zero(t, guard.Claims)
	})
	t.Run("namespace cleanup handoff and retired tier survive duplicate work", func(t *testing.T) {
		jobs := []rbac.NamespaceCleanup{
			{NamespaceUID: uuid.NewString(), Namespace: "old-shop"},
			{NamespaceUID: uuid.NewString(), Namespace: "old-shop"},
		}
		for _, job := range jobs {
			require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
			require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
			conflict := job
			conflict.Namespace = "wrong-shop"
			require.ErrorIs(t, store.ScheduleRBACNamespaceCleanup(t.Context(), conflict), rbac.ErrConflict)
			require.ErrorIs(t, store.CompleteRBACNamespaceCleanup(t.Context(), conflict), rbac.ErrConflict)
			state, err := store.ApplyRBAC(t.Context(), rbacCreate(job.NamespaceUID, rbac.TierState{}, 1))
			require.NoError(t, err)
			retired, err := store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: job.NamespaceUID,
				ExpectedRevision: state.Revision, RetireNamespace: true})
			require.NoError(t, err)
			require.True(t, retired.NamespaceRetired)
			reloaded, err := store.ReadRBACTier(t.Context(), job.NamespaceUID)
			require.NoError(t, err)
			require.True(t, reloaded.NamespaceRetired)
			_, err = store.ApplyRBAC(t.Context(), rbacCreate(job.NamespaceUID, retired, 2))
			require.ErrorIs(t, err, rbac.ErrConflict, "retired namespace tiers reject late author projection")
		}
		seen, after := map[string]bool{}, ""
		for {
			page, err := store.ListRBACNamespaceCleanups(t.Context(), after, 1)
			require.NoError(t, err)
			for _, job := range page.Jobs {
				require.False(t, seen[job.NamespaceUID])
				seen[job.NamespaceUID] = true
				require.NoError(t, store.CompleteRBACNamespaceCleanup(t.Context(), job))
				require.NoError(t, store.CompleteRBACNamespaceCleanup(t.Context(), job))
			}
			if page.Next == "" {
				break
			}
			require.NotEqual(t, after, page.Next)
			after = page.Next
		}
		for _, job := range jobs {
			require.True(t, seen[job.NamespaceUID])
		}
	})
	t.Run("committed catch-up spans several candidates in one publication", func(t *testing.T) {
		tier := uuid.NewString()
		resources := make([]*rbac.Resource, rbac.MaxCandidateResources+1)
		for index := range resources {
			resources[index] = rbacRole(tier, fmt.Sprintf("catchup-%d", index))
		}
		write := rbacCreate(tier, rbac.TierState{}, 1, resources...)
		state, err := store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
		require.EqualValues(t, 1, state.Revision)
		snapshot, err := datastore.ReadRBACSnapshot(t.Context(), store, tier)
		require.NoError(t, err)
		require.Len(t, snapshot.Resources, len(resources))
		for _, resource := range resources {
			indexed, err := store.GetRBACByUID(t.Context(), resource.Metadata.UID)
			require.NoError(t, err)
			require.Equal(t, resource.Metadata.UID, indexed.Metadata.UID)
		}
		replayed, err := store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
		require.Equal(t, state, replayed)
		write.Projection = nil
		write.ExpectedRevision = state.Revision
		_, err = store.ApplyRBAC(t.Context(), write)
		require.ErrorIs(t, err, rbac.ErrInvalid, "lifecycle writes do not inherit committed recovery bounds")
		require.Error(t, rbac.ValidateCandidate(resources), "external candidate bounds remain unchanged")
	})
	t.Run("committed projection rebases lifecycle-only revisions", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "projection-race")
		state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, role))
		require.NoError(t, err)
		key := rbac.ResourceKey{TierID: tier, Kind: role.Kind, Name: role.Metadata.Name}
		current, err := store.GetRBAC(t.Context(), key)
		require.NoError(t, err)
		desired, err := rbac.CloneResource(current)
		require.NoError(t, err)
		desired.Spec = json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["update"]}]}`)
		write := rbacCreate(tier, state, 2)
		write.Changes = []rbac.Change{{Resource: desired, ExpectedUID: desired.Metadata.UID, ExpectedResourceVersion: desired.Metadata.ResourceVersion}}
		current.Metadata.Finalizers = []string{rbac.LifecycleFinalizer}
		current.Status = json.RawMessage(`{"observedGeneration":1,"conditions":[]}`)
		_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision,
			Changes: []rbac.Change{{Resource: current, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}}})
		require.NoError(t, err)
		result, err := admission.ProjectCommittedRBAC(t.Context(), store, write)
		require.NoError(t, err)
		require.False(t, result.ProjectionPending)
		after, err := store.GetRBAC(t.Context(), key)
		require.NoError(t, err)
		require.JSONEq(t, string(desired.Spec), string(after.Spec))
		require.JSONEq(t, string(current.Status), string(after.Status))
		require.Equal(t, current.Metadata.Finalizers, after.Metadata.Finalizers)
		require.EqualValues(t, 2, after.Metadata.Generation)
		require.Equal(t, "1", write.Changes[0].ExpectedResourceVersion)
		replayed, err := admission.ProjectCommittedRBAC(t.Context(), store, write)
		require.NoError(t, err)
		require.Equal(t, result.State, replayed.State)
	})
	t.Run("committed author deletion starts termination atomically", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "terminating")
		role.Metadata.Finalizers = []string{rbac.LifecycleFinalizer}
		state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, role))
		require.NoError(t, err)
		key := rbac.ResourceKey{TierID: tier, Kind: role.Kind, Name: role.Metadata.Name}
		before, err := store.GetRBAC(t.Context(), key)
		require.NoError(t, err)
		change := rbac.Change{Resource: before, ExpectedUID: before.Metadata.UID, ExpectedResourceVersion: before.Metadata.ResourceVersion, Terminate: true}
		_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision, Changes: []rbac.Change{change}})
		require.ErrorIs(t, err, rbac.ErrInvalid, "author termination requires committed projection identity")
		write := rbacCreate(tier, state, 2)
		write.Changes = []rbac.Change{change}
		state, err = store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
		after, err := store.GetRBAC(t.Context(), key)
		require.NoError(t, err)
		require.NotNil(t, after.Metadata.DeletionTimestamp)
		require.Equal(t, before.Metadata.UID, after.Metadata.UID)
		require.Equal(t, before.Metadata.Generation, after.Metadata.Generation)
		require.Equal(t, before.Metadata.Finalizers, after.Metadata.Finalizers)
		require.Equal(t, before.Spec, after.Spec)
		require.Equal(t, write.Projection.GitRevision, after.Metadata.Revision)
		require.Equal(t, 1, state.RoleCount, "termination does not reclaim a quota slot")
		replayed, err := store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
		require.Equal(t, state, replayed)
	})
	t.Run("atomic candidate and lost acknowledgement", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "reader")
		write := rbacCreate(tier, rbac.TierState{}, 1, role)
		_, err := store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
		// Discard the first result, then replay exactly as after a lost reply.
		state, err := store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
		require.EqualValues(t, 1, state.Revision)
		require.Equal(t, 1, state.RoleCount)
		got, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Equal(t, "1", got.Metadata.ResourceVersion)

		stale := rbacCreate(tier, rbac.TierState{}, 2, rbacRole(tier, "stale"))
		_, err = store.ApplyRBAC(t.Context(), stale)
		require.ErrorIs(t, err, datastore.ErrConflict)

		replacement := *got
		replacement.Spec = json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["update"]}]}`)
		invalid := rbacCreate(tier, state, 2, rbacRole(tier, "must-not-exist"))
		invalid.Changes = append(invalid.Changes, rbac.Change{
			Resource: &replacement, ExpectedUID: got.Metadata.UID, ExpectedResourceVersion: "stale",
		})
		_, err = store.ApplyRBAC(t.Context(), invalid)
		require.ErrorIs(t, err, datastore.ErrConflict)
		_, err = store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: tier, Kind: rbac.Role, Name: "must-not-exist"})
		require.ErrorIs(t, err, datastore.ErrNotFound)
		got, err = store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.JSONEq(t, string(role.Spec), string(got.Spec))
		after, err := store.ReadRBACTier(t.Context(), tier)
		require.NoError(t, err)
		require.Equal(t, state, after)
	})
	t.Run("stable pages reject a changed revision", func(t *testing.T) {
		tier := uuid.NewString()
		state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, rbacRole(tier, "a"), rbacRole(tier, "b")))
		require.NoError(t, err)
		page, err := store.ListRBAC(t.Context(), rbac.ListRequest{TierID: tier, Limit: 1, ExpectedRevision: &state.Revision})
		require.NoError(t, err)
		require.Len(t, page.Resources, 1)
		require.NotEmpty(t, page.Next)
		next, err := store.ListRBAC(t.Context(), rbac.ListRequest{TierID: tier, Limit: 1, After: page.Next, ExpectedRevision: &state.Revision})
		require.NoError(t, err)
		require.Len(t, next.Resources, 1)
		require.NotEqual(t, page.Resources[0].Metadata.UID, next.Resources[0].Metadata.UID)
		snapshot, err := datastore.ReadRBACSnapshot(t.Context(), store, tier)
		require.NoError(t, err)
		require.Len(t, snapshot.Resources, 2)
		require.Equal(t, state, snapshot.State)
		inconsistent := &inconsistentRBACPages{Store: store}
		_, err = datastore.ReadRBACSnapshot(t.Context(), inconsistent, tier)
		require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
		require.Equal(t, 3, inconsistent.calls, "a changing source must not trigger unbounded snapshot retries")
		_, err = store.ApplyRBAC(t.Context(), rbacCreate(tier, state, 2, rbacRole(tier, "c")))
		require.NoError(t, err)
		_, err = store.ListRBAC(t.Context(), rbac.ListRequest{TierID: tier, Limit: 1, After: page.Next, ExpectedRevision: &state.Revision})
		require.ErrorIs(t, err, datastore.ErrConflict)
		_, err = store.ListRBAC(t.Context(), rbac.ListRequest{TierID: tier, Limit: 1001})
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	})
	t.Run("status CAS does not replace spec or Git identity", func(t *testing.T) {
		tier := uuid.NewString()
		resource := rbacRole(tier, "status")
		state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, resource))
		require.NoError(t, err)
		current, err := store.GetRBACByUID(t.Context(), resource.Metadata.UID)
		require.NoError(t, err)
		update := *current
		update.Status = json.RawMessage(`{"observedGeneration":1,"conditions":[]}`)
		write := rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision, Changes: []rbac.Change{
			{Resource: &update, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion},
		}}
		after, err := store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
		require.Equal(t, state.Projection, after.Projection)
		got, err := store.GetRBACByUID(t.Context(), resource.Metadata.UID)
		require.NoError(t, err)
		require.EqualValues(t, 1, got.Metadata.Generation)
		require.Equal(t, "2", got.Metadata.ResourceVersion)
		_, err = store.ApplyRBAC(t.Context(), write)
		require.ErrorIs(t, err, datastore.ErrConflict)
	})
	t.Run("concurrent quota admission", func(t *testing.T) {
		runRBACQuotaRace(t, store, store)
	})
	t.Run("concurrent status updates", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "status-race")
		state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, role))
		require.NoError(t, err)
		current, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		errs := make([]error, 2)
		start := make(chan struct{})
		var workers sync.WaitGroup
		for i := range errs {
			workers.Go(func() {
				update := *current
				update.Status = json.RawMessage(fmt.Sprintf(`{"observedGeneration":1,"winner":%d}`, i))
				<-start
				_, errs[i] = store.ApplyRBAC(t.Context(), rbac.TierWrite{
					TierID: tier, ExpectedRevision: state.Revision,
					Changes: []rbac.Change{{Resource: &update, ExpectedUID: update.Metadata.UID, ExpectedResourceVersion: update.Metadata.ResourceVersion}},
				})
			})
		}
		close(start)
		workers.Wait()
		require.NotEqual(t, errs[0] == nil, errs[1] == nil)
		for _, err := range errs {
			if err != nil {
				require.ErrorIs(t, err, datastore.ErrConflict)
			}
		}
		after, err := store.ReadRBACTier(t.Context(), tier)
		require.NoError(t, err)
		require.Equal(t, state.Revision+1, after.Revision)
		require.Equal(t, state.Projection, after.Projection)
	})
	t.Run("instrumented store retains the capability", func(t *testing.T) {
		wrapped := rbacStore(t, datastore.NewInstrumentedDatastore(ds, "contract", zap.NewNop()))
		state, err := wrapped.ReadRBACTier(t.Context(), uuid.NewString())
		require.NoError(t, err)
		require.Zero(t, state.Revision)
	})
	t.Run("UID and status boundaries", func(t *testing.T) {
		tier := uuid.NewString()
		role := rbacRole(tier, "identity")
		state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, role))
		require.NoError(t, err)
		current, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		write := rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision, Changes: []rbac.Change{
			{Resource: current, ExpectedUID: uuid.NewString(), ExpectedResourceVersion: current.Metadata.ResourceVersion},
		}}
		_, err = store.ApplyRBAC(t.Context(), write)
		require.ErrorIs(t, err, datastore.ErrConflict)
		write.Changes[0].ExpectedUID = current.Metadata.UID
		current.Spec = json.RawMessage(`{"rules":[]}`)
		_, err = store.ApplyRBAC(t.Context(), write)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument, "status path must not alter desired spec")
		again, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.JSONEq(t, string(role.Spec), string(again.Spec), "returned resources cannot mutate stored state")
	})
	t.Run("claim acquisition races termination", func(t *testing.T) {
		for range 8 {
			uid := uuid.NewString()
			guard, err := store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: uid, Action: rbac.GuardRegister})
			require.NoError(t, err)
			claim := &rbac.RoleClaim{RoleUID: uid, BindingUID: uuid.NewString(),
				Binding: rbac.ResourceKey{TierID: uuid.NewString(), Kind: rbac.RoleBinding, Name: "racer"}}
			mutations := []rbac.GuardMutation{
				{RoleUID: uid, ExpectedVersion: guard.Version, Action: rbac.GuardAcquire, Claim: claim},
				{RoleUID: uid, ExpectedVersion: guard.Version, Action: rbac.GuardClose},
			}
			var workers sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for i := range mutations {
				workers.Go(func() {
					<-start
					_, errs[i] = store.MutateRBACRoleGuard(t.Context(), mutations[i])
				})
			}
			close(start)
			workers.Wait()
			require.NotEqual(t, errs[0] == nil, errs[1] == nil)
			guard, err = store.ReadRBACRoleGuard(t.Context(), uid)
			require.NoError(t, err)
			if errs[0] == nil {
				require.EqualValues(t, 1, guard.Claims)
				guard, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: uid, ExpectedVersion: guard.Version, Action: rbac.GuardClose})
				require.NoError(t, err)
				_, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: uid, ExpectedVersion: guard.Version, Action: rbac.GuardFinalize})
				require.Error(t, err)
			} else {
				require.Zero(t, guard.Claims)
				require.Equal(t, rbac.GuardTerminating, guard.State)
			}
		}
	})
	t.Run("claims close before finalization", func(t *testing.T) {
		roleUID := uuid.NewString()
		guard, err := store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: roleUID, Action: rbac.GuardRegister})
		require.NoError(t, err)
		claim := &rbac.RoleClaim{
			RoleUID: roleUID, BindingUID: uuid.NewString(),
			Binding: rbac.ResourceKey{TierID: uuid.NewString(), Kind: rbac.RoleBinding, Name: "pending"},
		}
		acquire := rbac.GuardMutation{RoleUID: roleUID, ExpectedVersion: guard.Version, Action: rbac.GuardAcquire, Claim: claim}
		guard, err = store.MutateRBACRoleGuard(t.Context(), acquire)
		require.NoError(t, err)
		replayed, err := store.MutateRBACRoleGuard(t.Context(), acquire)
		require.NoError(t, err)
		require.Equal(t, guard, replayed)
		require.EqualValues(t, 1, guard.Claims)
		guard, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: roleUID, ExpectedVersion: guard.Version, Action: rbac.GuardClose})
		require.NoError(t, err)
		_, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: roleUID, ExpectedVersion: guard.Version, Action: rbac.GuardFinalize})
		require.Error(t, err)
		_, err = store.MutateRBACRoleGuard(t.Context(), acquire)
		require.Error(t, err, "termination must not reopen an existing pending acquisition")
		page, err := store.ListRBACRoleClaims(t.Context(), roleUID, "", 10)
		require.NoError(t, err)
		require.Equal(t, []rbac.RoleClaim{*claim}, page.Claims)
		guard, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: roleUID, ExpectedVersion: guard.Version, Action: rbac.GuardRelease, Claim: claim})
		require.NoError(t, err)
		guard, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: roleUID, ExpectedVersion: guard.Version, Action: rbac.GuardFinalize})
		require.NoError(t, err)
		require.Equal(t, rbac.GuardFinalized, guard.State)
		_, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: roleUID, Action: rbac.GuardRegister})
		require.Error(t, err)
	})
}

func runRBACQuotaRace(t *testing.T, first, second rbac.Store) {
	t.Helper()
	tier := uuid.NewString()
	state := rbac.TierState{}
	for offset := 0; offset < 498; {
		count := min(250, 498-offset)
		resources := make([]*rbac.Resource, count)
		for i := range resources {
			resources[i] = rbacRole(tier, fmt.Sprintf("seed-%03d", offset+i))
		}
		var err error
		state, err = first.ApplyRBAC(t.Context(), rbacCreate(tier, state, offset+1, resources...))
		require.NoError(t, err)
		offset += count
	}
	writes := []rbac.TierWrite{
		rbacCreate(tier, state, 600, rbacRole(tier, "left-a"), rbacRole(tier, "left-b")),
		rbacCreate(tier, state, 601, rbacRole(tier, "right-a"), rbacRole(tier, "right-b")),
	}
	results := make([]error, 2)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i, store := range []rbac.Store{first, second} {
		workers.Go(func() {
			<-start
			_, results[i] = store.ApplyRBAC(t.Context(), writes[i])
		})
	}
	close(start)
	workers.Wait()
	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, datastore.ErrConflict)
		}
	}
	require.Equal(t, 1, successes)
	state, err := first.ReadRBACTier(t.Context(), tier)
	require.NoError(t, err)
	require.Equal(t, 500, state.RoleCount)
	_, err = first.ApplyRBAC(t.Context(), rbacCreate(tier, state, 700, rbacRole(tier, "over-quota")))
	require.ErrorIs(t, err, rbac.ErrQuota)
}
