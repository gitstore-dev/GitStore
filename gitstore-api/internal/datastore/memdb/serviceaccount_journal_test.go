// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountJournalCommittedWritesAndNameReuse(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	account := &datastore.ServiceAccount{UID: "00000000-0000-0000-0000-000000000065", Name: "ci-bot",
		CreationTimestamp: time.Now().UTC(), Generation: 1, ResourceVersion: "1"}
	require.NoError(t, store.CreateServiceAccount(t.Context(), account))
	require.ErrorIs(t, store.CreateServiceAccount(t.Context(), account), datastore.ErrAlreadyExists)
	_, err = store.UpdateServiceAccountKeys(t.Context(), account.UID, nil, nil, "stale")
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.NoError(t, store.SetServiceAccountDisabled(t.Context(), account.UID, true))
	require.NoError(t, store.DeleteServiceAccount(t.Context(), account.UID))
	require.ErrorIs(t, store.DeleteServiceAccount(t.Context(), account.UID), datastore.ErrNotFound)
	replacement := *account
	replacement.UID = "00000000-0000-0000-0000-000000000066"
	require.NoError(t, store.CreateServiceAccount(t.Context(), &replacement))
	events, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 16)
	require.NoError(t, err)
	require.Len(t, events, 4)
	for index, event := range events {
		require.Equal(t, "ServiceAccount", event.Kind)
		require.Empty(t, event.Namespace, "workloads are cluster-scoped")
		require.Equal(t, account.Name, event.Name)
		require.EqualValues(t, index+1, event.Sequence)
		var observed datastore.ServiceAccount
		require.NoError(t, json.Unmarshal(event.Payload, &observed))
		if index < 3 {
			require.Equal(t, account.UID, observed.UID)
		} else {
			require.Equal(t, replacement.UID, observed.UID)
		}
	}
	require.Equal(t, datastore.ResourceWatchAdded, events[0].Type)
	require.Equal(t, datastore.ResourceWatchModified, events[1].Type)
	require.Equal(t, datastore.ResourceWatchDeleted, events[2].Type)
	require.JSONEq(t, `{"UID":"00000000-0000-0000-0000-000000000065"}`, string(events[2].Payload))
	require.Equal(t, datastore.ResourceWatchAdded, events[3].Type)
}

func TestServiceAccountJournalConcurrentUpdatesFollowCommitOrder(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	account := &datastore.ServiceAccount{UID: "00000000-0000-0000-0000-000000000067", Name: "parallel",
		CreationTimestamp: time.Now().UTC(), Generation: 1, ResourceVersion: "1"}
	require.NoError(t, store.CreateServiceAccount(t.Context(), account))
	var writers sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		writers.Go(func() { failures <- store.SetServiceAccountDisabled(t.Context(), account.UID, true) })
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	events, err := store.(datastore.ResourceWatchCapable).ResourceWatchJournal().ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 32)
	require.NoError(t, err)
	require.Len(t, events, 17)
	expected := *account
	for index, event := range events {
		var observed datastore.ServiceAccount
		require.NoError(t, json.Unmarshal(event.Payload, &observed))
		if index > 0 {
			datastore.AdvanceServiceAccountSystemVersion(&expected)
		}
		require.Equal(t, expected.ResourceVersion, observed.ResourceVersion)
	}
}
