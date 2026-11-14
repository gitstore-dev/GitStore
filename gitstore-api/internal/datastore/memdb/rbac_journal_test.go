// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func journalRBACResource(kind rbac.Kind, tier, name string) *rbac.Resource {
	resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: kind,
		Metadata: rbac.Metadata{Name: name, UID: uuid.NewString(), Labels: map[string]string{"team": "catalog"}},
		Spec:     json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`)}
	if kind.Namespaced() {
		resource.Metadata.Namespace, resource.NamespaceUID = "shop", tier
	}
	if !kind.IsRole() {
		resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"reader"},"subjects":[{"kind":"User","name":"alice"}]}`)
	}
	return resource
}

func journalRBACWrite(tier string, state rbac.TierState, resources ...*rbac.Resource) rbac.TierWrite {
	write := rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision, ExpectedGitRevision: state.Projection.GitRevision,
		Projection: &rbac.ProjectionIdentity{RepositoryID: "11111111-1111-4111-8111-111111111111", GitRef: "refs/heads/main",
			GitRevision: fmt.Sprintf("main@sha1:%040x", state.Revision+1), TreeDigest: fmt.Sprintf("%064x", state.Revision+1), Actor: "journal-test"}}
	for _, resource := range resources {
		write.Changes = append(write.Changes, rbac.Change{Resource: resource})
	}
	return write
}

func TestRBACJournalCommittedWritesReplayAndDeletion(t *testing.T) {
	for _, kind := range []rbac.Kind{rbac.Role, rbac.ClusterRole, rbac.RoleBinding, rbac.ClusterRoleBinding} {
		t.Run(string(kind), func(t *testing.T) {
			ds, err := New()
			require.NoError(t, err)
			store := ds.(datastore.RBACCapable).RBACStore()
			journal := ds.(datastore.ResourceWatchCapable).ResourceWatchJournal()
			tier := rbac.ClusterTierID
			if kind.Namespaced() {
				tier = uuid.NewString()
			}
			resource := journalRBACResource(kind, tier, "reader")
			write := journalRBACWrite(tier, rbac.TierState{}, resource)
			state, err := store.ApplyRBAC(t.Context(), write)
			require.NoError(t, err)
			_, err = store.ApplyRBAC(t.Context(), write)
			require.NoError(t, err, "lost-reply replay must not duplicate events")
			stale := journalRBACWrite(tier, rbac.TierState{}, journalRBACResource(kind, tier, "stale"))
			stale.Projection.GitRevision, stale.Projection.TreeDigest = fmt.Sprintf("main@sha1:%040x", 99), fmt.Sprintf("%064x", 99)
			_, err = store.ApplyRBAC(t.Context(), stale)
			require.ErrorIs(t, err, datastore.ErrConflict)
			got, err := store.GetRBACByUID(t.Context(), resource.Metadata.UID)
			require.NoError(t, err)
			got.Status = json.RawMessage(`{"observedGeneration":1}`)
			state, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision,
				Changes: []rbac.Change{{Resource: got, ExpectedUID: got.Metadata.UID, ExpectedResourceVersion: got.Metadata.ResourceVersion}}})
			require.NoError(t, err)
			got, err = store.GetRBACByUID(t.Context(), resource.Metadata.UID)
			require.NoError(t, err)
			now := time.Now().UTC()
			got.Metadata.DeletionTimestamp = &now
			state, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision,
				Changes: []rbac.Change{{Resource: got, ExpectedUID: got.Metadata.UID, ExpectedResourceVersion: got.Metadata.ResourceVersion}}})
			require.NoError(t, err)
			if kind.IsRole() {
				for index, action := range []rbac.GuardAction{rbac.GuardRegister, rbac.GuardClose, rbac.GuardFinalize} {
					_, err := store.MutateRBACRoleGuard(t.Context(), rbac.GuardMutation{RoleUID: resource.Metadata.UID, ExpectedVersion: uint64(index), Action: action})
					require.NoError(t, err)
				}
			}
			got, err = store.GetRBACByUID(t.Context(), resource.Metadata.UID)
			require.NoError(t, err)
			_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision,
				Changes: []rbac.Change{{Resource: got, Remove: true, ExpectedUID: got.Metadata.UID, ExpectedResourceVersion: got.Metadata.ResourceVersion}}})
			require.NoError(t, err)
			events, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 32)
			require.NoError(t, err)
			require.Len(t, events, 8)
			for index := range 4 {
				event, progress := events[index*2], events[index*2+1]
				require.Equal(t, string(kind), event.Kind)
				require.Equal(t, resource.Metadata.Namespace, event.Namespace)
				require.Equal(t, resource.Metadata.Name, event.Name)
				require.EqualValues(t, index*2+1, event.Sequence)
				var observed rbac.Resource
				require.NoError(t, json.Unmarshal(event.Payload, &observed))
				require.Equal(t, resource.Metadata.UID, observed.Metadata.UID)
				if index == 3 {
					require.Equal(t, datastore.ResourceWatchDeleted, event.Type)
					require.Empty(t, observed.Spec, "deletion retains identity, not the policy body")
				} else {
					require.Equal(t, fmt.Sprint(index+1), observed.Metadata.ResourceVersion)
					require.Equal(t, "catalog", event.SelectorLabels["team"])
				}
				require.Equal(t, datastore.ResourceWatchProgress, progress.Type)
				var tierProgress datastore.RBACTierProgress
				require.NoError(t, json.Unmarshal(progress.Payload, &tierProgress))
				require.Equal(t, tier, tierProgress.TierID)
				require.EqualValues(t, index+1, tierProgress.Revision)
			}
			resumed, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{Epoch: events[3].Epoch, Sequence: events[3].Sequence}, 32)
			require.NoError(t, err)
			require.Equal(t, events[4:], resumed)
		})
	}
}

