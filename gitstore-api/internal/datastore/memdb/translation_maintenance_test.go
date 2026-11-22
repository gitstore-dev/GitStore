// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationSourceAuditFindsMissingNamespaceAnchor(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	before, err := store.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	txn := store.db.Txn(true)
	raw, err := txn.First("translation_source_placements", "id", source.UID)
	require.NoError(t, err)
	anchor := raw.(*translationSourcePlacements).Placements[0]
	require.Equal(t, datastore.TranslationNamespaceIndex, anchor.Index.Kind)
	raw, err = txn.First("translation_index_cell", "id", translationCellID(anchor))
	require.NoError(t, err)
	require.NotNil(t, raw)
	require.NoError(t, txn.Delete("translation_index_cell", raw))
	txn.Commit()
	page, err := store.ListTranslationSourceAudit(t.Context(), source.NamespaceID, "", 1)
	require.NoError(t, err)
	require.Equal(t, []datastore.TranslationMaintenanceIntent{*before}, page.Items)
	after, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, after, "discovery cannot change source authority")
	require.NoError(t, store.RecoverTranslationSourceAudit(t.Context(), page.Items[0]))
	txn = store.db.Txn(false)
	defer txn.Abort()
	raw, err = txn.First("translation_index_cell", "id", translationCellID(anchor))
	require.NoError(t, err)
	require.NotNil(t, raw, "source-audit recovery must reuse the accepted namespace allocation")
}

func TestTranslationCellAuditFindsMissingReservationAuthority(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	page, err := store.ListTranslationIndexCellAudit(t.Context(), source.NamespaceID, "", 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	observed := page.Items[0]
	txn := store.db.Txn(true)
	raw, err := txn.First("translation_index_reservation", "id", observed.Cell.Token)
	require.NoError(t, err)
	require.NotNil(t, raw)
	require.NoError(t, txn.Delete("translation_index_reservation", raw))
	txn.Commit()
	page, err = store.ListTranslationIndexCellAudit(t.Context(), source.NamespaceID, "", 1)
	require.NoError(t, err)
	require.Equal(t, []datastore.TranslationIndexAuditCell{observed}, page.Items,
		"physical-cell discovery cannot depend on the missing reservation")
	require.ErrorIs(t, store.RecoverTranslationIndexCellAudit(t.Context(), source.NamespaceID, observed), datastore.ErrRepairRequired)
	unchanged, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, unchanged)
	read := store.db.Txn(false)
	defer read.Abort()
	record := datastore.TranslationIndexReservation{Index: observed.Index, Entry: observed.Cell.Entry}
	raw, err = read.First("translation_index_cell", "id", translationCellID(record))
	require.NoError(t, err)
	require.NotNil(t, raw)
	require.Equal(t, observed.Cell, raw.(*translationIndexCellRow).Cell, "failed audit must retain unproven cell authority")
}

func TestTranslationMaintenanceRepairsOwnerProjectionWithoutChangingSource(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	source.ResourceVersion = "2"
	owner, ownerRepository := uuid.NewString(), uuid.NewString()
	source.OwnerReferences = json.RawMessage(`[{"uid":"` + owner + `","repositoryID":"` + ownerRepository + `","blockOwnerDeletion":true}]`)
	require.NoError(t, store.UpdateTranslation(t.Context(), source, "1"))
	before, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	state, err := store.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	bounds, err := store.ResourceWatchJournal().Bounds(t.Context())
	require.NoError(t, err)
	txn := store.db.Txn(true)
	require.NoError(t, deleteOwnerReferenceProjections(txn, "Translation", source.UID))
	txn.Commit()
	scope := datastore.OwnerReferenceScope{Namespace: source.Namespace, RepositoryID: ownerRepository}
	has, err := store.HasBlockingOwnerDependents(t.Context(), scope, owner)
	require.NoError(t, err)
	require.False(t, has)
	require.NoError(t, store.RecoverTranslation(t.Context(), source.UID))
	has, err = store.HasBlockingOwnerDependents(t.Context(), scope, owner)
	require.NoError(t, err)
	require.True(t, has)
	after, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	afterState, err := store.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, state, afterState)
	afterBounds, err := store.ResourceWatchJournal().Bounds(t.Context())
	require.NoError(t, err)
	require.Equal(t, bounds, afterBounds)
}

