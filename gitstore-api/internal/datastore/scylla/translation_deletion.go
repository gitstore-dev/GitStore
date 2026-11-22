// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

type translationSourceDeletion struct {
	Placements   []datastore.TranslationIndexReservation
	VacancyToken string
}

func (s *scyllaDatastore) DeleteTranslation(ctx context.Context, uid, expected string) error {
	row, err := s.loadTranslationSourceRow(ctx, uid)
	if err != nil {
		return err
	}
	if row.Phase != "Stable" || row.ResourceVersion != expected || len(row.Finalizers) != 0 {
		return datastore.ErrConflict
	}
	var intent translationSourceDeletion
	if json.Unmarshal([]byte(row.MaintenanceIntent), &intent.Placements) != nil {
		return datastore.ErrRepairRequired
	}
	intent.VacancyToken = uuid.NewString()
	if err := s.validateTranslationDeletion(ctx, row, intent); err != nil {
		return err
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	version, err := datastore.AdvanceTranslationIndexVersion(row.CoordinationVersion)
	if err != nil {
		return err
	}
	return s.withNamespaceCatalogFence(ctx, row.Namespace, true, func() error {
		applied, err := s.translationIndexCAS(ctx,
			"UPDATE translations_by_key SET phase='Deleting',coordination_version=?,maintenance_intent=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF phase='Stable' AND coordination_version=? AND resource_version=?",
			version, string(raw), row.NamespaceUID, row.CreationTimestamp, row.UID, row.CoordinationVersion, expected)
		if err != nil {
			return err
		}
		if !applied {
			return datastore.ErrConflict
		}
		pending := *row
		pending.Phase, pending.CoordinationVersion, pending.MaintenanceIntent = "Deleting", version, string(raw)
		return s.completeTranslationDeletion(ctx, &pending)
	})
}

func (s *scyllaDatastore) validateTranslationDeletion(ctx context.Context, row *translationSourceRow, intent translationSourceDeletion) error {
	if _, err := uuid.Parse(intent.VacancyToken); err != nil || len(row.Finalizers) != 0 {
		return datastore.ErrRepairRequired
	}
	binding, err := translationRowBinding(row)
	if err != nil {
		return err
	}
	last := binding.Epochs[len(binding.Epochs)-1]
	shard, err := datastore.TranslationIndexShard(row.UID.String())
	if err != nil {
		return err
	}
	indexes := []datastore.TranslationIndexKey{
		{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: row.NamespaceUID.String(), Shard: shard},
		{Kind: datastore.TranslationTargetNameIndex, NamespaceUID: row.NamespaceUID.String(), Shard: shard, Target: last.Target},
	}
	if last.BoundUID != "" {
		indexes = append(indexes, datastore.TranslationIndexKey{
			Kind: datastore.TranslationTargetUIDIndex, NamespaceUID: row.NamespaceUID.String(), Shard: shard, TargetUID: last.BoundUID,
		})
	}
	if len(intent.Placements) != len(indexes) {
		return datastore.ErrRepairRequired
	}
	for i, placement := range intent.Placements {
		indexes[i].Segment = placement.Index.Segment
		version, epoch := row.ResourceVersion, row.ReferenceEpoch
		if i == 0 {
			version, epoch = "1", translationCreationToken(row.UID.String(), "reference")
		}
		if placement.Index != indexes[i] || placement.Entry.TranslationUID != row.UID.String() ||
			placement.Entry.SourceVersion != version || placement.Entry.ReferenceEpoch != epoch ||
			!placement.Entry.CreationTimestamp.Equal(row.CreationTimestamp) {
			return datastore.ErrRepairRequired
		}
		persisted, err := s.GetTranslationIndexReservation(ctx, placement.Token)
		if err != nil {
			return err
		}
		if !datastore.TranslationReservationMatchesPlacement(persisted, placement) {
			return datastore.ErrRepairRequired
		}
	}
	return nil
}

func (s *scyllaDatastore) completeTranslationDeletion(ctx context.Context, row *translationSourceRow) error {
	if row.Phase == "Retired" && row.MaintenanceIntent == "" {
		return s.finishRetiredTranslation(ctx, row)
	}
	if row.Phase != "Deleting" && row.Phase != "Retired" {
		return datastore.ErrRepairRequired
	}
	var intent translationSourceDeletion
	if len(row.MaintenanceIntent) > 12<<20 || json.Unmarshal([]byte(row.MaintenanceIntent), &intent) != nil {
		return datastore.ErrRepairRequired
	}
	if err := s.validateTranslationDeletion(ctx, row, intent); err != nil {
		return err
	}
	if row.Phase == "Deleting" {
		for _, placement := range intent.Placements[1:] {
			if err := s.RemoveTranslationIndex(ctx, placement); err != nil {
				return err
			}
		}
		if err := s.vacateTranslationName(ctx, row, intent.VacancyToken); err != nil {
			return err
		}
		if err := s.syncTranslationSourceProjections(ctx, row.CoordinationVersion, row.source(), nil); err != nil {
			return err
		}
		version, err := datastore.AdvanceTranslationIndexVersion(row.CoordinationVersion)
		if err != nil {
			return err
		}
		applied, err := s.translationIndexCAS(ctx,
			"UPDATE translations_by_key SET phase='Retired',coordination_version=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF phase='Deleting' AND coordination_version=?",
			version, row.NamespaceUID, row.CreationTimestamp, row.UID, row.CoordinationVersion)
		if err != nil {
			return err
		}
		if !applied {
			current, err := s.readTranslationSourceRow(ctx, row.NamespaceUID, row.CreationTimestamp, row.UID)
			if err != nil {
				return err
			}
			if current.Phase != "Retired" {
				return datastore.ErrConflict
			}
			if current.MaintenanceIntent == "" {
				return s.finishRetiredTranslation(ctx, current)
			}
			row = current
		} else {
			next := *row
			next.Phase, next.CoordinationVersion = "Retired", version
			row = &next
		}
	}
	return s.finishRetiredTranslation(ctx, row)
}

func (s *scyllaDatastore) finishRetiredTranslation(ctx context.Context, row *translationSourceRow) error {
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translations_by_uid SET retired=true,name=null,namespace=null,expected_name_token=null,reservation_token=null WHERE uid=? IF namespace_uid=? AND creation_timestamp=?",
		row.UID, row.NamespaceUID, row.CreationTimestamp)
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrRepairRequired
	}
	if row.MaintenanceIntent != "" || row.BindingSnapshot != "" {
		if err := s.compactRetiredTranslation(ctx, row); err != nil {
			return err
		}
	}
	// This immutable creation reservation is derivable after compaction.
	// Remove discovery last, including when resuming an already compacted row.
	source := &datastore.Translation{UID: row.UID.String(), NamespaceID: row.NamespaceUID.String(),
		CreationTimestamp: row.CreationTimestamp, ResourceVersion: "1"}
	binding := &datastore.TranslationBinding{CurrentEpoch: translationCreationToken(source.UID, "reference")}
	anchor, err := s.reserveTranslationCreationIndex(ctx, source, binding, false)
	if err != nil {
		return err
	}
	return s.RemoveTranslationIndex(ctx, anchor)
}

