// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

func (m *memdbDatastore) ListTranslationDependents(ctx context.Context, target datastore.TranslationTargetKey, targetUID, cursor string, limit int) (datastore.TranslationDependentPage, error) {
	return datastore.ReadTranslationDependentPage(ctx, target, targetUID, cursor, limit, m.getTranslationIndexHead, m.readTranslationIndexSegment)
}

func readTranslationSourceSegment(ctx context.Context, txn *gomemdb.Txn, index datastore.TranslationIndexKey, position *datastore.PageCursor, backward bool, limit int) ([]datastore.TranslationIndexCell, error) {
	prefix := translationHeadID(index) + "\x00"
	start := prefix
	if backward {
		start += "\xff"
	}
	if position != nil {
		start = string(namespaceOrderKey(translationHeadID(index), position.CreatedAt, position.ID))
	}
	var iter gomemdb.ResultIterator
	var err error
	if backward {
		iter, err = txn.ReverseLowerBound("translation_index_cell", "namespace_order", start)
	} else {
		iter, err = txn.LowerBound("translation_index_cell", "namespace_order", start)
	}
	if err != nil {
		return nil, err
	}
	var result []datastore.TranslationIndexCell
	for raw := iter.Next(); raw != nil && len(result) < limit; raw = iter.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := raw.(*translationIndexCellRow)
		if !strings.HasPrefix(row.NamespaceOrder, prefix) {
			break
		}
		if position != nil && row.Cell.Entry.TranslationUID == position.ID &&
			row.Cell.Entry.CreationTimestamp.Equal(position.CreatedAt) {
			continue
		}
		result = append(result, row.Cell)
	}
	return result, nil
}

func (m *memdbDatastore) getTranslationIndexHead(ctx context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("translation_index_head", "id", translationHeadID(index))
	if err != nil || raw == nil {
		return nil, err
	}
	head := raw.(*translationIndexHeadRow).Head
	return &head, nil
}

func (m *memdbDatastore) readTranslationIndexSegment(ctx context.Context, index datastore.TranslationIndexKey, after string, limit int) ([]datastore.TranslationIndexCell, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	prefix := translationHeadID(index) + "/"
	start := prefix
	if after != "" {
		start += after + "\x00"
	}
	iter, err := txn.LowerBound("translation_index_cell", "id", start)
	if err != nil {
		return nil, err
	}
	var result []datastore.TranslationIndexCell
	for raw := iter.Next(); raw != nil && len(result) < limit; raw = iter.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := raw.(*translationIndexCellRow)
		if !strings.HasPrefix(row.ID, prefix) {
			break
		}
		result = append(result, row.Cell)
	}
	return result, nil
}
