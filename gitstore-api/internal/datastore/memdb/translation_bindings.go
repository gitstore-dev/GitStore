// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

func (m *memdbDatastore) TranslationBindingStore() datastore.TranslationBindingStore { return m }

func (m *memdbDatastore) GetTranslationBinding(ctx context.Context, uid string) (*datastore.TranslationBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("translation_binding", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return datastore.CloneTranslationBinding(raw.(*datastore.TranslationBinding)), nil
}

func (m *memdbDatastore) CompareAndSwapTranslationBinding(ctx context.Context, candidate *datastore.TranslationBinding, expected string) error {
	if candidate == nil {
		return datastore.ErrInvalidArgument
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	source, err := txn.First("translation", "id", candidate.TranslationUID)
	if err != nil || source == nil {
		return notFoundOrErr(err)
	}
	raw, err := txn.First("translation_binding", "id", candidate.TranslationUID)
	if err != nil {
		return err
	}
	if raw == nil {
		return datastore.ErrRepairRequired
	}
	if err := datastore.ValidateTranslationBindingUpdate(raw.(*datastore.TranslationBinding), candidate, source.(*datastore.Translation), expected); err != nil {
		return err
	}
	if err := txn.Insert("translation_binding", datastore.CloneTranslationBinding(candidate)); err != nil {
		return err
	}
	if err := syncTranslationSourceIndexes(txn, source.(*datastore.Translation), candidate, datastore.ResourceWatchModified); err != nil {
		return err
	}
	txn.Commit()
	return nil
}

func syncTranslationBinding(txn *gomemdb.Txn, source *datastore.Translation, eventType datastore.ResourceWatchEventType) error {
	raw, err := txn.First("translation_binding", "id", source.UID)
	if err != nil {
		return err
	}
	if eventType == datastore.ResourceWatchDeleted {
		if raw == nil {
			return datastore.ErrRepairRequired
		}
		return txn.Delete("translation_binding", raw)
	}
	var current *datastore.TranslationBinding
	if raw != nil {
		current = raw.(*datastore.TranslationBinding)
	}
	if (current == nil) != (eventType == datastore.ResourceWatchAdded) {
		return datastore.ErrRepairRequired
	}
	next, err := datastore.PrepareTranslationBinding(source, current)
	if err != nil {
		return err
	}
	return txn.Insert("translation_binding", next)
}
