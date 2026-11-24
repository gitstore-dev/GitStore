// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func runTranslationIndexPublicationContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store := translationIndexes(t, backend)
	for _, kind := range []datastore.TranslationIndexKind{
		datastore.TranslationNamespaceIndex, datastore.TranslationTargetNameIndex, datastore.TranslationTargetUIDIndex,
	} {
		t.Run(string(kind), func(t *testing.T) {
			uid := newID()
			shard, err := datastore.TranslationIndexShard(uid)
			require.NoError(t, err)
			index := datastore.TranslationIndexKey{Kind: kind, NamespaceUID: newID(), Shard: shard}
			switch kind {
			case datastore.TranslationTargetNameIndex:
				index.Target = datastore.TranslationTargetKey{
					NamespaceUID: index.NamespaceUID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "Laptop",
				}
			case datastore.TranslationTargetUIDIndex:
				index.TargetUID = newID()
			}
			entry := datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1",
				ReferenceEpoch: newID(), CreationTimestamp: time.Now().UTC()}
			reserved, err := store.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
			require.NoError(t, err)
			forged := reserved
			forged.Entry.SourceVersion = "another-source"
			require.ErrorIs(t, store.CommitTranslationIndex(t.Context(), forged), datastore.ErrConflict)
			require.ErrorIs(t, store.RemoveTranslationIndex(t.Context(), forged), datastore.ErrConflict)
			require.NoError(t, store.CommitTranslationIndex(t.Context(), reserved))
			require.NoError(t, store.CommitTranslationIndex(t.Context(), reserved))
			committed, err := store.GetTranslationIndexReservation(t.Context(), reserved.Token)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationReservationCommitted, committed.Phase)

			// Removal may win before publication. Its retained row must fence
			// both a delayed retry and the next reservation's stale predecessor.
			entry.SourceVersion = "2"
			removed, err := store.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
			require.NoError(t, err)
			require.NoError(t, store.RemoveTranslationIndex(t.Context(), removed))
			require.NoError(t, store.RemoveTranslationIndex(t.Context(), removed))
			require.ErrorIs(t, store.CommitTranslationIndex(t.Context(), removed), datastore.ErrConflict)
			state, err := store.GetTranslationIndexReservation(t.Context(), removed.Token)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationReservationRemoved, state.Phase)
			require.NoError(t, store.RemoveTranslationIndex(t.Context(), reserved))
			entry.SourceVersion = "3"
			next, err := store.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
			require.NoError(t, err)
			require.EqualValues(t, 2, next.Entry.Slot, "removed allocations are never reused")
			require.NoError(t, store.CommitTranslationIndex(t.Context(), next))
			require.NoError(t, store.RemoveTranslationIndex(t.Context(), removed))

			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, store.RemoveTranslationIndex(ctx, next), context.Canceled)
			state, err = store.GetTranslationIndexReservation(t.Context(), next.Token)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationReservationCommitted, state.Phase)
		})
	}
}
