// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func referenceFixture(t *testing.T) (rbac.Store, rbac.ResourceKey, *rbac.Resource, *rbac.Resource) {
	t.Helper()
	reader, origin := snapshotFixture(t)
	store := reader.Store
	role, err := store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: origin.NamespaceUID, Kind: rbac.Role, Name: "reader"})
	require.NoError(t, err)
	state, err := store.ReadRBACTier(t.Context(), origin.NamespaceUID)
	require.NoError(t, err)
	identity := state.Projection
	identity.GitRevision = "main@sha1:" + strings.Repeat("2", 40)
	binding := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.RoleBinding, NamespaceUID: origin.NamespaceUID,
		Metadata: rbac.Metadata{Name: "readers", Namespace: "shop", UID: uuid.NewString()},
		Spec:     json.RawMessage(`{"roleRef":{"kind":"Role","name":"reader"},"subjects":[{"kind":"User","name":"alice"}]}`)}
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{
		TierID: origin.NamespaceUID, ExpectedRevision: state.Revision, ExpectedGitRevision: state.Projection.GitRevision, Projection: &identity,
		Changes: []rbac.Change{{Resource: binding}},
	})
	require.NoError(t, err)
	key := rbac.ResourceKey{TierID: origin.NamespaceUID, Kind: rbac.RoleBinding, Name: "readers"}
	binding, err = store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	return store, key, role, binding
}

type smallClaimPages struct {
	rbac.Store
	pages int
}

func (s *smallClaimPages) ListRBACRoleClaims(ctx context.Context, uid, after string, limit int) (rbac.ClaimPage, error) {
	s.pages++
	if limit != 1000 {
		return rbac.ClaimPage{}, rbac.ErrInvalid
	}
	return s.Store.ListRBACRoleClaims(ctx, uid, after, 1)
}

func TestRoleCompletionRecoversPagedPendingClaimsButRetainsLiveAndUnknownBindings(t *testing.T) {
	store, key, role, binding := referenceFixture(t)
	references := rbac.References{Store: store}
	_, err := references.Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	for _, name := range []string{"lost-ack-one", "lost-ack-two"} {
		guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		claim := &rbac.RoleClaim{RoleUID: role.Metadata.UID, BindingUID: uuid.NewString(),
			Binding: rbac.ResourceKey{TierID: key.TierID, Kind: rbac.RoleBinding, Name: name}}
		_, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{
			RoleUID: role.Metadata.UID, ExpectedVersion: guard.Version, Action: rbac.GuardAcquire, Claim: claim})
		require.NoError(t, err)
	}
	require.NoError(t, references.CloseRole(t.Context(), rbac.ResourceKey{TierID: key.TierID, Kind: rbac.Role, Name: "reader"}, role.Metadata.UID))
	require.ErrorIs(t, (rbac.References{Store: uncertainBindingRead{Store: store}}).RecoverRoleClaims(t.Context(), role.Metadata.UID), rbac.ErrUnavailable)
	guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	require.EqualValues(t, 3, guard.Claims)
	paged := &smallClaimPages{Store: store}
	require.NoError(t, (rbac.References{Store: paged}).RecoverRoleClaims(t.Context(), role.Metadata.UID))
	require.Greater(t, paged.pages, 1)
	guard, err = store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	require.EqualValues(t, 1, guard.Claims, "only the resolved live binding remains a blocker")
}

type uncertainBindingRead struct{ rbac.Store }

func (s uncertainBindingRead) GetRBAC(context.Context, rbac.ResourceKey) (*rbac.Resource, error) {
	return nil, rbac.ErrUnavailable
}

type lostClaimRelease struct{ rbac.Store }

func (s lostClaimRelease) MutateRBACRoleGuard(ctx context.Context, mutation rbac.GuardMutation) (rbac.RoleGuard, error) {
	guard, err := s.Store.MutateRBACRoleGuard(ctx, mutation)
	if err == nil && mutation.Action == rbac.GuardRelease {
		return rbac.RoleGuard{}, rbac.ErrUnavailable
	}
	return guard, err
}

func TestClaimReleaseReplacementRecoversAfterSuppressionAndLostReply(t *testing.T) {
	store, key, role, binding := referenceFixture(t)
	resolved, err := (rbac.References{Store: store}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	now := time.Now().UTC()
	resolved.Metadata.DeletionTimestamp = &now
	state, err := store.ReadRBACTier(t.Context(), key.TierID)
	require.NoError(t, err)
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
		Changes: []rbac.Change{{Resource: resolved, ExpectedUID: binding.Metadata.UID, ExpectedResourceVersion: resolved.Metadata.ResourceVersion}}})
	require.NoError(t, err)
	claims, err := store.ListRBACRoleClaims(t.Context(), role.Metadata.UID, "", 1000)
	require.NoError(t, err)
	require.Len(t, claims.Claims, 1)
	released, err := (rbac.References{Store: lostClaimRelease{store}}).RecoverClaim(t.Context(), claims.Claims[0])
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.False(t, released)
	guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	require.Zero(t, guard.Claims, "lost reply follows the durable release")
	released, err = (rbac.References{Store: store}).RecoverClaim(t.Context(), claims.Claims[0])
	require.NoError(t, err)
	require.True(t, released, "replacement safely retries the uncertain release")
	current, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	require.NotNil(t, current.Metadata.DeletionTimestamp, "claim release must not undo grant suppression")
}

