// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "time"

type TranslationIndexCell struct {
	Entry   TranslationIndexEntry
	Token   string
	Retired bool
}

func TranslationReservationMatchesPlacement(record, supplied TranslationIndexReservation) bool {
	supplied.Phase, supplied.ResourceVersion = record.Phase, record.ResourceVersion
	record.Entry.CreationTimestamp = record.Entry.CreationTimestamp.UTC().Truncate(time.Millisecond)
	supplied.Entry.CreationTimestamp = supplied.Entry.CreationTimestamp.UTC().Truncate(time.Millisecond)
	return record == supplied
}

// Slots are never reused within a segment. A retained retired cell fences a
// delayed publisher even when removal interrupted before its reservation CAS.
func PlanTranslationIndexCell(current *TranslationIndexCell, placement TranslationIndexReservation, retire bool) (*TranslationIndexCell, error) {
	switch placement.Phase {
	case TranslationReservationReserved, TranslationReservationCommitted:
	case TranslationReservationRemoved:
		if !retire {
			return nil, ErrConflict
		}
	default:
		return nil, ErrConflict
	}
	if placement.Entry.Slot >= MaxTranslationSegmentRows {
		return nil, ErrRepairRequired
	}
	next := &TranslationIndexCell{Entry: placement.Entry, Token: placement.Token, Retired: retire}
	if current == nil {
		return next, nil
	}

	if current.Entry.Slot >= MaxTranslationSegmentRows || current.Entry.TranslationUID != placement.Entry.TranslationUID ||
		!current.Entry.CreationTimestamp.Equal(placement.Entry.CreationTimestamp) {
		return nil, ErrRepairRequired
	}
	if current.Entry.Slot > placement.Entry.Slot {
		return nil, nil
	}
	if current.Entry.Slot == placement.Entry.Slot {
		if current.Token != placement.Token || current.Entry.SourceVersion != placement.Entry.SourceVersion ||
			current.Entry.ReferenceEpoch != placement.Entry.ReferenceEpoch {
			return nil, ErrRepairRequired
		}
		if current.Retired && !retire {
			return nil, ErrConflict
		}
		if current.Retired == retire {
			return nil, nil
		}
	}
	return next, nil
}

func ValidateTranslationSourcePlacements(source *Translation, binding *TranslationBinding, placements []TranslationIndexReservation) error {
	if source == nil || binding == nil || binding.TranslationUID != source.UID ||
		binding.SourceVersion != source.ResourceVersion || len(binding.Epochs) == 0 || len(binding.Epochs) > MaxTranslationEpochs {
		return ErrRepairRequired
	}
	last := binding.Epochs[len(binding.Epochs)-1]
	if last.Epoch == "" || last.Epoch != binding.CurrentEpoch || last.Target.NamespaceUID != source.NamespaceID {
		return ErrRepairRequired
	}
	shard, err := TranslationIndexShard(source.UID)
	if err != nil {
		return ErrRepairRequired
	}
	indexes := []TranslationIndexKey{
		{Kind: TranslationNamespaceIndex, NamespaceUID: source.NamespaceID, Shard: shard},
		{Kind: TranslationTargetNameIndex, NamespaceUID: source.NamespaceID, Shard: shard, Target: last.Target},
	}
	if last.BoundUID != "" {
		indexes = append(indexes, TranslationIndexKey{
			Kind: TranslationTargetUIDIndex, NamespaceUID: source.NamespaceID, Shard: shard, TargetUID: last.BoundUID,
		})
	}
	if len(placements) != len(indexes) {
		return ErrRepairRequired
	}
	for i, placement := range placements {
		indexes[i].Segment = placement.Index.Segment
		version, epoch := source.ResourceVersion, binding.CurrentEpoch
		if i == 0 {
			version, epoch = "1", binding.Epochs[0].Epoch
		}
		if placement.Index != indexes[i] || placement.Entry.TranslationUID != source.UID ||
			placement.Entry.SourceVersion != version || placement.Entry.ReferenceEpoch != epoch ||
			!placement.Entry.CreationTimestamp.UTC().Truncate(time.Millisecond).Equal(source.CreationTimestamp.UTC().Truncate(time.Millisecond)) {
			return ErrRepairRequired
		}
	}
	return nil
}

func PlanTranslationSourceIndexRepair(head *TranslationIndexHead, current *TranslationIndexCell, placement TranslationIndexReservation) (*TranslationIndexCell, error) {
	index := placement.Index
	index.Segment = 0
	if head == nil || head.Index != index || head.Segment < placement.Index.Segment ||
		head.Segment == placement.Index.Segment && (head.NextSlot <= placement.Entry.Slot || head.ReservedBytes < placement.ReservedBytes) {
		return nil, ErrRepairRequired
	}
	if placement.Phase != TranslationReservationCommitted && placement.Phase != TranslationReservationReserved ||
		current != nil && current.Entry.Slot > placement.Entry.Slot {
		return nil, ErrRepairRequired
	}
	next, err := PlanTranslationIndexCell(current, placement, false)
	if err != nil {
		return nil, ErrRepairRequired
	}
	return next, nil
}