func (s *scyllaDatastore) vacateTranslationName(ctx context.Context, row *translationSourceRow, vacancy string) error {
	claim := mustParseUUID(translationCreationToken(row.UID.String(), "claim"))
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translations_by_name SET phase='Vacant',reservation_token=?,uid=null,creation_timestamp=null,resource_version=null WHERE namespace_uid=? AND name=? IF phase='Reserved' AND uid=? AND reservation_token=?",
		mustParseUUID(vacancy), row.NamespaceUID, translationNameKey(row.Name), row.UID, claim)
	if err != nil || applied {
		return err
	}
	owner, _, phase, err := s.translationNameOwner(ctx, row.NamespaceUID, row.Name)
	if err != nil {
		return err
	}
	if phase == "" || (phase == "Reserved" && owner == row.UID) {
		return datastore.ErrRepairRequired
	}
	// A later vacancy or owner proves this claim was already released. Never
	// adopt that token: a delayed worker must not release a newer incarnation.
	return nil
}

func (s *scyllaDatastore) compactRetiredTranslation(ctx context.Context, row *translationSourceRow) error {
	version, err := datastore.AdvanceTranslationIndexVersion(row.CoordinationVersion)
	if err != nil {
		return err
	}
	next := &translationSourceRow{
		productRow:   productRow{UID: row.UID, CreationTimestamp: row.CreationTimestamp, ResourceVersion: row.ResourceVersion},
		NamespaceUID: row.NamespaceUID, Phase: "Retired", CoordinationVersion: version,
		ExpectedCoordinationVersion: row.CoordinationVersion,
	}
	metadata := translationSourceTable.Metadata()
	var columns []string
	for _, column := range metadata.Columns {
		if !slices.Contains(metadata.PartKey, column) {
			columns = append(columns, column)
		}
	}
	statement, names := translationSourceTable.Update(columns...)
	query := s.session.Query(statement+" IF phase='Retired' AND coordination_version=?", append(names, "expected_coordination_version")).
		WithContext(ctx).BindStruct(next)
	query.Consistency(gocql.Quorum)
	query.SerialConsistency(gocql.Serial)
	applied, err := query.ExecCASRelease()
	if err != nil || applied {
		return err
	}
	current, err := s.readTranslationSourceRow(ctx, row.NamespaceUID, row.CreationTimestamp, row.UID)
	if err != nil {
		return err
	}
	if current.Phase != "Retired" || current.MaintenanceIntent != "" {
		return datastore.ErrConflict
	}
	return nil
}
