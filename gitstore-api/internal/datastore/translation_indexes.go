// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"time"

	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
)

func TranslationIndexShard(uid string) (uint8, error) {
	id, err := uuid.Parse(uid)
	if err != nil || id.String() != uid {
		return 0, ErrInvalidArgument
	}
	digest := sha256.Sum256(id[:])
	return digest[0] % TranslationIndexShards, nil
}

func TranslationIndexPayloadBytes(index TranslationIndexKey, entry TranslationIndexEntry) int64 {
	return 128 + int64(len(index.Kind)) + int64(len(index.NamespaceUID)) +
		int64(len(index.Target.NamespaceUID)) + int64(len(index.Target.APIVersion)) +
		int64(len(index.Target.Kind)) + int64(len(index.Target.Name)) + int64(len(index.TargetUID)) +
		int64(len(entry.TranslationUID)) + int64(len(entry.SourceVersion)) + int64(len(entry.ReferenceEpoch))
}

func validateTranslationIndexRequest(index TranslationIndexKey, entry TranslationIndexEntry, token string, payloadBytes int64) error {
	shard, err := TranslationIndexShard(entry.TranslationUID)
	if err != nil || shard != index.Shard || entry.SourceVersion == "" || len(entry.SourceVersion) > 256 ||
		entry.CreationTimestamp.IsZero() || entry.Slot != 0 || index.Segment != 0 ||
		payloadBytes < TranslationIndexPayloadBytes(index, entry) || payloadBytes > MaxTranslationSegmentBytes {
		return ErrInvalidArgument
	}
	for _, id := range []string{entry.ReferenceEpoch, token} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return ErrInvalidArgument
		}
	}
	return validateTranslationIndexIdentity(index)
}

func validateTranslationIndexIdentity(index TranslationIndexKey) error {
	if id, err := uuid.Parse(index.NamespaceUID); err != nil || id.String() != index.NamespaceUID ||
		index.Shard >= TranslationIndexShards || index.Segment > math.MaxInt64 {
		return ErrInvalidArgument
	}
	switch index.Kind {
	case TranslationNamespaceIndex, TranslationTargetPendingIndex, TranslationTargetMaintenanceNamespaceIndex:
		if index.Target != (TranslationTargetKey{}) || index.TargetUID != "" {
			return ErrInvalidArgument
		}
	case TranslationTargetNameIndex:
		if index.Target.NamespaceUID != index.NamespaceUID || index.Target.Name == "" || index.TargetUID != "" ||
			len(index.Target.Name) > translation.MaxDocumentBytes {
			return ErrInvalidArgument
		}
		if _, err := translation.DescriptorFor(index.Target.APIVersion, index.Target.Kind); err != nil {
			return ErrInvalidArgument
		}
		encoded, err := json.Marshal(index.Target)
		if err != nil || len(encoded) > translation.MaxDocumentBytes-4096 {
			return ErrInvalidArgument
		}
	case TranslationTargetUIDIndex:
		if index.Target != (TranslationTargetKey{}) {
			return ErrInvalidArgument
		}
		if parsed, err := uuid.Parse(index.TargetUID); err != nil || parsed.String() != index.TargetUID {
			return ErrInvalidArgument
		}
	default:
		return ErrInvalidArgument
	}
	return nil
}

func AdvanceTranslationIndexVersion(current string) (string, error) {
	if current == "" || len(current) > 256 {
		return "", ErrRepairRequired
	}
	next := nextResourceVersion(current)
	if len(next) > 256 || ValidateCatalogUpdateVersion(current, current, next) != nil {
		return "", ErrRepairRequired
	}
	return next, nil
}

func TranslationReservationMatchesRequest(record TranslationIndexReservation, index TranslationIndexKey, entry TranslationIndexEntry, token string, payloadBytes int64) bool {
	record.Index.Segment = 0
	record.Entry.Slot = 0
	if !record.Entry.CreationTimestamp.Truncate(time.Millisecond).Equal(entry.CreationTimestamp.Truncate(time.Millisecond)) {
		return false
	}
	record.Entry.CreationTimestamp = entry.CreationTimestamp
	return record.Index == index && record.Entry == entry && record.Token == token && record.PayloadBytes == payloadBytes
}

