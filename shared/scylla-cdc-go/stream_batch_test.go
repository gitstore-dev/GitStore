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
