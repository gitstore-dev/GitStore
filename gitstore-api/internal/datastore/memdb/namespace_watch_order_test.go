// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestNamespaceMutationSerializesCommitAndJournalPublication(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	memory := store.(*memdbDatastore)

	memory.namespaceMutationMu.Lock()
	locked := true
	defer func() {
		if locked {
			memory.namespaceMutationMu.Unlock()
		}
	}()

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- memory.CreateNamespace(context.Background(), &datastore.Namespace{
			UID:               "01990000-0000-7000-8000-000000000001",
			Name:              "ordered",
			ResourceVersion:   datastore.NamespaceInitialResourceVersion,
			CreationTimestamp: time.Now().UTC(),
			UpdateTimestamp:   time.Now().UTC(),
		})
	}()
	<-started

	require.Never(t, func() bool {
		_, getErr := memory.GetNamespaceByName(context.Background(), "ordered")
		return getErr == nil
	}, 25*time.Millisecond, 5*time.Millisecond)
	bounds, err := memory.Bounds(context.Background())
	require.NoError(t, err)
	require.Zero(t, bounds.HighWater)

	memory.namespaceMutationMu.Unlock()
	locked = false
	require.NoError(t, <-result)

	_, err = memory.GetNamespaceByName(context.Background(), "ordered")
	require.NoError(t, err)
	bounds, err = memory.Bounds(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(1), bounds.HighWater)
}

func TestResourceWatchJournalLinearizesKindsInOneCursorSpace(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	ctx := context.Background()
	lease, acquired, err := journal.AcquireLease(ctx, "test", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	_, err = journal.Append(ctx, lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "Namespace", Name: "shop"}, time.Hour)
	require.NoError(t, err)
	_, err = journal.Append(ctx, lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "Repository", Namespace: "shop", Name: "catalog"}, time.Hour)
	require.NoError(t, err)

	events, err := journal.ReadAfter(ctx, datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, uint64(1), events[0].Sequence)
	require.Equal(t, "Namespace", events[0].Kind)
	require.Equal(t, uint64(2), events[1].Sequence)
	require.Equal(t, "Repository", events[1].Kind)
	require.Equal(t, "shop", events[1].Namespace)
}

func TestRepositoryMutationsPublishCommittedResourceWatchEvents(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	ctx := context.Background()
	repository := &datastore.Repository{
		UID: "01990000-0000-7000-8000-000000000111", Namespace: "shop", Name: "catalog",
		ResourceVersion: datastore.RepositoryInitialResourceVersion, CreationTimestamp: time.Now().UTC(), UpdateTimestamp: time.Now().UTC(),
		Labels: map[string]string{"team": "store"},
	}
	require.NoError(t, store.CreateRepository(ctx, repository))

	repository.ResourceVersion = "2"
	repository.Labels = map[string]string{"team": "platform"}
	require.NoError(t, store.UpdateRepository(ctx, repository, datastore.RepositoryInitialResourceVersion))

	// Controller status and foreground-deletion/finalizer writes are normal
	// authoritative Repository modifications and must be visible to watches.
	repository.ResourceVersion = "3"
	repository.Status = []byte(`{"observedGeneration":1,"conditions":[{"type":"Ready","status":"TRUE"}]}`)
	require.NoError(t, store.UpdateRepository(ctx, repository, "2"))
	repository.ResourceVersion = "4"
	repository.Finalizers = []string{"gitstore.dev/foreground-deletion"}
	require.NoError(t, store.UpdateRepository(ctx, repository, "3"))

	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	beforeRejectedWrite, err := journal.ReadAfter(ctx, datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	stale := *repository
	stale.ResourceVersion = "5"
	stale.Labels = map[string]string{"team": "stale"}
	require.ErrorIs(t, store.UpdateRepository(ctx, &stale, "3"), datastore.ErrConflict)
	afterRejectedWrite, err := journal.ReadAfter(ctx, datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, afterRejectedWrite, len(beforeRejectedWrite), "rejected optimistic write must not publish")
	// A repeated persisted value is an explicit no-op: it cannot manufacture a
	// synthetic lifecycle event merely because an API caller retried it.
	require.NoError(t, store.UpdateRepository(ctx, repository, "4"))
	afterNoopWrite, err := journal.ReadAfter(ctx, datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, afterNoopWrite, len(beforeRejectedWrite), "no-op write must not publish")

	require.NoError(t, store.DeleteRepository(ctx, repository.UID))
	events, err := journal.ReadAfter(ctx, datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, events, 5)
	for _, event := range events {
		require.Equal(t, "Repository", event.Kind)
		require.Equal(t, "shop", event.Namespace)
		require.Equal(t, "catalog", event.Name)
	}
	require.Equal(t, datastore.ResourceWatchAdded, events[0].Type)
	var postimage datastore.Repository
	require.NoError(t, json.Unmarshal(events[0].Payload, &postimage))
	require.Equal(t, "shop", postimage.Namespace)
	require.Equal(t, "catalog", postimage.Name)
	require.Equal(t, datastore.ResourceWatchModified, events[1].Type)
	require.Equal(t, map[string]string{"team": "store"}, events[1].PreviousSelectorLabels)
	require.Equal(t, map[string]string{"team": "platform"}, events[1].SelectorLabels)
	require.Equal(t, datastore.ResourceWatchModified, events[2].Type)
	require.Contains(t, string(events[2].Payload), `"Ready"`)
	require.Equal(t, datastore.ResourceWatchModified, events[3].Type)
	require.Contains(t, string(events[3].Payload), `foreground-deletion`)
	require.Equal(t, datastore.ResourceWatchDeleted, events[4].Type)
	require.Empty(t, events[4].Payload)
	require.Equal(t, map[string]string{"team": "platform"}, events[4].SelectorLabels)
	require.ErrorIs(t, store.DeleteRepository(ctx, repository.UID), datastore.ErrNotFound)
	finalEvents, err := journal.ReadAfter(ctx, datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, finalEvents, len(events), "rejected delete must not publish")
}
