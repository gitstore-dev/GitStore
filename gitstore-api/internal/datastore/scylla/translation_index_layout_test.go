// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationReservationTargetLayoutsAndStalePreparation(t *testing.T) {
	first, second := newTestStores(t)
	a, b := translationAllocator(t, first), translationAllocator(t, second)
	for _, kind := range []datastore.TranslationIndexKind{datastore.TranslationTargetNameIndex, datastore.TranslationTargetUIDIndex} {
		t.Run(string(kind), func(t *testing.T) {
			uid, token := uuid.NewString(), uuid.NewString()
			shard, err := datastore.TranslationIndexShard(uid)
			require.NoError(t, err)
			index := datastore.TranslationIndexKey{Kind: kind, NamespaceUID: uuid.NewString(), Shard: shard}
			if kind == datastore.TranslationTargetNameIndex {
				index.Target = datastore.TranslationTargetKey{
					NamespaceUID: index.NamespaceUID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "Laptop",
				}
			} else {
				index.TargetUID = uuid.NewString()
			}
			entry := datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1",
				ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC()}
			reservation, err := a.ReserveTranslationIndex(t.Context(), index, entry, token, 1024)
			require.NoError(t, err)
			replayed, err := b.ReserveTranslationIndex(t.Context(), index, entry, token, 1024)
			require.NoError(t, err)
			require.Equal(t, reservation, replayed, "timestamp precision must survive the persisted retry roundtrip")
			if kind == datastore.TranslationTargetNameIndex {
				index.Target.Name = "laptop"
			} else {
				index.TargetUID = uuid.NewString()
			}
			_, err = b.ReserveTranslationIndex(t.Context(), index, entry, token, 1024)
			require.ErrorIs(t, err, datastore.ErrConflict, "a token cannot change target identity or name case")
		})
	}

	uid := uuid.NewString()
	shard, err := datastore.TranslationIndexShard(uid)
	require.NoError(t, err)
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: uuid.NewString(), Shard: shard}
	entry := datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1",
		ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	_, err = a.ReserveTranslationIndex(t.Context(), index, entry, uuid.NewString(), 512)
	require.NoError(t, err)
	// A Preparing record can lose its head CAS to another token. Persist that
	// state without inventing ownership of the competing token's slot.
	token := uuid.NewString()
	tokenUUID, err := gocql.ParseUUID(token)
	require.NoError(t, err)
	namespaceUUID, err := gocql.ParseUUID(index.NamespaceUID)
	require.NoError(t, err)
	sourceUUID, err := gocql.ParseUUID(uid)
	require.NoError(t, err)
	target, err := json.Marshal(struct {
		Target    datastore.TranslationTargetKey
		TargetUID string
	}{})
	require.NoError(t, err)
	raw := newRawSession(t)
	require.NoError(t, raw.Query(
		"INSERT INTO translation_index_reservations_by_token (reservation_token,resource_version,index_kind,namespace_uid,target_key,shard,segment,slot,uid,source_version,reference_epoch,payload_bytes,phase,creation_timestamp,expected_head_version,allocation_version,reserved_bytes) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		tokenUUID, "1", string(index.Kind), namespaceUUID, string(target), int8(shard), int64(0), 0,
		sourceUUID, entry.SourceVersion, entry.ReferenceEpoch, int64(512), "Preparing", entry.CreationTimestamp, "", "1", int64(512),
	).Exec())
	replanned, err := b.ReserveTranslationIndex(t.Context(), index, entry, token, 512)
	require.NoError(t, err)
	require.EqualValues(t, 1, replanned.Entry.Slot)
	require.Equal(t, "3", replanned.ResourceVersion, "replanning and acknowledgement advance the record CAS independently")
	replayed, err := a.ReserveTranslationIndex(t.Context(), index, entry, token, 512)
	require.NoError(t, err)
	require.Equal(t, replanned, replayed)
}
