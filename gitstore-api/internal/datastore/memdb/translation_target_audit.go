// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	gomemdb "github.com/hashicorp/go-memdb"
)

var translationTargetAuditTables = [...]struct{ kind, table string }{
	{"Product", "product"},
	{"ProductVariant", "product_variant"},
	{"CategoryTaxonomy", "category_taxonomy"},
	{"Collection", "collection"},
	{"File", "file"},
}

func (m *memdbDatastore) GetTranslationTargetAuditProgress(ctx context.Context, namespace string) (datastore.TranslationAuditProgress, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationAuditProgress{}, err
	}
	if _, _, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace, "memdb", "", 1); err != nil {
		return datastore.TranslationAuditProgress{}, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("translation_target_audit", "id", namespace)
	if err != nil || raw == nil {
		return datastore.TranslationAuditProgress{}, notFoundOrErr(err)
	}
	progress := *raw.(*datastore.TranslationAuditProgress)
	if err := datastore.ValidateTranslationTargetAuditProgress(progress); err != nil {
		return datastore.TranslationAuditProgress{}, err
	}
	if _, _, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace, "memdb", progress.Cursor, 1); err != nil {
		return datastore.TranslationAuditProgress{}, datastore.ErrRepairRequired
	}
	return progress, nil
}

func (m *memdbDatastore) RepairTranslationTargetAuditTurn(ctx context.Context, namespace string, limit int) (datastore.TranslationAuditProgress, error) {
	current, err := m.GetTranslationTargetAuditProgress(ctx, namespace)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return datastore.TranslationAuditProgress{}, err
	}
	next, err := datastore.PlanTranslationTargetAuditTurn(ctx, m, namespace, current, limit)
	if err != nil || next == current {
		return next, err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	raw, err := txn.First("translation_target_audit", "id", namespace)
	if err != nil {
		return datastore.TranslationAuditProgress{}, err
	}
	if (raw == nil && current.ResourceVersion != "") ||
		(raw != nil && *raw.(*datastore.TranslationAuditProgress) != current) {
		return datastore.TranslationAuditProgress{}, datastore.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return datastore.TranslationAuditProgress{}, err
	}
	if err := txn.Insert("translation_target_audit", &next); err != nil {
		return datastore.TranslationAuditProgress{}, err
	}
	txn.Commit()
	return next, nil
}

func (m *memdbDatastore) ReadTranslationTargetMutationEpoch(ctx context.Context, namespace string) (string, error) {
	return m.readTranslationTargetEpoch(ctx, namespace, false)
}

func (m *memdbDatastore) ReadTranslationTargetCoverageEpoch(ctx context.Context, namespace string) (string, error) {
	return m.readTranslationTargetEpoch(ctx, namespace, true)
}