func TestTranslationMaintenanceRepairsHintsAtomicallyWithoutNewAllocations(t *testing.T) {
	for _, corrupt := range []string{"missing-cells", "retired-cell", "retired-reservation", "contradictory-epoch", "forged-placement", "missing-head"} {
		t.Run(corrupt, func(t *testing.T) {
			store, source := newTranslationSourceFixture(t)
			binding, err := store.GetTranslationBinding(t.Context(), source.UID)
			require.NoError(t, err)
			binding.ResourceVersion, binding.Epochs[0].BoundUID = "2", uuid.NewString()
			require.NoError(t, store.CompareAndSwapTranslationBinding(t.Context(), binding, "1"))
			before, err := store.GetTranslationMaintenance(t.Context(), source.UID)
			require.NoError(t, err)
			require.NoError(t, store.RecoverTranslationSourceAudit(t.Context(), *before))
			watermark, err := store.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, before, watermark)
			bounds, err := store.ResourceWatchJournal().Bounds(t.Context())
			require.NoError(t, err)
			txn := store.db.Txn(true)
			raw, err := txn.First("translation_source_placements", "id", source.UID)
			require.NoError(t, err)
			state := *raw.(*translationSourcePlacements)
			placements := state.Placements
			require.Len(t, placements, 3)
			for i, placement := range placements {
				raw, err := txn.First("translation_index_cell", "id", translationCellID(placement))
				require.NoError(t, err)
				require.NotNil(t, raw)
				if corrupt == "missing-cells" || i == 0 {
					require.NoError(t, txn.Delete("translation_index_cell", raw))
				} else if i == 1 {
					cell := *raw.(*translationIndexCellRow)
					switch corrupt {
					case "missing-head":
						head, err := txn.First("translation_index_head", "id", translationHeadID(placements[0].Index))
						require.NoError(t, err)
						require.NoError(t, txn.Delete("translation_index_head", head))
					case "retired-cell":
						cell.Cell.Retired = true
						require.NoError(t, txn.Insert("translation_index_cell", &cell))
					case "retired-reservation":
						require.NoError(t, changeTranslationIndexCellInTxn(txn, placement, true))
					case "contradictory-epoch":
						cell.Cell.Entry.ReferenceEpoch = "contradiction"
						require.NoError(t, txn.Insert("translation_index_cell", &cell))
					case "forged-placement":
						state.Placements = slices.Clone(placements)
						state.Placements[i].Entry.SourceVersion = "999"
						require.NoError(t, txn.Insert("translation_source_placements", &state))
					}
				}
			}
			txn.Commit()
			err = store.RecoverTranslationSourceAudit(t.Context(), *before)
			if corrupt == "missing-cells" {
				require.NoError(t, err)
				watermark, err = store.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
				require.NoError(t, err)
				require.Equal(t, before, watermark)
			} else {
				require.ErrorIs(t, err, datastore.ErrRepairRequired)
				_, err = store.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
				require.ErrorIs(t, err, datastore.ErrRepairRequired, "failed re-audit must invalidate its old watermark")
			}
			read := store.db.Txn(false)
			defer read.Abort()
			for i, placement := range placements {
				raw, err := read.First("translation_index_cell", "id", translationCellID(placement))
				require.NoError(t, err)
				if corrupt == "missing-cells" {
					require.NotNil(t, raw)
					require.Equal(t, placement.Token, raw.(*translationIndexCellRow).Cell.Token)
					require.False(t, raw.(*translationIndexCellRow).Cell.Retired)
				} else if i == 0 {
					require.Nil(t, raw, "preflight failure cannot leave a partial repair")
				}
			}
			after, err := store.GetTranslationMaintenance(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			current, err := store.GetTranslationBinding(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, binding, current)
			afterBounds, err := store.ResourceWatchJournal().Bounds(t.Context())
			require.NoError(t, err)
			require.Equal(t, bounds, afterBounds, "hint repair cannot manufacture public watch events")
		})
	}
}
