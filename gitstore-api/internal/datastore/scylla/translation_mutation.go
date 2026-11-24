// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

type translationSourceMutation struct {
	Mode     string
	Source   *datastore.Translation
	Binding  *datastore.TranslationBinding
	Previous []datastore.TranslationIndexReservation
	Tokens   []string
}

type translationMutationSource struct {
	*datastore.Translation
	Spec, Status, OwnerReferences []byte
}

func (intent translationSourceMutation) MarshalJSON() ([]byte, error) {
	type fields translationSourceMutation
	var source *translationMutationSource
	if intent.Source != nil {
		source = &translationMutationSource{
			Translation: intent.Source, Spec: intent.Source.Spec,
			Status: intent.Source.Status, OwnerReferences: intent.Source.OwnerReferences,
		}
	}
	return json.Marshal(struct {
		*fields
		Source *translationMutationSource
	}{fields: (*fields)(&intent), Source: source})
}

func (intent *translationSourceMutation) UnmarshalJSON(raw []byte) error {
	type fields translationSourceMutation
	decoded := struct {
		*fields
		Source *translationMutationSource
	}{fields: (*fields)(intent)}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	intent.Source = nil
	if source := decoded.Source; source != nil && source.Translation != nil {
		intent.Source = source.Translation
		intent.Source.Spec, intent.Source.Status = source.Spec, source.Status
		intent.Source.OwnerReferences = source.OwnerReferences
	}
	return nil
}

