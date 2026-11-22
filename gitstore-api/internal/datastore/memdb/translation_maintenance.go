// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

func (m *memdbDatastore) GetTranslationMaintenance(ctx context.Context, uid string) (*datastore.TranslationMaintenanceIntent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	return translationMaintenanceInTxn(txn, uid)
}

func translationMaintenanceInTxn(txn *gomemdb.Txn, uid string) (*datastore.TranslationMaintenanceIntent, error) {
	raw, err := txn.First("translation", "id", uid)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		raw, err := txn.First("deleted_translation_uid", "id", uid)
		if err != nil || raw == nil {
			return nil, notFoundOrErr(err)
		}
		retired := raw.(*deletedTranslationUID)
		return &datastore.TranslationMaintenanceIntent{
			ID: uid + "/" + retired.ResourceVersion, TranslationUID: uid,
			NamespaceUID: retired.NamespaceUID, CreationTimestamp: retired.CreationTimestamp,
			ResourceVersion: retired.ResourceVersion, SourceVersion: retired.SourceVersion, Phase: "Retired",
		}, nil
	}
	source := raw.(*datastore.Translation)
	raw, err = txn.First("translation_source_placements", "id", uid)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, datastore.ErrRepairRequired
	}
	version := raw.(*translationSourcePlacements).ResourceVersion
	return &datastore.TranslationMaintenanceIntent{
		ID: uid + "/" + version, TranslationUID: uid, NamespaceUID: source.NamespaceID,
		CreationTimestamp: source.CreationTimestamp, ResourceVersion: version, SourceVersion: source.ResourceVersion, Phase: "Stable",
	}, nil
}

func (m *memdbDatastore) ListTranslationMaintenance(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationMaintenancePage, error) {
	ns, err := m.GetNamespaceByName(ctx, namespace)
	if err != nil {
		return datastore.TranslationMaintenancePage{}, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	indexed, err := datastore.ReadTranslationNamespacePage(ctx, ns.UID, cursor, false, limit,
		m.getTranslationIndexHead,
		func(ctx context.Context, index datastore.TranslationIndexKey, position *datastore.PageCursor, backward bool, count int) ([]datastore.TranslationIndexCell, error) {
			return readTranslationSourceSegment(ctx, txn, index, position, backward, count)
		})
	if err != nil {
		return datastore.TranslationMaintenancePage{}, err
	}
	result := datastore.TranslationMaintenancePage{Next: indexed.Next, Probes: indexed.Probes}
	for _, entry := range indexed.Items {
		state, err := m.GetTranslationMaintenance(ctx, entry.TranslationUID)
		if err != nil {
			return datastore.TranslationMaintenancePage{}, err
		}
		if state.NamespaceUID != ns.UID {
			return datastore.TranslationMaintenancePage{}, datastore.ErrRepairRequired
		}
		result.Items = append(result.Items, *state)
	}
	return result, nil
}

func (m *memdbDatastore) RecoverTranslation(ctx context.Context, uid string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	raw, err := txn.First("translation", "id", uid)
	if err != nil {
		return err
	}
	if raw == nil {
		raw, err = txn.First("deleted_translation_uid", "id", uid)
		if err != nil || raw == nil {
			return notFoundOrErr(err)
		}
		return nil
	}
	source := raw.(*datastore.Translation)
	raw, err = txn.First("translation_binding", "id", uid)
	if err != nil {
		return err
	}
	if raw == nil {
		return datastore.ErrRepairRequired
	}
	binding := raw.(*datastore.TranslationBinding)
	raw, err = txn.First("translation_source_placements", "id", uid)
	if err != nil {
		return err
	}
	if raw == nil {
		return datastore.ErrRepairRequired
	}
	placements := raw.(*translationSourcePlacements).Placements
	if err := datastore.ValidateTranslationSourcePlacements(source, binding, placements); err != nil {
		return err
	}
	var repairs []datastore.TranslationIndexReservation
	for _, placement := range placements {
		raw, err := txn.First("translation_index_reservation", "id", placement.Token)
		if err != nil {
			return err
		}
		if raw == nil {
			return datastore.ErrRepairRequired
		}
		record := raw.(*translationIndexReservationRow).Reservation
		if !datastore.TranslationReservationMatchesPlacement(record, placement) {
			return datastore.ErrRepairRequired
		}
		raw, err = txn.First("translation_index_head", "id", translationHeadID(record.Index))
		if err != nil {
			return err
		}
		var head *datastore.TranslationIndexHead
		if raw != nil {
			head = &raw.(*translationIndexHeadRow).Head
		}
		raw, err = txn.First("translation_index_cell", "id", translationCellID(record))
		if err != nil {
			return err
		}
		var cell *datastore.TranslationIndexCell
		if raw != nil {
			cell = &raw.(*translationIndexCellRow).Cell
		}
		next, err := datastore.PlanTranslationSourceIndexRepair(head, cell, record)
		if err != nil {
			return err
		}
		if next != nil || record.Phase != datastore.TranslationReservationCommitted {
			repairs = append(repairs, record)
		}
	}
	for _, repair := range repairs {
		if err := changeTranslationIndexCellInTxn(txn, repair, false); err != nil {
			return err
		}
	}
	if err := syncOwnerReferenceProjections(txn, source.Namespace, source.RepositoryID, "Translation", source.UID, source.Name, source.ResourceVersion, source.OwnerReferences); err != nil {
		return err
	}
	txn.Commit()
	return nil
}
