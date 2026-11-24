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

func (s *scyllaDatastore) RecoverTranslationSourceAudit(ctx context.Context, hint datastore.TranslationMaintenanceIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateTranslationSourceAuditHint(hint); err != nil {
		return err
	}
	namespace, uid := mustParseUUID(hint.NamespaceUID), mustParseUUID(hint.TranslationUID)
	current, err := s.translationMaintenanceAtKey(ctx, namespace, hint.CreationTimestamp, uid)
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
	if current.Phase == "Unpublished" {
		// Audit discovery cannot turn a missing full source into proof that
		// creation never completed. Preserve remaining identity authority.
		return datastore.ErrRepairRequired
	}
	identity, err := s.readTranslationIdentity(ctx, hint.TranslationUID)
	if errors.Is(err, datastore.ErrNotFound) {
		if current.Phase != "Retired" {
			// Live UID metadata may retain a non-redundant frozen name-vacancy
			// token. A discovery hint cannot reconstruct or reset that authority.
			return datastore.ErrRepairRequired
		}
		// A retained Retired source is permanent proof, not an inferred absence.
		// Never overwrite another UID lifetime or clear existing retirement.
		_, err = s.translationIndexCAS(ctx,
			"INSERT INTO translations_by_uid (uid,namespace_uid,creation_timestamp,retired) VALUES (?,?,?,true) IF NOT EXISTS",
			uid, namespace, hint.CreationTimestamp)
		if err != nil {
			return err
		}
		identity, err = s.readTranslationIdentity(ctx, hint.TranslationUID)
	}
	if err != nil {
		return err
	}
	if identity.NamespaceUID != namespace || identity.UID != uid ||
		!identity.CreatedAt.Equal(hint.CreationTimestamp) || (identity.Retired && current.Phase != "Retired") {
		return datastore.ErrRepairRequired
	}
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translations_by_key SET repair_watermark=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF coordination_version=? AND phase=?",
		"", namespace, hint.CreationTimestamp, uid, hint.ResourceVersion, hint.Phase)
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	if err := s.recoverTranslation(ctx, hint.TranslationUID, true); err != nil {
		return err
	}
	phase := "Stable"
	if hint.Phase == "Deleting" || hint.Phase == "Retired" {
		phase = "Retired"
	}
	applied, err = s.translationIndexCAS(ctx,
		"UPDATE translations_by_key SET repair_watermark=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF coordination_version=? AND phase=?",
		hint.ResourceVersion, namespace, hint.CreationTimestamp, uid, hint.ResourceVersion, phase)
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) GetTranslationSourceAuditWatermark(ctx context.Context, uid string) (*datastore.TranslationMaintenanceIntent, error) {
	identity, err := s.readTranslationIdentity(ctx, uid)
	if err != nil {
		return nil, err
	}
	state := &datastore.TranslationMaintenanceIntent{
		NamespaceUID: identity.NamespaceUID.String(), TranslationUID: identity.UID.String(),
		CreationTimestamp: identity.CreatedAt,
	}
	var watermark string
	err = s.session.Session.Query(
		"SELECT phase,coordination_version,resource_version,repair_watermark FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
		identity.NamespaceUID, identity.CreatedAt, identity.UID,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&state.Phase, &state.ResourceVersion, &state.SourceVersion, &watermark)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, datastore.ErrRepairRequired
	}
	if err != nil {
		return nil, err
	}
	state.ID = state.TranslationUID + "/" + state.ResourceVersion
	if datastore.ValidateTranslationSourceAuditHint(*state) != nil ||
		(state.Phase != "Stable" && state.Phase != "Retired") ||
		identity.Retired != (state.Phase == "Retired") ||
		watermark == "" || watermark != state.ResourceVersion {
		return nil, datastore.ErrRepairRequired
	}
	return state, nil
}

func (s *scyllaDatastore) ListTranslationSourceAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationSourceAuditPage, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationSourceAuditPage{}, err
	}
	state, err := datastore.DecodeTranslationSourceAuditCursor(namespace, "scylla", cursor, limit)
	if err != nil {
		return datastore.TranslationSourceAuditPage{}, err
	}
	// Range rows are discovery hints only. Matching authoritative headers use
	// SERIAL point reads; a later audit watermark must fence the entire scan.
	rows := s.session.Session.Query("SELECT namespace_uid,creation_timestamp,uid FROM translations_by_key").
		WithContext(ctx).Consistency(gocql.Quorum).PageSize(limit).PageState(state).Iter()
	page := datastore.TranslationSourceAuditPage{}
	var owner, uid gocql.UUID
	var created time.Time
	for rows.Scan(&owner, &created, &uid) {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return datastore.TranslationSourceAuditPage{}, err
		}
		page.Scanned++
		if page.Scanned > limit {
			_ = rows.Close()
			return datastore.TranslationSourceAuditPage{}, datastore.ErrRepairRequired
		}
		if owner.String() != namespace {
			continue
		}
		header, err := s.translationMaintenanceAtKey(ctx, owner, created, uid)
		if err != nil {
			_ = rows.Close()
			return datastore.TranslationSourceAuditPage{}, err
		}
		page.Items = append(page.Items, *header)
	}
	next := append([]byte(nil), rows.PageState()...)
	if err := rows.Close(); err != nil {
		return datastore.TranslationSourceAuditPage{}, err
	}
	if len(next) != 0 {
		page.Next, err = datastore.EncodeTranslationSourceAuditCursor(namespace, "scylla", next)
	}
	return page, err
}

