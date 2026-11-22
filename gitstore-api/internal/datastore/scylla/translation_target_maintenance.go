// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

var translationTargetMaintenanceRoot = uuid.NewSHA1(uuid.NameSpaceOID, []byte("gitstore/translation/target-maintenance")).String()

func translationTargetMaintenanceAnchorToken(namespace string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("gitstore/translation/target-maintenance/"+namespace)).String()
}

type translationTargetPendingRecord struct {
	Fence    datastore.TranslationTargetFence
	Expected string
}

func (s *scyllaDatastore) TranslationTargetMaintenanceStore() datastore.TranslationTargetMaintenanceStore {
	return s
}

func translationTargetMaintenanceEntry(id, token string) datastore.TranslationIndexEntry {
	return datastore.TranslationIndexEntry{
		TranslationUID: id, SourceVersion: "1", ReferenceEpoch: token,
		CreationTimestamp: time.Unix(1, 0).UTC(),
	}
}

func (s *scyllaDatastore) publishTranslationTargetMaintenanceCell(ctx context.Context, kind datastore.TranslationIndexKind, namespace, id, token string, bytes int64) error {
	shard, err := datastore.TranslationIndexShard(id)
	if err != nil {
		return err
	}
	index := datastore.TranslationIndexKey{Kind: kind, NamespaceUID: namespace, Shard: shard}
	entry := translationTargetMaintenanceEntry(id, token)
	record, err := s.ReserveTranslationIndex(ctx, index, entry, token, bytes+datastore.TranslationIndexPayloadBytes(index, entry))
	if err != nil {
		return err
	}
	return s.CommitTranslationIndex(ctx, record)
}

func (s *scyllaDatastore) readTranslationTargetPending(ctx context.Context, id string) (*translationTargetPendingRecord, bool, error) {
	parsed, err := gocql.ParseUUID(id)
	if err != nil {
		return nil, false, datastore.ErrInvalidArgument
	}
	var raw string
	var closed *bool
	err = s.session.Session.Query(
		"SELECT record,closed FROM translation_target_write_intents_by_id WHERE id=?", parsed,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&raw, &closed)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, false, datastore.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	var record translationTargetPendingRecord
	if closed == nil || len(raw) > translation.MaxDocumentBytes+(2<<20)+datastore.MaxTranslationTargetIntentBytes+4096 ||
		json.Unmarshal([]byte(raw), &record) != nil || datastore.ValidateTranslationTargetFence(&record.Fence) != nil ||
		record.Fence.Phase != datastore.TranslationTargetUpdating || record.Fence.Intent.ID != id ||
		record.Fence.Token == record.Expected {
		return nil, false, datastore.ErrRepairRequired
	}
	if record.Expected != "" {
		if previous, err := uuid.Parse(record.Expected); err != nil || previous.String() != record.Expected {
			return nil, false, datastore.ErrRepairRequired
		}
	}
	return &record, *closed, nil
}

func (s *scyllaDatastore) prepareTranslationTargetPending(ctx context.Context, next *datastore.TranslationTargetFence, expected string) (*datastore.TranslationTargetFence, error) {
	candidate := translationTargetPendingRecord{Fence: *datastore.CloneTranslationTargetFence(next), Expected: expected}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	_, err = s.translationIndexCAS(ctx,
		"INSERT INTO translation_target_write_intents_by_id (id,record,closed) VALUES (?,?,false) IF NOT EXISTS",
		mustParseUUID(next.Intent.ID), string(raw))
	if err != nil {
		return nil, err
	}
	record, closed, err := s.readTranslationTargetPending(ctx, next.Intent.ID)
	if err != nil {
		return nil, err
	}
	candidate.Fence.Token = record.Fence.Token
	if closed || !reflect.DeepEqual(candidate, *record) {
		return nil, datastore.ErrConflict
	}
	namespace := next.Target.NamespaceUID
	anchorToken := translationTargetMaintenanceAnchorToken(namespace)
	if err := s.publishTranslationTargetMaintenanceCell(ctx, datastore.TranslationTargetMaintenanceNamespaceIndex,
		translationTargetMaintenanceRoot, namespace, anchorToken, 0); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if err := s.publishTranslationTargetMaintenanceCell(ctx, datastore.TranslationTargetPendingIndex,
		namespace, next.Intent.ID, next.Intent.ID, int64(len(raw))); err != nil {
		return nil, err
	}
	return datastore.CloneTranslationTargetFence(&record.Fence), nil
}

