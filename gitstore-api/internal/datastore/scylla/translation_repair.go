// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"math"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/gocql/gocql"
)

// Inspection previews one bounded discovery page without repairing, publishing
// watermarks or accepting a caller-provided cursor. It is not a completeness proof.
type TranslationAuditInspection struct {
	Progress     datastore.TranslationAuditProgress           `json:"progress"`
	NextPhase    string                                       `json:"nextPhase"`
	Scanned      int                                          `json:"scanned"`
	Sources      []datastore.TranslationMaintenanceIntent     `json:"sources,omitempty"`
	Reservations []string                                     `json:"reservations,omitempty"`
	Cells        []datastore.TranslationIndexAuditCell        `json:"cells,omitempty"`
	Targets      []datastore.TranslationTargetSourceAuditHint `json:"targets,omitempty"`
}

func (s *ProjectionRepairService) RepairTranslationTargetAuditTurn(ctx context.Context, namespace string, limit int) (datastore.TranslationAuditProgress, error) {
	store, err := s.translationAuditStore()
	if err != nil {
		return datastore.TranslationAuditProgress{}, err
	}
	return store.RepairTranslationTargetAuditTurn(ctx, namespace, limit)
}

func (s *ProjectionRepairService) InspectTranslationTargetAuditTurn(ctx context.Context, namespace string, limit int) (TranslationAuditInspection, error) {
	fail := func(err error) (TranslationAuditInspection, error) { return TranslationAuditInspection{}, err }
	if _, _, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace, "scylla", "", limit); err != nil {
		return fail(err)
	}
	store, err := s.translationAuditStore()
	if err != nil {
		return fail(err)
	}
	progress, err := store.GetTranslationTargetAuditProgress(ctx, namespace)
	if errors.Is(err, datastore.ErrNotFound) {
		progress = datastore.TranslationAuditProgress{NamespaceUID: namespace, Phase: "Repairing"}
	} else if err != nil {
		return fail(err)
	}
	epoch, err := store.ReadTranslationTargetCoverageEpoch(ctx, namespace)
	if err != nil {
		return fail(err)
	}
	result := TranslationAuditInspection{Progress: progress, NextPhase: progress.Phase}
	cursor := progress.Cursor
	if progress.DescriptorDigest != translation.CapabilityDigest() ||
		(progress.Phase != "Repairing" && progress.TargetMutationEpoch != epoch) {
		result.NextPhase, cursor = "Repairing", ""
	} else if progress.Phase == "Verified" {
		return result, nil
	}
	page, err := store.ListTranslationTargetSourceAudit(ctx, namespace, cursor, limit)
	if err != nil {
		return fail(err)
	}
	result.Scanned, result.Targets = page.Scanned, page.Items
	return result, nil
}

func (s *ProjectionRepairService) InspectTranslationAuditTurn(ctx context.Context, namespace string, limit int) (TranslationAuditInspection, error) {
	fail := func(err error) (TranslationAuditInspection, error) { return TranslationAuditInspection{}, err }
	if _, err := datastore.DecodeTranslationSourceAuditCursor(namespace, "scylla", "", limit); err != nil {
		return fail(err)
	}
	store, err := s.translationAuditStore()
	if err != nil {
		return fail(err)
	}
	progress, err := store.GetTranslationAuditProgress(ctx, namespace)
	if errors.Is(err, datastore.ErrNotFound) {
		progress = datastore.TranslationAuditProgress{NamespaceUID: namespace, Phase: "Sources"}
	} else if err != nil {
		return fail(err)
	}
	result := TranslationAuditInspection{Progress: progress, NextPhase: progress.Phase}
	cursor := progress.Cursor
	if progress.Phase == "Scanned" {
		result.NextPhase, cursor = "Sources", ""
	}
	switch result.NextPhase {
	case "Sources", "VerifySources":
		page, err := store.ListTranslationSourceAudit(ctx, namespace, cursor, limit)
		if err != nil {
			return fail(err)
		}
		result.Scanned, result.Sources = page.Scanned, page.Items
	case "Reservations":
		page, err := store.ListTranslationIndexAudit(ctx, namespace, cursor, limit)
		if err != nil {
			return fail(err)
		}
		result.Scanned, result.Reservations = page.Scanned, page.Tokens
	case "Cells":
		page, err := store.ListTranslationIndexCellAudit(ctx, namespace, cursor, limit)
		if err != nil {
			return fail(err)
		}
		result.Scanned, result.Cells = page.Scanned, page.Items
	}
	return result, nil
}