var _ datastore.TranslationSourceAuditStore = (*scyllaDatastore)(nil)

func (s *scyllaDatastore) ListTranslationIndexAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationIndexAuditPage, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexAuditPage{}, err
	}
	state, err := datastore.DecodeTranslationIndexAuditCursor(namespace, "scylla", cursor, limit)
	if err != nil {
		return datastore.TranslationIndexAuditPage{}, err
	}
	rows := s.session.Session.Query("SELECT reservation_token,namespace_uid,index_kind FROM translation_index_reservations_by_token").
		WithContext(ctx).Consistency(gocql.Quorum).PageSize(limit).PageState(state).Iter()
	page := datastore.TranslationIndexAuditPage{}
	var token, owner gocql.UUID
	var kind datastore.TranslationIndexKind
	for rows.Scan(&token, &owner, &kind) {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return datastore.TranslationIndexAuditPage{}, err
		}
		page.Scanned++
		if page.Scanned > limit {
			_ = rows.Close()
			return datastore.TranslationIndexAuditPage{}, datastore.ErrRepairRequired
		}
		if owner.String() == namespace && datastore.IsTranslationSourceIndex(kind) {
			page.Tokens = append(page.Tokens, token.String())
		}
	}
	next := append([]byte(nil), rows.PageState()...)
	if err := rows.Close(); err != nil {
		return datastore.TranslationIndexAuditPage{}, err
	}
	if len(next) > 0 {
		page.Next, err = datastore.EncodeTranslationIndexAuditCursor(namespace, "scylla", next)
	}
	return page, err
}

func (s *scyllaDatastore) RecoverTranslationIndexAudit(ctx context.Context, namespace, token string) error {
	if _, err := datastore.DecodeTranslationIndexAuditCursor(namespace, "scylla", "", 1); err != nil {
		return err
	}
	// Read the immutable reservation before current source authority. A later
	// source intent cannot adopt its token: all new intents allocate fresh ones.
	record, err := s.GetTranslationIndexReservation(ctx, token)
	if err != nil {
		return err
	}
	if record.Index.NamespaceUID != namespace || !datastore.IsTranslationSourceIndex(record.Index.Kind) {
		return datastore.ErrInvalidArgument
	}
	row, err := s.readTranslationSourceRow(ctx, mustParseUUID(namespace), record.Entry.CreationTimestamp, mustParseUUID(record.Entry.TranslationUID))
	if errors.Is(err, datastore.ErrNotFound) {
		identity, identityErr := s.readTranslationIdentity(ctx, record.Entry.TranslationUID)
		if identityErr != nil {
			if errors.Is(identityErr, datastore.ErrNotFound) {
				return datastore.ErrRepairRequired
			}
			return identityErr
		}
		if !identity.Retired || identity.NamespaceUID.String() != namespace ||
			!identity.CreatedAt.Equal(record.Entry.CreationTimestamp) {
			return datastore.ErrRepairRequired
		}
		return s.retireAuditedTranslationReservation(ctx, record)
	}
	if err != nil {
		return err
	}
	identity, err := s.readTranslationIdentity(ctx, record.Entry.TranslationUID)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.ErrRepairRequired
	}
	if err != nil {
		return err
	}
	if identity.NamespaceUID.String() != namespace || !identity.CreatedAt.Equal(record.Entry.CreationTimestamp) ||
		(identity.Retired && row.Phase != "Retired") {
		return datastore.ErrRepairRequired
	}
	if row.Phase != "Stable" && row.Phase != "Retired" {
		hint, err := s.translationMaintenanceAtKey(ctx, row.NamespaceUID, row.CreationTimestamp, row.UID)
		if err != nil {
			return err
		}
		if err := s.RecoverTranslationSourceAudit(ctx, *hint); err != nil {
			return err
		}
		// Recovery may have completed or replanned this allocation. Reload both
		// reservation and source in order on the next bounded audit attempt.
		return datastore.ErrConflict
	}
	if row.Phase == "Retired" {
		return s.retireAuditedTranslationReservation(ctx, record)
	}
	if row.Phase != "Stable" {
		return datastore.ErrConflict
	}
	binding, err := translationRowBinding(row)
	if err != nil {
		return err
	}
	var placements []datastore.TranslationIndexReservation
	if len(row.MaintenanceIntent) > 12<<20 ||
		json.Unmarshal([]byte(row.MaintenanceIntent), &placements) != nil ||
		datastore.ValidateTranslationSourcePlacements(row.source(), binding, placements) != nil {
		return datastore.ErrRepairRequired
	}
	for _, placement := range placements {
		if placement.Token == token {
			if !datastore.TranslationReservationMatchesPlacement(record, placement) {
				return datastore.ErrRepairRequired
			}
			return s.recoverStableTranslation(ctx, row)
		}
	}
	if err := s.confirmStableTranslation(ctx, row, nil); err != nil {
		return err
	}
	return s.retireAuditedTranslationReservation(ctx, record)
}

func (s *scyllaDatastore) retireAuditedTranslationReservation(ctx context.Context, record datastore.TranslationIndexReservation) error {
	if record.Phase == datastore.TranslationReservationPreparing {
		return datastore.ErrRepairRequired
	}
	return s.RemoveTranslationIndex(ctx, record)
}

var _ datastore.TranslationIndexAuditStore = (*scyllaDatastore)(nil)
