// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"math"
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

func TestProductNamespaceShardsDistributeOneRepository(t *testing.T) {
	var counts [productNamespaceShardCount]int
	for i := range 100000 {
		var uid gocql.UUID
		uid[12], uid[13], uid[14], uid[15] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		shard := productNamespaceShard(uid.String())
		require.Equal(t, shard, productNamespaceShard(uid.String()))
		require.GreaterOrEqual(t, shard, int8(0))
		require.Less(t, int(shard), productNamespaceShardCount)
		counts[shard]++
	}
	for _, count := range counts {
		require.Greater(t, count, 1200)
		require.Less(t, count, 1900)
	}
}

func TestProductProjectionCapsPageBeforeMergeAllocation(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Millisecond)
	for _, size := range []int{datastore.MaxProductPageSize + 1, math.MaxInt32, math.MaxInt} {
		for _, backward := range []bool{false, true} {
			items, err := collectProductProjectionPage(t.Context(), "shop", backward, size, nil, productNamespaceShardCount, datastore.MaxProductPageSize+3,
				func(_ context.Context, shard int8, _ *categoryProductRow) ([]categoryProductRow, error) {
					if shard != 0 {
						return nil, nil
					}
					rows := make([]categoryProductRow, datastore.MaxProductPageSize+2)
					for i := range rows {
						index := len(rows) - i
						if backward {
							index = i + 1
						}
						var uid gocql.UUID
						uid[14], uid[15] = byte(index>>8), byte(index)
						rows[i] = categoryProductRow{CreationTimestamp: created.Add(time.Duration(index) * time.Millisecond), ProductUID: uid}
					}
					return rows, nil
				}, func(_ context.Context, row categoryProductRow) (*datastore.Product, error) {
					return &datastore.Product{UID: row.ProductUID.String(), Namespace: "shop", CreationTimestamp: row.CreationTimestamp}, nil
				})
			require.NoError(t, err)
			require.Len(t, items, datastore.MaxProductPageSize+1)
		}
	}
}

func TestProductPagesRejectNegativeSizesBeforeDatastoreAccess(t *testing.T) {
	store := &scyllaDatastore{}
	for _, page := range []datastore.PageParams{{First: -1}, {Last: math.MinInt}, {First: -1, Last: 1}, {First: 1, Last: -1}} {
		_, err := store.ListProducts(t.Context(), "shop", page)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		_, err = store.ListCategoryProducts(t.Context(), "shop", "category", page)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	}
}

func TestProductProjectionSmallPagesRefillSkewedShard(t *testing.T) {
	for _, backward := range []bool{false, true} {
		name := "forward"
		if backward {
			name = "backward"
		}
		t.Run(name, func(t *testing.T) {
			created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			var queries atomic.Int64
			items, err := collectProductProjectionPage(t.Context(), "shop", backward, 10, nil, productNamespaceShardCount, 2,
				func(_ context.Context, shard int8, cursor *categoryProductRow) ([]categoryProductRow, error) {
					queries.Add(1)
					if shard != 0 {
						return nil, nil
					}
					var rows []categoryProductRow
					for index := range 30 {
						i := 29 - index
						if backward {
							i = index
						}
						var uid gocql.UUID
						uid[15] = byte(i + 1)
						row := categoryProductRow{CreationTimestamp: created.Add(time.Duration(i) * time.Second), ProductUID: uid}
						if cursor != nil && ((backward && !categoryProductRowBefore(*cursor, row)) ||
							(!backward && !categoryProductRowBefore(row, *cursor))) {
							continue
						}
						rows = append(rows, row)
						if len(rows) == 2 {
							break
						}
					}
					return rows, nil
				}, func(_ context.Context, row categoryProductRow) (*datastore.Product, error) {
					if row.ProductUID[15] > 20 {
						return nil, datastore.ErrNotFound
					}
					return &datastore.Product{UID: row.ProductUID.String(), Namespace: "shop", CreationTimestamp: row.CreationTimestamp}, nil
				})
			require.NoError(t, err)
			require.Len(t, items, 11)
			require.Greater(t, queries.Load(), int64(productNamespaceShardCount))
			require.LessOrEqual(t, queries.Load(), int64(projectionListQueryBudget))
			for i := 1; i < len(items); i++ {
				require.True(t, items[i].CreationTimestamp.Before(items[i-1].CreationTimestamp))
			}
			expected := byte(20)
			if backward {
				expected = 11
			}
			uid, err := gocql.ParseUUID(items[0].UID)
			require.NoError(t, err)
			require.Equal(t, expected, uid[15])
		})
	}
}

