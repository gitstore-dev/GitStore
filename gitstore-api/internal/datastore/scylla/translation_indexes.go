// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

type translationIndexTarget struct {
	Target    datastore.TranslationTargetKey
	TargetUID string
}

func translationHeadTarget(index datastore.TranslationIndexKey) string {
	switch index.Kind {
	case datastore.TranslationNamespaceIndex, datastore.TranslationTargetPendingIndex, datastore.TranslationTargetMaintenanceNamespaceIndex:
		return ""
	case datastore.TranslationTargetUIDIndex:
		return index.TargetUID
	default:
		raw, _ := json.Marshal(index.Target)
		digest := sha256.Sum256(raw)
		return hex.EncodeToString(digest[:])
	}
}

func (s *scyllaDatastore) translationIndexCAS(ctx context.Context, statement string, args ...any) (bool, error) {
	return s.session.Session.Query(statement, args...).WithContext(ctx).
		Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).MapScanCAS(make(map[string]any))
}

func (s *scyllaDatastore) getTranslationIndexHead(ctx context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
	index.Segment = 0
	head := &datastore.TranslationIndexHead{Index: index}
	var segment int64
	var slot int
	var pending gocql.UUID
	err := s.session.Session.Query(
		"SELECT resource_version,segment,next_slot,reserved_bytes,pending_token FROM translation_index_heads_by_shard WHERE index_kind=? AND namespace_uid=? AND target_key=? AND shard=?",
		string(index.Kind), mustParseUUID(index.NamespaceUID), translationHeadTarget(index), int8(index.Shard),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&head.ResourceVersion, &segment, &slot, &head.ReservedBytes, &pending)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if segment < 0 || slot < 0 || slot > datastore.MaxTranslationSegmentRows ||
		head.ReservedBytes < 0 || head.ReservedBytes > datastore.MaxTranslationSegmentBytes {
		return nil, datastore.ErrRepairRequired
	}
	if _, err := datastore.AdvanceTranslationIndexVersion(head.ResourceVersion); err != nil {
		return nil, err
	}
	head.Segment, head.NextSlot = uint64(segment), uint32(slot)
	if pending != (gocql.UUID{}) {
		head.PendingToken = pending.String()
	}
	return head, nil
}

