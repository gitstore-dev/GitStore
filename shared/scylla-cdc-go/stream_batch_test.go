// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 GitStore contributors

package scyllacdc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

type windowConsumer struct {
	consumed        int
	completed       []gocql.UUID
	emptyCalls      int
	consumeError    error
	completionError error
}

func (c *windowConsumer) Consume(context.Context, Change) error {
	c.consumed++
	return c.consumeError
}

func (c *windowConsumer) QueryCompleted(_ context.Context, at gocql.UUID) error {
	c.completed = append(c.completed, at)
	return c.completionError
}

func (c *windowConsumer) Empty(context.Context, gocql.UUID) error {
	c.emptyCalls++
	return nil
}

func (*windowConsumer) End() error { return nil }

type legacyWindowConsumer struct{ consumer *windowConsumer }

func (c legacyWindowConsumer) Consume(ctx context.Context, change Change) error {
	return c.consumer.Consume(ctx, change)
}

func (c legacyWindowConsumer) Empty(ctx context.Context, at gocql.UUID) error {
	return c.consumer.QueryCompleted(ctx, at)
}

func (legacyWindowConsumer) End() error { return nil }

type windowFactory map[string]ChangeConsumer

func (f windowFactory) CreateChangeConsumer(_ context.Context, input CreateChangeConsumerInput) (ChangeConsumer, error) {
	return f[string(input.StreamID)], nil
}

type windowIterator struct {
	batch    cdcChangeBatchCols
	row      *ChangeRow
	closeErr error
	cancel   context.CancelFunc
}

func (i *windowIterator) Next() (cdcChangeBatchCols, *ChangeRow) {
	row := i.row
	i.row = nil
	return i.batch, row
}

func (i *windowIterator) Close() error {
	if i.cancel != nil {
		i.cancel()
	}
	return i.closeErr
}

func TestStreamBatchCatchupProvesWiderEmptyWindows(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := gocql.MinTimeUUID(time.Now().Add(-24 * time.Hour))
	consumer, quiet := &windowConsumer{}, &windowConsumer{}
	reader := newStreamBatchReader(&ReaderConfig{
		Logger: noLogger{}, ProgressManager: noProgressManager{},
		ChangeConsumerFactory: windowFactory{string([]byte{1}): consumer, string([]byte{2}): quiet},
		Advanced: AdvancedReaderConfig{
			QueryTimeWindowSize: 30 * time.Second, MaxEmptyQueryTimeWindowSize: time.Hour,
			ConfidenceWindowSize: time.Second,
		},
	}, start.Time().Add(-time.Hour), []StreamID{{1}, {2}}, "owned", "resources", start)
	widths := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour}
	var ends []gocql.UUID
	reader.queryRangeFunc = func(begin, end gocql.UUID) (cdcIterator, error) {
		index := len(ends)
		if index >= len(widths) {
			t.Fatal("reader failed to stop at the proved frontier")
		}
		if width := end.Time().Sub(begin.Time()); width != widths[index] {
			t.Fatalf("query %d width = %s, want %s", index, width, widths[index])
		}
		if index > 0 && begin != ends[index-1] {
			t.Fatal("empty-range catch-up skipped an unqueried interval")
		}
		ends = append(ends, end)
		if len(ends) == len(widths) {
			reader.close(end)
		}
		return &windowIterator{}, nil
	}
	if err := reader.run(ctx); err != nil {
		t.Fatal(err)
	}
	for _, current := range []*windowConsumer{consumer, quiet} {
		if len(current.completed) != len(ends) || current.consumed != 0 {
			t.Fatalf("unexpected empty-range delivery: completed=%d consumed=%d", len(current.completed), current.consumed)
		}
		for i, end := range ends {
			if current.completed[i] != end {
				t.Fatal("completion did not match a successfully closed empty query")
			}
		}
	}
}

