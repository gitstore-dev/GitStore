// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationIndexAllocationBoundsAndPendingRecovery(t *testing.T) {
	uid := uuid.NewString()
	shard, err := TranslationIndexShard(uid)
	require.NoError(t, err)
	index := TranslationIndexKey{Kind: TranslationNamespaceIndex, NamespaceUID: uuid.NewString(), Shard: shard}
	entry := TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1", ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC()}
	token := uuid.NewString()
	head, reservation, err := PlanTranslationIndexReservation(nil, index, entry, token, 512)
	require.NoError(t, err)
	require.EqualValues(t, 1, head.NextSlot)
	require.EqualValues(t, 0, reservation.Entry.Slot)
	require.Equal(t, token, head.PendingToken)
	require.Equal(t, TranslationReservationPreparing, reservation.Phase)
	require.Empty(t, reservation.ExpectedHeadVersion)
	require.Equal(t, head.ResourceVersion, reservation.AllocationVersion)
	_, _, err = PlanTranslationIndexReservation(head, index, entry, uuid.NewString(), 512)
	require.ErrorIs(t, err, ErrConflict, "uncertain capacity cannot be skipped or reused")
	_, err = FinishTranslationIndexReservation(head, reservation)
	require.ErrorIs(t, err, ErrConflict, "the durable reservation must be acknowledged before clearing the pending head")
	reservation.Phase = TranslationReservationReserved
	cleared, err := FinishTranslationIndexReservation(head, reservation)
	require.NoError(t, err)
	require.Empty(t, cleared.PendingToken)
	require.NotEqual(t, head.ResourceVersion, cleared.ResourceVersion)
	require.Equal(t, token, head.PendingToken, "pure transitions do not modify their input")
	next, second, err := PlanTranslationIndexReservation(cleared, index, entry, uuid.NewString(), 512)
	require.NoError(t, err)
	require.EqualValues(t, 1, second.Entry.Slot)
	require.EqualValues(t, 2, next.NextSlot)
	require.EqualValues(t, 1024, next.ReservedBytes)
	require.Equal(t, cleared.ResourceVersion, second.ExpectedHeadVersion)

	for _, limit := range []string{"rows", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			full := *cleared
			full.Segment = 9
			if limit == "rows" {
				full.NextSlot = MaxTranslationSegmentRows
			} else {
				full.ReservedBytes = MaxTranslationSegmentBytes - 511
			}
			rolled, allocated, err := PlanTranslationIndexReservation(&full, index, entry, uuid.NewString(), 512)
			require.NoError(t, err)
			require.EqualValues(t, 10, rolled.Segment)
			require.EqualValues(t, 10, allocated.Index.Segment)
			require.EqualValues(t, 0, allocated.Entry.Slot)
			require.EqualValues(t, 1, rolled.NextSlot)
			require.EqualValues(t, 512, rolled.ReservedBytes)
		})
	}
	invalid := *cleared
	invalid.ResourceVersion = "corrupt"
	_, _, err = PlanTranslationIndexReservation(&invalid, index, entry, uuid.NewString(), 512)
	require.ErrorIs(t, err, ErrRepairRequired)
	full := *cleared
	full.Segment, full.NextSlot = math.MaxInt64, MaxTranslationSegmentRows
	_, _, err = PlanTranslationIndexReservation(&full, index, entry, uuid.NewString(), 512)
	require.ErrorIs(t, err, ErrRepairRequired, "segment identifiers cannot wrap or overflow CQL bigint")
	tampered := reservation
	tampered.ReservedBytes++
	_, err = FinishTranslationIndexReservation(head, tampered)
	require.ErrorIs(t, err, ErrRepairRequired)
}

func TestTranslationIndexAllocationRejectsUnsafeRequests(t *testing.T) {
	uid := uuid.NewString()
	shard, err := TranslationIndexShard(uid)
	require.NoError(t, err)
	index := TranslationIndexKey{Kind: TranslationNamespaceIndex, NamespaceUID: uuid.NewString(), Shard: shard}
	entry := TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1", ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now()}
	for _, size := range []int64{-1, 0, 1, MaxTranslationSegmentBytes + 1} {
		_, _, err := PlanTranslationIndexReservation(nil, index, entry, uuid.NewString(), size)
		require.ErrorIs(t, err, ErrInvalidArgument)
	}
	index.Shard = (shard + 1) % TranslationIndexShards
	_, _, err = PlanTranslationIndexReservation(nil, index, entry, uuid.NewString(), 512)
	require.ErrorIs(t, err, ErrInvalidArgument, "shards are derived from immutable Translation UID")
	index.Shard = shard
	_, _, err = PlanTranslationIndexReservation(nil, index, entry, "invalid", 512)
	require.ErrorIs(t, err, ErrInvalidArgument)
	index.Kind = TranslationTargetNameIndex
	index.Target = TranslationTargetKey{NamespaceUID: index.NamespaceUID, APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind: "Product", Name: strings.Repeat("\x00", 1<<19)}
	_, _, err = PlanTranslationIndexReservation(nil, index, entry, uuid.NewString(), 1<<20)
	require.ErrorIs(t, err, ErrInvalidArgument, "escaped metadata must also fit the resource-sized reservation record")
}
