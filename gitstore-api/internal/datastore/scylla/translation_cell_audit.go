// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

func (s *scyllaDatastore) ListTranslationIndexCellAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationIndexCellAuditPage, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexCellAuditPage{}, err
	}
	kind, state, err := datastore.DecodeTranslationIndexCellAuditCursor(namespace, "scylla", cursor, limit)
	if err != nil {
		return datastore.TranslationIndexCellAuditPage{}, err
	}
	table := "translations_by_namespace"
	columns := "namespace_uid,shard,segment,creation_timestamp,uid,source_version,reference_epoch,slot,reservation_token,retired"
	var owner, uid, token, targetUID gocql.UUID
	var shard int8
	var segment int64
	var slot int
	var apiVersion, targetKind, nameKey string
	item := datastore.TranslationIndexAuditCell{}
	dest := []any{&owner, &shard, &segment, &item.Cell.Entry.CreationTimestamp, &uid,
		&item.Cell.Entry.SourceVersion, &item.Cell.Entry.ReferenceEpoch, &slot, &token, &item.Cell.Retired}
	switch kind {
	case datastore.TranslationTargetNameIndex:
		table = "translation_dependents_by_target_name"
		columns += ",api_version,kind,name"
		dest = append(dest, &apiVersion, &targetKind, &nameKey)
	case datastore.TranslationTargetUIDIndex:
		table = "translation_dependents_by_target_uid"
		columns += ",target_uid"
		dest = append(dest, &targetUID)
	}
	rows := s.session.Session.Query("SELECT " + columns + " FROM " + table).
		WithContext(ctx).Consistency(gocql.Quorum).PageSize(limit).PageState(state).Iter()
	page := datastore.TranslationIndexCellAuditPage{}
	for rows.Scan(dest...) {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return datastore.TranslationIndexCellAuditPage{}, err
		}
		page.Scanned++
		if page.Scanned > limit {
			_ = rows.Close()
			return datastore.TranslationIndexCellAuditPage{}, datastore.ErrRepairRequired
		}
		if owner.String() != namespace {
			continue
		}
		if shard < 0 || shard >= datastore.TranslationIndexShards || segment < 0 ||
			slot < 0 || slot >= datastore.MaxTranslationSegmentRows {
			_ = rows.Close()
			return datastore.TranslationIndexCellAuditPage{}, datastore.ErrRepairRequired
		}
		item.Index = datastore.TranslationIndexKey{Kind: kind, NamespaceUID: namespace, Shard: uint8(shard), Segment: uint64(segment)}
		item.Cell.Token, item.Cell.Entry.TranslationUID, item.Cell.Entry.Slot = token.String(), uid.String(), uint32(slot)
		switch kind {
		case datastore.TranslationTargetNameIndex:
			// The physical name key is a digest, not a reversible resource name.
			// Missing or contradictory reservation authority must stop this scan.
			record, err := s.GetTranslationIndexReservation(ctx, item.Cell.Token)
			if err != nil {
				_ = rows.Close()
				if errors.Is(err, datastore.ErrNotFound) {
					return datastore.TranslationIndexCellAuditPage{}, datastore.ErrRepairRequired
				}
				return datastore.TranslationIndexCellAuditPage{}, err
			}
			item.Index.Target = record.Index.Target
			if record.Index.Target.APIVersion != apiVersion || record.Index.Target.Kind != targetKind ||
				translationHeadTarget(record.Index) != nameKey || !datastore.TranslationAuditCellMatchesReservation(item, record) {
				_ = rows.Close()
				return datastore.TranslationIndexCellAuditPage{}, datastore.ErrRepairRequired
			}
		case datastore.TranslationTargetUIDIndex:
			item.Index.TargetUID = targetUID.String()
		}
		if datastore.ValidateTranslationIndexAuditCell(namespace, item) != nil {
			_ = rows.Close()
			return datastore.TranslationIndexCellAuditPage{}, datastore.ErrRepairRequired
		}
		page.Items = append(page.Items, item)
	}
	next := append([]byte(nil), rows.PageState()...)
	if err := rows.Close(); err != nil {
		return datastore.TranslationIndexCellAuditPage{}, err
	}
	if len(next) == 0 {
		switch kind {
		case datastore.TranslationNamespaceIndex:
			kind = datastore.TranslationTargetNameIndex
		case datastore.TranslationTargetNameIndex:
			kind = datastore.TranslationTargetUIDIndex
		default:
			return page, nil
		}
	}
	page.Next, err = datastore.EncodeTranslationIndexCellAuditCursor(namespace, "scylla", kind, next)
	return page, err
}

func (s *scyllaDatastore) RecoverTranslationIndexCellAudit(ctx context.Context, namespace string, observed datastore.TranslationIndexAuditCell) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateTranslationIndexAuditCell(namespace, observed); err != nil {
		return err
	}
	record, err := s.GetTranslationIndexReservation(ctx, observed.Cell.Token)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.ErrRepairRequired
	}
	if err != nil {
		return err
	}
	if !datastore.TranslationAuditCellMatchesReservation(observed, record) {
		return datastore.ErrRepairRequired
	}
	current, err := s.getTranslationIndexCell(ctx, translationCellKey(record), record.Entry.TranslationUID)
	if err != nil {
		return err
	}
	if current == nil || !datastore.TranslationAuditCellsEqual(observed.Cell, *current) {
		return datastore.ErrConflict
	}
	return s.RecoverTranslationIndexAudit(ctx, namespace, record.Token)
}

var _ datastore.TranslationIndexCellAuditStore = (*scyllaDatastore)(nil)
