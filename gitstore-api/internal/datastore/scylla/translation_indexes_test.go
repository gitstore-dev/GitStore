// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type translationReservationStore interface {
	ReserveTranslationIndex(context.Context, datastore.TranslationIndexKey, datastore.TranslationIndexEntry, string, int64) (datastore.TranslationIndexReservation, error)
	GetTranslationIndexReservation(context.Context, string) (datastore.TranslationIndexReservation, error)
}

func translationAllocator(t *testing.T, store datastore.Datastore) translationReservationStore {
	t.Helper()
	allocator, ok := store.(translationReservationStore)
	require.True(t, ok, "Scylla must persist and recover idempotent segment reservations")
	return allocator
}

func TestTranslationReservationConcurrentStoresAndInterruptedStates(t *testing.T) {
	first, second := newTestStores(t)
	a, b := translationAllocator(t, first), translationAllocator(t, second)
	raw := newRawSession(t)
	for _, phase := range []string{"Preparing", "Reserved"} {
		t.Run(phase, func(t *testing.T) {
			uid, token := uuid.NewString(), uuid.NewString()
			shard, err := datastore.TranslationIndexShard(uid)
			require.NoError(t, err)
			index := datastore.TranslationIndexKey{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: uuid.NewString(), Shard: shard}
			entry := datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1",
				ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
			type result struct {
				reservation datastore.TranslationIndexReservation
				err         error
			}

			results := make(chan result, 2)
			var workers sync.WaitGroup
			for _, allocator := range []translationReservationStore{a, b} {
				workers.Go(func() {
					var reservation datastore.TranslationIndexReservation
					var err error
					for range 8 {
						reservation, err = allocator.ReserveTranslationIndex(t.Context(), index, entry, token, datastore.MaxTranslationSegmentBytes)
						if !errors.Is(err, datastore.ErrConflict) {
							break
						}
					}
					results <- result{reservation, err}
				})
			}
			workers.Wait()
			close(results)
			var reserved datastore.TranslationIndexReservation
			for result := range results {
				require.NoError(t, result.err)
				if reserved.Token == "" {
					reserved = result.reservation
				}
				require.Equal(t, reserved, result.reservation)
			}
			require.EqualValues(t, 0, reserved.Index.Segment)
			require.Equal(t, datastore.TranslationReservationReserved, reserved.Phase)

			// Restore the exact persisted state at each interruption boundary;
			// this is state injection, not a physical process/network fault.
			recordVersion := "2"
			if phase == "Preparing" {
				recordVersion = "1"
			}
			tokenUUID, err := gocql.ParseUUID(token)
			require.NoError(t, err)
			namespaceUUID, err := gocql.ParseUUID(index.NamespaceUID)
			require.NoError(t, err)
			require.NoError(t, raw.Query("UPDATE translation_index_reservations_by_token SET phase=?,resource_version=? WHERE reservation_token=?",
				phase, recordVersion, tokenUUID).Exec())
			require.NoError(t, raw.Query("UPDATE translation_index_heads_by_shard SET resource_version=?,pending_token=? WHERE index_kind=? AND namespace_uid=? AND target_key=? AND shard=?",
				reserved.AllocationVersion, tokenUUID, string(index.Kind), namespaceUUID, "", int8(shard)).Exec())
			replacement := translationAllocator(t, newTestStore(t))
			recovered, err := replacement.ReserveTranslationIndex(t.Context(), index, entry, token, datastore.MaxTranslationSegmentBytes)
			require.NoError(t, err)
			require.Equal(t, reserved, recovered, "a replacement store recovers the original capacity assignment")
			next, err := replacement.ReserveTranslationIndex(t.Context(), index, entry, uuid.NewString(), 512)
			require.NoError(t, err)
			require.EqualValues(t, 1, next.Index.Segment)
			require.EqualValues(t, 0, next.Entry.Slot)
			entry.SourceVersion = "2"
			_, err = a.ReserveTranslationIndex(t.Context(), index, entry, token, datastore.MaxTranslationSegmentBytes)
			require.ErrorIs(t, err, datastore.ErrConflict)
		})
	}
}