func TestProductProjectionHydrationIsBoundedAndOrdered(t *testing.T) {
	for _, backward := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "backward"}[backward], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			var calls, active, peak atomic.Int64
			started := make(chan struct{})
			items, err := collectProductProjectionPage(ctx, "shop", backward, 33, nil, productNamespaceShardCount, 64,
				func(_ context.Context, shard int8, _ *categoryProductRow) ([]categoryProductRow, error) {
					if shard != 0 {
						return nil, nil
					}
					rows := make([]categoryProductRow, 64)
					for i := range rows {
						rows[i].ProductUID[15] = byte(i + 1)
						rows[i].CreationTimestamp = created.Add(-time.Duration(i) * time.Second)
					}
					if backward {
						sort.Slice(rows, func(i, j int) bool { return categoryProductRowBefore(rows[i], rows[j]) })
					}
					return rows, nil
				}, func(ctx context.Context, row categoryProductRow) (*datastore.Product, error) {
					current := active.Add(1)
					defer active.Add(-1)
					for previous := peak.Load(); current > previous; previous = peak.Load() {
						if peak.CompareAndSwap(previous, current) {
							break
						}
					}
					call := calls.Add(1)
					if call == productProjectionHydrationConcurrency {
						close(started)
					}
					select {
					case <-started:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					select {
					case <-time.After(time.Duration(row.ProductUID[15]%3) * time.Millisecond):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return &datastore.Product{UID: row.ProductUID.String(), Namespace: "shop", CreationTimestamp: row.CreationTimestamp}, nil
				})
			require.NoError(t, err)
			require.Len(t, items, 34)
			require.Equal(t, int64(34), calls.Load(), "do not hydrate beyond the requested lookahead")
			require.Equal(t, int64(productProjectionHydrationConcurrency), peak.Load())
			require.Zero(t, active.Load(), "all hydration work must finish before returning")
			for i := 1; i < len(items); i++ {
				require.True(t, items[i].CreationTimestamp.Before(items[i-1].CreationTimestamp))
			}
		})
	}
}

func TestProductProjectionHydrationDoesNotWaitForSlowBatchTail(t *testing.T) {
	for _, backward := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "backward"}[backward], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			release := make(chan struct{})
			releaseTail := sync.OnceFunc(func() { close(release) })
			defer releaseTail()
			tailStarted, nextStarted := make(chan struct{}), make(chan struct{})
			uidAt := func(index int) byte {
				if backward {
					return byte(64 - index)
				}
				return byte(index + 1)
			}
			var calls, active atomic.Int64
			type outcome struct {
				items []*datastore.Product
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				items, err := collectProductProjectionPage(ctx, "shop", backward, 33, nil, 1, 64,
					func(context.Context, int8, *categoryProductRow) ([]categoryProductRow, error) {
						rows := make([]categoryProductRow, 64)
						for index := range rows {
							rows[index].ProductUID[15] = uidAt(index)
							rows[index].CreationTimestamp = time.Unix(int64(64-uidAt(index)), 0)
						}
						return rows, nil
					}, func(ctx context.Context, row categoryProductRow) (*datastore.Product, error) {
						calls.Add(1)
						active.Add(1)
						defer active.Add(-1)
						if row.ProductUID[15] == uidAt(productProjectionHydrationConcurrency-1) {
							close(tailStarted)
							select {
							case <-release:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
						if row.ProductUID[15] == uidAt(productProjectionHydrationConcurrency) {
							close(nextStarted)
						}
						return &datastore.Product{UID: row.ProductUID.String(), Namespace: "shop", CreationTimestamp: row.CreationTimestamp}, nil
					})
				done <- outcome{items: items, err: err}
			}()
			select {
			case <-tailStarted:
			case <-ctx.Done():
				t.Fatal("slow tail did not start")
			}
			select {
			case <-nextStarted:
			case <-time.After(time.Second):
				t.Error("completed hydration slots were not reused while the batch tail was blocked")
			}
			releaseTail()
			select {
			case result := <-done:
				require.NoError(t, result.err)
				require.Len(t, result.items, 34)
				require.Equal(t, int64(34), calls.Load(), "do not hydrate past the requested lookahead")
				require.Zero(t, active.Load())
				for index, product := range result.items {
					expected := index
					if backward {
						expected = 33 - index
					}
					var uid gocql.UUID
					uid[15] = uidAt(expected)
					require.Equal(t, uid.String(), product.UID)
				}
			case <-ctx.Done():
				t.Fatal("hydration did not finish after releasing the tail")
			}
		})
	}
}