func TestStreamBatchCatchupDoesNotConsumeOrPublishUnprovedProbe(t *testing.T) {
	for _, mode := range []string{"hot", "generation-hot", "query-error", "close-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			start := gocql.MinTimeUUID(time.Now().Add(-time.Hour))
			firstEnd := gocql.MinTimeUUID(start.Time().Add(30 * time.Second))
			fallbackEnd := gocql.MinTimeUUID(firstEnd.Time().Add(30 * time.Second))
			generationEnd := gocql.MaxTimeUUID(firstEnd.Time().Add(15 * time.Second))
			consumer := &windowConsumer{}
			reader := newStreamBatchReader(&ReaderConfig{
				Logger: noLogger{}, ProgressManager: noProgressManager{},
				ChangeConsumerFactory: windowFactory{string([]byte{1}): consumer},
				Advanced: AdvancedReaderConfig{
					QueryTimeWindowSize: 30 * time.Second, MaxEmptyQueryTimeWindowSize: time.Hour,
					ConfidenceWindowSize: time.Second, PostFailedQueryDelay: time.Hour, MaxPostFailedQueryDelay: time.Hour,
				},
			}, start.Time().Add(-time.Hour), []StreamID{{1}}, "owned", "resources", start)
			queries := 0
			reader.queryRangeFunc = func(begin, end gocql.UUID) (cdcIterator, error) {
				queries++
				if queries == 1 {
					iter := &windowIterator{}
					if mode == "generation-hot" {
						iter.cancel = func() { reader.close(generationEnd) }
					}
					return iter, nil
				}
				if queries == 2 {
					expected := gocql.MinTimeUUID(firstEnd.Time().Add(time.Minute))
					if mode == "generation-hot" {
						expected = generationEnd
					}
					if begin != firstEnd || end != expected {
						t.Fatal("historical empty range was not followed by a wider probe")
					}
					if mode == "query-error" {
						cancel()
						return nil, errors.New("probe query failed")
					}
				} else if queries != 3 || begin != firstEnd || end != fallbackEnd {
					t.Fatal("nonempty probe was not reread within the ordinary query bound")
				}
				iter := &windowIterator{
					batch: cdcChangeBatchCols{streamID: []byte{1}, time: gocql.MinTimeUUID(firstEnd.Time().Add(time.Second))},
					row:   &ChangeRow{cdcCols: cdcChangeRowCols{operation: int8(PostImage), endOfBatch: true}},
				}
				if mode == "close-error" {
					iter.closeErr, iter.cancel = errors.New("probe close failed"), cancel
				} else if queries == 3 && mode != "generation-hot" {
					reader.close(end)
				}
				return iter, nil
			}
			err := reader.run(ctx)
			if mode == "hot" || mode == "generation-hot" {
				if err != nil || queries != 3 || consumer.consumed != 1 ||
					len(consumer.completed) != 2 || consumer.completed[1] != fallbackEnd {
					t.Fatalf("nonempty fallback: err=%v queries=%d consumed=%d completed=%v",
						err, queries, consumer.consumed, consumer.completed)
				}
			} else if !errors.Is(err, context.Canceled) || queries != 2 || consumer.consumed != 0 ||
				len(consumer.completed) != 1 || reader.perStreamProgress[string([]byte{1})] != firstEnd {
				t.Fatalf("failed probe advanced progress: err=%v queries=%d consumed=%d completed=%v",
					err, queries, consumer.consumed, consumer.completed)
			}
		})
	}
}

func TestStreamBatchCatchupDoesNotPublishPastGenerationDiscoveredDuringProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := gocql.MinTimeUUID(time.Now().Add(-time.Hour))
	boundary := gocql.MaxTimeUUID(start.Time().Add(45 * time.Second))
	consumer := &windowConsumer{}
	reader := newStreamBatchReader(&ReaderConfig{
		Logger: noLogger{}, ProgressManager: noProgressManager{},
		ChangeConsumerFactory: windowFactory{string([]byte{1}): consumer},
		Advanced: AdvancedReaderConfig{
			QueryTimeWindowSize: 30 * time.Second, MaxEmptyQueryTimeWindowSize: time.Hour,
			ConfidenceWindowSize: time.Second,
		},
	}, start.Time().Add(-time.Hour), []StreamID{{1}}, "owned", "resources", start)
	queries := 0
	reader.queryRangeFunc = func(_, end gocql.UUID) (cdcIterator, error) {
		queries++
		iter := &windowIterator{}
		if queries == 2 {
			if CompareTimeUUID(end, boundary) <= 0 {
				t.Fatal("test did not probe beyond the newly discovered generation end")
			}
			iter.cancel = func() { reader.close(boundary) }
		} else if queries > 2 {
			t.Fatal("reader continued past the completed generation")
		}
		return iter, nil
	}
	if err := reader.run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(consumer.completed) != 2 || consumer.completed[1] != boundary ||
		reader.perStreamProgress[string([]byte{1})] != boundary {
		t.Fatalf("probe acknowledged the next generation: %v", consumer.completed)
	}
}

