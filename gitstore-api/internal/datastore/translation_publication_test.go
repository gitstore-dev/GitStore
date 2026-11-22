// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationIndexRetirementFencesPausedPublisher(t *testing.T) {
	record := datastore.TranslationIndexReservation{
		Entry: datastore.TranslationIndexEntry{
			TranslationUID: uuid.NewString(), SourceVersion: "1",
			ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC(),
		},
		Token: uuid.NewString(), Phase: datastore.TranslationReservationReserved,
	}
	retired, err := datastore.PlanTranslationIndexCell(nil, record, true)
	require.NoError(t, err)
	require.True(t, retired.Retired)
	_, err = datastore.PlanTranslationIndexCell(retired, record, false)
	require.ErrorIs(t, err, datastore.ErrConflict, "a paused publisher cannot resurrect a removed cell before reservation acknowledgement")

	next := record
	next.Token, next.Entry.SourceVersion, next.Entry.Slot = uuid.NewString(), "2", 1
	current, err := datastore.PlanTranslationIndexCell(retired, next, false)
	require.NoError(t, err)
	require.False(t, current.Retired)
	require.Equal(t, next.Token, current.Token)
	for _, retire := range []bool{false, true} {
		change, err := datastore.PlanTranslationIndexCell(current, record, retire)
		require.NoError(t, err)
		require.Nil(t, change, "late publication and removal cannot change a newer slot")
	}
	corrupt := *current
	corrupt.Token = uuid.NewString()
	_, err = datastore.PlanTranslationIndexCell(&corrupt, next, false)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "one slot cannot have two owners")
}