func TestPendingClaimRecoveryRetainsUnknownOrStillAcquiringReferences(t *testing.T) {
	store, key, role, binding := referenceFixture(t)
	guard, err := store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: role.Metadata.UID, Action: rbac.GuardRegister})
	require.NoError(t, err)
	claim := rbac.RoleClaim{RoleUID: role.Metadata.UID, BindingUID: binding.Metadata.UID, Binding: key}
	_, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{
		RoleUID: guard.RoleUID, ExpectedVersion: guard.Version, Action: rbac.GuardAcquire, Claim: &claim,
	})
	require.NoError(t, err)
	for _, source := range []rbac.Store{store, uncertainBindingRead{Store: store}} {
		released, err := (rbac.References{Store: source}).RecoverClaim(t.Context(), claim)
		require.False(t, released)
		require.True(t, err == nil || errors.Is(err, rbac.ErrUnavailable))
		guard, err = store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.EqualValues(t, 1, guard.Claims)
	}
}

func TestResolutionReplacementReusesClaimAcquiredBeforePublicationCrash(t *testing.T) {
	store, key, role, binding := referenceFixture(t)
	prepared, err := (rbac.References{Store: store}).PrepareResolution(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	target, err := rbac.ResolvedRoleUID(prepared)
	require.NoError(t, err)
	require.Equal(t, role.Metadata.UID, target)
	current, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	target, err = rbac.ResolvedRoleUID(current)
	require.NoError(t, err)
	require.Empty(t, target, "preparation has not published the binding")
	resolved, err := (rbac.References{Store: store}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	target, err = rbac.ResolvedRoleUID(resolved)
	require.NoError(t, err)
	require.Equal(t, role.Metadata.UID, target)
	claims, err := store.ListRBACRoleClaims(t.Context(), role.Metadata.UID, "", 1000)
	require.NoError(t, err)
	require.Len(t, claims.Claims, 1, "replacement cannot duplicate the pre-crash durable claim")
	require.Equal(t, binding.Metadata.UID, claims.Claims[0].BindingUID)
}

func TestReferencesClaimBeforeResolutionAndKeepLiveResolvedClaim(t *testing.T) {
	store, key, role, binding := referenceFixture(t)
	coordinator := rbac.References{Store: store}
	resolved, err := coordinator.Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	target, err := rbac.ResolvedRoleUID(resolved)
	require.NoError(t, err)
	require.Equal(t, role.Metadata.UID, target)
	claims, err := store.ListRBACRoleClaims(t.Context(), target, "", 1000)
	require.NoError(t, err)
	require.Len(t, claims.Claims, 1)
	released, err := coordinator.RecoverClaim(t.Context(), claims.Claims[0])
	require.NoError(t, err)
	require.False(t, released)
	require.NoError(t, coordinator.CloseRole(t.Context(),
		rbac.ResourceKey{TierID: key.TierID, Kind: rbac.Role, Name: "reader"}, role.Metadata.UID))
	guard, err := store.ReadRBACRoleGuard(t.Context(), target)
	require.NoError(t, err)
	_, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{
		RoleUID: target, Action: rbac.GuardFinalize, ExpectedVersion: guard.Version,
	})
	require.ErrorIs(t, err, rbac.ErrConflict)
}

type delayedResolution struct {
	rbac.Store
	started chan struct{}
	resume  chan struct{}
}

func (s *delayedResolution) ApplyRBAC(ctx context.Context, write rbac.TierWrite) (rbac.TierState, error) {
	for _, change := range write.Changes {
		target, _ := rbac.ResolvedRoleUID(change.Resource)
		if target != "" {
			close(s.started)
			select {
			case <-s.resume:
			case <-ctx.Done():
				return rbac.TierState{}, ctx.Err()
			}
		}
	}
	return s.Store.ApplyRBAC(ctx, write)
}

func TestPendingClaimRecoveryFencesAnInFlightResolverBeforeRelease(t *testing.T) {
	store, key, role, binding := referenceFixture(t)
	delayed := &delayedResolution{Store: store, started: make(chan struct{}), resume: make(chan struct{})}
	coordinator := rbac.References{Store: delayed}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := coordinator.Resolve(ctx, key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
		done <- err
	}()
	select {
	case <-delayed.started:
	case <-ctx.Done():
		t.Fatal("resolver did not reach publication")
	}
	require.NoError(t, coordinator.CloseRole(t.Context(),
		rbac.ResourceKey{TierID: key.TierID, Kind: rbac.Role, Name: "reader"}, role.Metadata.UID))
	claims, err := store.ListRBACRoleClaims(t.Context(), role.Metadata.UID, "", 1000)
	require.NoError(t, err)
	require.Len(t, claims.Claims, 1)
	released, err := coordinator.RecoverClaim(t.Context(), claims.Claims[0])
	require.NoError(t, err)
	require.True(t, released)
	close(delayed.resume)
	require.ErrorIs(t, <-done, rbac.ErrConflict)
	guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	require.Zero(t, guard.Claims)
	_, err = store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{
		RoleUID: guard.RoleUID, ExpectedVersion: guard.Version, Action: rbac.GuardFinalize,
	})
	require.NoError(t, err)
	current, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	target, err := rbac.ResolvedRoleUID(current)
	require.NoError(t, err)
	require.Empty(t, target)
}
