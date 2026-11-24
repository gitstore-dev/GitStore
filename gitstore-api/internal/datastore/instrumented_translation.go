// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"time"
)

func (d *InstrumentedDatastore) TranslationBindingStore() TranslationBindingStore {
	capable, ok := d.next.(TranslationBindingCapable)
	if !ok {
		return nil
	}
	next := capable.TranslationBindingStore()
	if next == nil {
		return nil
	}
	return &instrumentedTranslationBindingStore{next: next, parent: d}
}

type instrumentedTranslationBindingStore struct {
	next   TranslationBindingStore
	parent *InstrumentedDatastore
}

func (s *instrumentedTranslationBindingStore) GetTranslationBinding(ctx context.Context, uid string) (*TranslationBinding, error) {
	start := time.Now()
	binding, err := s.next.GetTranslationBinding(ctx, uid)
	s.parent.observe("GetTranslationBinding", start, err)
	return binding, err
}

func (s *instrumentedTranslationBindingStore) CompareAndSwapTranslationBinding(ctx context.Context, binding *TranslationBinding, expected string) error {
	start := time.Now()
	err := s.next.CompareAndSwapTranslationBinding(ctx, binding, expected)
	s.parent.observe("CompareAndSwapTranslationBinding", start, err)
	return err
}

func (d *InstrumentedDatastore) TranslationStore() TranslationStore {
	capable, ok := d.next.(TranslationCapable)
	if !ok {
		return nil
	}
	next := capable.TranslationStore()
	if next == nil {
		return nil
	}
	return &instrumentedTranslationStore{next: next, parent: d}
}

type instrumentedTranslationStore struct {
	next   TranslationStore
	parent *InstrumentedDatastore
}

func (s *instrumentedTranslationStore) CreateTranslation(ctx context.Context, source *Translation) error {
	start := time.Now()
	err := s.next.CreateTranslation(ctx, source)
	s.parent.observe("CreateTranslation", start, err)
	return err
}

func (s *instrumentedTranslationStore) GetTranslation(ctx context.Context, uid string) (*Translation, error) {
	start := time.Now()
	source, err := s.next.GetTranslation(ctx, uid)
	s.parent.observe("GetTranslation", start, err)
	return source, err
}

func (s *instrumentedTranslationStore) GetTranslationByName(ctx context.Context, namespace, name string) (*Translation, error) {
	start := time.Now()
	source, err := s.next.GetTranslationByName(ctx, namespace, name)
	s.parent.observe("GetTranslationByName", start, err)
	return source, err
}

func (s *instrumentedTranslationStore) ListTranslations(ctx context.Context, namespace string, page PageParams) (*TranslationPage, error) {
	start := time.Now()
	sources, err := s.next.ListTranslations(ctx, namespace, page)
	s.parent.observe("ListTranslations", start, err)
	return sources, err
}

func (s *instrumentedTranslationStore) UpdateTranslation(ctx context.Context, source *Translation, expected string) error {
	start := time.Now()
	err := s.next.UpdateTranslation(ctx, source, expected)
	s.parent.observe("UpdateTranslation", start, err)
	return err
}

func (s *instrumentedTranslationStore) UpdateTranslationStatus(ctx context.Context, uid, expected string, status json.RawMessage) (*Translation, error) {
	start := time.Now()
	source, err := s.next.UpdateTranslationStatus(ctx, uid, expected, status)
	s.parent.observe("UpdateTranslationStatus", start, err)
	return source, err
}

func (s *instrumentedTranslationStore) DeleteTranslation(ctx context.Context, uid, expected string) error {
	start := time.Now()
	err := s.next.DeleteTranslation(ctx, uid, expected)
	s.parent.observe("DeleteTranslation", start, err)
	return err
}

func (s *instrumentedTranslationStore) RequestTranslationRevalidation(ctx context.Context, source *Translation) (bool, error) {
	start := time.Now()
	queued, err := s.next.RequestTranslationRevalidation(ctx, source)
	s.parent.observe("RequestTranslationRevalidation", start, err)
	return queued, err
}
