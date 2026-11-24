// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/require"
)

func TestTranslationSourceAuditFindsMissingAnchorAndUIDLocator(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	store := first.(datastore.TranslationStore)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	before, err := first.(datastore.TranslationIndexStore).GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	raw := newRawSession(t)
	key := []any{mustTranslationUUID(t, source.NamespaceID), source.CreationTimestamp, mustTranslationUUID(t, source.UID)}
	var intent string
	require.NoError(t, raw.Query(
		"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?", key...,
	).Scan(&intent))
	var placements []datastore.TranslationIndexReservation
	require.NoError(t, json.Unmarshal([]byte(intent), &placements))
	anchor := placements[0]
	require.Equal(t, datastore.TranslationNamespaceIndex, anchor.Index.Kind)
	query, args := translationPublishedCellQuery(t, anchor.Index, anchor.Entry)
	require.NoError(t, raw.Query("DELETE"+query[strings.Index(query, " FROM "):], args...).Exec())
	require.NoError(t, raw.Query("DELETE FROM translations_by_uid WHERE uid=?", mustTranslationUUID(t, source.UID)).Exec())
	_, err = second.(datastore.TranslationStore).GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	audit := second.(datastore.TranslationSourceAuditStore)
	var discovered []datastore.TranslationMaintenanceIntent
	cursor, finished := "", false
	for range 256 {
		page, err := audit.ListTranslationSourceAudit(t.Context(), source.NamespaceID, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Scanned, 1)
		discovered = append(discovered, page.Items...)
		if page.Next == "" {
			finished = true
			break
		}
		require.NotEqual(t, cursor, page.Next)
		cursor = page.Next
	}
	require.True(t, finished)
	require.Equal(t, []datastore.TranslationMaintenanceIntent{*before}, discovered)
	var unchanged string
	require.NoError(t, raw.Query(
		"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?", key...,
	).Scan(&unchanged))
	require.Equal(t, intent, unchanged, "discovery must not mutate source or repair missing authority speculatively")
	require.ErrorIs(t, second.(datastore.TranslationIndexAuditStore).RecoverTranslationIndexAudit(t.Context(), source.NamespaceID, anchor.Token),
		datastore.ErrRepairRequired, "index audit must preserve missing live UID authority rather than treating it as source absence")
	require.ErrorIs(t, audit.RecoverTranslationSourceAudit(t.Context(), discovered[0]), datastore.ErrRepairRequired,
		"a live source cannot reconstruct lost frozen UID/name-vacancy authority")
	_, err = store.GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestTranslationSourceAuditRestoresRetiredUIDAuthority(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	source.Finalizers = nil
	store := first.(datastore.TranslationStore)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "1"))
	retired, err := first.(datastore.TranslationIndexStore).GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, "Retired", retired.Phase)
	replacement := *source
	replacement.UID = newID()
	require.NoError(t, store.CreateTranslation(t.Context(), &replacement))
	raw := newRawSession(t)
	require.NoError(t, raw.Query("DELETE FROM translations_by_uid WHERE uid=?", mustTranslationUUID(t, source.UID)).Exec())
	require.NoError(t, second.(datastore.TranslationSourceAuditStore).RecoverTranslationSourceAudit(t.Context(), *retired))
	watermark, err := first.(datastore.TranslationSourceAuditStore).GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, retired, watermark, "the fenced watermark must survive an independent datastore instance")
	after, err := first.(datastore.TranslationIndexStore).GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, retired, after)
	_, err = second.(datastore.TranslationStore).GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	require.ErrorIs(t, store.CreateTranslation(t.Context(), source), datastore.ErrAlreadyExists,
		"retained retired source authority must fence a delayed reuse after restoring its UID tombstone")
	current, err := store.GetTranslationByName(t.Context(), replacement.Namespace, replacement.Name)
	require.NoError(t, err)
	require.Equal(t, &replacement, current, "repairing an old retirement must not release or rewrite a replacement name owner")
}

