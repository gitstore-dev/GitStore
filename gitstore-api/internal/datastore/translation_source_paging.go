// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
)

type TranslationNamespacePage struct {
	TranslationDependentPage
	ItemCursors map[string]string
}

func ResolveTranslationNamespacePage(ctx context.Context, indexed TranslationNamespacePage, params PageParams,
	load func(context.Context, TranslationIndexEntry) (*Translation, error),
) (*TranslationPage, error) {
	result := &TranslationPage{
		PageResult:  PageResult[Translation]{Items: []*Translation{}},
		ItemCursors: make(map[string]string),
	}
	for _, entry := range indexed.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source, err := load(ctx, entry)
		if err != nil {
			return nil, err
		}
		if source != nil {
			result.Items = append(result.Items, source)
			result.ItemCursors[source.UID] = indexed.ItemCursors[source.UID]
		}
	}
	if params.Last > 0 {
		slices.Reverse(result.Items)
		result.HasPrevious, result.HasNext = indexed.Next != "", params.Before != ""
		result.PreviousCursor, result.NextCursor = indexed.Next, params.Before
		if len(result.Items) > 0 {
			result.NextCursor = result.ItemCursors[result.Items[len(result.Items)-1].UID]
		}
	} else {
		result.HasNext, result.HasPrevious = indexed.Next != "", params.After != ""
		result.NextCursor, result.PreviousCursor = indexed.Next, params.After
		if len(result.Items) > 0 {
			result.PreviousCursor = result.ItemCursors[result.Items[0].UID]
		}
	}
	return result, nil
}

type translationNamespaceCursor struct {
	Version     int
	Scope       string
	Shard       int
	Segment     uint64
	HeadSegment uint64
	HeadKnown   bool
	Backward    bool
	AfterUID    string
	CreatedAt   time.Time
}

func (cursor translationNamespaceCursor) encode() string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Namespace order is shard/segment ascending, then creation timestamp/UID
// descending. Reversing the traversal preserves that same connection order.
func ReadTranslationNamespacePage(
	ctx context.Context, namespace, encoded string, backward bool, limit int,
	readHead func(context.Context, TranslationIndexKey) (*TranslationIndexHead, error),
	readSegment func(context.Context, TranslationIndexKey, *PageCursor, bool, int) ([]TranslationIndexCell, error),
) (TranslationNamespacePage, error) {
	if err := ctx.Err(); err != nil {
		return TranslationNamespacePage{}, err
	}
	id, err := uuid.Parse(namespace)
	if err != nil || id.String() != namespace || limit < 0 {
		return TranslationNamespacePage{}, ErrInvalidArgument
	}
	if limit == 0 {
		limit = 100
	}
	limit = min(limit, MaxTranslationPageSize)
	cursor := translationNamespaceCursor{Version: 2, Scope: namespace, Backward: backward}
	if backward {
		cursor.Shard = TranslationIndexShards - 1
	}
	if encoded != "" {
		if len(encoded) > 2048 {
			return TranslationNamespacePage{}, ErrInvalidArgument
		}
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || json.Unmarshal(raw, &cursor) != nil ||
			cursor.Version != 2 || cursor.Scope != namespace || cursor.Shard < 0 || cursor.Shard >= TranslationIndexShards ||
			cursor.HeadSegment > math.MaxInt64 || cursor.Segment > cursor.HeadSegment ||
			(!cursor.HeadKnown && (cursor.Segment != 0 || cursor.HeadSegment != 0 || cursor.AfterUID != "")) ||
			(cursor.AfterUID == "") != cursor.CreatedAt.IsZero() {
			return TranslationNamespacePage{}, ErrInvalidArgument
		}
		if cursor.AfterUID != "" {
			if id, err := uuid.Parse(cursor.AfterUID); err != nil || id.String() != cursor.AfterUID {
				return TranslationNamespacePage{}, ErrInvalidArgument
			}
		}
	}
	advanceShard := func() {
		if backward {
			cursor.Shard--
		} else {
			cursor.Shard++
		}
		cursor.HeadKnown, cursor.Segment, cursor.HeadSegment = false, 0, 0
		cursor.AfterUID, cursor.CreatedAt = "", time.Time{}
	}
	advanceSegment := func() {
		cursor.AfterUID, cursor.CreatedAt = "", time.Time{}
		if backward {
			if cursor.Segment == 0 {
				advanceShard()
			} else {
				cursor.Segment--
			}
		} else if cursor.Segment == cursor.HeadSegment {
			advanceShard()
		} else {
			cursor.Segment++
		}
	}
	if cursor.Backward != backward && cursor.AfterUID == "" {
		if cursor.HeadKnown {
			advanceSegment()
		} else {
			advanceShard()
		}
	}
	cursor.Backward = backward
	result := TranslationNamespacePage{ItemCursors: make(map[string]string)}
	remaining := limit
	for cursor.Shard >= 0 && cursor.Shard < TranslationIndexShards {
		if result.Probes == MaxTranslationIndexProbes || remaining == 0 {
			result.Next = cursor.encode()
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return TranslationNamespacePage{}, err
		}
		index := TranslationIndexKey{Kind: TranslationNamespaceIndex, NamespaceUID: namespace, Shard: uint8(cursor.Shard)}
		if !cursor.HeadKnown {
			result.Probes++
			head, err := readHead(ctx, index)
			if err != nil {
				return TranslationNamespacePage{}, err
			}
			if head == nil {
				advanceShard()
				continue
			}
			if head.Index != index || head.Segment > math.MaxInt64 {
				return TranslationNamespacePage{}, ErrRepairRequired
			}
			cursor.HeadKnown, cursor.HeadSegment = true, head.Segment
			if backward {
				cursor.Segment = head.Segment
			}
			continue
		}
		index.Segment = cursor.Segment
		var position *PageCursor
		if cursor.AfterUID != "" {
			position = &PageCursor{ID: cursor.AfterUID, CreatedAt: cursor.CreatedAt}
		}
		result.Probes++
		cells, err := readSegment(ctx, index, position, backward, remaining)
		if err != nil {
			return TranslationNamespacePage{}, err
		}
		if len(cells) > remaining {
			return TranslationNamespacePage{}, ErrRepairRequired
		}
		for _, cell := range cells {
			entry := cell.Entry
			shard, err := TranslationIndexShard(entry.TranslationUID)
			if err != nil || shard != index.Shard || entry.Slot >= MaxTranslationSegmentRows ||
				entry.SourceVersion == "" || len(entry.SourceVersion) > 256 || entry.CreationTimestamp.IsZero() {
				return TranslationNamespacePage{}, ErrRepairRequired
			}
			cursor.AfterUID, cursor.CreatedAt = entry.TranslationUID, entry.CreationTimestamp
			if !cell.Retired {
				result.Items = append(result.Items, entry)
				result.ItemCursors[entry.TranslationUID] = cursor.encode()
			}
		}
		if len(cells) < remaining {
			advanceSegment()
		}
		remaining -= len(cells)
	}
	return result, nil
}
