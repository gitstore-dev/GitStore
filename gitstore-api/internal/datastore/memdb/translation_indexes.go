// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	gomemdb "github.com/hashicorp/go-memdb"
)

type translationIndexHeadRow struct {
	ID   string
	Head datastore.TranslationIndexHead
}

type translationIndexReservationRow struct {
	ID          string
	Reservation datastore.TranslationIndexReservation
}

func translationAllocationTable(name string) *gomemdb.TableSchema {
	return &gomemdb.TableSchema{Name: name, Indexes: map[string]*gomemdb.IndexSchema{
		"id": {Name: "id", Unique: true, Indexer: &gomemdb.StringFieldIndex{Field: "ID"}},
	}}
}

func translationHeadID(index datastore.TranslationIndexKey) string {
	raw, _ := json.Marshal(index)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (m *memdbDatastore) ReserveTranslationIndex(ctx context.Context, index datastore.TranslationIndexKey, entry datastore.TranslationIndexEntry, token string, payloadBytes int64) (datastore.TranslationIndexReservation, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	if _, _, err := datastore.PlanTranslationIndexReservation(nil, index, entry, token, payloadBytes); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	reservation, err := reserveTranslationIndexInTxn(txn, index, entry, token, payloadBytes)
	if err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	txn.Commit()
	return reservation, nil
}

func reserveTranslationIndexInTxn(txn *gomemdb.Txn, index datastore.TranslationIndexKey, entry datastore.TranslationIndexEntry, token string, payloadBytes int64) (datastore.TranslationIndexReservation, error) {
	record, err := txn.First("translation_index_reservation", "id", token)
	if err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	if record != nil {
		reservation := record.(*translationIndexReservationRow).Reservation
		if !datastore.TranslationReservationMatchesRequest(reservation, index, entry, token, payloadBytes) {
			return datastore.TranslationIndexReservation{}, datastore.ErrConflict
		}
		if reservation.Phase != datastore.TranslationReservationReserved &&
			reservation.Phase != datastore.TranslationReservationCommitted && reservation.Phase != datastore.TranslationReservationRemoved {
			return datastore.TranslationIndexReservation{}, datastore.ErrRepairRequired
		}
		return reservation, nil
	}
	id := translationHeadID(index)
	raw, err := txn.First("translation_index_head", "id", id)
	if err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	var current *datastore.TranslationIndexHead
	if raw != nil {
		current = &raw.(*translationIndexHeadRow).Head
	}
	planned, reservation, err := datastore.PlanTranslationIndexReservation(current, index, entry, token, payloadBytes)
	if err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	reservation.Phase, reservation.ResourceVersion = datastore.TranslationReservationReserved, "2"
	finished, err := datastore.FinishTranslationIndexReservation(planned, reservation)
	if err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	if err := txn.Insert("translation_index_head", &translationIndexHeadRow{ID: id, Head: *finished}); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	if err := txn.Insert("translation_index_reservation", &translationIndexReservationRow{ID: token, Reservation: reservation}); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	return reservation, nil
}

func (m *memdbDatastore) GetTranslationIndexReservation(ctx context.Context, token string) (datastore.TranslationIndexReservation, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	if _, err := uuid.Parse(token); err != nil {
		return datastore.TranslationIndexReservation{}, datastore.ErrInvalidArgument
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("translation_index_reservation", "id", token)
	if err != nil || raw == nil {
		return datastore.TranslationIndexReservation{}, notFoundOrErr(err)
	}
	return raw.(*translationIndexReservationRow).Reservation, nil
}