func (s *scyllaDatastore) GetTranslationIndexReservation(ctx context.Context, token string) (datastore.TranslationIndexReservation, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	parsed, err := gocql.ParseUUID(token)
	if err != nil {
		return datastore.TranslationIndexReservation{}, datastore.ErrInvalidArgument
	}
	var record datastore.TranslationIndexReservation
	var namespace, uid gocql.UUID
	var target string
	var shard int8
	var segment int64
	var slot int
	err = s.session.Session.Query(
		"SELECT resource_version,index_kind,namespace_uid,target_key,shard,segment,slot,uid,source_version,reference_epoch,payload_bytes,phase,creation_timestamp,expected_head_version,allocation_version,reserved_bytes FROM translation_index_reservations_by_token WHERE reservation_token=?",
		parsed,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(
		&record.ResourceVersion, &record.Index.Kind, &namespace, &target, &shard, &segment, &slot,
		&uid, &record.Entry.SourceVersion, &record.Entry.ReferenceEpoch, &record.PayloadBytes, &record.Phase,
		&record.Entry.CreationTimestamp, &record.ExpectedHeadVersion, &record.AllocationVersion, &record.ReservedBytes,
	)
	if errors.Is(err, gocql.ErrNotFound) {
		return record, datastore.ErrNotFound
	}
	if err != nil {
		return record, err
	}
	var identity translationIndexTarget
	if len(target) > 2<<20 || json.Unmarshal([]byte(target), &identity) != nil ||
		shard < 0 || shard >= datastore.TranslationIndexShards || segment < 0 ||
		slot < 0 || slot >= datastore.MaxTranslationSegmentRows || record.ReservedBytes < record.PayloadBytes ||
		record.ReservedBytes > datastore.MaxTranslationSegmentBytes {
		return datastore.TranslationIndexReservation{}, datastore.ErrRepairRequired
	}
	record.Token, record.Index.NamespaceUID, record.Entry.TranslationUID = token, namespace.String(), uid.String()
	record.Index.Target, record.Index.TargetUID = identity.Target, identity.TargetUID
	record.Index.Shard, record.Index.Segment, record.Entry.Slot = uint8(shard), uint64(segment), uint32(slot)
	if _, err := datastore.AdvanceTranslationIndexVersion(record.ResourceVersion); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	expectedAllocation := "1"
	if record.ExpectedHeadVersion != "" {
		expectedAllocation, err = datastore.AdvanceTranslationIndexVersion(record.ExpectedHeadVersion)
		if err != nil {
			return datastore.TranslationIndexReservation{}, err
		}
	}
	if record.AllocationVersion != expectedAllocation {
		return datastore.TranslationIndexReservation{}, datastore.ErrRepairRequired
	}
	index, entry := record.Index, record.Entry
	index.Segment, entry.Slot = 0, 0
	if _, _, err := datastore.PlanTranslationIndexReservation(nil, index, entry, token, record.PayloadBytes); err != nil {
		return datastore.TranslationIndexReservation{}, datastore.ErrRepairRequired
	}
	switch record.Phase {
	case datastore.TranslationReservationPreparing, datastore.TranslationReservationReserved,
		datastore.TranslationReservationCommitted, datastore.TranslationReservationRemoved:
		return record, nil
	default:
		return datastore.TranslationIndexReservation{}, datastore.ErrRepairRequired
	}
}

func (s *scyllaDatastore) putTranslationReservation(ctx context.Context, record datastore.TranslationIndexReservation, expected string) (bool, error) {
	if expected != "" {
		return s.translationIndexCAS(ctx,
			"UPDATE translation_index_reservations_by_token SET resource_version=?,segment=?,slot=?,expected_head_version=?,allocation_version=?,reserved_bytes=? WHERE reservation_token=? IF resource_version=? AND phase='Preparing'",
			record.ResourceVersion, int64(record.Index.Segment), int(record.Entry.Slot), record.ExpectedHeadVersion,
			record.AllocationVersion, record.ReservedBytes, mustParseUUID(record.Token), expected)
	}
	target, err := json.Marshal(translationIndexTarget{record.Index.Target, record.Index.TargetUID})
	if err != nil {
		return false, err
	}
	return s.translationIndexCAS(ctx,
		"INSERT INTO translation_index_reservations_by_token (reservation_token,resource_version,index_kind,namespace_uid,target_key,shard,segment,slot,uid,source_version,reference_epoch,payload_bytes,phase,creation_timestamp,expected_head_version,allocation_version,reserved_bytes) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) IF NOT EXISTS",
		mustParseUUID(record.Token), record.ResourceVersion, string(record.Index.Kind), mustParseUUID(record.Index.NamespaceUID),
		string(target), int8(record.Index.Shard), int64(record.Index.Segment), int(record.Entry.Slot),
		mustParseUUID(record.Entry.TranslationUID), record.Entry.SourceVersion, record.Entry.ReferenceEpoch,
		record.PayloadBytes, string(record.Phase), record.Entry.CreationTimestamp,
		record.ExpectedHeadVersion, record.AllocationVersion, record.ReservedBytes)
}

func (s *scyllaDatastore) allocateTranslationHead(ctx context.Context, head *datastore.TranslationIndexHead, expected string) (bool, error) {
	index := head.Index
	if expected == "" {
		return s.translationIndexCAS(ctx,
			"INSERT INTO translation_index_heads_by_shard (index_kind,namespace_uid,target_key,shard,resource_version,segment,next_slot,reserved_bytes,pending_token) VALUES (?,?,?,?,?,?,?,?,?) IF NOT EXISTS",
			string(index.Kind), mustParseUUID(index.NamespaceUID), translationHeadTarget(index), int8(index.Shard),
			head.ResourceVersion, int64(head.Segment), int(head.NextSlot), head.ReservedBytes, mustParseUUID(head.PendingToken))
	}
	return s.translationIndexCAS(ctx,
		"UPDATE translation_index_heads_by_shard SET resource_version=?,segment=?,next_slot=?,reserved_bytes=?,pending_token=? WHERE index_kind=? AND namespace_uid=? AND target_key=? AND shard=? IF resource_version=? AND pending_token=null",
		head.ResourceVersion, int64(head.Segment), int(head.NextSlot), head.ReservedBytes, mustParseUUID(head.PendingToken),
		string(index.Kind), mustParseUUID(index.NamespaceUID), translationHeadTarget(index), int8(index.Shard), expected)
}

func (s *scyllaDatastore) recoverTranslationHead(ctx context.Context, head *datastore.TranslationIndexHead) error {
	record, err := s.GetTranslationIndexReservation(ctx, head.PendingToken)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.ErrRepairRequired
	}
	if err != nil {
		return err
	}
	check := record
	check.Phase = datastore.TranslationReservationReserved
	finished, err := datastore.FinishTranslationIndexReservation(head, check)
	if err != nil {
		return err
	}
	if record.Phase == datastore.TranslationReservationPreparing {
		next, err := datastore.AdvanceTranslationIndexVersion(record.ResourceVersion)
		if err != nil {
			return err
		}
		applied, err := s.translationIndexCAS(ctx,
			"UPDATE translation_index_reservations_by_token SET resource_version=?,phase='Reserved' WHERE reservation_token=? IF resource_version=? AND phase='Preparing'",
			next, mustParseUUID(record.Token), record.ResourceVersion)
		if err != nil {
			return err
		}
		if !applied {
			return datastore.ErrConflict
		}
	}
	index := head.Index
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translation_index_heads_by_shard SET resource_version=?,pending_token=null WHERE index_kind=? AND namespace_uid=? AND target_key=? AND shard=? IF resource_version=? AND pending_token=?",
		finished.ResourceVersion, string(index.Kind), mustParseUUID(index.NamespaceUID), translationHeadTarget(index), int8(index.Shard),
		head.ResourceVersion, mustParseUUID(head.PendingToken))
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func translationHeadAfterAllocation(head *datastore.TranslationIndexHead, record datastore.TranslationIndexReservation) bool {
	if head == nil {
		return false
	}
	actual, ok := new(big.Int).SetString(head.ResourceVersion, 10)
	if !ok {
		return false
	}
	allocated, ok := new(big.Int).SetString(record.AllocationVersion, 10)
	return ok && actual.Cmp(allocated) > 0
}

