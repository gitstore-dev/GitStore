// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

func (s *scyllaDatastore) ListTranslationDependents(ctx context.Context, target datastore.TranslationTargetKey, targetUID, cursor string, limit int) (datastore.TranslationDependentPage, error) {
	return datastore.ReadTranslationDependentPage(ctx, target, targetUID, cursor, limit, s.getTranslationIndexHead, s.readTranslationIndexSegment)
}

func (s *scyllaDatastore) ListTranslations(ctx context.Context, namespace string, params datastore.PageParams) (*datastore.TranslationPage, error) {
	params, err := params.BoundedTranslationPage()
	if err != nil || namespace == "" {
		return nil, datastore.ErrInvalidArgument
	}
	ns, err := s.GetNamespaceByName(ctx, namespace)
	if err != nil {
		return nil, err
	}
	cursor := params.After
	if params.Last > 0 {
		cursor = params.Before
	}
	indexed, err := datastore.ReadTranslationNamespacePage(ctx, ns.UID, cursor, params.Last > 0, params.Limit(),
		s.getTranslationIndexHead, s.readTranslationSourceSegment)
	if err != nil {
		return nil, err
	}
	return datastore.ResolveTranslationNamespacePage(ctx, indexed, params, func(ctx context.Context, entry datastore.TranslationIndexEntry) (*datastore.Translation, error) {
		row, err := s.readTranslationSourceRow(ctx, mustParseUUID(ns.UID), entry.CreationTimestamp, mustParseUUID(entry.TranslationUID))
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if row.Phase == "Retired" {
			return nil, nil
		}
		if row.Phase != "Stable" {
			return nil, datastore.ErrRepairRequired
		}
		source := row.source()
		if source.Namespace != namespace || source.NamespaceID != ns.UID || datastore.ValidateTranslationSource(source) != nil {
			return nil, datastore.ErrRepairRequired
		}
		return source, nil
	})
}

func (s *scyllaDatastore) readTranslationSourceSegment(ctx context.Context, index datastore.TranslationIndexKey, position *datastore.PageCursor, backward bool, limit int) ([]datastore.TranslationIndexCell, error) {
	key := translationCellPartition(index)
	statement := "SELECT uid,creation_timestamp,source_version,reference_epoch,slot,reservation_token,retired FROM " + key.table + " WHERE " + key.predicate()
	if position != nil {
		comparison := "<"
		if backward {
			comparison = ">"
		}
		statement += " AND (creation_timestamp,uid)" + comparison + "(?,?)"
		key.values = append(key.values, position.CreatedAt, mustParseUUID(position.ID))
	}
	if backward {
		statement += " ORDER BY creation_timestamp ASC,uid ASC"
	}
	statement += " LIMIT ?"
	key.values = append(key.values, limit)
	// This range read returns derived hints only; source rows are checked with
	// SERIAL point reads before anything is returned as an accepted resource.
	iter := s.session.Session.Query(statement, key.values...).WithContext(ctx).Consistency(gocql.Quorum).PageSize(limit).Iter()
	var cell datastore.TranslationIndexCell
	var uid, token gocql.UUID
	var slot int
	var result []datastore.TranslationIndexCell
	for iter.Scan(&uid, &cell.Entry.CreationTimestamp, &cell.Entry.SourceVersion, &cell.Entry.ReferenceEpoch, &slot, &token, &cell.Retired) {
		if slot < 0 || slot >= datastore.MaxTranslationSegmentRows || token == (gocql.UUID{}) {
			_ = iter.Close()
			return nil, datastore.ErrRepairRequired
		}
		cell.Entry.TranslationUID, cell.Entry.Slot, cell.Token = uid.String(), uint32(slot), token.String()
		result = append(result, cell)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *scyllaDatastore) readTranslationIndexSegment(ctx context.Context, index datastore.TranslationIndexKey, after string, limit int) ([]datastore.TranslationIndexCell, error) {
	key := translationCellPartition(index)
	statement := "SELECT uid,creation_timestamp,source_version,reference_epoch,slot,reservation_token,retired FROM " + key.table + " WHERE " + key.predicate()
	if after != "" {
		statement += " AND uid>?"
		key.values = append(key.values, mustParseUUID(after))
	}
	statement += " LIMIT ?"
	key.values = append(key.values, limit)
	iter := s.session.Session.Query(statement, key.values...).WithContext(ctx).Consistency(gocql.Serial).PageSize(limit).Iter()
	var cell datastore.TranslationIndexCell
	var uid, token gocql.UUID
	var slot int
	var result []datastore.TranslationIndexCell
	for iter.Scan(&uid, &cell.Entry.CreationTimestamp, &cell.Entry.SourceVersion, &cell.Entry.ReferenceEpoch, &slot, &token, &cell.Retired) {
		if slot < 0 || slot >= datastore.MaxTranslationSegmentRows || token == (gocql.UUID{}) {
			_ = iter.Close()
			return nil, datastore.ErrRepairRequired
		}
		cell.Entry.TranslationUID, cell.Entry.Slot, cell.Token = uid.String(), uint32(slot), token.String()
		result = append(result, cell)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return result, nil
}