func TestTranslationSourceAuditInvalidatesPriorWatermarkBeforeFailedRepair(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	store := first.(datastore.TranslationStore)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	before, err := first.(datastore.TranslationIndexStore).GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	audit := first.(datastore.TranslationSourceAuditStore)
	require.NoError(t, audit.RecoverTranslationSourceAudit(t.Context(), *before))
	watermark, err := second.(datastore.TranslationSourceAuditStore).GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, watermark)
	raw := newRawSession(t)
	var intent string
	require.NoError(t, raw.Query(
		"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
		mustTranslationUUID(t, source.NamespaceID), source.CreationTimestamp, mustTranslationUUID(t, source.UID),
	).Scan(&intent))
	var placements []datastore.TranslationIndexReservation
	require.NoError(t, json.Unmarshal([]byte(intent), &placements))
	require.GreaterOrEqual(t, len(placements), 2)
	require.NoError(t, raw.Query("DELETE FROM translation_index_reservations_by_token WHERE reservation_token=?",
		mustTranslationUUID(t, placements[1].Token)).Exec())
	require.ErrorIs(t, second.(datastore.TranslationSourceAuditStore).RecoverTranslationSourceAudit(t.Context(), *before),
		datastore.ErrRepairRequired)
	_, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "an older successful audit cannot certify failed repair")
	cellAudit := second.(datastore.TranslationIndexCellAuditStore)
	cursor, detected := "", false
	for range 1024 {
		page, err := cellAudit.ListTranslationIndexCellAudit(t.Context(), source.NamespaceID, cursor, 1)
		if err != nil {
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Empty(t, page.Next)
			detected = true
			break
		}
		if page.Next == "" {
			break
		}
		require.NotEqual(t, cursor, page.Next)
		cursor = page.Next
	}
	require.True(t, detected, "independent physical-cell scan must find the missing name-reservation authority")
	unchanged, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, unchanged)
	after, err := first.(datastore.TranslationIndexStore).GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after, "invalidating private audit authority cannot change accepted source/private versions")
}

func TestTranslationSourceAuditDoesNotOverwriteContradictoryUIDLifetime(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	source.Finalizers = nil
	store := first.(datastore.TranslationStore)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "1"))
	retired, err := first.(datastore.TranslationIndexStore).GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	raw := newRawSession(t)
	different := mustTranslationUUID(t, newID())
	require.NoError(t, raw.Query("UPDATE translations_by_uid SET namespace_uid=? WHERE uid=?", different, mustTranslationUUID(t, source.UID)).Exec())
	require.ErrorIs(t, second.(datastore.TranslationSourceAuditStore).RecoverTranslationSourceAudit(t.Context(), *retired),
		datastore.ErrRepairRequired)
	var retained gocql.UUID
	require.NoError(t, raw.Query("SELECT namespace_uid FROM translations_by_uid WHERE uid=?", mustTranslationUUID(t, source.UID)).Scan(&retained))
	require.Equal(t, different, retained, "a conflicting authority must not be overwritten from a discovery hint")
}

func TestTranslationSourceAuditDoesNotInferRetirementFromMissingSource(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), source))
	raw := newRawSession(t)
	uid, namespace := mustTranslationUUID(t, source.UID), mustTranslationUUID(t, source.NamespaceID)
	require.NoError(t, raw.Query("DELETE FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
		namespace, source.CreationTimestamp, uid).Exec())
	hint, err := second.(datastore.TranslationIndexStore).GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, "Unpublished", hint.Phase)
	require.ErrorIs(t, second.(datastore.TranslationSourceAuditStore).RecoverTranslationSourceAudit(t.Context(), *hint),
		datastore.ErrRepairRequired, "an absent authoritative row does not prove a never-published source")
	var retired bool
	require.NoError(t, raw.Query("SELECT retired FROM translations_by_uid WHERE uid=?", uid).Scan(&retired))
	require.False(t, retired, "audit must retain the existing UID authority for explicit repair")
	replacement := *source
	replacement.UID = newID()
	require.ErrorIs(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), &replacement),
		datastore.ErrAlreadyExists, "audit must not release the existing name claim")
	var count int
	require.NoError(t, raw.Query("SELECT count(*) FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
		namespace, source.CreationTimestamp, uid).Scan(&count))
	require.Zero(t, count, "audit must not invent a retained retired source from an absence hint")
}

