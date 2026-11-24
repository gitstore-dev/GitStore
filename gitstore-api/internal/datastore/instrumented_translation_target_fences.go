// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
)

func (d *InstrumentedDatastore) ReadTranslationTargetNamespaceMetadata(ctx context.Context, namespace string) (*TranslationTargetNamespaceMetadata, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetNamespaceMetadataStore)
	if !ok {
		return nil, ErrProjectionNotReady
	}
	metadata, err := next.ReadTranslationTargetNamespaceMetadata(ctx, namespace)
	d.observe("ReadTranslationTargetNamespaceMetadata", start, err)
	return metadata, err
}

func (d *InstrumentedDatastore) ReadTargetEvidence(ctx context.Context, target TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetFenceStore)
	if !ok {
		return nil, ErrProjectionNotReady
	}
	evidence, err := next.ReadTargetEvidence(ctx, target)
	d.observe("ReadTargetEvidence", start, err)
	return evidence, err
}

func (d *InstrumentedDatastore) ReadTranslationTargetMetadata(ctx context.Context, fence *TranslationTargetFence) (*TranslationTargetMetadata, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetMetadataStore)
	if !ok {
		return nil, ErrProjectionNotReady
	}
	metadata, err := next.ReadTranslationTargetMetadata(ctx, fence)
	d.observe("ReadTranslationTargetMetadata", start, err)
	return metadata, err
}

func (d *InstrumentedDatastore) GetTranslationTargetFence(ctx context.Context, target TranslationTargetKey) (*TranslationTargetFence, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetWriteFenceStore)
	if !ok {
		return nil, ErrProjectionNotReady
	}
	fence, err := next.GetTranslationTargetFence(ctx, target)
	d.observe("GetTranslationTargetFence", start, err)
	return fence, err
}

func (d *InstrumentedDatastore) BeginTranslationTargetWrite(ctx context.Context, target TranslationTargetKey, intent TranslationTargetWriteIntent, expected string) (*TranslationTargetFence, error) {
	start := time.Now()
	next, ok := d.next.(TranslationTargetWriteFenceStore)
	if !ok {
		return nil, ErrProjectionNotReady
	}
	fence, err := next.BeginTranslationTargetWrite(ctx, target, intent, expected)
	d.observe("BeginTranslationTargetWrite", start, err)
	return fence, err
}

func (d *InstrumentedDatastore) CompleteTranslationTargetWrite(ctx context.Context, target TranslationTargetKey, token string, evidence *catalog.TranslationTargetEvidence) error {
	start := time.Now()
	next, ok := d.next.(TranslationTargetWriteFenceStore)
	if !ok {
		return ErrProjectionNotReady
	}
	err := next.CompleteTranslationTargetWrite(ctx, target, token, evidence)
	d.observe("CompleteTranslationTargetWrite", start, err)
	return err
}

func (d *InstrumentedDatastore) TranslationTargetMaintenanceStore() TranslationTargetMaintenanceStore {
	capable, ok := d.next.(TranslationTargetMaintenanceCapable)
	if !ok {
		return nil
	}
	next := capable.TranslationTargetMaintenanceStore()
	if next == nil {
		return nil
	}
	return &instrumentedTranslationTargetMaintenanceStore{next: next, parent: d}
}

type instrumentedTranslationTargetMaintenanceStore struct {
	next   TranslationTargetMaintenanceStore
	parent *InstrumentedDatastore
}

func (s *instrumentedTranslationTargetMaintenanceStore) ListTranslationTargetMaintenanceNamespaces(ctx context.Context, cursor string, limit int) (TranslationDependentPage, error) {
	start := time.Now()
	page, err := s.next.ListTranslationTargetMaintenanceNamespaces(ctx, cursor, limit)
	s.parent.observe("ListTranslationTargetMaintenanceNamespaces", start, err)
	return page, err
}

func (s *instrumentedTranslationTargetMaintenanceStore) RecoverTranslationTargetMaintenance(ctx context.Context, namespace, cursor string, limit int) (TranslationDependentPage, error) {
	start := time.Now()
	page, err := s.next.RecoverTranslationTargetMaintenance(ctx, namespace, cursor, limit)
	s.parent.observe("RecoverTranslationTargetMaintenance", start, err)
	return page, err
}

func (s *instrumentedTranslationTargetMaintenanceStore) RecoverTranslationTargetMaintenanceTurn(ctx context.Context) error {
	start := time.Now()
	err := s.next.RecoverTranslationTargetMaintenanceTurn(ctx)
	s.parent.observe("RecoverTranslationTargetMaintenanceTurn", start, err)
	return err
}
