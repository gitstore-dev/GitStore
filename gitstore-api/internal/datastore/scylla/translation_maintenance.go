// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

func (s *scyllaDatastore) GetTranslationMaintenance(ctx context.Context, uid string) (*datastore.TranslationMaintenanceIntent, error) {
	identity, err := s.readTranslationIdentity(ctx, uid)
	if err != nil {
		return nil, err
	}
	return s.translationMaintenanceAtKey(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
}

func (s *scyllaDatastore) translationMaintenanceAtKey(ctx context.Context, namespace gocql.UUID, created time.Time, uid gocql.UUID) (*datastore.TranslationMaintenanceIntent, error) {
	state := &datastore.TranslationMaintenanceIntent{
		NamespaceUID: namespace.String(), TranslationUID: uid.String(), CreationTimestamp: created,
	}
	err := s.session.Session.Query(
		"SELECT phase,coordination_version,resource_version FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
		namespace, created, uid,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&state.Phase, &state.ResourceVersion, &state.SourceVersion)
	if errors.Is(err, gocql.ErrNotFound) {
		state.Phase, state.ResourceVersion, state.SourceVersion = "Unpublished", "0", "1"
	} else if err != nil {
		return nil, err
	} else {
		if _, err := datastore.AdvanceTranslationIndexVersion(state.ResourceVersion); err != nil {
			return nil, err
		}
		switch state.Phase {
		case "Creating", "Stable", "Updating", "Deleting", "Retired":
		default:
			return nil, datastore.ErrRepairRequired
		}
	}
	state.ID = state.TranslationUID + "/" + state.ResourceVersion
	return state, nil
}

func (s *scyllaDatastore) ListTranslationMaintenance(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationMaintenancePage, error) {
	ns, err := s.GetNamespaceByName(ctx, namespace)
	if err != nil {
		return datastore.TranslationMaintenancePage{}, err
	}
	indexed, err := datastore.ReadTranslationNamespacePage(ctx, ns.UID, cursor, false, limit,
		s.getTranslationIndexHead, s.readTranslationSourceSegment)
	if err != nil {
		return datastore.TranslationMaintenancePage{}, err
	}
	result := datastore.TranslationMaintenancePage{Next: indexed.Next, Probes: indexed.Probes}
	for _, entry := range indexed.Items {
		state, err := s.translationMaintenanceAtKey(ctx, mustParseUUID(ns.UID), entry.CreationTimestamp, mustParseUUID(entry.TranslationUID))
		if err != nil {
			return datastore.TranslationMaintenancePage{}, err
		}
		result.Items = append(result.Items, *state)
	}
	return result, nil
}

func (s *scyllaDatastore) recoverStableTranslation(ctx context.Context, row *translationSourceRow) error {
	var placements []datastore.TranslationIndexReservation
	binding, err := translationRowBinding(row)
	if err != nil || len(row.MaintenanceIntent) > 12<<20 ||
		json.Unmarshal([]byte(row.MaintenanceIntent), &placements) != nil ||
		datastore.ValidateTranslationSourcePlacements(row.source(), binding, placements) != nil {
		return s.confirmStableTranslation(ctx, row, datastore.ErrRepairRequired)
	}
	var repairs []datastore.TranslationIndexReservation
	for _, placement := range placements {
		record, err := s.GetTranslationIndexReservation(ctx, placement.Token)
		if errors.Is(err, datastore.ErrNotFound) || errors.Is(err, datastore.ErrInvalidArgument) {
			err = datastore.ErrRepairRequired
		}
		if err != nil {
			return s.confirmStableTranslation(ctx, row, err)
		}
		if !datastore.TranslationReservationMatchesPlacement(record, placement) {
			return s.confirmStableTranslation(ctx, row, datastore.ErrRepairRequired)
		}
		head, err := s.getTranslationIndexHead(ctx, record.Index)
		if err != nil {
			return s.confirmStableTranslation(ctx, row, err)
		}
		cell, err := s.getTranslationIndexCell(ctx, translationCellKey(record), row.UID.String())
		if err != nil {
			return s.confirmStableTranslation(ctx, row, err)
		}
		next, err := datastore.PlanTranslationSourceIndexRepair(head, cell, record)
		if err != nil {
			return s.confirmStableTranslation(ctx, row, err)
		}
		if next != nil || record.Phase != datastore.TranslationReservationCommitted {
			repairs = append(repairs, record)
		}
	}
	if err := s.confirmStableTranslation(ctx, row, nil); err != nil {
		return err
	}
	for _, repair := range repairs {
		// Reuse the accepted allocation and its retained retirement fence.
		// A concurrent source transition can retire it, never resurrect it.
		if err := s.CommitTranslationIndex(ctx, repair); err != nil {
			return s.confirmStableTranslation(ctx, row, err)
		}
	}
	if err := s.syncTranslationSourceProjections(ctx, row.CoordinationVersion, nil, row.source()); err != nil {
		return s.confirmStableTranslation(ctx, row, err)
	}
	return s.confirmStableTranslation(ctx, row, nil)
}

func (s *scyllaDatastore) confirmStableTranslation(ctx context.Context, row *translationSourceRow, cause error) error {
	current, err := s.translationMaintenanceAtKey(ctx, row.NamespaceUID, row.CreationTimestamp, row.UID)
	if err != nil {
		return err
	}
	if current.Phase != "Stable" || current.ResourceVersion != row.CoordinationVersion {
		return datastore.ErrConflict
	}
	return cause
}