func TestProductProjectionHydrationKeepsLateErrorsInProjectionOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	firstFailure, laterFailure := errors.New("earlier authority lookup"), errors.New("later authority lookup")
	release := make(chan struct{})
	releaseEarlier := sync.OnceFunc(func() { close(release) })
	defer releaseEarlier()
	laterFinished := make(chan struct{})
	var active atomic.Int64
	done := make(chan error, 1)
	go func() {
		items, err := collectProductProjectionPage(ctx, "shop", false, 33, nil, 1, 64,
			func(context.Context, int8, *categoryProductRow) ([]categoryProductRow, error) {
				rows := make([]categoryProductRow, 64)
				for index := range rows {
					rows[index].ProductUID[15] = byte(index + 1)
					rows[index].CreationTimestamp = time.Unix(int64(64-index), 0)
				}
				return rows, nil
			}, func(ctx context.Context, row categoryProductRow) (*datastore.Product, error) {
				active.Add(1)
				defer active.Add(-1)
				switch row.ProductUID[15] {
				case productProjectionHydrationConcurrency + 1:
					select {
					case <-release:
						return nil, firstFailure
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				case productProjectionHydrationConcurrency + 2:
					close(laterFinished)
					return nil, laterFailure
				default:
					return &datastore.Product{UID: row.ProductUID.String(), Namespace: "shop", CreationTimestamp: row.CreationTimestamp}, nil
				}
			})
		if items != nil {
			err = errors.Join(err, errors.New("failed hydration returned a partial page"))
		}
		done <- err
	}()
	select {
	case <-laterFinished:
	case <-ctx.Done():
		t.Fatal("later failure did not finish before the earlier lookup")
	}
	select {
	case <-done:
		t.Fatal("hydration chose an out-of-order failure")
	default:
	}
	releaseEarlier()
	select {
	case err := <-done:
		require.ErrorIs(t, err, firstFailure)
		require.NotErrorIs(t, err, laterFailure)
		require.NotContains(t, err.Error(), "partial page")
		require.Zero(t, active.Load())
	case <-ctx.Done():
		t.Fatal("failed hydration did not join its callbacks")
	}
}

func TestProductProjectionHydrationJoinsCancellationAndErrors(t *testing.T) {
	failure := errors.New("authority unavailable")
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancelled"}[cancelRequest], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var active atomic.Int64
			started := make(chan struct{}, productProjectionHydrationConcurrency)
			release := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				items, err := collectProductProjectionPage(ctx, "shop", false, 33, nil, productNamespaceShardCount, 64,
					func(_ context.Context, shard int8, _ *categoryProductRow) ([]categoryProductRow, error) {
						if shard != 0 {
							return nil, nil
						}
						rows := make([]categoryProductRow, 64)
						for i := range rows {
							rows[i].ProductUID[15] = byte(i + 1)
							rows[i].CreationTimestamp = time.Unix(int64(64-i), 0)
						}
						return rows, nil
					}, func(ctx context.Context, row categoryProductRow) (*datastore.Product, error) {
						active.Add(1)
						defer active.Add(-1)
						started <- struct{}{}
						if !cancelRequest && row.ProductUID[15] == 1 {
							return nil, failure
						}
						select {
						case <-release:
							return &datastore.Product{UID: row.ProductUID.String(), Namespace: "shop", CreationTimestamp: row.CreationTimestamp}, nil
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					})
				if items != nil {
					err = errors.Join(err, errors.New("failed hydration returned a partial page"))
				}
				done <- err
			}()
			for range productProjectionHydrationConcurrency {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("bounded hydration cohort did not start")
				}
			}
			select {
			case <-done:
				t.Fatal("hydration returned while its callbacks were still running")
			default:
			}
			expected := failure
			if cancelRequest {
				expected = context.Canceled
				cancel()
			} else {
				close(release)
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, expected)
				require.NotContains(t, err.Error(), "partial page")
			case <-time.After(5 * time.Second):
				t.Fatal("hydration callbacks did not finish")
			}
			require.Zero(t, active.Load())
		})
	}
}

func TestProductProjectionHydrationExactLookupBudget(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(map[int]string{0: "exhausted", 1: "more-candidates"}[extra], func(t *testing.T) {
			var lookups atomic.Int64
			items, err := collectProductProjectionPage(t.Context(), "shop", false, 10, nil, 1, projectionListLookupBudget+2,
				func(context.Context, int8, *categoryProductRow) ([]categoryProductRow, error) {
					return make([]categoryProductRow, projectionListLookupBudget+extra), nil
				}, func(context.Context, categoryProductRow) (*datastore.Product, error) {
					lookups.Add(1)
					return nil, datastore.ErrNotFound
				})
			require.Equal(t, int64(projectionListLookupBudget), lookups.Load())
			if extra == 0 {
				require.NoError(t, err)
				require.Empty(t, items)
			} else {
				require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
				require.Nil(t, items)
			}
		})
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

func TestCategoryProductPageRejectsDuplicateMembership(t *testing.T) {
	var uid gocql.UUID
	uid[15] = 1
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	product := &datastore.Product{UID: uid.String(), Namespace: "shop", CreationTimestamp: created}
	for _, backward := range []bool{false, true} {
		items, err := collectCategoryProductPage(t.Context(), "shop", backward, 1, nil,
			func(_ context.Context, shard int8, _ *categoryProductRow) ([]categoryProductRow, error) {
				if shard < 2 {
					return []categoryProductRow{{ProductUID: uid, CreationTimestamp: created}}, nil
				}
				return nil, nil
			}, func(context.Context, string) (*datastore.Product, error) { return product, nil })
		require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
		require.ErrorContains(t, err, "duplicate")
		require.Nil(t, items, "a duplicate lookahead cannot create a truncated page or false next-page flag")
	}
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