func (s *scyllaDatastore) loadTranslationSourceRow(ctx context.Context, uid string) (*translationSourceRow, error) {
	identity, err := s.readTranslationIdentity(ctx, uid)
	if err != nil {
		return nil, err
	}
	if identity.Retired {
		return nil, datastore.ErrNotFound
	}
	row, err := s.readTranslationSourceRow(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
	if err != nil {
		return nil, err
	}
	if row.Phase == "Retired" {
		return nil, datastore.ErrNotFound
	}
	if row.Name != identity.Name || row.Namespace != identity.Namespace {
		return nil, datastore.ErrRepairRequired
	}
	return row, nil
}

func validateTranslationSourceTransition(current, proposed *datastore.Translation, expected string) error {
	if err := datastore.ValidateCatalogUpdateVersion(current.ResourceVersion, expected, proposed.ResourceVersion); err != nil {
		return err
	}
	if proposed.UID != current.UID || proposed.Namespace != current.Namespace || proposed.NamespaceID != current.NamespaceID ||
		proposed.Name != current.Name || proposed.RepositoryID != current.RepositoryID ||
		proposed.CreationActor != current.CreationActor || !proposed.CreationTimestamp.Equal(current.CreationTimestamp) {
		return datastore.ErrConflict
	}
	generation := current.Generation
	if !bytes.Equal(proposed.Spec, current.Spec) || proposed.Body != current.Body {
		generation++
	}
	if proposed.Generation != generation {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) UpdateTranslation(ctx context.Context, proposed *datastore.Translation, expected string) error {
	if err := datastore.ValidateTranslationSource(proposed); err != nil {
		return err
	}
	return s.withNamespaceCatalogFence(ctx, proposed.Namespace, false, func() error {
		if err := s.catalogParentsActive(ctx, proposed.Namespace, proposed.RepositoryID); err != nil {
			return err
		}
		row, err := s.loadTranslationSourceRow(ctx, proposed.UID)
		if err != nil {
			return err
		}
		if row.Phase != "Stable" {
			return datastore.ErrConflict
		}
		current := row.source()
		if err := validateTranslationSourceTransition(current, proposed, expected); err != nil {
			return err
		}
		binding, err := translationRowBinding(row)
		if err != nil {
			return err
		}
		desired := *proposed
		desired.Status = current.Status
		next, err := datastore.PrepareTranslationBinding(&desired, binding)
		if err != nil {
			return err
		}
		return s.beginTranslationSourceMutation(ctx, row, &desired, next, "Source")
	})
}

func (s *scyllaDatastore) UpdateTranslationStatus(ctx context.Context, uid, expected string, status json.RawMessage) (*datastore.Translation, error) {
	row, err := s.loadTranslationSourceRow(ctx, uid)
	if err != nil {
		return nil, err
	}
	if row.Phase != "Stable" {
		return nil, datastore.ErrConflict
	}
	desired := row.source()
	if err := datastore.ApplyTranslationStatus(desired, expected, status); err != nil {
		return nil, err
	}
	binding, err := translationRowBinding(row)
	if err != nil {
		return nil, err
	}
	next, err := datastore.PrepareTranslationBinding(desired, binding)
	if err != nil {
		return nil, err
	}
	err = s.withNamespaceCatalogFence(ctx, row.Namespace, true, func() error {
		return s.beginTranslationSourceMutation(ctx, row, desired, next, "Status")
	})
	if err != nil {
		return nil, err
	}
	return desired, nil
}

func (s *scyllaDatastore) GetTranslationBinding(ctx context.Context, uid string) (*datastore.TranslationBinding, error) {
	row, err := s.loadTranslationSourceRow(ctx, uid)
	if err != nil {
		return nil, err
	}
	if row.Phase != "Stable" {
		return nil, datastore.ErrRepairRequired
	}
	return translationRowBinding(row)
}

func (s *scyllaDatastore) CompareAndSwapTranslationBinding(ctx context.Context, proposed *datastore.TranslationBinding, expected string) error {
	if proposed == nil {
		return datastore.ErrInvalidArgument
	}
	row, err := s.loadTranslationSourceRow(ctx, proposed.TranslationUID)
	if err != nil {
		return err
	}
	if row.Phase != "Stable" {
		return datastore.ErrConflict
	}
	current, err := translationRowBinding(row)
	if err != nil {
		return err
	}
	source := row.source()
	if err := datastore.ValidateTranslationBindingUpdate(current, proposed, source, expected); err != nil {
		return err
	}
	return s.withNamespaceCatalogFence(ctx, row.Namespace, true, func() error {
		return s.beginTranslationSourceMutation(ctx, row, source, proposed, "Binding")
	})
}

func (s *scyllaDatastore) beginTranslationSourceMutation(ctx context.Context, row *translationSourceRow, source *datastore.Translation, binding *datastore.TranslationBinding, mode string) error {
	var previous []datastore.TranslationIndexReservation
	if json.Unmarshal([]byte(row.MaintenanceIntent), &previous) != nil || len(previous) < 2 || len(previous) > 3 {
		return datastore.ErrRepairRequired
	}
	intent := translationSourceMutation{Mode: mode, Source: source, Binding: binding, Previous: previous,
		Tokens: []string{uuid.NewString()}}
	if binding.Epochs[len(binding.Epochs)-1].BoundUID != "" {
		intent.Tokens = append(intent.Tokens, uuid.NewString())
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if len(raw) > 12<<20 {
		return datastore.ErrInvalidArgument
	}
	version, err := datastore.AdvanceTranslationIndexVersion(row.CoordinationVersion)
	if err != nil {
		return err
	}
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translations_by_key SET phase='Updating',coordination_version=?,maintenance_intent=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF phase='Stable' AND coordination_version=? AND resource_version=?",
		version, string(raw), row.NamespaceUID, row.CreationTimestamp, row.UID, row.CoordinationVersion, row.ResourceVersion)
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	pending := *row
	pending.Phase, pending.CoordinationVersion, pending.MaintenanceIntent = "Updating", version, string(raw)
	return s.completeTranslationSourceMutation(ctx, &pending)
}

func (s *scyllaDatastore) completeTranslationSourceMutation(ctx context.Context, row *translationSourceRow) error {
	var intent translationSourceMutation
	if len(row.MaintenanceIntent) > 12<<20 || json.Unmarshal([]byte(row.MaintenanceIntent), &intent) != nil ||
		datastore.ValidateTranslationSource(intent.Source) != nil || intent.Binding == nil ||
		intent.Binding.TranslationUID != row.UID.String() || intent.Binding.SourceVersion != intent.Source.ResourceVersion ||
		len(intent.Binding.Epochs) == 0 || len(intent.Binding.Epochs) > datastore.MaxTranslationEpochs ||
		len(intent.Previous) < 2 || len(intent.Previous) > 3 || len(intent.Tokens) < 1 || len(intent.Tokens) > 2 {
		return datastore.ErrRepairRequired
	}
	switch intent.Mode {
	case "Source", "Status", "Binding":
	default:
		return datastore.ErrRepairRequired
	}
	current := row.source()
	if intent.Source.UID != current.UID || intent.Source.NamespaceID != current.NamespaceID ||
		intent.Source.Namespace != current.Namespace || intent.Source.Name != current.Name ||
		!intent.Source.CreationTimestamp.Equal(current.CreationTimestamp) {
		return datastore.ErrRepairRequired
	}
	last := intent.Binding.Epochs[len(intent.Binding.Epochs)-1]
	if last.Epoch != intent.Binding.CurrentEpoch || last.Target.NamespaceUID != row.NamespaceUID.String() ||
		(last.BoundUID != "" && len(intent.Tokens) != 2) || (last.BoundUID == "" && len(intent.Tokens) != 1) {
		return datastore.ErrRepairRequired
	}
	for i, previous := range intent.Previous {
		version, epoch := current.ResourceVersion, row.ReferenceEpoch
		if i == 0 {
			version, epoch = "1", translationCreationToken(current.UID, "reference")
			if previous.Index.Kind != datastore.TranslationNamespaceIndex {
				return datastore.ErrRepairRequired
			}
		} else if previous.Index.Kind == datastore.TranslationNamespaceIndex {
			return datastore.ErrRepairRequired
		}
		if previous.Entry.TranslationUID != current.UID || previous.Index.NamespaceUID != current.NamespaceID ||
			previous.Entry.SourceVersion != version || previous.Entry.ReferenceEpoch != epoch {
			return datastore.ErrRepairRequired
		}
	}
	shard, err := datastore.TranslationIndexShard(current.UID)
	if err != nil {
		return err
	}
	entry := datastore.TranslationIndexEntry{TranslationUID: current.UID, SourceVersion: intent.Source.ResourceVersion,
		ReferenceEpoch: last.Epoch, CreationTimestamp: current.CreationTimestamp}
	indexes := []datastore.TranslationIndexKey{
		{Kind: datastore.TranslationTargetNameIndex, NamespaceUID: current.NamespaceID, Shard: shard, Target: last.Target},
	}
	if last.BoundUID != "" {
		indexes = append(indexes, datastore.TranslationIndexKey{
			Kind: datastore.TranslationTargetUIDIndex, NamespaceUID: current.NamespaceID, Shard: shard, TargetUID: last.BoundUID,
		})
	}
	placements := []datastore.TranslationIndexReservation{intent.Previous[0]}
	for i, index := range indexes {
		reserved, err := s.ReserveTranslationIndex(ctx, index, entry, intent.Tokens[i],
			max(1024, datastore.TranslationIndexPayloadBytes(index, entry)))
		if err != nil {
			return err
		}
		if err := s.CommitTranslationIndex(ctx, reserved); err != nil {
			return err
		}
		placements = append(placements, reserved)
	}
	for _, previous := range intent.Previous[1:] {
		if err := s.RemoveTranslationIndex(ctx, previous); err != nil {
			return err
		}
	}
	if err := s.syncTranslationSourceProjections(ctx, row.CoordinationVersion, current, intent.Source); err != nil {
		return err
	}
	next := toTranslationSourceRow(intent.Source)
	next.CoordinationVersion, err = datastore.AdvanceTranslationIndexVersion(row.CoordinationVersion)
	if err != nil {
		return err
	}
	next.ExpectedCoordinationVersion = row.CoordinationVersion
	next.Phase, next.ReferenceEpoch = "Stable", intent.Binding.CurrentEpoch
	raw, err := json.Marshal(intent.Binding)
	if err != nil {
		return err
	}
	next.BindingSnapshot = string(raw)
	raw, err = json.Marshal(placements)
	if err != nil {
		return err
	}
	next.MaintenanceIntent = string(raw)
	metadata := translationSourceTable.Metadata()
	var columns []string
	for _, column := range metadata.Columns {
		if !slices.Contains(metadata.PartKey, column) && column != "revalidation_version" {
			columns = append(columns, column)
		}
	}
	statement, names := translationSourceTable.Update(columns...)
	query := s.session.Query(statement+" IF phase='Updating' AND coordination_version=?", append(names, "expected_coordination_version")).
		WithContext(ctx).BindStruct(next)
	query.Consistency(gocql.Quorum)
	query.SerialConsistency(gocql.Serial)
	applied, err := query.ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		current, err := s.loadTranslationSourceRow(ctx, next.UID.String())
		if err != nil {
			return err
		}
		if current.Phase != "Stable" || current.ResourceVersion != next.ResourceVersion || current.BindingSnapshot != next.BindingSnapshot {
			return datastore.ErrConflict
		}
	}
	return nil
}

func (s *scyllaDatastore) RecoverTranslation(ctx context.Context, uid string) error {
	return s.recoverTranslation(ctx, uid, false)
}

func (s *scyllaDatastore) recoverTranslation(ctx context.Context, uid string, requireSource bool) error {
	identity, err := s.readTranslationIdentity(ctx, uid)
	if err != nil {
		return err
	}
	row, err := s.readTranslationSourceRow(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		if requireSource {
			return datastore.ErrRepairRequired
		}
		row, err = s.fenceUnpublishedTranslation(ctx, identity)
	}
	if err != nil {
		return err
	}
	if row.Phase == "Deleting" || row.Phase == "Retired" {
		return s.completeTranslationDeletion(ctx, row)
	}
	if identity.Retired || row.Name != identity.Name || row.Namespace != identity.Namespace {
		return datastore.ErrRepairRequired
	}
	if row.Phase == "Creating" {
		return s.RecoverTranslationCreation(ctx, uid)
	}
	if row.Phase == "Stable" {
		return s.recoverStableTranslation(ctx, row)
	}
	if row.Phase != "Updating" {
		return datastore.ErrRepairRequired
	}
	return s.withNamespaceCatalogFence(ctx, row.Namespace, true, func() error {
		return s.completeTranslationSourceMutation(ctx, row)
	})
}
