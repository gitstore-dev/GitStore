// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

func (s *scyllaDatastore) GetTranslationTargetAuditProgress(ctx context.Context, namespace string) (datastore.TranslationAuditProgress, error) {
	fail := func(err error) (datastore.TranslationAuditProgress, error) {
		return datastore.TranslationAuditProgress{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if _, _, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace, "scylla", "", 1); err != nil {
		return fail(err)
	}
	var raw string
	err := s.session.Session.Query("SELECT progress FROM translation_target_audits_by_namespace_uid WHERE namespace_uid=?",
		mustParseUUID(namespace)).WithContext(ctx).Consistency(gocql.Serial).Scan(&raw)
	if errors.Is(err, gocql.ErrNotFound) {
		return fail(datastore.ErrNotFound)
	}
	if err != nil {
		return fail(err)
	}
	var progress datastore.TranslationAuditProgress
	if len(raw) > 40<<10 || json.Unmarshal([]byte(raw), &progress) != nil ||
		progress.NamespaceUID != namespace || datastore.ValidateTranslationTargetAuditProgress(progress) != nil {
		return fail(datastore.ErrRepairRequired)
	}
	if _, _, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace, "scylla", progress.Cursor, 1); err != nil {
		return fail(datastore.ErrRepairRequired)
	}
	return progress, nil
}

func (s *scyllaDatastore) RepairTranslationTargetAuditTurn(ctx context.Context, namespace string, limit int) (datastore.TranslationAuditProgress, error) {
	fail := func(err error) (datastore.TranslationAuditProgress, error) {
		return datastore.TranslationAuditProgress{}, err
	}
	current, err := s.GetTranslationTargetAuditProgress(ctx, namespace)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return fail(err)
	}
	next, err := datastore.PlanTranslationTargetAuditTurn(ctx, s, namespace, current, limit)
	if errors.Is(err, datastore.ErrConflict) {
		name, nameErr := s.translationTargetAuditNamespace(ctx, namespace)
		if nameErr != nil {
			return fail(nameErr)
		}
		// Classification and revocation use the same frozen owner. A racing
		// proved writer must not inherit cancellation from an unproved grant.
		if recoveryErr := s.recoverTranslationTargetMutation(ctx, name, true); recoveryErr != nil {
			return fail(recoveryErr)
		}
	}
	if err != nil || next == current {
		return next, err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return fail(err)
	}
	if err := s.mutations.injector.Inject("advance-target-audit", failureBefore); err != nil {
		return fail(err)
	}
	var applied bool
	if current.ResourceVersion == "" {
		applied, err = s.translationIndexCAS(ctx,
			"INSERT INTO translation_target_audits_by_namespace_uid (namespace_uid,progress) VALUES (?,?) IF NOT EXISTS",
			mustParseUUID(namespace), string(raw))
	} else {
		previous, marshalErr := json.Marshal(current)
		if marshalErr != nil {
			return fail(marshalErr)
		}
		applied, err = s.translationIndexCAS(ctx,
			"UPDATE translation_target_audits_by_namespace_uid SET progress=? WHERE namespace_uid=? IF progress=?",
			string(raw), mustParseUUID(namespace), string(previous))
	}
	if err != nil {
		return fail(err)
	}
	if !applied {
		return fail(datastore.ErrConflict)
	}
	if err := s.mutations.injector.Inject("advance-target-audit", failureAfter); err != nil {
		return fail(err)
	}
	return next, nil
}

func (s *scyllaDatastore) ReadTranslationTargetMutationEpoch(ctx context.Context, namespace string) (string, error) {
	return s.readTranslationTargetEpoch(ctx, namespace, false)
}

func (s *scyllaDatastore) ReadTranslationTargetCoverageEpoch(ctx context.Context, namespace string) (string, error) {
	return s.readTranslationTargetEpoch(ctx, namespace, true)
}

