// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	gomemdb "github.com/hashicorp/go-memdb"
)

type translationSourcePlacements struct {
	ID, ResourceVersion, RepairWatermark string
	Placements                           []datastore.TranslationIndexReservation
	RevalidationVersion                  string
}

func syncTranslationSourceIndexes(txn *gomemdb.Txn, source *datastore.Translation, binding *datastore.TranslationBinding, event datastore.ResourceWatchEventType) error {
	raw, err := txn.First("translation_source_placements", "id", source.UID)
	if err != nil {
		return err
	}
	if (raw == nil) != (event == datastore.ResourceWatchAdded) {
		return datastore.ErrRepairRequired
	}
	var previous []datastore.TranslationIndexReservation
	version := "1"
	if raw != nil {
		state := raw.(*translationSourcePlacements)
		previous = state.Placements
		version, err = datastore.AdvanceTranslationIndexVersion(state.ResourceVersion)
		if err != nil {
			return err
		}
		if len(previous) < 2 || len(previous) > 3 {
			return datastore.ErrRepairRequired
		}
	}
	if event == datastore.ResourceWatchDeleted {
		for _, placement := range previous {
			if err := changeTranslationIndexCellInTxn(txn, placement, true); err != nil {
				return err
			}
		}
		if err := txn.Insert("deleted_translation_uid", &deletedTranslationUID{
			UID: source.UID, NamespaceUID: source.NamespaceID, CreationTimestamp: source.CreationTimestamp,
			ResourceVersion: version, SourceVersion: source.ResourceVersion,
		}); err != nil {
			return err
		}
		return txn.Delete("translation_source_placements", raw)
	}
	if binding == nil || len(binding.Epochs) == 0 || binding.SourceVersion != source.ResourceVersion {
		return datastore.ErrRepairRequired
	}
	last := binding.Epochs[len(binding.Epochs)-1]
	shard, err := datastore.TranslationIndexShard(source.UID)
	if err != nil {
		return err
	}
	entry := datastore.TranslationIndexEntry{TranslationUID: source.UID, SourceVersion: source.ResourceVersion,
		ReferenceEpoch: binding.CurrentEpoch, CreationTimestamp: source.CreationTimestamp}
	indexes := []datastore.TranslationIndexKey{
		{Kind: datastore.TranslationTargetNameIndex, NamespaceUID: source.NamespaceID, Shard: shard, Target: last.Target},
	}
	if last.BoundUID != "" {
		indexes = append(indexes, datastore.TranslationIndexKey{
			Kind: datastore.TranslationTargetUIDIndex, NamespaceUID: source.NamespaceID, Shard: shard, TargetUID: last.BoundUID,
		})
	}
	var placements []datastore.TranslationIndexReservation
	if len(previous) == 0 {
		indexes = append([]datastore.TranslationIndexKey{
			{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: source.NamespaceID, Shard: shard},
		}, indexes...)
	} else {
		placements = append(placements, previous[0])
	}
	for _, index := range indexes {
		placement, err := reserveTranslationIndexInTxn(txn, index, entry, uuid.NewString(),
			max(1024, datastore.TranslationIndexPayloadBytes(index, entry)))
		if err != nil {
			return err
		}
		if err := changeTranslationIndexCellInTxn(txn, placement, false); err != nil {
			return err
		}
		placements = append(placements, placement)
	}
	if len(previous) > 0 {
		for _, placement := range previous[1:] {
			if err := changeTranslationIndexCellInTxn(txn, placement, true); err != nil {
				return err
			}
		}
	}
	return txn.Insert("translation_source_placements", &translationSourcePlacements{ID: source.UID, ResourceVersion: version, Placements: placements})
}