func TestTranslationMaintenanceRestoresMissingCellsWithoutNewAllocations(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), source))
	bindings := first.(datastore.TranslationBindingStore)
	binding, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	binding.ResourceVersion, binding.Epochs[0].BoundUID = "2", newID()
	require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), binding, "1"))
	raw := newRawSession(t)
	var intent string
	require.NoError(t, raw.Query(
		"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
		mustTranslationUUID(t, source.NamespaceID), source.CreationTimestamp, mustTranslationUUID(t, source.UID),
	).Scan(&intent))
	var placements []datastore.TranslationIndexReservation
	require.NoError(t, json.Unmarshal([]byte(intent), &placements))
	require.Len(t, placements, 3)
	maintenance := first.(datastore.TranslationIndexStore)
	before, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	reservations := make([]datastore.TranslationIndexReservation, len(placements))
	for i, placement := range placements {
		reservations[i], err = maintenance.GetTranslationIndexReservation(t.Context(), placement.Token)
		require.NoError(t, err)
		query, args := translationPublishedCellQuery(t, placement.Index, placement.Entry)
		require.NoError(t, raw.Query("DELETE"+query[strings.Index(query, " FROM "):], args...).Exec())
	}
	var workers sync.WaitGroup
	errs := make(chan error, 2)
	for _, backend := range []datastore.Datastore{first, second} {
		workers.Go(func() {
			errs <- backend.(datastore.TranslationIndexStore).RecoverTranslation(t.Context(), source.UID)
		})
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	after, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after, "hint repair cannot manufacture a new source/private version")
	for i, placement := range placements {
		query, args := translationPublishedCellQuery(t, placement.Index, placement.Entry)
		query = strings.Replace(query, "SELECT reservation_token,retired", "SELECT source_version,reference_epoch,slot,reservation_token,retired", 1)
		var version, epoch string
		var slot int
		var token gocql.UUID
		var retired bool
		require.NoError(t, raw.Query(query, args...).Scan(&version, &epoch, &slot, &token, &retired))
		require.Equal(t, placement.Entry.SourceVersion, version)
		require.Equal(t, placement.Entry.ReferenceEpoch, epoch)
		require.Equal(t, int(placement.Entry.Slot), slot)
		require.Equal(t, placement.Token, token.String())
		require.False(t, retired)
		current, err := maintenance.GetTranslationIndexReservation(t.Context(), placement.Token)
		require.NoError(t, err)
		require.Equal(t, reservations[i], current, "repair reuses the already committed allocation")
	}
	recovered, err := second.(datastore.TranslationBindingStore).GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, binding, recovered)
}

func TestTranslationMaintenanceRejectsRetiredOrContradictoryLivePlacements(t *testing.T) {
	for _, corrupt := range []string{"retired-cell", "retired-reservation", "contradictory-epoch", "forged-placement", "missing-head"} {
		t.Run(corrupt, func(t *testing.T) {
			backend := newTestStore(t)
			source := newTranslationCreationSource(t, backend)
			store := backend.(datastore.TranslationStore)
			require.NoError(t, store.CreateTranslation(t.Context(), source))
			raw := newRawSession(t)
			var intent string
			key := []any{mustTranslationUUID(t, source.NamespaceID), source.CreationTimestamp, mustTranslationUUID(t, source.UID)}
			require.NoError(t, raw.Query(
				"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?", key...,
			).Scan(&intent))
			var placements []datastore.TranslationIndexReservation
			require.NoError(t, json.Unmarshal([]byte(intent), &placements))
			selected := placements[1]
			query, args := translationPublishedCellQuery(t, selected.Index, selected.Entry)
			switch corrupt {
			case "missing-head":
				anchor := placements[0]
				require.NoError(t, raw.Query(
					"DELETE FROM translation_index_heads_by_shard WHERE index_kind=? AND namespace_uid=? AND target_key='' AND shard=?",
					string(anchor.Index.Kind), mustTranslationUUID(t, source.NamespaceID), int8(anchor.Index.Shard),
				).Exec())
			case "retired-cell":
				statement := "UPDATE" + query[strings.Index(query, " FROM ")+5:]
				statement = strings.Replace(statement, " WHERE ", " SET retired=true WHERE ", 1)
				require.NoError(t, raw.Query(statement, args...).Exec())
			case "retired-reservation":
				require.NoError(t, backend.(datastore.TranslationIndexStore).RemoveTranslationIndex(t.Context(), selected))
			case "contradictory-epoch":
				statement := "UPDATE" + query[strings.Index(query, " FROM ")+5:]
				statement = strings.Replace(statement, " WHERE ", " SET reference_epoch='contradiction' WHERE ", 1)
				require.NoError(t, raw.Query(statement, args...).Exec())
			case "forged-placement":
				placements[1].Entry.SourceVersion = "999"
				encoded, err := json.Marshal(placements)
				require.NoError(t, err)
				require.NoError(t, raw.Query(
					"UPDATE translations_by_key SET maintenance_intent=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
					append([]any{string(encoded)}, key...)...,
				).Exec())
			}
			require.ErrorIs(t, backend.(datastore.TranslationIndexStore).RecoverTranslation(t.Context(), source.UID), datastore.ErrRepairRequired)
			unchanged, err := store.GetTranslation(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, source, unchanged, "unprovable corruption cannot reset accepted source or binding history")
		})
	}
}

func mustTranslationUUID(t *testing.T, value string) gocql.UUID {
	t.Helper()
	uid, err := gocql.ParseUUID(value)
	require.NoError(t, err)
	return uid
}
