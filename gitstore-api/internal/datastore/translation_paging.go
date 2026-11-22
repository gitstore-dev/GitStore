// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"

	"github.com/google/uuid"
)

type translationDependentCursor struct {
	Version     int
	Scope       string
	Shard       uint8
	Segment     uint64
	HeadSegment uint64
	HeadKnown   bool
	AfterUID    string
}

// Each head or segment read consumes a probe. Retired cells consume the row
// budget too, so sparse or churned partitions cannot cause an unbounded turn.
func ReadTranslationDependentPage(
	ctx context.Context, target TranslationTargetKey, targetUID, encoded string, limit int,
	readHead func(context.Context, TranslationIndexKey) (*TranslationIndexHead, error),
	readSegment func(context.Context, TranslationIndexKey, string, int) ([]TranslationIndexCell, error),
) (TranslationDependentPage, error) {
	if err := ctx.Err(); err != nil {
		return TranslationDependentPage{}, err
	}
	index := TranslationIndexKey{Kind: TranslationTargetNameIndex, NamespaceUID: target.NamespaceUID, Target: target}
	if targetUID != "" {
		index.Kind, index.Target, index.TargetUID = TranslationTargetUIDIndex, TranslationTargetKey{}, targetUID
	}
	return ReadTranslationIndexPage(ctx, index, encoded, limit, readHead, readSegment)
}

// ReadTranslationIndexPage pages UID-ordered thin indexes, not the timestamp-
// ordered namespace source layout.
func ReadTranslationIndexPage(
	ctx context.Context, index TranslationIndexKey, encoded string, limit int,
	readHead func(context.Context, TranslationIndexKey) (*TranslationIndexHead, error),
	readSegment func(context.Context, TranslationIndexKey, string, int) ([]TranslationIndexCell, error),
) (TranslationDependentPage, error) {
	if err := ctx.Err(); err != nil {
		return TranslationDependentPage{}, err
	}
	if index.Kind == TranslationNamespaceIndex || index.Shard != 0 || index.Segment != 0 {
		return TranslationDependentPage{}, ErrInvalidArgument
	}
	if err := validateTranslationIndexIdentity(index); err != nil || limit < 0 {
		return TranslationDependentPage{}, ErrInvalidArgument
	}
	if limit == 0 {
		limit = 100
	}
	limit = min(limit, MaxTranslationPageSize)
	raw, _ := json.Marshal(index)
	digest := sha256.Sum256(raw)
	cursor := translationDependentCursor{Version: 1, Scope: hex.EncodeToString(digest[:])}
	if encoded != "" {
		scope := cursor.Scope
		if len(encoded) > 2048 {
			return TranslationDependentPage{}, ErrInvalidArgument
		}
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.Scope != scope ||
			cursor.Shard >= TranslationIndexShards || cursor.HeadSegment > math.MaxInt64 ||
			(cursor.HeadKnown && cursor.Segment > cursor.HeadSegment) ||
			(!cursor.HeadKnown && (cursor.Segment != 0 || cursor.HeadSegment != 0 || cursor.AfterUID != "")) {
			return TranslationDependentPage{}, ErrInvalidArgument
		}
		if cursor.AfterUID != "" {
			if id, err := uuid.Parse(cursor.AfterUID); err != nil || id.String() != cursor.AfterUID {
				return TranslationDependentPage{}, ErrInvalidArgument
			}
		}
	}
	var result TranslationDependentPage
	remaining := limit
	for cursor.Shard < TranslationIndexShards {
		if cursor.HeadKnown && cursor.Segment > cursor.HeadSegment {
			cursor.Shard++
			cursor.HeadKnown, cursor.Segment, cursor.HeadSegment, cursor.AfterUID = false, 0, 0, ""
			continue
		}
		if result.Probes == MaxTranslationIndexProbes || remaining == 0 {
			raw, _ := json.Marshal(cursor)
			result.Next = base64.RawURLEncoding.EncodeToString(raw)
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return TranslationDependentPage{}, err
		}
		index.Shard, index.Segment = cursor.Shard, 0
		if !cursor.HeadKnown {
			result.Probes++
			head, err := readHead(ctx, index)
			if err != nil {
				return TranslationDependentPage{}, err
			}
			if head == nil {
				cursor.Shard++
				continue
			}
			if head.Index != index || head.Segment > math.MaxInt64 {
				return TranslationDependentPage{}, ErrRepairRequired
			}
			cursor.HeadKnown, cursor.HeadSegment = true, head.Segment
			continue
		}
		index.Segment = cursor.Segment
		result.Probes++
		cells, err := readSegment(ctx, index, cursor.AfterUID, remaining)
		if err != nil {
			return TranslationDependentPage{}, err
		}
		if len(cells) > remaining {
			return TranslationDependentPage{}, ErrRepairRequired
		}
		for _, cell := range cells {
			shard, err := TranslationIndexShard(cell.Entry.TranslationUID)
			if err != nil || shard != index.Shard || cell.Entry.Slot >= MaxTranslationSegmentRows ||
				cell.Entry.SourceVersion == "" || len(cell.Entry.SourceVersion) > 256 || cell.Entry.CreationTimestamp.IsZero() {
				return TranslationDependentPage{}, ErrRepairRequired
			}
			cursor.AfterUID = cell.Entry.TranslationUID
			if !cell.Retired {
				result.Items = append(result.Items, cell.Entry)
			}
		}
		if len(cells) < remaining {
			cursor.Segment++
			cursor.AfterUID = ""
		}
		remaining -= len(cells)
	}
	return result, nil
}
