// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/json"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	gomemdb "github.com/hashicorp/go-memdb"
)

func (m *memdbDatastore) RecoverTranslationSourceAudit(ctx context.Context, hint datastore.TranslationMaintenanceIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateTranslationSourceAuditHint(hint); err != nil {
		return err
	}
	current, err := m.GetTranslationMaintenance(ctx, hint.TranslationUID)
	if err != nil {
		return err
	}
	if !hint.CreationTimestamp.Equal(current.CreationTimestamp) {
		return datastore.ErrConflict
	}
	hint.CreationTimestamp = current.CreationTimestamp
	if hint != *current {
		return datastore.ErrConflict
	}
	if err := m.writeTranslationSourceAuditWatermark(ctx, hint, ""); err != nil {
		return err
	}
	if err := m.RecoverTranslation(ctx, hint.TranslationUID); err != nil {
		return err
	}
	return m.writeTranslationSourceAuditWatermark(ctx, hint, hint.ResourceVersion)
}

func (m *memdbDatastore) writeTranslationSourceAuditWatermark(ctx context.Context, hint datastore.TranslationMaintenanceIntent, watermark string) error {
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	current, err := translationMaintenanceInTxn(txn, hint.TranslationUID)
	if err != nil {
		return err
	}
	if !hint.CreationTimestamp.Equal(current.CreationTimestamp) {
		return datastore.ErrConflict
	}
	hint.CreationTimestamp = current.CreationTimestamp
	if hint != *current {
		return datastore.ErrConflict
	}
	if current.Phase == "Retired" {
		raw, err := txn.First("deleted_translation_uid", "id", hint.TranslationUID)
		if err != nil {
			return err
		}
		next := *raw.(*deletedTranslationUID)
		next.RepairWatermark = watermark
		if err := txn.Insert("deleted_translation_uid", &next); err != nil {
			return err
		}
	} else {
		raw, err := txn.First("translation_source_placements", "id", hint.TranslationUID)
		if err != nil {
			return err
		}
		next := *raw.(*translationSourcePlacements)
		next.RepairWatermark = watermark
		if err := txn.Insert("translation_source_placements", &next); err != nil {
			return err
		}
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetTranslationSourceAuditWatermark(ctx context.Context, uid string) (*datastore.TranslationMaintenanceIntent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	state, err := translationMaintenanceInTxn(txn, uid)
	if err != nil {
		return nil, err
	}
	table := "translation_source_placements"
	if state.Phase == "Retired" {
		table = "deleted_translation_uid"
	}
	raw, err := txn.First(table, "id", uid)
	if err != nil {
		return nil, err
	}
	watermark := ""
	switch row := raw.(type) {
	case *translationSourcePlacements:
		watermark = row.RepairWatermark
	case *deletedTranslationUID:
		watermark = row.RepairWatermark
	}
	if watermark == "" || watermark != state.ResourceVersion {
		return nil, datastore.ErrRepairRequired
	}
	return state, nil
}

func (m *memdbDatastore) ListTranslationSourceAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationSourceAuditPage, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationSourceAuditPage{}, err
	}
	payload, err := datastore.DecodeTranslationSourceAuditCursor(namespace, "memdb", cursor, limit)
	if err != nil {
		return datastore.TranslationSourceAuditPage{}, err
	}
	position := struct{ Phase, After string }{Phase: "Live"}
	if len(payload) != 0 && json.Unmarshal(payload, &position) != nil {
		return datastore.TranslationSourceAuditPage{}, datastore.ErrInvalidArgument
	}
	if position.Phase != "Live" && position.Phase != "Retired" {
		return datastore.TranslationSourceAuditPage{}, datastore.ErrInvalidArgument
	}
	if position.After != "" {
		if id, err := uuid.Parse(position.After); err != nil || id.String() != position.After {
			return datastore.TranslationSourceAuditPage{}, datastore.ErrInvalidArgument
		}
	}
	table := "translation"
	if position.Phase == "Retired" {
		table = "deleted_translation_uid"
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	var rows gomemdb.ResultIterator
	if position.After == "" {
		rows, err = txn.Get(table, "id")
	} else {
		rows, err = txn.LowerBound(table, "id", position.After)
	}
	if err != nil {
		return datastore.TranslationSourceAuditPage{}, err
	}
	page := datastore.TranslationSourceAuditPage{}
	for raw := rows.Next(); raw != nil; raw = rows.Next() {
		if err := ctx.Err(); err != nil {
			return datastore.TranslationSourceAuditPage{}, err
		}
		var uid, owner string
		switch source := raw.(type) {
		case *datastore.Translation:
			uid, owner = source.UID, source.NamespaceID
		case *deletedTranslationUID:
			uid, owner = source.UID, source.NamespaceUID
		default:
			return datastore.TranslationSourceAuditPage{}, datastore.ErrRepairRequired
		}
		if uid == position.After {
			continue
		}
		position.After = uid
		page.Scanned++
		if owner == namespace {
			state, err := translationMaintenanceInTxn(txn, uid)
			if err != nil {
				return datastore.TranslationSourceAuditPage{}, err
			}
			page.Items = append(page.Items, *state)
		}
		if page.Scanned == limit {
			payload, err := json.Marshal(position)
			if err != nil {
				return datastore.TranslationSourceAuditPage{}, err
			}
			page.Next, err = datastore.EncodeTranslationSourceAuditCursor(namespace, "memdb", payload)
			return page, err
		}
	}
	if position.Phase == "Live" {
		position.Phase, position.After = "Retired", ""
		payload, err := json.Marshal(position)
		if err != nil {
			return datastore.TranslationSourceAuditPage{}, err
		}
		page.Next, err = datastore.EncodeTranslationSourceAuditCursor(namespace, "memdb", payload)
		return page, err
	}
	return page, nil
}

var _ datastore.TranslationSourceAuditStore = (*memdbDatastore)(nil)

func (m *memdbDatastore) ListTranslationIndexAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationIndexAuditPage, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexAuditPage{}, err
	}
	position, err := datastore.DecodeTranslationIndexAuditCursor(namespace, "memdb", cursor, limit)
	if err != nil {
		return datastore.TranslationIndexAuditPage{}, err
	}
	after := string(position)
	if after != "" {
		if id, err := uuid.Parse(after); err != nil || id.String() != after {
			return datastore.TranslationIndexAuditPage{}, datastore.ErrInvalidArgument
		}
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	var rows gomemdb.ResultIterator
	if after == "" {
		rows, err = txn.Get("translation_index_reservation", "id")
	} else {
		rows, err = txn.LowerBound("translation_index_reservation", "id", after)
	}
	if err != nil {
		return datastore.TranslationIndexAuditPage{}, err
	}
	page := datastore.TranslationIndexAuditPage{}
	for raw := rows.Next(); raw != nil; raw = rows.Next() {
		if err := ctx.Err(); err != nil {
			return datastore.TranslationIndexAuditPage{}, err
		}
		row := raw.(*translationIndexReservationRow)
		if row.ID == after {
			continue
		}
		after = row.ID
		page.Scanned++
		if row.Reservation.Index.NamespaceUID == namespace && datastore.IsTranslationSourceIndex(row.Reservation.Index.Kind) {
			page.Tokens = append(page.Tokens, row.ID)
		}
		if page.Scanned == limit {
			page.Next, err = datastore.EncodeTranslationIndexAuditCursor(namespace, "memdb", []byte(after))
			return page, err
		}
	}
	return page, nil
}

