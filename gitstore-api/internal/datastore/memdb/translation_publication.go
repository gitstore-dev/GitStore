// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

type translationIndexCellRow struct {
	ID, NamespaceOrder string
	Index              datastore.TranslationIndexKey
	Cell               datastore.TranslationIndexCell
}

func translationCellTable() *gomemdb.TableSchema {
	schema := translationAllocationTable("translation_index_cell")
	schema.Indexes["namespace_order"] = &gomemdb.IndexSchema{
		Name: "namespace_order", Unique: true, AllowMissing: true,
		Indexer: &gomemdb.StringFieldIndex{Field: "NamespaceOrder"},
	}
	return schema
}

func translationCellID(placement datastore.TranslationIndexReservation) string {
	key := translationHeadID(placement.Index) + "/" + placement.Entry.TranslationUID
	if placement.Index.Kind == datastore.TranslationNamespaceIndex {
		key += "/" + placement.Entry.CreationTimestamp.UTC().Format(time.RFC3339Nano)
	}
	return key
}

func (m *memdbDatastore) CommitTranslationIndex(ctx context.Context, placement datastore.TranslationIndexReservation) error {
	return m.changeTranslationIndexCell(ctx, placement, false)
}

func (m *memdbDatastore) RemoveTranslationIndex(ctx context.Context, placement datastore.TranslationIndexReservation) error {
	return m.changeTranslationIndexCell(ctx, placement, true)
}

func (m *memdbDatastore) changeTranslationIndexCell(ctx context.Context, supplied datastore.TranslationIndexReservation, retire bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	if err := changeTranslationIndexCellInTxn(txn, supplied, retire); err != nil {
		return err
	}
	txn.Commit()
	return nil
}

func changeTranslationIndexCellInTxn(txn *gomemdb.Txn, supplied datastore.TranslationIndexReservation, retire bool) error {
	raw, err := txn.First("translation_index_reservation", "id", supplied.Token)
	if err != nil || raw == nil {
		return notFoundOrErr(err)
	}
	record := raw.(*translationIndexReservationRow).Reservation
	if !datastore.TranslationReservationMatchesPlacement(record, supplied) {
		return datastore.ErrConflict
	}
	id := translationCellID(record)
	raw, err = txn.First("translation_index_cell", "id", id)
	if err != nil {
		return err
	}
	var current *datastore.TranslationIndexCell
	if raw != nil {
		current = &raw.(*translationIndexCellRow).Cell
	}
	next, err := datastore.PlanTranslationIndexCell(current, record, retire)
	if err != nil {
		return err
	}
	if next != nil {
		row := &translationIndexCellRow{ID: id, Index: record.Index, Cell: *next}
		if record.Index.Kind == datastore.TranslationNamespaceIndex {
			row.NamespaceOrder = string(namespaceOrderKey(translationHeadID(record.Index), record.Entry.CreationTimestamp, record.Entry.TranslationUID))
		}
		if err := txn.Insert("translation_index_cell", row); err != nil {
			return err
		}
	}
	phase := datastore.TranslationReservationCommitted
	if retire {
		phase = datastore.TranslationReservationRemoved
	}
	if record.Phase != phase {
		record.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(record.ResourceVersion)
		if err != nil {
			return err
		}
		record.Phase = phase
		if err := txn.Insert("translation_index_reservation", &translationIndexReservationRow{ID: record.Token, Reservation: record}); err != nil {
			return err
		}
	}
	return nil
}
