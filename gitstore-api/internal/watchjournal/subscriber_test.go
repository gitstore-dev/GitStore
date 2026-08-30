// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
)

type subscriberJournal struct {
	mu     sync.Mutex
	bounds datastore.NamespaceWatchBounds
	events []datastore.NamespaceWatchEvent
	limits []int
	read   func(datastore.NamespaceWatchCursor, int) ([]datastore.NamespaceWatchEvent, error)
}

func (j *subscriberJournal) Bounds(context.Context) (datastore.NamespaceWatchBounds, error) {
	return j.bounds, nil
}

func (j *subscriberJournal) ReadAfter(_ context.Context, cursor datastore.NamespaceWatchCursor, limit int) ([]datastore.NamespaceWatchEvent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.limits = append(j.limits, limit)
	if j.read != nil {
		return j.read(cursor, limit)
	}
	out := make([]datastore.NamespaceWatchEvent, 0, limit)
	for _, event := range j.events {
		if event.Sequence > cursor.Sequence {
			out = append(out, event)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (*subscriberJournal) Append(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceWatchEvent, time.Duration) (datastore.NamespaceWatchEvent, error) {
	panic("unused")
}
func (*subscriberJournal) AcquireLease(context.Context, string, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	panic("unused")
}
func (*subscriberJournal) RenewLease(context.Context, datastore.NamespaceWatchLease, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	panic("unused")
}
func (*subscriberJournal) ReleaseLease(context.Context, datastore.NamespaceWatchLease) error {
	panic("unused")
}
func (*subscriberJournal) LoadProgress(context.Context, string) (datastore.NamespaceCDCProgress, error) {
	panic("unused")
}
func (*subscriberJournal) SaveProgress(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceCDCProgress) error {
	panic("unused")
}

func TestSubscriberResumesStrictlyAfterCursorInBoundedPages(t *testing.T) {
	t.Parallel()
	epoch := uuid.NewString()
	now := time.Now().UTC()
	events := make([]datastore.NamespaceWatchEvent, 600)
	for i := range events {
		events[i] = datastore.NamespaceWatchEvent{Epoch: epoch, Sequence: uint64(i + 1), Type: datastore.NamespaceWatchModified, At: now}
	}
	journal := &subscriberJournal{
		bounds: datastore.NamespaceWatchBounds{Epoch: epoch, Oldest: 1, HighWater: 600, UpdatedAt: now},
		events: events,
	}
	subscriber := NewSubscriber(journal, SubscriberConfig{
		ReadBatchSize: 256, MaxReplayEvents: 100000, BufferSize: 700,
		PollMin: time.Millisecond, PollMax: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := subscriber.Subscribe(ctx, EncodeCursor(epoch, 10))
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	for want := uint64(11); want <= 600; want++ {
		select {
		case event := <-stream.Events:
			if event.Sequence != want {
				t.Fatalf("sequence = %d, want %d", event.Sequence, want)
			}
		case err := <-stream.Errors:
			t.Fatalf("unexpected terminal error: %v", err)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for sequence %d", want)
		}
	}
	cancel()
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if len(journal.limits) < 3 {
		t.Fatalf("ReadAfter calls = %d, want at least 3", len(journal.limits))
	}
	for _, limit := range journal.limits {
		if limit != 256 {
			t.Fatalf("ReadAfter limit = %d, want 256", limit)
		}
	}
}

func TestSubscriberRejectsReplayBeyondConfiguredCap(t *testing.T) {
	t.Parallel()
	epoch := uuid.NewString()
	journal := &subscriberJournal{bounds: datastore.NamespaceWatchBounds{
		Epoch: epoch, Oldest: 1, HighWater: 100001, UpdatedAt: time.Now().UTC(),
	}}
	subscriber := NewSubscriber(journal, SubscriberConfig{MaxReplayEvents: 100000})
	_, err := subscriber.Subscribe(context.Background(), EncodeCursor(epoch, 0))
	assertTerminalReason(t, err, CodeExpired, ReasonReplayLimit)
}

func TestSubscriberBackpressuresRetainedReplayInsteadOfExpiring(t *testing.T) {
	t.Parallel()
	epoch := uuid.NewString()
	now := time.Now().UTC()
	events := make([]datastore.NamespaceWatchEvent, 128)
	for i := range events {
		events[i] = datastore.NamespaceWatchEvent{Epoch: epoch, Sequence: uint64(i + 1), At: now}
	}
	journal := &subscriberJournal{
		bounds: datastore.NamespaceWatchBounds{Epoch: epoch, Oldest: 1, HighWater: 128, UpdatedAt: now},
		events: events,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := NewSubscriber(journal, SubscriberConfig{
		BufferSize: 1, ReadBatchSize: 128, MaxReplayEvents: 100000,
		BackpressureTimeout: time.Second,
	}).Subscribe(ctx, EncodeCursor(epoch, 0))
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	for want := uint64(1); want <= 128; want++ {
		select {
		case event := <-stream.Events:
			if event.Sequence != want {
				t.Fatalf("sequence = %d, want %d", event.Sequence, want)
			}
		case terminal := <-stream.Errors:
			t.Fatalf("unexpected terminal error: %v", terminal)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for sequence %d", want)
		}
	}
}
