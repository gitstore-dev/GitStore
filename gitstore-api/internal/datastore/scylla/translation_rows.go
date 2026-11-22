// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/table"
)

func (s *scyllaDatastore) TranslationStore() datastore.TranslationStore               { return s }
func (s *scyllaDatastore) TranslationBindingStore() datastore.TranslationBindingStore { return s }
func (s *scyllaDatastore) TranslationIndexStore() datastore.TranslationIndexStore     { return s }

type translationSourceRow struct {
	productRow
	NamespaceUID                gocql.UUID `db:"namespace_uid"`
	SourceBlobOID               string     `db:"source_blob_oid"`
	SourceDigest                string     `db:"source_digest"`
	RawSource                   []byte     `db:"raw_source"`
	ReferenceEpoch              string     `db:"reference_epoch"`
	CoordinationVersion         string     `db:"coordination_version"`
	Phase                       string     `db:"phase"`
	BindingSnapshot             string     `db:"binding_snapshot"`
	MaintenanceIntent           string     `db:"maintenance_intent"`
	RevalidationVersion         string     `db:"revalidation_version"`
	ExpectedCoordinationVersion string     `db:"expected_coordination_version"`
}

var translationSourceTable = table.New(table.Metadata{
	Name: "translations_by_key",
	Columns: append(authoritativeColumns(true, true),
		"namespace_uid", "source_blob_oid", "source_digest", "raw_source", "reference_epoch",
		"coordination_version", "phase", "binding_snapshot", "maintenance_intent", "revalidation_version"),
	PartKey: []string{"namespace_uid", "creation_timestamp", "uid"},
})

func (s *scyllaDatastore) RequestTranslationRevalidation(ctx context.Context, source *datastore.Translation) (bool, error) {
	if err := datastore.ValidateTranslationRevalidationRequest(source); err != nil {
		return false, err
	}
	existing := make(map[string]any)
	applied, err := s.session.Query(
		"UPDATE translations_by_key SET revalidation_version=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF phase='Stable' AND resource_version=? AND revalidation_version!=?",
		nil).WithContext(ctx).Bind(source.ResourceVersion, mustParseUUID(source.NamespaceID), source.CreationTimestamp,
		mustParseUUID(source.UID), source.ResourceVersion, source.ResourceVersion).
		Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).MapScanCAS(existing)
	if err != nil || applied {
		return applied, err
	}
	if existing["phase"] == "Stable" && existing["resource_version"] == source.ResourceVersion &&
		existing["revalidation_version"] == source.ResourceVersion {
		return false, nil
	}
	return false, datastore.ErrConflict
}

func toTranslationSourceRow(source *datastore.Translation) *translationSourceRow {
	return &translationSourceRow{
		productRow: productRow{
			Namespace: source.Namespace, CreationTimestamp: source.CreationTimestamp,
			UID: mustParseUUID(source.UID), Name: source.Name, APIVersion: source.APIVersion, Kind: source.Kind,
			Generation: source.Generation, ResourceVersion: source.ResourceVersion, Revision: source.Revision,
			CreationActor: source.CreationActor, UpdateTimestamp: source.UpdateTimestamp, UpdateActor: source.UpdateActor,
			Labels: source.Labels, Annotations: source.Annotations, OwnerReferences: string(source.OwnerReferences),
			Finalizers: source.Finalizers, DeletionTimestamp: source.DeletionTimestamp, RepositoryID: optionalUUID(source.RepositoryID),
			SourcePath: source.SourcePath, GitCommitSHA: source.GitCommitSHA, GitRef: source.GitRef,
			Spec: string(source.Spec), Body: source.Body, Status: string(source.Status),
		},
		NamespaceUID: mustParseUUID(source.NamespaceID), SourceBlobOID: source.SourceBlobOID,
		SourceDigest: source.SourceDigest, RawSource: source.RawSource,
	}
}

func (row *translationSourceRow) source() *datastore.Translation {
	base := fromProductRow(&row.productRow)
	return &datastore.Translation{
		APIVersion: base.APIVersion, Kind: base.Kind, UID: base.UID, Namespace: base.Namespace, Name: base.Name,
		Generation: base.Generation, ResourceVersion: base.ResourceVersion, Revision: base.Revision,
		CreationTimestamp: base.CreationTimestamp, CreationActor: base.CreationActor,
		UpdateTimestamp: base.UpdateTimestamp, UpdateActor: base.UpdateActor,
		Labels: base.Labels, Annotations: base.Annotations, OwnerReferences: base.OwnerReferences,
		Finalizers: base.Finalizers, DeletionTimestamp: base.DeletionTimestamp,
		NamespaceID: row.NamespaceUID.String(), RepositoryID: base.RepositoryID, SourcePath: base.SourcePath,
		GitCommitSHA: base.GitCommitSHA, GitRef: base.GitRef, SourceBlobOID: row.SourceBlobOID,
		SourceDigest: row.SourceDigest, RawSource: row.RawSource, Spec: base.Spec, Body: base.Body, Status: base.Status,
	}
}

func (s *scyllaDatastore) readTranslationSourceRow(ctx context.Context, namespace gocql.UUID, createdAt time.Time, uid gocql.UUID) (*translationSourceRow, error) {
	var row translationSourceRow
	query := s.session.Query("SELECT "+strings.Join(translationSourceTable.Metadata().Columns, ",")+
		" FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?", nil).
		WithContext(ctx).Bind(namespace, createdAt, uid)
	query.Consistency(gocql.Serial)
	if err := query.GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	if _, err := datastore.AdvanceTranslationIndexVersion(row.CoordinationVersion); err != nil {
		return nil, err
	}
	switch row.Phase {
	case "Creating", "Stable", "Updating", "Deleting", "Retired":
	default:
		return nil, datastore.ErrRepairRequired
	}
	return &row, nil
}

func (s *scyllaDatastore) insertTranslationSourceRow(ctx context.Context, row *translationSourceRow) (bool, error) {
	statement, names := translationSourceTable.Insert()
	query := s.session.Query(statement+" IF NOT EXISTS", names).WithContext(ctx).BindStruct(row)
	query.Consistency(gocql.Quorum)
	query.SerialConsistency(gocql.Serial)
	return query.ExecCASRelease()
}

func translationRowBinding(row *translationSourceRow) (*datastore.TranslationBinding, error) {
	var binding datastore.TranslationBinding
	if json.Unmarshal([]byte(row.BindingSnapshot), &binding) != nil ||
		binding.TranslationUID != row.UID.String() || binding.SourceVersion != row.ResourceVersion ||
		binding.CurrentEpoch != row.ReferenceEpoch || len(binding.Epochs) == 0 ||
		len(binding.Epochs) > datastore.MaxTranslationEpochs ||
		binding.Epochs[len(binding.Epochs)-1].Epoch != binding.CurrentEpoch {
		return nil, datastore.ErrRepairRequired
	}
	return &binding, nil
}