func (s *scyllaDatastore) GetTranslationAuditProgress(ctx context.Context, namespace string) (datastore.TranslationAuditProgress, error) {
	fail := func(err error) (datastore.TranslationAuditProgress, error) {
		return datastore.TranslationAuditProgress{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if _, err := datastore.DecodeTranslationSourceAuditCursor(namespace, "scylla", "", 1); err != nil {
		return fail(err)
	}
	progress := datastore.TranslationAuditProgress{NamespaceUID: namespace}
	var run gocql.UUID
	err := s.session.Session.Query(
		"SELECT resource_version,run_id,phase,cursor,scanned FROM translation_audit_progress_by_namespace WHERE namespace_uid=?",
		mustParseUUID(namespace),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(
		&progress.ResourceVersion, &run, &progress.Phase, &progress.Cursor, &progress.Scanned)
	if errors.Is(err, gocql.ErrNotFound) {
		return fail(datastore.ErrNotFound)
	}
	if err != nil {
		return fail(err)
	}
	if _, err := datastore.AdvanceTranslationIndexVersion(progress.ResourceVersion); err != nil {
		return fail(datastore.ErrRepairRequired)
	}
	if run == (gocql.UUID{}) || len(progress.Cursor) > 32<<10 || progress.Scanned < 0 {
		return fail(datastore.ErrRepairRequired)
	}
	switch progress.Phase {
	case "Sources", "Reservations", "Cells", "VerifySources":
	case "Scanned":
		if progress.Cursor != "" {
			return fail(datastore.ErrRepairRequired)
		}
	default:
		return fail(datastore.ErrRepairRequired)
	}
	progress.RunID = run.String()
	return progress, nil
}

func (s *scyllaDatastore) RepairTranslationAuditTurn(ctx context.Context, namespace string, limit int) (datastore.TranslationAuditProgress, error) {
	fail := func(err error) (datastore.TranslationAuditProgress, error) {
		return datastore.TranslationAuditProgress{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if _, err := datastore.DecodeTranslationSourceAuditCursor(namespace, "scylla", "", limit); err != nil {
		return fail(err)
	}
	progress, err := s.GetTranslationAuditProgress(ctx, namespace)
	if errors.Is(err, datastore.ErrNotFound) {
		run, err := gocql.RandomUUID()
		if err != nil {
			return fail(err)
		}
		progress = datastore.TranslationAuditProgress{
			NamespaceUID: namespace, RunID: run.String(), ResourceVersion: "1", Phase: "Sources",
		}
		applied, err := s.translationIndexCAS(ctx,
			"INSERT INTO translation_audit_progress_by_namespace (namespace_uid,resource_version,run_id,phase,cursor,scanned) VALUES (?,?,?,?,?,?) IF NOT EXISTS",
			mustParseUUID(namespace), progress.ResourceVersion, run, progress.Phase, "", int64(0))
		if err != nil {
			return fail(err)
		}
		if !applied {
			return fail(datastore.ErrConflict)
		}
	} else if err != nil {
		return fail(err)
	}
	next := progress
	if next.Phase == "Scanned" {
		run, err := gocql.RandomUUID()
		if err != nil {
			return fail(err)
		}
		next.RunID, next.Phase, next.Cursor, next.Scanned = run.String(), "Sources", "", 0
	}
	scanned, cursor, phase := 0, "", next.Phase
	switch next.Phase {
	case "Sources", "VerifySources":
		page, err := datastore.RecoverTranslationSourceAuditPage(ctx, s, namespace, next.Cursor, limit)
		if err != nil {
			return fail(err)
		}
		scanned, cursor = page.Scanned, page.Next
		if cursor == "" {
			phase = "Reservations"
			if next.Phase == "VerifySources" {
				phase = "Scanned"
			}
		}
	case "Reservations":
		page, err := s.ListTranslationIndexAudit(ctx, namespace, next.Cursor, limit)
		if err != nil {
			return fail(err)
		}
		scanned, cursor = page.Scanned, page.Next
		if !validTranslationAuditPage(scanned, len(page.Tokens), cursor, next.Cursor, limit) {
			return fail(datastore.ErrRepairRequired)
		}
		for _, token := range page.Tokens {
			if err := s.RecoverTranslationIndexAudit(ctx, namespace, token); err != nil {
				return fail(err)
			}
		}
		if cursor == "" {
			phase = "Cells"
		}
	case "Cells":
		page, err := s.ListTranslationIndexCellAudit(ctx, namespace, next.Cursor, limit)
		if err != nil {
			return fail(err)
		}
		scanned, cursor = page.Scanned, page.Next
		if !validTranslationAuditPage(scanned, len(page.Items), cursor, next.Cursor, limit) {
			return fail(datastore.ErrRepairRequired)
		}
		for _, cell := range page.Items {
			if err := s.RecoverTranslationIndexCellAudit(ctx, namespace, cell); err != nil {
				return fail(err)
			}
		}
		if cursor == "" {
			phase = "VerifySources"
		}
	}
	if next.Scanned > math.MaxInt64-int64(scanned) {
		return fail(datastore.ErrRepairRequired)
	}
	next.Scanned += int64(scanned)
	next.Phase, next.Cursor = phase, cursor
	next.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(progress.ResourceVersion)
	if err != nil {
		return fail(err)
	}
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translation_audit_progress_by_namespace SET resource_version=?,run_id=?,phase=?,cursor=?,scanned=? WHERE namespace_uid=? IF resource_version=? AND run_id=? AND phase=? AND cursor=? AND scanned=?",
		next.ResourceVersion, mustParseUUID(next.RunID), next.Phase, next.Cursor, next.Scanned, mustParseUUID(namespace),
		progress.ResourceVersion, mustParseUUID(progress.RunID), progress.Phase, progress.Cursor, progress.Scanned)
	if err != nil {
		return fail(err)
	}
	if !applied {
		return fail(datastore.ErrConflict)
	}
	return next, nil
}

func validTranslationAuditPage(scanned, items int, cursor, prior string, limit int) bool {
	return scanned >= 0 && scanned <= limit && items <= scanned &&
		len(cursor) <= 32<<10 && (cursor == "" || cursor != prior)
}

var _ datastore.TranslationAuditRepairStore = (*scyllaDatastore)(nil)
