// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

type translationCellLocation struct {
	table   string
	columns []string
	values  []any
}

func translationCellPartition(index datastore.TranslationIndexKey) translationCellLocation {
	namespace := mustParseUUID(index.NamespaceUID)
	switch index.Kind {
	case datastore.TranslationTargetPendingIndex, datastore.TranslationTargetMaintenanceNamespaceIndex:
		return translationCellLocation{
			"translation_target_maintenance_by_segment",
			[]string{"index_kind", "namespace_uid", "shard", "segment"},
			[]any{string(index.Kind), namespace, int8(index.Shard), int64(index.Segment)},
		}
	case datastore.TranslationNamespaceIndex:
		return translationCellLocation{
			"translations_by_namespace",
			[]string{"namespace_uid", "shard", "segment"},
			[]any{namespace, int8(index.Shard), int64(index.Segment)},
		}
	case datastore.TranslationTargetNameIndex:
		return translationCellLocation{
			"translation_dependents_by_target_name",
			[]string{"namespace_uid", "api_version", "kind", "name", "shard", "segment"},
			[]any{namespace, index.Target.APIVersion, index.Target.Kind, translationHeadTarget(index), int8(index.Shard), int64(index.Segment)},
		}
	default:
		return translationCellLocation{
			"translation_dependents_by_target_uid",
			[]string{"namespace_uid", "target_uid", "shard", "segment"},
			[]any{namespace, mustParseUUID(index.TargetUID), int8(index.Shard), int64(index.Segment)},
		}
	}
}

func translationCellKey(record datastore.TranslationIndexReservation) translationCellLocation {
	key := translationCellPartition(record.Index)
	if record.Index.Kind == datastore.TranslationNamespaceIndex {
		key.columns = append(key.columns, "creation_timestamp")
		key.values = append(key.values, record.Entry.CreationTimestamp)
	}
	key.columns = append(key.columns, "uid")
	key.values = append(key.values, mustParseUUID(record.Entry.TranslationUID))
	return key
}

func (key translationCellLocation) predicate() string {
	return strings.Join(key.columns, "=? AND ") + "=?"
}

func (s *scyllaDatastore) getTranslationIndexCell(ctx context.Context, key translationCellLocation, uid string) (*datastore.TranslationIndexCell, error) {
	var cell datastore.TranslationIndexCell
	var token gocql.UUID
	var slot int
	cell.Entry.TranslationUID = uid
	err := s.session.Session.Query(
		"SELECT creation_timestamp,source_version,reference_epoch,slot,reservation_token,retired FROM "+key.table+" WHERE "+key.predicate(),
		key.values...,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(
		&cell.Entry.CreationTimestamp, &cell.Entry.SourceVersion, &cell.Entry.ReferenceEpoch, &slot, &token, &cell.Retired,
	)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if slot < 0 || slot >= datastore.MaxTranslationSegmentRows || token == (gocql.UUID{}) {
		return nil, datastore.ErrRepairRequired
	}
	cell.Entry.Slot, cell.Token = uint32(slot), token.String()
	return &cell, nil
}

func (s *scyllaDatastore) putTranslationIndexCell(ctx context.Context, key translationCellLocation, current, next *datastore.TranslationIndexCell) (bool, error) {
	columns := []string{"source_version", "reference_epoch", "slot", "reservation_token", "retired"}
	values := []any{next.Entry.SourceVersion, next.Entry.ReferenceEpoch, int(next.Entry.Slot), mustParseUUID(next.Token), next.Retired}
	if key.table != "translations_by_namespace" {
		columns = append(columns, "creation_timestamp")
		values = append(values, next.Entry.CreationTimestamp)
	}
	if current == nil {
		columns = append(columns, key.columns...)
		values = append(values, key.values...)
		return s.translationIndexCAS(ctx,
			"INSERT INTO "+key.table+" ("+strings.Join(columns, ",")+") VALUES ("+
				strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")+") IF NOT EXISTS", values...)
	}
	values = append(values, key.values...)
	values = append(values, int(current.Entry.Slot), mustParseUUID(current.Token))
	return s.translationIndexCAS(ctx,
		"UPDATE "+key.table+" SET "+strings.Join(columns, "=?,")+"=? WHERE "+key.predicate()+
			" IF slot=? AND reservation_token=?", values...)
}

func (s *scyllaDatastore) CommitTranslationIndex(ctx context.Context, placement datastore.TranslationIndexReservation) error {
	return s.changeTranslationIndexCell(ctx, placement, false)
}

func (s *scyllaDatastore) RemoveTranslationIndex(ctx context.Context, placement datastore.TranslationIndexReservation) error {
	return s.changeTranslationIndexCell(ctx, placement, true)
}

func (s *scyllaDatastore) changeTranslationIndexCell(ctx context.Context, supplied datastore.TranslationIndexReservation, retire bool) error {
	for range 4 {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := s.GetTranslationIndexReservation(ctx, supplied.Token)
		if err != nil {
			return err
		}
		if !datastore.TranslationReservationMatchesPlacement(record, supplied) {
			return datastore.ErrConflict
		}
		key := translationCellKey(record)
		current, err := s.getTranslationIndexCell(ctx, key, record.Entry.TranslationUID)
		if err != nil {
			return err
		}
		next, err := datastore.PlanTranslationIndexCell(current, record, retire)
		if err != nil {
			return err
		}
		if next != nil {
			applied, err := s.putTranslationIndexCell(ctx, key, current, next)
			if err != nil {
				return err
			}
			if !applied {
				continue
			}
		}
		phase := datastore.TranslationReservationCommitted
		if retire {
			phase = datastore.TranslationReservationRemoved
		}
		if record.Phase == phase {
			return nil
		}
		version, err := datastore.AdvanceTranslationIndexVersion(record.ResourceVersion)
		if err != nil {
			return err
		}
		applied, err := s.translationIndexCAS(ctx,
			"UPDATE translation_index_reservations_by_token SET resource_version=?,phase=? WHERE reservation_token=? IF resource_version=? AND phase=?",
			version, string(phase), mustParseUUID(record.Token), record.ResourceVersion, string(record.Phase))
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return datastore.ErrConflict
}