func (m *memdbDatastore) RecoverTranslationIndexAudit(ctx context.Context, namespace, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := datastore.DecodeTranslationIndexAuditCursor(namespace, "memdb", "", 1); err != nil {
		return err
	}
	if id, err := uuid.Parse(token); err != nil || id.String() != token {
		return datastore.ErrInvalidArgument
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	raw, err := txn.First("translation_index_reservation", "id", token)
	if err != nil || raw == nil {
		return notFoundOrErr(err)
	}
	record := raw.(*translationIndexReservationRow).Reservation
	if record.Index.NamespaceUID != namespace || !datastore.IsTranslationSourceIndex(record.Index.Kind) {
		return datastore.ErrInvalidArgument
	}
	raw, err = txn.First("translation", "id", record.Entry.TranslationUID)
	if err != nil {
		return err
	}
	retire := true
	if raw == nil {
		raw, err = txn.First("deleted_translation_uid", "id", record.Entry.TranslationUID)
		if err != nil {
			return err
		}
		if raw == nil {
			return datastore.ErrRepairRequired
		}
		retired := raw.(*deletedTranslationUID)
		if retired.NamespaceUID != namespace || !retired.CreationTimestamp.Equal(record.Entry.CreationTimestamp) {
			return datastore.ErrRepairRequired
		}
	} else {
		source := raw.(*datastore.Translation)
		if source.NamespaceID != namespace || !source.CreationTimestamp.Equal(record.Entry.CreationTimestamp) {
			return datastore.ErrRepairRequired
		}
		raw, err = txn.First("translation_binding", "id", source.UID)
		if err != nil {
			return err
		}
		if raw == nil {
			return datastore.ErrRepairRequired
		}
		binding := raw.(*datastore.TranslationBinding)
		raw, err = txn.First("translation_source_placements", "id", source.UID)
		if err != nil {
			return err
		}
		if raw == nil {
			return datastore.ErrRepairRequired
		}
		placements := raw.(*translationSourcePlacements).Placements
		if err := datastore.ValidateTranslationSourcePlacements(source, binding, placements); err != nil {
			return err
		}
		for _, placement := range placements {
			if placement.Token == token {
				if !datastore.TranslationReservationMatchesPlacement(record, placement) {
					return datastore.ErrRepairRequired
				}
				retire = false
				break
			}
		}
	}
	if err := changeTranslationIndexCellInTxn(txn, record, retire); err != nil {
		return err
	}
	txn.Commit()
	return nil
}

var _ datastore.TranslationIndexAuditStore = (*memdbDatastore)(nil)
