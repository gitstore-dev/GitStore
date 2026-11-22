// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"time"
)

func (d *InstrumentedDatastore) VerifyTranslationTargetSourceCoverage(ctx context.Context, hint TranslationTargetSourceAuditHint) error {
	start := time.Now()
	next, ok := d.next.(TranslationTargetCoverageAuditStore)
	if !ok {
		return ErrProjectionNotReady
	}
	err := next.VerifyTranslationTargetSourceCoverage(ctx, hint)
	d.observe("VerifyTranslationTargetSourceCoverage", start, err)
	return err
}

func (d *InstrumentedDatastore) ReadTranslationTargetCoverageEpoch(ctx context.Context, namespace string) (string, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetCoverageEpochStore)
	if !ok {
		return "", ErrProjectionNotReady
	}
	epoch, err := next.ReadTranslationTargetCoverageEpoch(ctx, namespace)
	d.observe("ReadTranslationTargetCoverageEpoch", start, err)
	return epoch, err
}

func (d *InstrumentedDatastore) GetTranslationTargetAuditProgress(ctx context.Context, namespace string) (TranslationAuditProgress, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetAuditStore)
	if !ok {
		return TranslationAuditProgress{}, ErrProjectionNotReady
	}
	progress, err := next.GetTranslationTargetAuditProgress(ctx, namespace)
	d.observe("GetTranslationTargetAuditProgress", start, err)
	return progress, err
}

func (d *InstrumentedDatastore) RepairTranslationTargetAuditTurn(ctx context.Context, namespace string, limit int) (TranslationAuditProgress, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetAuditStore)
	if !ok {
		return TranslationAuditProgress{}, ErrProjectionNotReady
	}
	progress, err := next.RepairTranslationTargetAuditTurn(ctx, namespace, limit)
	d.observe("RepairTranslationTargetAuditTurn", start, err)
	return progress, err
}

func (d *InstrumentedDatastore) ReadTranslationTargetMutationEpoch(ctx context.Context, namespace string) (string, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetMutationEpochStore)
	if !ok {
		return "", ErrProjectionNotReady
	}
	epoch, err := next.ReadTranslationTargetMutationEpoch(ctx, namespace)
	d.observe("ReadTranslationTargetMutationEpoch", start, err)
	return epoch, err
}

func (d *InstrumentedDatastore) ListTranslationTargetSourceAudit(ctx context.Context, namespace, cursor string, limit int) (TranslationTargetSourceAuditPage, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetSourceAuditStore)
	if !ok {
		return TranslationTargetSourceAuditPage{}, ErrProjectionNotReady
	}
	page, err := next.ListTranslationTargetSourceAudit(ctx, namespace, cursor, limit)
	d.observe("ListTranslationTargetSourceAudit", start, err)
	return page, err
}

func (d *InstrumentedDatastore) RecoverTranslationTargetSourceAudit(ctx context.Context, hint TranslationTargetSourceAuditHint) error {
	start := time.Now()
	next, ok := d.next.(TranslationTargetSourceAuditStore)
	if !ok {
		return ErrProjectionNotReady
	}
	err := next.RecoverTranslationTargetSourceAudit(ctx, hint)
	d.observe("RecoverTranslationTargetSourceAudit", start, err)
	return err
}

func (d *InstrumentedDatastore) RepairTranslationAuditTurn(ctx context.Context, namespace string, limit int) (TranslationAuditProgress, error) {
	start := time.Now()
	next, ok := d.next.(TranslationAuditRepairStore)
	if !ok {
		return TranslationAuditProgress{}, ErrProjectionNotReady
	}
	progress, err := next.RepairTranslationAuditTurn(ctx, namespace, limit)
	d.observe("RepairTranslationAuditTurn", start, err)
	return progress, err
}

func (d *InstrumentedDatastore) GetTranslationAuditProgress(ctx context.Context, namespace string) (TranslationAuditProgress, error) {
	start := time.Now()
	next, ok := d.next.(TranslationAuditRepairStore)
	if !ok {
		return TranslationAuditProgress{}, ErrProjectionNotReady
	}
	progress, err := next.GetTranslationAuditProgress(ctx, namespace)
	d.observe("GetTranslationAuditProgress", start, err)
	return progress, err
}

func (d *InstrumentedDatastore) ListTranslationIndexCellAudit(ctx context.Context, namespace, cursor string, limit int) (TranslationIndexCellAuditPage, error) {
	start := time.Now()
	next, ok := d.next.(TranslationIndexCellAuditStore)
	if !ok {
		return TranslationIndexCellAuditPage{}, ErrProjectionNotReady
	}
	page, err := next.ListTranslationIndexCellAudit(ctx, namespace, cursor, limit)
	d.observe("ListTranslationIndexCellAudit", start, err)
	return page, err
}

func (d *InstrumentedDatastore) RecoverTranslationIndexCellAudit(ctx context.Context, namespace string, observed TranslationIndexAuditCell) error {
	start := time.Now()
	next, ok := d.next.(TranslationIndexCellAuditStore)
	if !ok {
		return ErrProjectionNotReady
	}
	err := next.RecoverTranslationIndexCellAudit(ctx, namespace, observed)
	d.observe("RecoverTranslationIndexCellAudit", start, err)
	return err
}

