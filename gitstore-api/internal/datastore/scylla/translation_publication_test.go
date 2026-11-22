// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type translationPublicationStore interface {
	translationReservationStore
	CommitTranslationIndex(context.Context, datastore.TranslationIndexReservation) error
	RemoveTranslationIndex(context.Context, datastore.TranslationIndexReservation) error
}

func translationPublisher(t *testing.T, backend datastore.Datastore) translationPublicationStore {
	t.Helper()
	store, ok := backend.(translationPublicationStore)
	require.True(t, ok)
	return store
}

func TestTranslationIndexPublicationInterruptionAndLateWriters(t *testing.T) {
	first, second := newTestStores(t)
	a, b := translationPublisher(t, first), translationPublisher(t, second)
	raw := newRawSession(t)
	for _, kind := range []datastore.TranslationIndexKind{
		datastore.TranslationNamespaceIndex, datastore.TranslationTargetNameIndex, datastore.TranslationTargetUIDIndex,
	} {
		t.Run(string(kind), func(t *testing.T) {
			uid, namespace := uuid.New(), uuid.New()
			shard, err := datastore.TranslationIndexShard(uid.String())
			require.NoError(t, err)
			index := datastore.TranslationIndexKey{Kind: kind, NamespaceUID: namespace.String(), Shard: shard}
			switch kind {
			case datastore.TranslationTargetNameIndex:
				index.Target = datastore.TranslationTargetKey{
					NamespaceUID: index.NamespaceUID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "Laptop",
				}
			case datastore.TranslationTargetUIDIndex:
				index.TargetUID = uuid.NewString()
			}
			entry := datastore.TranslationIndexEntry{TranslationUID: uid.String(), SourceVersion: "1",
				ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
			record, err := a.ReserveTranslationIndex(t.Context(), index, entry, uuid.NewString(), 1024)
			require.NoError(t, err)
			require.NoError(t, a.RemoveTranslationIndex(t.Context(), record))
			token, err := gocql.ParseUUID(record.Token)
			require.NoError(t, err)
			// Restore the removal boundary after its retained cell was durable
			// but before its reservation acknowledgement. No physical fault.
			require.NoError(t, raw.Query(
				"UPDATE translation_index_reservations_by_token SET phase='Reserved',resource_version='2' WHERE reservation_token=?", token,
			).Exec())
			replacement := translationPublisher(t, newTestStore(t))
			require.ErrorIs(t, replacement.CommitTranslationIndex(t.Context(), record), datastore.ErrConflict)
			require.NoError(t, replacement.RemoveTranslationIndex(t.Context(), record))

			selection, args := translationPublishedCellQuery(t, index, entry)
			var actualToken gocql.UUID
			var retired bool
			require.NoError(t, raw.Query(selection, args...).Scan(&actualToken, &retired))
			require.Equal(t, token, actualToken)
			require.True(t, retired)

			entry.SourceVersion = "2"
			newer, err := b.ReserveTranslationIndex(t.Context(), index, entry, uuid.NewString(), 1024)
			require.NoError(t, err)
			require.NoError(t, b.CommitTranslationIndex(t.Context(), newer))
			// A paused older worker must not overwrite or retire the newer slot.
			require.NoError(t, raw.Query(
				"UPDATE translation_index_reservations_by_token SET phase='Reserved',resource_version='2' WHERE reservation_token=?", token,
			).Exec())
			require.NoError(t, replacement.CommitTranslationIndex(t.Context(), record))
			require.NoError(t, replacement.RemoveTranslationIndex(t.Context(), record))
			require.NoError(t, raw.Query(selection, args...).Scan(&actualToken, &retired))
			require.Equal(t, newer.Token, actualToken.String())
			require.False(t, retired)

			// Publication may have reached its cell before acknowledgement.
			newerToken, err := gocql.ParseUUID(newer.Token)
			require.NoError(t, err)
			require.NoError(t, raw.Query(
				"UPDATE translation_index_reservations_by_token SET phase='Reserved',resource_version='2' WHERE reservation_token=?", newerToken,
			).Exec())
			require.NoError(t, replacement.CommitTranslationIndex(t.Context(), newer))
			for round := range 4 {
				entry.SourceVersion = time.Unix(int64(round+3), 0).String()
				raced, err := a.ReserveTranslationIndex(t.Context(), index, entry, uuid.NewString(), 1024)
				require.NoError(t, err)
				errs := make(chan error, 2)
				var workers sync.WaitGroup
				for _, retire := range []bool{false, true} {
					workers.Go(func() {
						var err error
						for range 8 {
							if retire {
								err = b.RemoveTranslationIndex(t.Context(), raced)
							} else {
								err = a.CommitTranslationIndex(t.Context(), raced)
							}
							if !errors.Is(err, datastore.ErrConflict) {
								break
							}
						}
						if !retire && errors.Is(err, datastore.ErrConflict) {
							err = nil
						}
						errs <- err
					})
				}
				workers.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				require.NoError(t, raw.Query(selection, args...).Scan(&actualToken, &retired))
				require.Equal(t, raced.Token, actualToken.String())
				require.True(t, retired, "removal wins over every paused publisher")
				state, err := a.GetTranslationIndexReservation(t.Context(), raced.Token)
				require.NoError(t, err)
				require.Equal(t, datastore.TranslationReservationRemoved, state.Phase)
			}
		})
	}
}

func translationPublishedCellQuery(t *testing.T, index datastore.TranslationIndexKey, entry datastore.TranslationIndexEntry) (string, []any) {
	t.Helper()
	namespace, err := gocql.ParseUUID(index.NamespaceUID)
	require.NoError(t, err)
	uid, err := gocql.ParseUUID(entry.TranslationUID)
	require.NoError(t, err)
	switch index.Kind {
	case datastore.TranslationNamespaceIndex:
		return "SELECT reservation_token,retired FROM translations_by_namespace WHERE namespace_uid=? AND shard=? AND segment=? AND creation_timestamp=? AND uid=?",
			[]any{namespace, int8(index.Shard), int64(0), entry.CreationTimestamp, uid}
	case datastore.TranslationTargetNameIndex:
		encoded, err := json.Marshal(index.Target)
		require.NoError(t, err)
		digest := sha256.Sum256(encoded)
		return "SELECT reservation_token,retired FROM translation_dependents_by_target_name WHERE namespace_uid=? AND api_version=? AND kind=? AND name=? AND shard=? AND segment=? AND uid=?",
			[]any{namespace, index.Target.APIVersion, index.Target.Kind, hex.EncodeToString(digest[:]), int8(index.Shard), int64(0), uid}
	default:
		target, err := gocql.ParseUUID(index.TargetUID)
		require.NoError(t, err)
		return "SELECT reservation_token,retired FROM translation_dependents_by_target_uid WHERE namespace_uid=? AND target_uid=? AND shard=? AND segment=? AND uid=?",
			[]any{namespace, target, int8(index.Shard), int64(0), uid}
	}
}