func TestStreamBatchPublishesOnlyCompletedQueryWindows(t *testing.T) {
	for _, mode := range []string{"hot", "empty", "legacy-hot", "legacy-empty", "query-error", "iteration-error", "consumer-error", "completion-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			failure := errors.New("window failed")
			now := time.Now()
			start := gocql.MinTimeUUID(now.Add(-2 * time.Second))
			hot, quiet := &windowConsumer{}, &windowConsumer{}
			if mode == "consumer-error" {
				hot.consumeError = failure
			}
			if mode == "completion-error" {
				hot.completionError, quiet.completionError = failure, failure
			}
			factory := windowFactory{string([]byte{1}): hot, string([]byte{2}): quiet}
			if mode == "legacy-hot" || mode == "legacy-empty" {
				factory[string([]byte{1})], factory[string([]byte{2})] = legacyWindowConsumer{hot}, legacyWindowConsumer{quiet}
			}
			reader := newStreamBatchReader(&ReaderConfig{
				Logger: noLogger{}, ProgressManager: noProgressManager{},
				ChangeConsumerFactory: factory,
				Advanced: AdvancedReaderConfig{
					QueryTimeWindowSize: 10 * time.Second, ConfidenceWindowSize: 500 * time.Millisecond,
					PostFailedQueryDelay: time.Hour,
				},
			}, now.Add(-time.Hour), []StreamID{{1}, {2}}, "owned", "resources", start)
			var through gocql.UUID
			closeRequested := false
			reader.queryRangeFunc = func(_, end gocql.UUID) (cdcIterator, error) {
				through = end
				if !closeRequested {
					reader.close(end)
					closeRequested = true
				}
				if mode == "query-error" {
					cancel()
					return nil, failure
				}
				iter := &windowIterator{
					batch: cdcChangeBatchCols{streamID: []byte{1}, time: gocql.MinTimeUUID(now.Add(-time.Second))},
				}
				if mode != "empty" && mode != "legacy-empty" {
					iter.row = &ChangeRow{cdcCols: cdcChangeRowCols{operation: int8(PostImage), endOfBatch: true}}
				}
				if mode == "iteration-error" {
					iter.closeErr, iter.cancel = failure, cancel
				}
				return iter, nil
			}
			err := reader.run(ctx)
			switch mode {
			case "consumer-error", "completion-error":
				if !errors.Is(err, failure) {
					t.Fatalf("run returned %v, want consumer failure", err)
				}
			case "query-error", "iteration-error":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("run returned %v, want canceled failed query", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				for _, consumer := range []*windowConsumer{hot, quiet} {
					if consumer.emptyCalls != 0 {
						t.Fatal("completed-window consumer also received Empty")
					}
					if mode == "legacy-hot" {
						if len(consumer.completed) != 0 {
							t.Fatal("legacy consumer received an empty callback for a nonempty query")
						}
						continue
					}
					if len(consumer.completed) != 1 || consumer.completed[0] != through {
						t.Fatalf("completed query did not advance every stream: %v", consumer.completed)
					}
				}
				wantConsumed := 1
				if mode == "empty" || mode == "legacy-empty" {
					wantConsumed = 0
				}
				if hot.consumed != wantConsumed || quiet.consumed != 0 {
					t.Fatalf("unexpected change delivery: hot=%d quiet=%d", hot.consumed, quiet.consumed)
				}
			}
			if mode == "consumer-error" || mode == "iteration-error" || mode == "query-error" {
				if len(hot.completed)+len(quiet.completed) != 0 {
					t.Fatal("incomplete query published a read frontier")
				}
			}
		})
	}
}