func (m *memdbDatastore) readTranslationTargetEpoch(ctx context.Context, namespace string, coverage bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id, err := uuid.Parse(namespace); err != nil || id.String() != namespace {
		return "", datastore.ErrInvalidArgument
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespaces", "id", namespace)
	if err != nil || raw == nil {
		return "", notFoundOrErr(err)
	}
	clock, err := txn.First("translation_target_clock", "id", raw.(*datastore.Namespace).Name)
	if err != nil {
		return "", err
	}
	if clock == nil {
		return namespace, nil
	}
	epoch := clock.(*translationTargetMutationClock).Epoch
	if coverage {
		epoch = clock.(*translationTargetMutationClock).CoverageEpoch
	}
	if id, err := uuid.Parse(epoch); err != nil || id.String() != epoch {
		return "", datastore.ErrRepairRequired
	}
	if coverage && epoch == uuid.Nil.String() {
		return namespace, nil
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(namespace+"/"+epoch)).String(), nil
}

func (m *memdbDatastore) ListTranslationTargetSourceAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationTargetSourceAuditPage, error) {
	fail := func(err error) (datastore.TranslationTargetSourceAuditPage, error) {
		return datastore.TranslationTargetSourceAuditPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	layout, state, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace, "memdb", cursor, limit)
	if err != nil {
		return fail(err)
	}
	after := string(state)
	if after != "" {
		if uid, err := uuid.Parse(after); err != nil || uid.String() != after {
			return fail(datastore.ErrInvalidArgument)
		}
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	rawNamespace, err := txn.First("namespaces", "id", namespace)
	if err != nil || rawNamespace == nil {
		return fail(notFoundOrErr(err))
	}
	owner := rawNamespace.(*datastore.Namespace).Name
	source := translationTargetAuditTables[layout]
	var rows gomemdb.ResultIterator
	if after == "" {
		rows, err = txn.Get(source.table, "id")
	} else {
		rows, err = txn.LowerBound(source.table, "id", after)
	}
	if err != nil {
		return fail(err)
	}
	page := datastore.TranslationTargetSourceAuditPage{}
	for raw := rows.Next(); raw != nil; raw = rows.Next() {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		target, _ := translationMaterialView(raw)
		if target == nil {
			return fail(datastore.ErrRepairRequired)
		}
		if target.UID == after {
			continue
		}
		after = target.UID
		page.Scanned++
		live := target.Namespace == owner
		if live {
			err := checkTranslationTargetUID(txn, source.kind, target.UID)
			if err != nil && !errors.Is(err, datastore.ErrAlreadyExists) {
				return fail(err)
			}
			live = err == nil
		}
		if live {
			page.Items = append(page.Items, datastore.TranslationTargetSourceAuditHint{
				Target: datastore.TranslationTargetKey{
					NamespaceUID: namespace, APIVersion: target.APIVersion, Kind: source.kind, Name: target.Name,
				},
				Namespace: owner, UID: target.UID, CreationTimestamp: target.CreationTimestamp, ResourceVersion: target.ResourceVersion,
			})
		}
		if page.Scanned == limit {
			page.Next, err = datastore.EncodeTranslationTargetSourceAuditCursor(namespace, "memdb", layout, []byte(after))
			return page, err
		}
	}
	if layout+1 < len(translationTargetAuditTables) {
		page.Next, err = datastore.EncodeTranslationTargetSourceAuditCursor(namespace, "memdb", layout+1, nil)
	}
	return page, err
}

func (m *memdbDatastore) RecoverTranslationTargetSourceAudit(ctx context.Context, hint datastore.TranslationTargetSourceAuditHint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := datastore.TranslationTargetFenceID(hint.Target); err != nil {
		return datastore.ErrInvalidArgument
	}
	uid, err := uuid.Parse(hint.UID)
	if err != nil || uid.String() != hint.UID || hint.CreationTimestamp.IsZero() ||
		hint.Namespace == "" || len(hint.Namespace) > 253 || len(hint.ResourceVersion) > 256 {
		return datastore.ErrInvalidArgument
	}
	if _, err := datastore.AdvanceTranslationIndexVersion(hint.ResourceVersion); err != nil {
		return datastore.ErrRepairRequired
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	rawNamespace, err := txn.First("namespaces", "id", hint.Target.NamespaceUID)
	if err != nil || rawNamespace == nil {
		return notFoundOrErr(err)
	}
	if rawNamespace.(*datastore.Namespace).Name != hint.Namespace {
		return datastore.ErrRepairRequired
	}
	var table string
	for _, source := range translationTargetAuditTables {
		if source.kind == hint.Target.Kind {
			table = source.table
			break
		}
	}
	raw, err := txn.First(table, "id", hint.UID)
	if err != nil {
		return err
	}
	if raw == nil {
		return datastore.ErrConflict
	}
	target, _ := translationMaterialView(raw)
	if target == nil {
		return datastore.ErrRepairRequired
	}
	if target.Namespace != hint.Namespace || target.Name != hint.Target.Name ||
		target.ResourceVersion != hint.ResourceVersion || !target.CreationTimestamp.Equal(hint.CreationTimestamp) {
		return datastore.ErrConflict
	}
	evidence, err := m.ReadTargetEvidence(ctx, hint.Target)
	if errors.Is(err, datastore.ErrRepairRequired) {
		return m.backfillTranslationTargetSource(ctx, hint, table)
	}
	if err != nil {
		return err
	}
	if evidence.UID != hint.UID || evidence.ResourceVersion != hint.ResourceVersion {
		return datastore.ErrConflict
	}
	return nil
}

func (m *memdbDatastore) VerifyTranslationTargetSourceCoverage(ctx context.Context, hint datastore.TranslationTargetSourceAuditHint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateTranslationTargetSourceAuditHint(hint); err != nil {
		return err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	namespace, err := txn.First("namespaces", "id", hint.Target.NamespaceUID)
	if err != nil || namespace == nil {
		return notFoundOrErr(err)
	}
	if namespace.(*datastore.Namespace).Name != hint.Namespace {
		return datastore.ErrRepairRequired
	}
	if err := checkTranslationTargetUID(txn, hint.Target.Kind, hint.UID); err != nil {
		if errors.Is(err, datastore.ErrAlreadyExists) {
			return nil
		}
		return err
	}
	var table string
	for _, layout := range translationTargetAuditTables {
		if layout.kind == hint.Target.Kind {
			table = layout.table
			break
		}
	}
	raw, err := txn.First(table, "id", hint.UID)
	if err != nil {
		return err
	}
	if raw == nil {
		return datastore.ErrRepairRequired
	}
	source, identity := translationMaterialView(raw)
	if source == nil || identity == nil || source.Namespace != hint.Namespace ||
		source.Name != hint.Target.Name || source.APIVersion != hint.Target.APIVersion || source.Kind != hint.Target.Kind ||
		!source.CreationTimestamp.Equal(hint.CreationTimestamp) {
		return datastore.ErrRepairRequired
	}
	id, _ := datastore.TranslationTargetFenceID(hint.Target)
	head, err := txn.First("translation_target_fence", "id", id)
	if err != nil {
		return err
	}
	if head == nil {
		return datastore.ErrRepairRequired
	}
	return datastore.ValidateTranslationTargetSourceCoverage(hint, *identity, head.(*translationTargetFenceRow).Fence)
}

func (m *memdbDatastore) backfillTranslationTargetSource(ctx context.Context, hint datastore.TranslationTargetSourceAuditHint, table string) error {
	txn := m.db.Txn(true)
	defer txn.Abort()
	namespace, err := txn.First("namespaces", "id", hint.Target.NamespaceUID)
	if err != nil || namespace == nil {
		return notFoundOrErr(err)
	}
	if namespace.(*datastore.Namespace).Name != hint.Namespace {
		return datastore.ErrConflict
	}
	raw, err := txn.First(table, "id", hint.UID)
	if err != nil {
		return err
	}
	source, proof := translationMaterialView(raw)
	if source == nil {
		return datastore.ErrConflict
	}
	if source.Namespace != hint.Namespace || source.Name != hint.Target.Name ||
		source.ResourceVersion != hint.ResourceVersion || !source.CreationTimestamp.Equal(hint.CreationTimestamp) ||
		source.APIVersion != hint.Target.APIVersion || source.Kind != hint.Target.Kind {
		return datastore.ErrConflict
	}
	if proof.NamespaceUID != hint.Target.NamespaceUID || proof.WriteToken != "" || source.DeletionTimestamp != nil {
		return datastore.ErrRepairRequired
	}
	id, err := datastore.TranslationTargetFenceID(hint.Target)
	if err != nil {
		return err
	}
	head, err := txn.First("translation_target_fence", "id", id)
	if err != nil {
		return err
	}
	if head != nil {
		fence := head.(*translationTargetFenceRow).Fence
		if datastore.ValidateTranslationTargetFence(fence) != nil || fence.Evidence != nil {
			return datastore.ErrRepairRequired
		}
		if fence.Phase != datastore.TranslationTargetStable {
			return datastore.ErrConflict
		}
	}
	if err := checkTranslationTargetUID(txn, hint.Target.Kind, hint.UID); err != nil {
		return err
	}
	var replacement any
	switch current := raw.(type) {
	case *datastore.Product:
		copy := *current
		replacement = &copy
	case *datastore.ProductVariant:
		copy := *current
		replacement = &copy
	case *datastore.CategoryTaxonomy:
		copy := *current
		replacement = &copy
	case *datastore.Collection:
		copy := *current
		replacement = &copy
	case *datastore.File:
		copy := *current
		replacement = &copy
	default:
		return datastore.ErrRepairRequired
	}
	// Reuse initial proof construction inside this single atomic transaction;
	// the source lifetime and birth authority above must already be established.
	if err := txn.Delete(table, raw); err != nil {
		return err
	}
	if err := insertTranslationMaterialTarget(ctx, txn, table, replacement); err != nil {
		return err
	}
	_, next := translationMaterialView(replacement)
	if next.WriteToken == "" {
		return datastore.ErrRepairRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	txn.Commit()
	return nil
}

var _ datastore.TranslationTargetSourceAuditStore = (*memdbDatastore)(nil)
var _ datastore.TranslationTargetMutationEpochStore = (*memdbDatastore)(nil)
var _ datastore.TranslationTargetAuditStore = (*memdbDatastore)(nil)
