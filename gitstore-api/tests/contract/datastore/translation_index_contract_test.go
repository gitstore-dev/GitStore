// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func translationIndexes(t *testing.T, backend datastore.Datastore) datastore.TranslationIndexStore {
	t.Helper()
	capable, ok := backend.(datastore.TranslationIndexCapable)
	require.True(t, ok, "index maintenance must survive datastore decoration")
	store := capable.TranslationIndexStore()
	require.NotNil(t, store)
	return store
}

func runTranslationIndexReservationContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store := translationIndexes(t, backend)
	uid := newID()
	shard, err := datastore.TranslationIndexShard(uid)
	require.NoError(t, err)
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: newID(), Shard: shard}
	entry := datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1", ReferenceEpoch: newID(), CreationTimestamp: time.Now().UTC()}
	token := newID()
	type result struct {
		reservation datastore.TranslationIndexReservation
		err         error
	}
	results := make(chan result, 8)
	var workers sync.WaitGroup
	for range cap(results) {
		workers.Go(func() {
			var reservation datastore.TranslationIndexReservation
			var err error
			for range 8 {
				reservation, err = store.ReserveTranslationIndex(t.Context(), index, entry, token, datastore.MaxTranslationSegmentBytes)
				if !errors.Is(err, datastore.ErrConflict) {
					break
				}
			}
			results <- result{reservation, err}
		})
	}
	workers.Wait()
	close(results)
	var first datastore.TranslationIndexReservation
	for result := range results {
		require.NoError(t, result.err)
		if first.Token == "" {
			first = result.reservation
		}
		require.Equal(t, first, result.reservation, "retries must return one allocation, not consume more slots")
	}
	require.Equal(t, datastore.TranslationReservationReserved, first.Phase)
	require.EqualValues(t, 0, first.Index.Segment)
	second, err := store.ReserveTranslationIndex(t.Context(), index, entry, newID(), datastore.MaxTranslationSegmentBytes)
	require.NoError(t, err)
	require.EqualValues(t, 1, second.Index.Segment)
	require.EqualValues(t, 0, second.Entry.Slot)
	entry.SourceVersion = "2"
	_, err = store.ReserveTranslationIndex(t.Context(), index, entry, token, datastore.MaxTranslationSegmentBytes)
	require.ErrorIs(t, err, datastore.ErrConflict, "a retry token cannot be reassigned to another source transition")
	unchanged, err := store.GetTranslationIndexReservation(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, first, unchanged)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceledToken := newID()
	_, err = store.ReserveTranslationIndex(ctx, index, entry, canceledToken, 512)
	require.ErrorIs(t, err, context.Canceled)
	_, err = store.GetTranslationIndexReservation(t.Context(), canceledToken)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	third, err := store.ReserveTranslationIndex(t.Context(), index, entry, newID(), 512)
	require.NoError(t, err)
	require.EqualValues(t, 2, third.Index.Segment, "cancellation cannot consume capacity")
}
