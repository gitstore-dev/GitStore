// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

func (m *memdbDatastore) ListTranslationIndexCellAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationIndexCellAuditPage, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexCellAuditPage{}, err
	}
	kind, state, err := datastore.DecodeTranslationIndexCellAuditCursor(namespace, "memdb", cursor, limit)
	if err != nil {
		return datastore.TranslationIndexCellAuditPage{}, err
	}
	if kind != datastore.TranslationNamespaceIndex || len(state) > 512 {
		return datastore.TranslationIndexCellAuditPage{}, datastore.ErrInvalidArgument
	}
	after := string(state)
	txn := m.db.Txn(false)
	defer txn.Abort()
	var rows gomemdb.ResultIterator
	if after == "" {
		rows, err = txn.Get("translation_index_cell", "id")
	} else {
		rows, err = txn.LowerBound("translation_index_cell", "id", after)
	}
	if err != nil {
		return datastore.TranslationIndexCellAuditPage{}, err
	}
	page := datastore.TranslationIndexCellAuditPage{}
	for raw := rows.Next(); raw != nil; raw = rows.Next() {
		if err := ctx.Err(); err != nil {
			return datastore.TranslationIndexCellAuditPage{}, err
		}
		row := raw.(*translationIndexCellRow)
		if row.ID == after {
			continue
		}
		after = row.ID
		page.Scanned++
		if row.Index.NamespaceUID == "" {
			return datastore.TranslationIndexCellAuditPage{}, datastore.ErrRepairRequired
		}
		if row.Index.NamespaceUID == namespace && datastore.IsTranslationSourceIndex(row.Index.Kind) {
			item := datastore.TranslationIndexAuditCell{Index: row.Index, Cell: row.Cell}
			placement := datastore.TranslationIndexReservation{Index: row.Index, Entry: row.Cell.Entry}
			if datastore.ValidateTranslationIndexAuditCell(namespace, item) != nil || row.ID != translationCellID(placement) {
				return datastore.TranslationIndexCellAuditPage{}, datastore.ErrRepairRequired
			}
			page.Items = append(page.Items, item)
		}
		if page.Scanned == limit {
			page.Next, err = datastore.EncodeTranslationIndexCellAuditCursor(namespace, "memdb", kind, []byte(after))
			return page, err
		}
	}
	return page, nil
}

func (m *memdbDatastore) RecoverTranslationIndexCellAudit(ctx context.Context, namespace string, observed datastore.TranslationIndexAuditCell) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateTranslationIndexAuditCell(namespace, observed); err != nil {
		return err
	}
	record, err := m.GetTranslationIndexReservation(ctx, observed.Cell.Token)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.ErrRepairRequired
	}
	if err != nil {
		return err
	}
	if !datastore.TranslationAuditCellMatchesReservation(observed, record) {
		return datastore.ErrRepairRequired
	}
	txn := m.db.Txn(false)
	raw, err := txn.First("translation_index_cell", "id", translationCellID(record))
	txn.Abort()
	if err != nil {
		return err
	}
	if raw == nil || !datastore.TranslationAuditCellsEqual(observed.Cell, raw.(*translationIndexCellRow).Cell) {
		return datastore.ErrConflict
	}
	return m.RecoverTranslationIndexAudit(ctx, namespace, record.Token)
}

var _ datastore.TranslationIndexCellAuditStore = (*memdbDatastore)(nil)