func (s *scyllaDatastore) readTranslationTargetEpoch(ctx context.Context, namespace string, coverage bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id, err := gocql.ParseUUID(namespace); err != nil || id.String() != namespace {
		return "", datastore.ErrInvalidArgument
	}
	name, err := s.translationTargetAuditNamespace(ctx, namespace)
	if err != nil {
		return "", err
	}
	var epoch, owner *string
	column := "translation_epoch"
	if coverage {
		column = "translation_coverage_epoch"
	}
	err = s.session.Session.Query(
		"SELECT "+column+",translation_owner FROM "+translationTargetMutationClockTable+" WHERE namespace=?", name,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&epoch, &owner)
	if errors.Is(err, gocql.ErrNotFound) {
		return namespace, nil
	}
	if err != nil {
		return "", err
	}
	if epoch == nil || owner == nil || len(*owner) > 4<<10 {
		return "", datastore.ErrRepairRequired
	}
	if id, err := gocql.ParseUUID(*epoch); err != nil || id.String() != *epoch {
		return "", datastore.ErrRepairRequired
	}
	if *owner != "" {
		mutation, err := decodeTranslationTargetMutation(name, *owner)
		if err != nil {
			return "", err
		}
		if !coverage {
			return "", datastore.ErrConflict
		}
		preserves, err := s.translationTargetMutationPreservesCoverage(ctx, mutation)
		if err != nil {
			return "", err
		}
		if !preserves {
			return "", datastore.ErrConflict
		}
	}
	if coverage && *epoch == uuid.Nil.String() {
		return namespace, nil
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(namespace+"/"+*epoch)).String(), nil
}

func (s *scyllaDatastore) translationTargetAuditNamespace(ctx context.Context, namespace string) (string, error) {
	var name string
	err := s.session.Session.Query("SELECT name FROM namespaces_by_uid WHERE uid=?", mustParseUUID(namespace)).
		WithContext(ctx).Consistency(gocql.Serial).Scan(&name)
	if errors.Is(err, gocql.ErrNotFound) {
		return "", datastore.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if name == "" || len(name) > 253 {
		return "", datastore.ErrRepairRequired
	}
	return name, nil
}

func (s *scyllaDatastore) ListTranslationTargetSourceAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationTargetSourceAuditPage, error) {
	fail := func(err error) (datastore.TranslationTargetSourceAuditPage, error) {
		return datastore.TranslationTargetSourceAuditPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	layout, state, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace, "scylla", cursor, limit)
	if err != nil {
		return fail(err)
	}
	name, err := s.translationTargetAuditNamespace(ctx, namespace)
	if err != nil {
		return fail(err)
	}
	kinds := [...]string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"}
	kind := kinds[layout]
	rows := s.session.Session.Query("SELECT namespace,creation_timestamp,uid,name,api_version,resource_version,translation_retired FROM " + authoritativeTable(kind)).
		WithContext(ctx).Consistency(gocql.Quorum).PageSize(limit).PageState(state).Iter()
	page := datastore.TranslationTargetSourceAuditPage{}
	var owner, targetName, version, resourceVersion string
	var uid gocql.UUID
	var created time.Time
	var primaryRetired *bool
	for rows.Scan(&owner, &created, &uid, &targetName, &version, &resourceVersion, &primaryRetired) {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return fail(err)
		}
		page.Scanned++
		if page.Scanned > limit {
			_ = rows.Close()
			return fail(datastore.ErrRepairRequired)
		}
		if owner != name {
			continue
		}
		var retired *bool
		err := s.session.Session.Query(
			"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", kind, uid,
		).WithContext(ctx).Consistency(gocql.Serial).Scan(&retired)
		if err == nil && (retired == nil || *retired) {
			continue
		}
		if err != nil && !errors.Is(err, gocql.ErrNotFound) {
			_ = rows.Close()
			return fail(err)
		}
		if primaryRetired != nil {
			_ = rows.Close()
			if *primaryRetired {
				return fail(datastore.ErrConflict)
			}
			return fail(datastore.ErrRepairRequired)
		}
		page.Items = append(page.Items, datastore.TranslationTargetSourceAuditHint{
			Target:    datastore.TranslationTargetKey{NamespaceUID: namespace, APIVersion: version, Kind: kind, Name: targetName},
			Namespace: owner, UID: uid.String(), CreationTimestamp: created, ResourceVersion: resourceVersion,
		})
	}
	next := append([]byte(nil), rows.PageState()...)
	if err := rows.Close(); err != nil {
		return fail(err)
	}
	if len(next) == 0 {
		layout++
	}
	if layout < len(kinds) {
		page.Next, err = datastore.EncodeTranslationTargetSourceAuditCursor(namespace, "scylla", layout, next)
		if err != nil {
			return fail(err)
		}
	}
	return page, nil
}

