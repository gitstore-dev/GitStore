// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/require"
)

func TestCategoryProductShardIsStableAndBounded(t *testing.T) {
	uid := "00000000-0000-0000-0000-000000000001"
	got := categoryProductShard(uid)
	if got != categoryProductShard(uid) {
		t.Fatal("category product shard is not stable")
	}
	if got < 0 || got >= categoryProductShardCount {
		t.Fatalf("shard %d outside [0,%d)", got, categoryProductShardCount)
	}
}

func TestCategoryProductPageRefillsStaleShards(t *testing.T) {
	var rows [categoryProductShardCount][]categoryProductRow
	products := map[string]*datastore.Product{}
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 30 {
		var uid gocql.UUID
		uid[15] = byte(i + 1)
		row := categoryProductRow{CreationTimestamp: created.Add(-time.Duration(i) * time.Second), ProductUID: uid}
		shard := 0
		if i >= 12 {
			shard = i % 3
		}
		rows[shard] = append(rows[shard], row)
		if i < 10 {
			continue
		}
		products[uid.String()] = &datastore.Product{UID: uid.String(), Namespace: "shop", CreationTimestamp: row.CreationTimestamp}
		if i == 10 {
			products[uid.String()].Namespace = "foreign"
		}
		if i == 11 {
			products[uid.String()].CreationTimestamp = created
		}
	}
	read := func(page datastore.PageParams) *datastore.PageResult[datastore.Product] {
		t.Helper()
		var position *categoryProductRow
		cursor := page.After
		if page.Last > 0 {
			cursor = page.Before
		}
		if cursor != "" {
			parsed, err := parsePageCursor(cursor)
			require.NoError(t, err)
			uid, err := gocql.ParseUUID(parsed.ID)
			require.NoError(t, err)
			position = &categoryProductRow{CreationTimestamp: parsed.CreatedAt, ProductUID: uid}
		}
		var mu sync.Mutex
		queries := 0
		items, err := collectCategoryProductPage(t.Context(), "shop", page.Last > 0, page.Limit(), position,
			func(_ context.Context, shard int8, cursor *categoryProductRow) ([]categoryProductRow, error) {
				mu.Lock()
				queries++
				mu.Unlock()
				candidates := append([]categoryProductRow(nil), rows[shard]...)
				sort.Slice(candidates, func(i, j int) bool {
					if page.Last > 0 {
						return categoryProductRowBefore(candidates[i], candidates[j])
					}
					return categoryProductRowBefore(candidates[j], candidates[i])
				})
				result := make([]categoryProductRow, 0, page.Limit()+1)
				for _, row := range candidates {
					if cursor != nil && ((page.Last > 0 && !categoryProductRowBefore(*cursor, row)) ||
						(page.Last == 0 && !categoryProductRowBefore(row, *cursor))) {
						continue
					}
					result = append(result, row)
					if len(result) == page.Limit()+1 {
						break
					}
				}
				return result, nil
			}, func(_ context.Context, uid string) (*datastore.Product, error) {
				product, ok := products[uid]
				if !ok {
					return nil, datastore.ErrNotFound
				}
				return product, nil
			})
		require.NoError(t, err)
		require.GreaterOrEqual(t, queries, categoryProductShardCount)
		if cursor == "" && page.First > 0 {
			require.Greater(t, queries, categoryProductShardCount, "a stale hot shard must be refilled without skipping its live tail")
		}
		return buildPageResult(items, page.Limit(), page)
	}
	first := read(datastore.PageParams{First: 2})
	require.Len(t, first.Items, 2)
	require.True(t, first.HasNext)
	require.Equal(t, byte(13), mustParseUUID(first.Items[0].UID)[15])
	require.Equal(t, byte(14), mustParseUUID(first.Items[1].UID)[15])
	last := first.Items[1]
	second := read(datastore.PageParams{First: 2, After: encodeKeysetCursor(last.CreationTimestamp, last.UID)})
	require.Len(t, second.Items, 2)
	require.Equal(t, byte(15), mustParseUUID(second.Items[0].UID)[15])
	before := second.Items[0]
	backward := read(datastore.PageParams{Last: 2, Before: encodeKeysetCursor(before.CreationTimestamp, before.UID)})
	require.Equal(t, first.Items, backward.Items, "backward pagination must recover the exact forward window")
	require.False(t, backward.HasPrevious, "deleted rows are not evidence of another live page")
}

func TestCategoryProductPageErrorsAndWorkBudgets(t *testing.T) {
	for _, limit := range []int{1, datastore.DefaultPageSize} {
		t.Run(map[int]string{1: "query-budget", datastore.DefaultPageSize: "lookup-budget"}[limit], func(t *testing.T) {
			var queries, lookups atomic.Int64
			items, err := collectCategoryProductPage(t.Context(), "shop", false, limit, nil,
				func(_ context.Context, shard int8, _ *categoryProductRow) ([]categoryProductRow, error) {
					queries.Add(1)
					if shard != 0 {
						return nil, nil
					}
					// Persistently stale projections cannot create an unbounded scan.
					return make([]categoryProductRow, limit+1), nil
				}, func(context.Context, string) (*datastore.Product, error) {
					lookups.Add(1)
					return nil, datastore.ErrNotFound
				})
			require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
			require.Nil(t, items, "budget exhaustion must not return a success-shaped partial page")
			require.LessOrEqual(t, queries.Load(), int64(projectionListQueryBudget))
			require.LessOrEqual(t, lookups.Load(), int64(projectionListLookupBudget))
			if limit == 1 {
				require.Equal(t, int64(projectionListQueryBudget), queries.Load())
				require.ErrorContains(t, err, "query budget")
			} else {
				require.Equal(t, int64(projectionListLookupBudget), lookups.Load())
				require.ErrorContains(t, err, "lookup budget")
			}
		})
	}
	failure := errors.New("datastore unavailable")
	for _, failingStage := range []string{"shard", "hydrate"} {
		t.Run(failingStage, func(t *testing.T) {
			_, err := collectCategoryProductPage(t.Context(), "shop", false, 1, nil,
				func(_ context.Context, shard int8, _ *categoryProductRow) ([]categoryProductRow, error) {
					if failingStage == "shard" {
						return nil, failure
					}
					if shard == 0 {
						return []categoryProductRow{{}}, nil
					}
					return nil, nil
				}, func(context.Context, string) (*datastore.Product, error) { return nil, failure })
			require.ErrorIs(t, err, failure)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := collectCategoryProductPage(ctx, "shop", false, 1, nil,
		func(context.Context, int8, *categoryProductRow) ([]categoryProductRow, error) {
			t.Fatal("cancelled reads must not start shard work")
			return nil, nil
		}, nil)
	require.ErrorIs(t, err, context.Canceled)
}