func (s *scyllaDatastore) closeTranslationTargetPending(ctx context.Context, fence *datastore.TranslationTargetFence, sourceCleanupPending bool) error {
	record, closed, err := s.readTranslationTargetPending(ctx, fence.Intent.ID)
	if errors.Is(err, datastore.ErrNotFound) {
		// Older private metadata is settled by its source proof; backfill must
		// establish discovery before advertising a current-read capability.
		return nil
	}
	if err != nil {
		return err
	}
	if record.Fence.Token != fence.Token || record.Fence.Target != fence.Target {
		return datastore.ErrRepairRequired
	}
	if !closed {
		applied, err := s.translationIndexCAS(ctx,
			"UPDATE translation_target_write_intents_by_id SET closed=true WHERE id=? IF closed=false",
			mustParseUUID(fence.Intent.ID))
		if err != nil {
			return err
		}
		if !applied {
			latest, nowClosed, err := s.readTranslationTargetPending(ctx, fence.Intent.ID)
			if err != nil {
				return err
			}
			if !nowClosed || !reflect.DeepEqual(latest, record) {
				return datastore.ErrConflict
			}
		}
		if sourceCleanupPending {
			return nil
		}
	}
	if err := s.mutations.injector.Inject("retire-target-intent", failureBefore); err != nil {
		return err
	}
	placement, err := s.GetTranslationIndexReservation(ctx, fence.Intent.ID)
	if err != nil {
		return err
	}
	if placement.Index.Kind != datastore.TranslationTargetPendingIndex ||
		placement.Index.NamespaceUID != fence.Target.NamespaceUID || placement.Entry.TranslationUID != fence.Intent.ID {
		return datastore.ErrRepairRequired
	}
	if err := s.RemoveTranslationIndex(ctx, placement); err != nil {
		return err
	}
	// Unlike reusable Translation placements, each pending ID has a retained
	// closed authority of its own. A late cell replay can only rediscover that
	// tombstone; it cannot start the original expected-token head CAS again.
	key := translationCellKey(placement)
	values := append(key.values, int(placement.Entry.Slot), mustParseUUID(placement.Token))
	applied, err := s.translationIndexCAS(ctx, "DELETE FROM "+key.table+" WHERE "+key.predicate()+
		" IF retired=true AND slot=? AND reservation_token=?", values...)
	if err != nil || applied {
		return err
	}
	cell, err := s.getTranslationIndexCell(ctx, key, placement.Entry.TranslationUID)
	if err != nil {
		return err
	}
	if cell != nil {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) ListTranslationTargetMaintenanceNamespaces(ctx context.Context, cursor string, limit int) (datastore.TranslationDependentPage, error) {
	page, err := datastore.ReadTranslationIndexPage(ctx, datastore.TranslationIndexKey{
		Kind: datastore.TranslationTargetMaintenanceNamespaceIndex, NamespaceUID: translationTargetMaintenanceRoot,
	}, cursor, limit, s.getTranslationIndexHead, s.readTranslationIndexSegment)
	if err != nil {
		return datastore.TranslationDependentPage{}, err
	}
	for _, entry := range page.Items {
		if entry.SourceVersion != "1" || entry.ReferenceEpoch != translationTargetMaintenanceAnchorToken(entry.TranslationUID) ||
			!entry.CreationTimestamp.Equal(time.Unix(1, 0)) {
			return datastore.TranslationDependentPage{}, datastore.ErrRepairRequired
		}
	}
	return page, nil
}

func (s *scyllaDatastore) RecoverTranslationTargetMaintenance(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationDependentPage, error) {
	page, err := datastore.ReadTranslationIndexPage(ctx, datastore.TranslationIndexKey{
		Kind: datastore.TranslationTargetPendingIndex, NamespaceUID: namespace,
	}, cursor, limit, s.getTranslationIndexHead, s.readTranslationIndexSegment)
	if err != nil {
		return datastore.TranslationDependentPage{}, err
	}
	for _, entry := range page.Items {
		if entry.SourceVersion != "1" || entry.ReferenceEpoch != entry.TranslationUID ||
			!entry.CreationTimestamp.Equal(time.Unix(1, 0)) {
			return datastore.TranslationDependentPage{}, datastore.ErrRepairRequired
		}
		if err := s.recoverTranslationTargetPending(ctx, namespace, entry.TranslationUID); err != nil {
			return datastore.TranslationDependentPage{}, err
		}
	}
	return page, nil
}

func (s *scyllaDatastore) recoverTranslationTargetPending(ctx context.Context, namespace, id string) error {
	record, closed, err := s.readTranslationTargetPending(ctx, id)
	if err != nil {
		return err
	}
	if record.Fence.Target.NamespaceUID != namespace {
		return datastore.ErrRepairRequired
	}
	if closed {
		return s.settleTranslationTargetPending(ctx, &record.Fence)
	}
	current, err := s.GetTranslationTargetFence(ctx, record.Fence.Target)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return err
	}
	if current == nil && record.Expected != "" {
		return datastore.ErrRepairRequired
	}
	if current == nil || current.Token == record.Expected {
		if _, err := s.BeginTranslationTargetWrite(ctx, record.Fence.Target, *record.Fence.Intent, record.Expected); err != nil {
			return err
		}
		current, err = s.GetTranslationTargetFence(ctx, record.Fence.Target)
		if err != nil {
			return err
		}
	}
	if current.Token == record.Fence.Token && current.Phase == datastore.TranslationTargetUpdating {
		if !reflect.DeepEqual(current.Intent, record.Fence.Intent) {
			return datastore.ErrRepairRequired
		}
		if err := s.RecoverTranslationTargetWrite(ctx, record.Fence.Target); err != nil {
			return err
		}
	}
	// Any different token permanently excludes the original expected-token CAS,
	// even if a newer writer later cancels and restores identical material.
	return s.settleTranslationTargetPending(ctx, &record.Fence)
}