func (s *scyllaDatastore) ReserveTranslationIndex(ctx context.Context, index datastore.TranslationIndexKey, entry datastore.TranslationIndexEntry, token string, payloadBytes int64) (datastore.TranslationIndexReservation, error) {
	if err := ctx.Err(); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	if _, _, err := datastore.PlanTranslationIndexReservation(nil, index, entry, token, payloadBytes); err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	for range 4 {
		if err := ctx.Err(); err != nil {
			return datastore.TranslationIndexReservation{}, err
		}
		record, err := s.GetTranslationIndexReservation(ctx, token)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return datastore.TranslationIndexReservation{}, err
		}
		exists := err == nil
		if exists && !datastore.TranslationReservationMatchesRequest(record, index, entry, token, payloadBytes) {
			return datastore.TranslationIndexReservation{}, datastore.ErrConflict
		}
		head, err := s.getTranslationIndexHead(ctx, index)
		if err != nil {
			return datastore.TranslationIndexReservation{}, err
		}
		if exists && record.Phase != datastore.TranslationReservationPreparing && (head == nil || head.PendingToken != token) {
			if !translationHeadAfterAllocation(head, record) {
				return datastore.TranslationIndexReservation{}, datastore.ErrRepairRequired
			}
			return record, nil
		}
		if head != nil && head.PendingToken != "" {
			if err := s.recoverTranslationHead(ctx, head); err != nil && !errors.Is(err, datastore.ErrConflict) {
				return datastore.TranslationIndexReservation{}, err
			}
			continue
		}
		planned, candidate, err := datastore.PlanTranslationIndexReservation(head, index, entry, token, payloadBytes)
		if err != nil {
			return datastore.TranslationIndexReservation{}, err
		}
		if !exists {
			if _, err := s.putTranslationReservation(ctx, candidate, ""); err != nil {
				return datastore.TranslationIndexReservation{}, err
			}
			continue
		}
		if record.ExpectedHeadVersion != candidate.ExpectedHeadVersion {
			candidate.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(record.ResourceVersion)
			if err != nil {
				return datastore.TranslationIndexReservation{}, err
			}
			if _, err := s.putTranslationReservation(ctx, candidate, record.ResourceVersion); err != nil {
				return datastore.TranslationIndexReservation{}, err
			}
			continue
		}
		if record.AllocationVersion != candidate.AllocationVersion || record.Index.Segment != candidate.Index.Segment ||
			record.Entry.Slot != candidate.Entry.Slot || record.ReservedBytes != candidate.ReservedBytes ||
			record.Index.Segment > math.MaxInt64 {
			return datastore.TranslationIndexReservation{}, datastore.ErrRepairRequired
		}
		applied, err := s.allocateTranslationHead(ctx, planned, record.ExpectedHeadVersion)
		if err != nil {
			return datastore.TranslationIndexReservation{}, err
		}
		if !applied {
			continue
		}
		if err := s.recoverTranslationHead(ctx, planned); err != nil && !errors.Is(err, datastore.ErrConflict) {
			return datastore.TranslationIndexReservation{}, err
		}
	}
	return datastore.TranslationIndexReservation{}, datastore.ErrConflict
}
