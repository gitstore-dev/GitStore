// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationNamespacePageResumesBothDirections(t *testing.T) {
	namespace := uuid.NewString()
	var expected []string
	entries := make(map[uint8][]datastore.TranslationIndexCell)
	for _, shard := range []uint8{0, 63} {
		for range 2 {
			uid := translationSourcePageUID(t, shard)
			expected = append(expected, uid)
			entries[shard] = append(entries[shard], datastore.TranslationIndexCell{
				Entry: datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1",
					ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)},
				Token: uuid.NewString(),
			})
		}
	}
	head := func(_ context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
		if _, ok := entries[index.Shard]; !ok {
			return nil, nil
		}
		return &datastore.TranslationIndexHead{Index: index, Segment: 1}, nil
	}
	segment := func(_ context.Context, index datastore.TranslationIndexKey, position *datastore.PageCursor, _ bool, _ int) ([]datastore.TranslationIndexCell, error) {
		if position != nil {
			return nil, nil
		}
		return entries[index.Shard][index.Segment : index.Segment+1], nil
	}
	for _, reverse := range []bool{false, true} {
		var actual []string
		cursor, emptyTurns := "", 0
		for range 32 {
			page, err := datastore.ReadTranslationNamespacePage(t.Context(), namespace, cursor, reverse, 1, head, segment)
			require.NoError(t, err)
			require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
			require.LessOrEqual(t, len(page.Items), 1)
			if len(page.Items) == 0 {
				emptyTurns++
			}
			for _, entry := range page.Items {
				actual = append(actual, entry.TranslationUID)
				require.NotEmpty(t, page.ItemCursors[entry.TranslationUID])
			}
			if page.Next == "" {
				break
			}
			require.NotEqual(t, cursor, page.Next)
			cursor = page.Next
		}
		want := slices.Clone(expected)
		if reverse {
			slices.Reverse(want)
		}
		require.Equal(t, want, actual)
		require.Positive(t, emptyTurns, "sparse shards must yield bounded advancing empty turns")
	}
	first, err := datastore.ReadTranslationNamespacePage(t.Context(), namespace, "", false, 1, head, segment)
	require.NoError(t, err)
	second, err := datastore.ReadTranslationNamespacePage(t.Context(), namespace, first.Next, false, 1, head, segment)
	require.NoError(t, err)
	before, err := datastore.ReadTranslationNamespacePage(t.Context(), namespace, second.ItemCursors[expected[1]], true, 1, head, segment)
	require.NoError(t, err)
	require.Equal(t, expected[0], before.Items[0].TranslationUID)
	_, err = datastore.ReadTranslationNamespacePage(t.Context(), uuid.NewString(), first.Next, false, 1, head, segment)
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
}

func TestTranslationNamespacePageBoundsRetiredWorkAndErrors(t *testing.T) {
	namespace := uuid.NewString()
	head := func(_ context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
		return &datastore.TranslationIndexHead{Index: index, Segment: 100}, nil
	}
	calls := 0
	segment := func(_ context.Context, _ datastore.TranslationIndexKey, _ *datastore.PageCursor, _ bool, limit int) ([]datastore.TranslationIndexCell, error) {
		calls++
		require.Equal(t, datastore.MaxTranslationPageSize, limit)
		var cells []datastore.TranslationIndexCell
		for i := range limit {
			cells = append(cells, datastore.TranslationIndexCell{
				Entry: datastore.TranslationIndexEntry{TranslationUID: translationSourcePageUID(t, 0), SourceVersion: "1",
					CreationTimestamp: time.Now().UTC().Add(-time.Duration(i) * time.Second), Slot: uint32(i)},
				Token: uuid.NewString(), Retired: true,
			})
		}
		return cells, nil
	}
	page, err := datastore.ReadTranslationNamespacePage(t.Context(), namespace, "", false, 1000, head, segment)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.NotEmpty(t, page.Next)
	require.Equal(t, 1, calls, "retired cells consume the entire scan budget")
	failure := errors.New("unavailable namespace segment")
	page, err = datastore.ReadTranslationNamespacePage(t.Context(), namespace, "", false, 1, head,
		func(context.Context, datastore.TranslationIndexKey, *datastore.PageCursor, bool, int) ([]datastore.TranslationIndexCell, error) {
			return nil, failure
		})
	require.ErrorIs(t, err, failure)
	require.Empty(t, page)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = datastore.ReadTranslationNamespacePage(ctx, namespace, "", false, 1, head, segment)
	require.ErrorIs(t, err, context.Canceled)
}

func translationSourcePageUID(t *testing.T, shard uint8) string {
	t.Helper()
	for {
		uid := uuid.NewString()
		actual, err := datastore.TranslationIndexShard(uid)
		require.NoError(t, err)
		if actual == shard {
			return uid
		}
	}
}
