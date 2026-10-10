// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func journalCategoryFixture(uid string) *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{UID: uid, Namespace: "test-ns", Name: "shoes", Kind: "CategoryTaxonomy",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now()}
}

func TestCategoryTaxonomyJournalRecordsOnlyCommittedWritesInOrder(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	deletion := store.(datastore.CategoryTaxonomyDeletionStore)
	category := journalCategoryFixture("00000000-0000-0000-0000-0000000000c1")
	category.Labels = map[string]string{"team": "a"}
	require.NoError(t, store.CreateCategoryTaxonomy(t.Context(), category))
	require.ErrorIs(t, store.CreateCategoryTaxonomy(t.Context(), category), datastore.ErrAlreadyExists)

	category.Labels["team"] = "b"
	category.ResourceVersion = "2"
	require.NoError(t, store.UpdateCategoryTaxonomy(t.Context(), category))

	var writers sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		writers.Go(func() {
			for {
				current, err := store.GetCategoryTaxonomy(t.Context(), category.UID)
				if err != nil {
					failures <- err
					return
				}
				_, err = store.UpdateCategoryTaxonomyStatus(t.Context(), "test-ns", "shoes", datastore.CategoryTaxonomyStatusPatch{ResourceVersion: current.ResourceVersion})
				if errors.Is(err, datastore.ErrConflict) {
					continue
				}
				if err != nil {
					failures <- err
				}
				return
			}
		})
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}

	current, err := store.GetCategoryTaxonomy(t.Context(), category.UID)
	require.NoError(t, err)
	marked, err := deletion.MarkCategoryTaxonomyDeletion(t.Context(), "test-ns", "shoes", current.ResourceVersion, time.Now().UTC())
	require.NoError(t, err)
	_, err = deletion.CompleteCategoryTaxonomyDeletion(t.Context(), "test-ns", "shoes", marked.ResourceVersion, marked.UID)
	require.NoError(t, err)

	events, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 32)
	require.NoError(t, err)
	require.Len(t, events, 8, "create, update, 4 status writes, mark and complete deletion")
	types := make([]datastore.ResourceWatchEventType, 0, len(events))
	versions := make([]string, 0, len(events))
	for index, event := range events {
		require.Equal(t, "CategoryTaxonomy", event.Kind)
		require.Equal(t, "test-ns", event.Namespace)
		require.Equal(t, "shoes", event.Name)
		require.Equal(t, uint64(index+1), event.Sequence)
		types = append(types, event.Type)
		if event.Type == datastore.ResourceWatchDeleted {
			require.Empty(t, event.Payload)
			continue
		}
		var observed datastore.CategoryTaxonomy
		require.NoError(t, json.Unmarshal(event.Payload, &observed))
		versions = append(versions, observed.ResourceVersion)
	}
	require.Equal(t, datastore.ResourceWatchAdded, types[0])
	require.Equal(t, datastore.ResourceWatchDeleted, types[len(types)-1])
	require.Equal(t, "a", events[0].SelectorLabels["team"], "authored object mutation cannot change retained payloads")
	require.Equal(t, "a", events[1].PreviousSelectorLabels["team"])
	require.Equal(t, "b", events[1].SelectorLabels["team"])
	for index := 1; index < len(versions); index++ {
		require.NotEqual(t, versions[index-1], versions[index], "each committed write publishes a distinct version")
	}

	var terminating datastore.CategoryTaxonomy
	require.NoError(t, json.Unmarshal(events[6].Payload, &terminating))
	require.NotNil(t, terminating.DeletionTimestamp, "the Terminating transition is journaled before removal")
}

func TestCategoryTaxonomyJournalSkipsRejectedWrites(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	deletion := store.(datastore.CategoryTaxonomyDeletionStore)
	require.ErrorIs(t, store.UpdateCategoryTaxonomy(t.Context(), journalCategoryFixture("00000000-0000-0000-0000-0000000000c2")), datastore.ErrNotFound)
	require.ErrorIs(t, store.DeleteCategoryTaxonomy(t.Context(), "00000000-0000-0000-0000-0000000000c2"), datastore.ErrNotFound)
	_, err = deletion.MarkCategoryTaxonomyDeletion(t.Context(), "test-ns", "shoes", "1", time.Now())
	require.ErrorIs(t, err, datastore.ErrNotFound)

	events, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 8)
	require.NoError(t, err)
	require.Empty(t, events)
}
