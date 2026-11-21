// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type projectionFaultStore struct {
	datastore.RBACStore
	applyCalls, repairs int
	loseReply           bool
	pending             bool
	unavailable         bool
	cancel              context.CancelFunc
}

func (s *projectionFaultStore) ApplyRBAC(ctx context.Context, write datastore.RBACTierWrite) (datastore.RBACTierState, error) {
	s.applyCalls++
	if s.unavailable {
		return datastore.RBACTierState{}, datastore.ErrRBACUnavailable
	}
	state, err := s.RBACStore.ApplyRBAC(ctx, write)
	if err == nil && s.loseReply {
		s.loseReply = false
		if s.cancel != nil {
			s.cancel()
		}
		return datastore.RBACTierState{}, errors.New("lost committed reply")
	}
	return state, err
}

func (s *projectionFaultStore) ReadRBACTier(ctx context.Context, tier string) (datastore.RBACTierState, error) {
	if err := ctx.Err(); err != nil {
		return datastore.RBACTierState{}, err
	}
	state, err := s.RBACStore.ReadRBACTier(ctx, tier)
	state.ProjectionPending = s.pending
	return state, err
}

func (s *projectionFaultStore) RepairRBACProjection(ctx context.Context, tier string, revision uint64) error {
	s.repairs++
	if err := s.RBACStore.RepairRBACProjection(ctx, tier, revision); err != nil {
		return err
	}
	s.pending = false
	return nil
}

func committedRBACFixture(t *testing.T) (*projectionFaultStore, datastore.RBACTierWrite) {
	t.Helper()
	ds, err := memdb.New()
	require.NoError(t, err)
	store := &projectionFaultStore{RBACStore: ds.(datastore.RBACCapable).RBACStore()}
	tier := uuid.NewString()
	write := datastore.RBACTierWrite{
		TierID: tier,
		Projection: &datastore.RBACProjectionIdentity{
			RepositoryID: uuid.NewString(), GitRef: "refs/heads/main",
			GitRevision: "main@sha1:1111111111111111111111111111111111111111",
			TreeDigest:  "1111111111111111111111111111111111111111111111111111111111111111", Actor: "alice",
		},
		Changes: []datastore.RBACChange{{Resource: &datastore.RBACResource{
			APIVersion: "rbac.authorization.gitstore.dev/v1beta1", Kind: datastore.RBACRole,
			NamespaceUID: tier, Metadata: datastore.RBACMetadata{Name: "reader", Namespace: "shop", UID: uuid.NewString()},
			Spec: json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`),
		}}},
	}
	return store, write
}

func TestCommittedRBACProjectionRecoversReceiptWithoutRepublishing(t *testing.T) {
	for _, mode := range []string{"lost-reply", "index-repair", "canceled-after-commit"} {
		t.Run(mode, func(t *testing.T) {
			store, write := committedRBACFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store.loseReply = true
			store.pending = mode == "index-repair"
			if mode == "canceled-after-commit" {
				store.cancel = cancel
			}
			result, err := admission.ProjectCommittedRBAC(ctx, store, write)
			require.NoError(t, err)
			require.False(t, result.ProjectionPending)
			require.Equal(t, write.Projection.GitRevision, result.CommitSHA)
			require.EqualValues(t, 1, result.State.Revision)
			require.Equal(t, 1, store.applyCalls)
			require.Equal(t, 1, result.State.RoleCount)
			if store.pending {
				t.Fatal("repair receipt remained pending")
			}
			if mode == "index-repair" {
				require.Equal(t, 1, store.repairs)
			}
		})
	}
}

func TestCommittedRBACProjectionFailurePreservesCommitAndBoundsRetries(t *testing.T) {
	store, write := committedRBACFixture(t)
	store.unavailable = true
	result, err := admission.ProjectCommittedRBAC(t.Context(), store, write)
	require.ErrorIs(t, err, admission.ErrCommittedRBACProjectionPending)
	require.Equal(t, write.Projection.GitRevision, result.CommitSHA)
	require.True(t, result.ProjectionPending)
	require.Equal(t, 3, store.applyCalls)
	state, err := store.RBACStore.ReadRBACTier(t.Context(), write.TierID)
	require.NoError(t, err)
	require.Zero(t, state.Revision)
}

func TestCommittedRBACProjectionDoesNotReplaceNewerReceipt(t *testing.T) {
	store, write := committedRBACFixture(t)
	_, err := store.ApplyRBAC(t.Context(), write)
	require.NoError(t, err)
	stale := write
	identity := *write.Projection
	identity.GitRevision = "main@sha1:2222222222222222222222222222222222222222"
	stale.Projection = &identity
	result, err := admission.ProjectCommittedRBAC(t.Context(), store, stale)
	require.ErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
	require.True(t, result.ProjectionPending)
	require.Equal(t, 2, store.applyCalls)
	state, err := store.ReadRBACTier(t.Context(), write.TierID)
	require.NoError(t, err)
	require.Equal(t, *write.Projection, state.Projection)
}

func TestCommittedRBACProjectionRebasesConcurrentOwnedMetadata(t *testing.T) {
	store, initial := committedRBACFixture(t)
	state, err := store.RBACStore.ApplyRBAC(t.Context(), initial)
	require.NoError(t, err)
	key := datastore.RBACResourceKey{TierID: initial.TierID, Kind: datastore.RBACRole, Name: "reader"}
	current, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	desired := *current
	desired.Metadata.Labels = map[string]string{"team": "commerce"}
	desired.Spec = json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["update"]}]}`)
	projection := *initial.Projection
	projection.GitRevision = "main@sha1:2222222222222222222222222222222222222222"
	projection.TreeDigest = "2222222222222222222222222222222222222222222222222222222222222222"
	write := datastore.RBACTierWrite{TierID: initial.TierID, ExpectedRevision: state.Revision,
		ExpectedGitRevision: initial.Projection.GitRevision, Projection: &projection,
		Changes: []datastore.RBACChange{{Resource: &desired, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}}}
	current.Status = json.RawMessage(`{"observedGeneration":1,"conditions":[]}`)
	current.Metadata.Finalizers = []string{"rbac.authorization.gitstore.dev/lifecycle", "another-controller/finalizer"}
	_, err = store.RBACStore.ApplyRBAC(t.Context(), datastore.RBACTierWrite{TierID: initial.TierID, ExpectedRevision: state.Revision,
		Changes: []datastore.RBACChange{{Resource: current, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}}})
	require.NoError(t, err)
	result, err := admission.ProjectCommittedRBAC(t.Context(), store, write)
	require.NoError(t, err)
	require.False(t, result.ProjectionPending)
	updated, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	require.JSONEq(t, string(desired.Spec), string(updated.Spec))
	require.JSONEq(t, string(current.Status), string(updated.Status))
	require.Equal(t, current.Metadata.Finalizers, updated.Metadata.Finalizers)
	require.Equal(t, desired.Metadata.Labels, updated.Metadata.Labels)
	require.EqualValues(t, 2, updated.Metadata.Generation)
	require.Equal(t, "1", write.Changes[0].ExpectedResourceVersion, "repair must not mutate the caller's candidate")
	require.Empty(t, desired.Status)
}
