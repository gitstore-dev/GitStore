// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

type translationTargetFenceRow struct {
	ID    string
	Fence *datastore.TranslationTargetFence
}

func (m *memdbDatastore) GetTranslationTargetFence(ctx context.Context, target datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := datastore.TranslationTargetFenceID(target)
	if err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("translation_target_fence", "id", id)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	fence := raw.(*translationTargetFenceRow).Fence
	if fence == nil || fence.Target != target || datastore.ValidateTranslationTargetFence(fence) != nil {
		return nil, datastore.ErrRepairRequired
	}
	return datastore.CloneTranslationTargetFence(fence), nil
}

func (m *memdbDatastore) BeginTranslationTargetWrite(ctx context.Context, target datastore.TranslationTargetKey, intent datastore.TranslationTargetWriteIntent, expected string) (*datastore.TranslationTargetFence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := datastore.TranslationTargetFenceID(target)
	if err != nil {
		return nil, err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := txn.First("translation_target_fence", "id", id)
	if err != nil {
		return nil, err
	}
	var current *datastore.TranslationTargetFence
	if raw != nil {
		current = raw.(*translationTargetFenceRow).Fence
		if current == nil {
			return nil, datastore.ErrRepairRequired
		}
	}
	next, err := datastore.PlanTranslationTargetWrite(current, target, intent, expected)
	if err != nil {
		return nil, err
	}
	if err := txn.Insert("translation_target_fence", &translationTargetFenceRow{ID: id, Fence: next}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	txn.Commit()
	return datastore.CloneTranslationTargetFence(next), nil
}

func (m *memdbDatastore) CompleteTranslationTargetWrite(ctx context.Context, target datastore.TranslationTargetKey, token string, evidence *catalog.TranslationTargetEvidence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := datastore.TranslationTargetFenceID(target)
	if err != nil {
		return err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := txn.First("translation_target_fence", "id", id)
	if err != nil || raw == nil {
		return notFoundOrErr(err)
	}
	current := raw.(*translationTargetFenceRow).Fence
	if current == nil || current.Target != target {
		return datastore.ErrRepairRequired
	}
	next, err := datastore.PlanTranslationTargetCompletion(current, token, evidence)
	if err != nil {
		return err
	}
	if err := txn.Insert("translation_target_fence", &translationTargetFenceRow{ID: id, Fence: next}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	txn.Commit()
	return nil
}