func TestRBACJournalConcurrentCandidatesStayContiguous(t *testing.T) {
	ds, err := New()
	require.NoError(t, err)
	store := ds.(datastore.RBACCapable).RBACStore()
	tier := uuid.NewString()
	var writers sync.WaitGroup
	failures := make(chan error, 16)
	for index := range 16 {
		writers.Go(func() {
			for range 100 {
				state, err := store.ReadRBACTier(t.Context(), tier)
				if err != nil {
					failures <- err
					return
				}
				write := journalRBACWrite(tier, state,
					journalRBACResource(rbac.Role, tier, fmt.Sprintf("a-%d", index)), journalRBACResource(rbac.Role, tier, fmt.Sprintf("b-%d", index)))
				identity := uint64(index+1)*1000 + state.Revision + 1
				write.Projection.GitRevision, write.Projection.TreeDigest = fmt.Sprintf("main@sha1:%040x", identity), fmt.Sprintf("%064x", identity)
				_, err = store.ApplyRBAC(t.Context(), write)
				if err == nil {
					failures <- nil
					return
				}
				if !errors.Is(err, datastore.ErrConflict) {
					failures <- err
					return
				}
			}
			failures <- fmt.Errorf("writer exhausted bounded conflict retries")
		})
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	events, err := ds.(datastore.ResourceWatchCapable).ResourceWatchJournal().ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 256)
	require.NoError(t, err)
	require.Len(t, events, 48)
	for index := range 16 {
		first, second, progress := events[index*3], events[index*3+1], events[index*3+2]
		require.Equal(t, datastore.ResourceWatchAdded, first.Type)
		require.Equal(t, datastore.ResourceWatchAdded, second.Type)
		require.Equal(t, first.Name[2:], second.Name[2:], "one candidate's rows must not interleave")
		var observed datastore.RBACTierProgress
		require.NoError(t, json.Unmarshal(progress.Payload, &observed))
		require.EqualValues(t, index+1, observed.Revision)
	}
}