// Persist the Preparing reservation before CASing the returned head. Its
// pending token fences further allocation until Reserved is durable.
func PlanTranslationIndexReservation(current *TranslationIndexHead, index TranslationIndexKey, entry TranslationIndexEntry, token string, payloadBytes int64) (*TranslationIndexHead, TranslationIndexReservation, error) {
	if err := validateTranslationIndexRequest(index, entry, token, payloadBytes); err != nil {
		return nil, TranslationIndexReservation{}, err
	}
	entry.CreationTimestamp = entry.CreationTimestamp.UTC().Truncate(time.Millisecond)
	next := &TranslationIndexHead{Index: index, ResourceVersion: "1"}
	expected := ""
	if current != nil {
		if current.Index != index || current.Segment > math.MaxInt64 || current.NextSlot > MaxTranslationSegmentRows ||
			current.ReservedBytes < 0 || current.ReservedBytes > MaxTranslationSegmentBytes ||
			(current.NextSlot == 0 && current.ReservedBytes != 0) || current.ResourceVersion == "" || len(current.ResourceVersion) > 256 {
			return nil, TranslationIndexReservation{}, ErrRepairRequired
		}
		*next = *current
		next.ResourceVersion = nextResourceVersion(current.ResourceVersion)
		if len(next.ResourceVersion) > 256 ||
			ValidateCatalogUpdateVersion(current.ResourceVersion, current.ResourceVersion, next.ResourceVersion) != nil {
			return nil, TranslationIndexReservation{}, ErrRepairRequired
		}
		if current.PendingToken != "" {
			return nil, TranslationIndexReservation{}, ErrConflict
		}
		expected = current.ResourceVersion
	}
	if next.NextSlot == MaxTranslationSegmentRows || payloadBytes > MaxTranslationSegmentBytes-next.ReservedBytes {
		if next.Segment == math.MaxInt64 {
			return nil, TranslationIndexReservation{}, ErrRepairRequired
		}
		next.Segment++
		next.NextSlot, next.ReservedBytes = 0, 0
	}
	entry.Slot = next.NextSlot
	index.Segment = next.Segment
	next.NextSlot++
	next.ReservedBytes += payloadBytes
	next.PendingToken = token
	return next, TranslationIndexReservation{
		Index: index, Entry: entry, Token: token, ResourceVersion: "1", PayloadBytes: payloadBytes,
		Phase: TranslationReservationPreparing, ExpectedHeadVersion: expected,
		AllocationVersion: next.ResourceVersion, ReservedBytes: next.ReservedBytes,
	}, nil
}

// Finish never refunds capacity: even a removed reservation may have an old
// writer in flight. Backends CAS this head only after Reserved is durable.
func FinishTranslationIndexReservation(head *TranslationIndexHead, reservation TranslationIndexReservation) (*TranslationIndexHead, error) {
	if reservation.Phase != TranslationReservationReserved {
		return nil, ErrConflict
	}
	if head == nil {
		return nil, ErrRepairRequired
	}
	if head.ResourceVersion == "" || len(head.ResourceVersion) > 256 {
		return nil, ErrRepairRequired
	}
	if head.PendingToken != reservation.Token {
		return nil, ErrConflict
	}
	index := reservation.Index
	index.Segment = 0
	if head.Index != index || head.ResourceVersion != reservation.AllocationVersion ||
		head.Segment != reservation.Index.Segment || head.Segment > math.MaxInt64 || reservation.Entry.Slot >= MaxTranslationSegmentRows ||
		head.NextSlot != reservation.Entry.Slot+1 || head.ReservedBytes != reservation.ReservedBytes ||
		reservation.ReservedBytes < reservation.PayloadBytes || reservation.ReservedBytes > MaxTranslationSegmentBytes {
		return nil, ErrRepairRequired
	}
	next := *head
	next.ResourceVersion = nextResourceVersion(head.ResourceVersion)
	if len(next.ResourceVersion) > 256 ||
		ValidateCatalogUpdateVersion(head.ResourceVersion, head.ResourceVersion, next.ResourceVersion) != nil {
		return nil, ErrRepairRequired
	}
	next.PendingToken = ""
	return &next, nil
}