func (s *scyllaDatastore) RecoverTranslationTargetSourceAudit(ctx context.Context, hint datastore.TranslationTargetSourceAuditHint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := datastore.TranslationTargetFenceID(hint.Target); err != nil {
		return datastore.ErrInvalidArgument
	}
	uid, err := gocql.ParseUUID(hint.UID)
	if err != nil || uid.String() != hint.UID || hint.CreationTimestamp.IsZero() ||
		hint.Namespace == "" || len(hint.Namespace) > 253 || len(hint.ResourceVersion) > 256 {
		return datastore.ErrInvalidArgument
	}
	if _, err := datastore.AdvanceTranslationIndexVersion(hint.ResourceVersion); err != nil {
		return datastore.ErrRepairRequired
	}
	namespace, err := s.translationTargetAuditNamespace(ctx, hint.Target.NamespaceUID)
	if err != nil {
		return err
	}
	if namespace != hint.Namespace {
		return datastore.ErrRepairRequired
	}
	var name, version string
	err = s.session.Session.Query("SELECT name,resource_version FROM "+authoritativeTable(hint.Target.Kind)+
		" WHERE namespace=? AND creation_timestamp=? AND uid=?", namespace, hint.CreationTimestamp, uid).
		WithContext(ctx).Consistency(gocql.Serial).Scan(&name, &version)
	if errors.Is(err, gocql.ErrNotFound) {
		return datastore.ErrConflict
	}
	if err != nil {
		return err
	}
	if name != hint.Target.Name || version != hint.ResourceVersion {
		return datastore.ErrConflict
	}
	fence, err := s.GetTranslationTargetFence(ctx, hint.Target)
	if errors.Is(err, datastore.ErrNotFound) {
		return s.backfillTranslationTargetSource(ctx, hint, nil)
	}
	if err != nil {
		return err
	}
	if fence.Phase == datastore.TranslationTargetUpdating {
		if fence.Intent == nil || !fence.Intent.CreationTimestamp.Equal(hint.CreationTimestamp) ||
			(fence.Intent.NextUID != hint.UID && fence.Intent.PreviousUID != hint.UID) {
			return datastore.ErrConflict
		}
		if err := s.RecoverTranslationTargetWrite(ctx, hint.Target); err != nil {
			return err
		}
		return datastore.ErrConflict
	}
	evidence, err := s.ReadTargetEvidence(ctx, hint.Target)
	if errors.Is(err, datastore.ErrRepairRequired) && fence.Evidence == nil {
		return s.backfillTranslationTargetSource(ctx, hint, fence)
	}
	if err != nil {
		return err
	}
	if evidence.UID != hint.UID || evidence.ResourceVersion != hint.ResourceVersion ||
		!fence.CreationTimestamp.Equal(hint.CreationTimestamp) {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) VerifyTranslationTargetSourceCoverage(ctx context.Context, hint datastore.TranslationTargetSourceAuditHint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateTranslationTargetSourceAuditHint(hint); err != nil {
		return err
	}
	namespace, err := s.translationTargetAuditNamespace(ctx, hint.Target.NamespaceUID)
	if err != nil {
		return err
	}
	if namespace != hint.Namespace {
		return datastore.ErrRepairRequired
	}
	retired, err := translationTargetUIDRetired(ctx, s.session.Session, hint.Target.Kind, mustParseUUID(hint.UID))
	if err != nil || retired {
		return err
	}
	var name, version, kind string
	var identity *string
	var primaryRetired *bool
	err = s.session.Session.Query(
		"SELECT name,api_version,kind,translation_source_identity,translation_retired FROM "+
			authoritativeTable(hint.Target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
		namespace, hint.CreationTimestamp, mustParseUUID(hint.UID),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&name, &version, &kind, &identity, &primaryRetired)
	if errors.Is(err, gocql.ErrNotFound) {
		retired, err := translationTargetUIDRetired(ctx, s.session.Session, hint.Target.Kind, mustParseUUID(hint.UID))
		if err != nil || retired {
			return err
		}
		return datastore.ErrRepairRequired
	}
	if err != nil {
		return err
	}
	var source translationTargetSourceRecord
	if primaryRetired != nil {
		return datastore.ErrConflict
	}
	if name != hint.Target.Name || version != hint.Target.APIVersion || kind != hint.Target.Kind ||
		identity == nil || len(*identity) > 512 || json.Unmarshal([]byte(*identity), &source) != nil || source.Invalidated {
		return datastore.ErrRepairRequired
	}
	fence, err := s.GetTranslationTargetFence(ctx, hint.Target)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.ErrRepairRequired
	}
	if err != nil {
		return err
	}
	if intent := fence.Intent; intent != nil {
		var proof translationTargetSourceProof
		if json.Unmarshal([]byte(intent.SourceProof), &proof) != nil || proof.Namespace != namespace {
			return datastore.ErrRepairRequired
		}
		if intent.PreviousUID == "" {
			if proof.PreviousIdentity != nil {
				return datastore.ErrRepairRequired
			}
		} else {
			var previous translationTargetSourceRecord
			if proof.PreviousIdentity == nil || len(*proof.PreviousIdentity) > 512 ||
				json.Unmarshal([]byte(*proof.PreviousIdentity), &previous) != nil || previous.NamespaceUID != hint.Target.NamespaceUID {
				return datastore.ErrRepairRequired
			}
			if !intent.Backfilling && (fence.Evidence == nil || !fence.CreationTimestamp.Equal(intent.CreationTimestamp) ||
				(!intent.Deleting && (previous.WriteToken == "" || previous.ContentDigest != fence.Evidence.ContentDigest))) {
				return datastore.ErrRepairRequired
			}
		}
	}
	return datastore.ValidateTranslationTargetSourceCoverage(hint, source.TranslationTargetSourceIdentity, fence)
}

func (s *scyllaDatastore) backfillTranslationTargetSource(ctx context.Context, hint datastore.TranslationTargetSourceAuditHint, current *datastore.TranslationTargetFence) error {
	row := productRow{Namespace: hint.Namespace, UID: mustParseUUID(hint.UID), CreationTimestamp: hint.CreationTimestamp}
	var identity *string
	var retired *bool
	var deletion *time.Time
	err := s.session.Session.Query(
		"SELECT name,api_version,kind,generation,resource_version,revision,spec,body,translation_source_identity,translation_retired,deletion_timestamp FROM "+
			authoritativeTable(hint.Target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
		hint.Namespace, hint.CreationTimestamp, row.UID,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(
		&row.Name, &row.APIVersion, &row.Kind, &row.Generation, &row.ResourceVersion, &row.Revision,
		&row.Spec, &row.Body, &identity, &retired, &deletion)
	if err != nil {
		return err
	}
	if row.Name != hint.Target.Name || row.APIVersion != hint.Target.APIVersion || row.Kind != hint.Target.Kind ||
		row.ResourceVersion != hint.ResourceVersion {
		return datastore.ErrConflict
	}
	var previous translationTargetSourceRecord
	if identity == nil || len(*identity) > 512 || json.Unmarshal([]byte(*identity), &previous) != nil ||
		previous.NamespaceUID != hint.Target.NamespaceUID || deletion != nil ||
		(previous.WriteToken != "" && !previous.Invalidated) {
		return datastore.ErrRepairRequired
	}
	if err := s.checkLiveTranslationTargetUID(ctx, hint.Target.Kind, row.UID, retired); err != nil {
		return err
	}
	digest, err := translation.TargetContentDigestStringSpec(ctx, row.APIVersion, row.Kind, row.Spec, row.Body)
	if err != nil {
		return err
	}
	proof, err := json.Marshal(translationTargetSourceProof{Namespace: hint.Namespace, PreviousIdentity: identity})
	if err != nil {
		return err
	}
	id, err := gocql.RandomUUID()
	if err != nil {
		return err
	}
	intent := datastore.TranslationTargetWriteIntent{
		ID: id.String(), PreviousUID: hint.UID, NextUID: hint.UID, ExpectedResourceVersion: hint.ResourceVersion,
		SourceDigest: digest, SourceProof: string(proof), CreationTimestamp: hint.CreationTimestamp, Backfilling: true,
	}
	expected := ""
	if current != nil {
		expected = current.Token
	}
	planned, err := datastore.PlanTranslationTargetWrite(current, hint.Target, intent, expected)
	if err != nil {
		return err
	}
	evidence := &catalog.TranslationTargetEvidence{
		APIVersion: row.APIVersion, Kind: row.Kind, Namespace: row.Namespace, Name: row.Name,
		UID: hint.UID, Generation: row.Generation, ResourceVersion: row.ResourceVersion, Revision: row.Revision, ContentDigest: digest,
	}
	if _, err := datastore.PlanTranslationTargetCompletion(planned, planned.Token, evidence); err != nil {
		return err
	}
	if err := s.prepareTranslationTargetRetirement(ctx, hint.Target.Kind, hint.Namespace, hint.CreationTimestamp, hint.UID); err != nil {
		return err
	}
	pending, err := s.BeginTranslationTargetWrite(ctx, hint.Target, intent, expected)
	if err != nil {
		return err
	}
	next, err := json.Marshal(translationTargetSourceRecord{TranslationTargetSourceIdentity: datastore.TranslationTargetSourceIdentity{
		NamespaceUID: hint.Target.NamespaceUID, WriteToken: pending.Token, ContentDigest: digest,
	}})
	if err != nil {
		return err
	}
	mutation := translationTargetMutation{
		Namespace: hint.Namespace, Kind: hint.Target.Kind, UID: hint.UID, CreationTimestamp: hint.CreationTimestamp,
		PreviousIdentity: identity, NextIdentity: string(next), CancellationToken: pending.Token,
	}
	applied, err := s.executeTranslationTargetCAS(ctx, mutation, func() (bool, error) {
		return s.translationIndexCAS(ctx, "UPDATE "+authoritativeTable(hint.Target.Kind)+
			" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=? AND translation_retired=null AND translation_source_identity=?",
			string(next), hint.Namespace, hint.CreationTimestamp, row.UID, hint.ResourceVersion, identity)
	})
	if err != nil {
		return err
	}
	if !applied {
		return errors.Join(datastore.ErrConflict, s.RecoverTranslationTargetWrite(ctx, hint.Target))
	}
	return s.CompleteTranslationTargetWrite(ctx, hint.Target, pending.Token, evidence)
}

func (s *scyllaDatastore) recoverTranslationTargetBackfill(ctx context.Context, fence *datastore.TranslationTargetFence) error {
	intent, target := fence.Intent, fence.Target
	var proof translationTargetSourceProof
	var previous translationTargetSourceRecord
	if fence.Evidence != nil || intent.CreationTimestamp.IsZero() ||
		json.Unmarshal([]byte(intent.SourceProof), &proof) != nil || proof.Namespace == "" ||
		proof.PreviousIdentity == nil || len(*proof.PreviousIdentity) > 512 ||
		json.Unmarshal([]byte(*proof.PreviousIdentity), &previous) != nil ||
		previous.NamespaceUID != target.NamespaceUID || (previous.WriteToken != "" && !previous.Invalidated) {
		return datastore.ErrRepairRequired
	}
	for range 4 {
		evidence := &catalog.TranslationTargetEvidence{Namespace: proof.Namespace, UID: intent.NextUID}
		var identity *string
		var retired *bool
		var deletion *time.Time
		err := s.session.Session.Query(
			"SELECT name,api_version,kind,generation,resource_version,revision,translation_source_identity,translation_retired,deletion_timestamp FROM "+
				authoritativeTable(target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
			proof.Namespace, intent.CreationTimestamp, mustParseUUID(intent.NextUID),
		).WithContext(ctx).Consistency(gocql.Serial).Scan(
			&evidence.Name, &evidence.APIVersion, &evidence.Kind, &evidence.Generation, &evidence.ResourceVersion,
			&evidence.Revision, &identity, &retired, &deletion)
		if errors.Is(err, gocql.ErrNotFound) {
			proven, proofErr := translationTargetUIDRetired(ctx, s.session.Session, target.Kind, mustParseUUID(intent.NextUID))
			if proofErr != nil {
				return proofErr
			}
			if !proven {
				return datastore.ErrRepairRequired
			}
			return s.abortTranslationTargetWrite(ctx, fence, nil)
		}
		if err != nil {
			return err
		}
		var source translationTargetSourceRecord
		if identity == nil || len(*identity) > 512 || json.Unmarshal([]byte(*identity), &source) != nil ||
			source.NamespaceUID != target.NamespaceUID || evidence.Name != target.Name ||
			evidence.APIVersion != target.APIVersion || evidence.Kind != target.Kind {
			return datastore.ErrRepairRequired
		}
		if retired != nil {
			if !*retired {
				return datastore.ErrRepairRequired
			}
			if err := s.retireTranslationTargetUID(ctx, target.Kind, intent.NextUID); err != nil {
				return err
			}
			return s.abortTranslationTargetWrite(ctx, fence, nil)
		}
		if source.WriteToken == fence.Token {
			if source.Canceled {
				return s.abortTranslationTargetWrite(ctx, fence, nil)
			}
			if source.Invalidated || source.ContentDigest != intent.SourceDigest || deletion != nil {
				return datastore.ErrRepairRequired
			}
			evidence.ContentDigest = source.ContentDigest
			return s.CompleteTranslationTargetWrite(ctx, target, fence.Token, evidence)
		}
		if *identity != *proof.PreviousIdentity {
			return s.abortTranslationTargetWrite(ctx, fence, nil)
		}
		source.WriteToken, source.Canceled, source.Invalidated = fence.Token, true, true
		marker, err := json.Marshal(source)
		if err != nil {
			return err
		}
		mutation := translationTargetMutation{
			Namespace: proof.Namespace, Kind: target.Kind, UID: intent.NextUID, CreationTimestamp: intent.CreationTimestamp,
			PreviousIdentity: identity, NextIdentity: string(marker), CancellationToken: fence.Token,
		}
		applied, err := s.executeTranslationTargetCAS(ctx, mutation, func() (bool, error) {
			return s.translationIndexCAS(ctx, "UPDATE "+authoritativeTable(target.Kind)+
				" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=? AND translation_retired=null AND translation_source_identity=?",
				string(marker), proof.Namespace, intent.CreationTimestamp, mustParseUUID(intent.NextUID), evidence.ResourceVersion, identity)
		})
		if err != nil {
			return err
		}
		if applied {
			return s.abortTranslationTargetWrite(ctx, fence, nil)
		}
	}
	return datastore.ErrConflict
}

var _ datastore.TranslationTargetSourceAuditStore = (*scyllaDatastore)(nil)
var _ datastore.TranslationTargetMutationEpochStore = (*scyllaDatastore)(nil)
var _ datastore.TranslationTargetAuditStore = (*scyllaDatastore)(nil)