func (s *scyllaDatastore) settleTranslationTargetPending(ctx context.Context, fence *datastore.TranslationTargetFence) error {
	intent := fence.Intent
	if (intent.Deleting || intent.PreviousUID == "") && intent.SourceProof != "" {
		var proof translationTargetSourceProof
		if json.Unmarshal([]byte(intent.SourceProof), &proof) != nil || proof.Namespace == "" || intent.CreationTimestamp.IsZero() {
			return datastore.ErrRepairRequired
		}
		uid := intent.NextUID
		if intent.Deleting {
			uid = intent.PreviousUID
		}
		var retired *bool
		var identity *string
		err := s.session.Session.Query(
			"SELECT translation_retired,translation_source_identity FROM "+authoritativeTable(fence.Target.Kind)+
				" WHERE namespace=? AND creation_timestamp=? AND uid=?",
			proof.Namespace, intent.CreationTimestamp, mustParseUUID(uid),
		).WithContext(ctx).Consistency(gocql.Serial).Scan(&retired, &identity)
		if err != nil && !errors.Is(err, gocql.ErrNotFound) {
			return err
		}
		if err == nil && retired != nil {
			var source translationTargetSourceRecord
			if !*retired || identity == nil || len(*identity) > 512 || json.Unmarshal([]byte(*identity), &source) != nil ||
				source.NamespaceUID != fence.Target.NamespaceUID {
				return datastore.ErrRepairRequired
			}
			if err := s.prepareTranslationTargetRetirement(ctx, fence.Target.Kind, proof.Namespace, intent.CreationTimestamp, uid); err != nil {
				return err
			}
			if err := s.retireTranslationTargetUID(ctx, fence.Target.Kind, uid); err != nil {
				return err
			}
			// A replay of an old closed birth may encounter a later deletion.
			// Preserve that deletion's source proof until its owned head settles.
			if err := s.settleRetiredTranslationTargetHead(ctx, AuthoritativeResource{
				Kind: fence.Target.Kind, UID: uid, Namespace: proof.Namespace, CreationTimestamp: intent.CreationTimestamp,
			}); err != nil {
				return err
			}
			if _, err := s.translationIndexCAS(ctx,
				"DELETE FROM "+authoritativeTable(fence.Target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=? IF translation_retired=true AND translation_source_identity=?",
				proof.Namespace, intent.CreationTimestamp, mustParseUUID(uid), identity); err != nil {
				return err
			}
		}
	}
	return s.closeTranslationTargetPending(ctx, fence, false)
}

type translationTargetMaintenanceCursor struct {
	Cursor    string
	Namespace string
	Turns     int
}

func (s *scyllaDatastore) readTranslationTargetMaintenanceCursor(ctx context.Context, scope, namespace string) (*string, error) {
	var cursor string
	err := s.session.Session.Query(
		"SELECT cursor FROM translation_target_maintenance_cursors_by_namespace WHERE scope=? AND namespace_uid=?",
		scope, mustParseUUID(namespace),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&cursor)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(cursor) > 4096 {
		return nil, datastore.ErrRepairRequired
	}
	return &cursor, nil
}

func (s *scyllaDatastore) advanceTranslationTargetMaintenanceCursor(ctx context.Context, scope, namespace string, previous *string, next string) error {
	if previous != nil && *previous == next {
		return nil
	}
	var applied bool
	var err error
	if previous == nil {
		applied, err = s.translationIndexCAS(ctx,
			"INSERT INTO translation_target_maintenance_cursors_by_namespace (scope,namespace_uid,cursor) VALUES (?,?,?) IF NOT EXISTS",
			scope, mustParseUUID(namespace), next)
	} else {
		applied, err = s.translationIndexCAS(ctx,
			"UPDATE translation_target_maintenance_cursors_by_namespace SET cursor=? WHERE scope=? AND namespace_uid=? IF cursor=?",
			next, scope, mustParseUUID(namespace), *previous)
	}
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

// Discovery and recovery are separate durable phases: a turn spends at most
// sixteen head/segment probes and processes at most 256 thin cells. Per-namespace
// cursors provide round-robin progress without an unbounded in-memory map.
func (s *scyllaDatastore) RecoverTranslationTargetMaintenanceTurn(ctx context.Context) error {
	previous, err := s.readTranslationTargetMaintenanceCursor(ctx, "Discovery", translationTargetMaintenanceRoot)
	if err != nil {
		return err
	}
	var current translationTargetMaintenanceCursor
	if previous != nil && (json.Unmarshal([]byte(*previous), &current) != nil || len(current.Cursor) > 2048 ||
		current.Turns < 0 || current.Turns >= 4 || (current.Namespace == "" && current.Turns != 0)) {
		return datastore.ErrRepairRequired
	}
	var workErr error
	if current.Namespace == "" {
		page, err := s.ListTranslationTargetMaintenanceNamespaces(ctx, current.Cursor, 1)
		if err != nil {
			return err
		}
		current.Cursor = page.Next
		if len(page.Items) != 0 {
			current.Namespace = page.Items[0].TranslationUID
		}
	} else {
		if id, err := uuid.Parse(current.Namespace); err != nil || id.String() != current.Namespace {
			return datastore.ErrRepairRequired
		}
		cursor, err := s.readTranslationTargetMaintenanceCursor(ctx, "Pending", current.Namespace)
		if err != nil {
			return err
		}
		encoded := ""
		if cursor != nil {
			encoded = *cursor
		}
		page, err := s.RecoverTranslationTargetMaintenance(ctx, current.Namespace, encoded, datastore.MaxTranslationPageSize)
		workErr = err
		if workErr == nil {
			workErr = s.advanceTranslationTargetMaintenanceCursor(ctx, "Pending", current.Namespace, cursor, page.Next)
		}
		// A corrupt target does not prevent other namespaces from receiving a
		// turn. Its namespace cursor stays put for the next recovery attempt.
		current.Turns++
		if workErr != nil || page.Next == "" || current.Turns == 4 {
			current.Namespace, current.Turns = "", 0
		}
	}
	next, err := json.Marshal(current)
	if err != nil {
		return err
	}
	err = s.advanceTranslationTargetMaintenanceCursor(ctx, "Discovery", translationTargetMaintenanceRoot, previous, string(next))
	if workErr != nil {
		return workErr
	}
	return err
}

var _ datastore.TranslationTargetMaintenanceStore = (*scyllaDatastore)(nil)