func (d *InstrumentedDatastore) ListTranslationIndexAudit(ctx context.Context, namespace, cursor string, limit int) (TranslationIndexAuditPage, error) {
	start := time.Now()
	next, ok := d.next.(TranslationIndexAuditStore)
	if !ok {
		return TranslationIndexAuditPage{}, ErrProjectionNotReady
	}
	page, err := next.ListTranslationIndexAudit(ctx, namespace, cursor, limit)
	d.observe("ListTranslationIndexAudit", start, err)
	return page, err
}

func (d *InstrumentedDatastore) RecoverTranslationIndexAudit(ctx context.Context, namespace, token string) error {
	start := time.Now()
	next, ok := d.next.(TranslationIndexAuditStore)
	if !ok {
		return ErrProjectionNotReady
	}
	err := next.RecoverTranslationIndexAudit(ctx, namespace, token)
	d.observe("RecoverTranslationIndexAudit", start, err)
	return err
}

func (d *InstrumentedDatastore) RecoverTranslationSourceAudit(ctx context.Context, hint TranslationMaintenanceIntent) error {
	start := time.Now()
	next, ok := d.next.(TranslationSourceAuditStore)
	if !ok {
		return ErrProjectionNotReady
	}
	err := next.RecoverTranslationSourceAudit(ctx, hint)
	d.observe("RecoverTranslationSourceAudit", start, err)
	return err
}

func (d *InstrumentedDatastore) ListTranslationSourceAudit(ctx context.Context, namespace, cursor string, limit int) (TranslationSourceAuditPage, error) {
	start := time.Now()
	next, ok := d.next.(TranslationSourceAuditStore)
	if !ok {
		return TranslationSourceAuditPage{}, ErrProjectionNotReady
	}
	page, err := next.ListTranslationSourceAudit(ctx, namespace, cursor, limit)
	d.observe("ListTranslationSourceAudit", start, err)
	return page, err
}

func (d *InstrumentedDatastore) GetTranslationSourceAuditWatermark(ctx context.Context, uid string) (*TranslationMaintenanceIntent, error) {
	start := time.Now()
	next, ok := d.next.(TranslationSourceAuditStore)
	if !ok {
		return nil, ErrProjectionNotReady
	}
	watermark, err := next.GetTranslationSourceAuditWatermark(ctx, uid)
	d.observe("GetTranslationSourceAuditWatermark", start, err)
	return watermark, err
}

func (d *InstrumentedDatastore) TranslationIndexStore() TranslationIndexStore {
	capable, ok := d.next.(TranslationIndexCapable)
	if !ok {
		return nil
	}
	next := capable.TranslationIndexStore()
	if next == nil {
		return nil
	}
	return &instrumentedTranslationIndexStore{next: next, parent: d}
}

type instrumentedTranslationIndexStore struct {
	next   TranslationIndexStore
	parent *InstrumentedDatastore
}

func (s *instrumentedTranslationIndexStore) ReserveTranslationIndex(ctx context.Context, index TranslationIndexKey, entry TranslationIndexEntry, token string, size int64) (TranslationIndexReservation, error) {
	start := time.Now()
	reservation, err := s.next.ReserveTranslationIndex(ctx, index, entry, token, size)
	s.parent.observe("ReserveTranslationIndex", start, err)
	return reservation, err
}

func (s *instrumentedTranslationIndexStore) GetTranslationIndexReservation(ctx context.Context, token string) (TranslationIndexReservation, error) {
	start := time.Now()
	reservation, err := s.next.GetTranslationIndexReservation(ctx, token)
	s.parent.observe("GetTranslationIndexReservation", start, err)
	return reservation, err
}

func (s *instrumentedTranslationIndexStore) CommitTranslationIndex(ctx context.Context, reservation TranslationIndexReservation) error {
	start := time.Now()
	err := s.next.CommitTranslationIndex(ctx, reservation)
	s.parent.observe("CommitTranslationIndex", start, err)
	return err
}

func (s *instrumentedTranslationIndexStore) RemoveTranslationIndex(ctx context.Context, reservation TranslationIndexReservation) error {
	start := time.Now()
	err := s.next.RemoveTranslationIndex(ctx, reservation)
	s.parent.observe("RemoveTranslationIndex", start, err)
	return err
}

func (s *instrumentedTranslationIndexStore) ListTranslationDependents(ctx context.Context, target TranslationTargetKey, uid, cursor string, limit int) (TranslationDependentPage, error) {
	start := time.Now()
	page, err := s.next.ListTranslationDependents(ctx, target, uid, cursor, limit)
	s.parent.observe("ListTranslationDependents", start, err)
	return page, err
}

func (s *instrumentedTranslationIndexStore) GetTranslationMaintenance(ctx context.Context, uid string) (*TranslationMaintenanceIntent, error) {
	start := time.Now()
	intent, err := s.next.GetTranslationMaintenance(ctx, uid)
	s.parent.observe("GetTranslationMaintenance", start, err)
	return intent, err
}

func (s *instrumentedTranslationIndexStore) ListTranslationMaintenance(ctx context.Context, namespace, cursor string, limit int) (TranslationMaintenancePage, error) {
	start := time.Now()
	page, err := s.next.ListTranslationMaintenance(ctx, namespace, cursor, limit)
	s.parent.observe("ListTranslationMaintenance", start, err)
	return page, err
}

func (s *instrumentedTranslationIndexStore) RecoverTranslation(ctx context.Context, uid string) error {
	start := time.Now()
	err := s.next.RecoverTranslation(ctx, uid)
	s.parent.observe("RecoverTranslation", start, err)
	return err
}
