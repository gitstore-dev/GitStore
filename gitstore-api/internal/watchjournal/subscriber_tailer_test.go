// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
)

func TestSharedTailerPollsOnceForManyLiveSubscribers(t *testing.T) {
	epoch := uuid.NewString()
	now := time.Now().UTC()
	journal := &subscriberJournal{bounds: datastore.NamespaceWatchBounds{
		Epoch: epoch, UpdatedAt: now, ProgressAt: now,
	}}
	subscriber := NewSubscriber(journal, SubscriberConfig{
		ReadBatchSize: 16, MaxReplayEvents: 1000, BufferSize: 8,
		PollMin: time.Millisecond, PollMax: 5 * time.Millisecond,
	})

	const subscriberCount = 100
	streams := make([]*Stream, 0, subscriberCount)
	cancels := make([]context.CancelFunc, 0, subscriberCount)
	for range subscriberCount {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := subscriber.Subscribe(ctx, "")
		if err != nil {
			cancel()
			t.Fatalf("Subscribe() error = %v", err)
		}
		streams = append(streams, stream)
		cancels = append(cancels, cancel)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	journal.mu.Lock()
	journal.limits = nil
	journal.events = append(journal.events, datastore.NamespaceWatchEvent{
		Epoch: epoch, Sequence: 1, Type: datastore.NamespaceWatchAdded, At: now,
	})
	journal.bounds.HighWater = 1
	journal.bounds.UpdatedAt = now
	journal.bounds.ProgressAt = now
	journal.mu.Unlock()

	for index, stream := range streams {
		select {
		case event := <-stream.Events:
			if event.Sequence != 1 {
				t.Fatalf("subscriber %d sequence = %d, want 1", index, event.Sequence)
			}
		case err := <-stream.Errors:
			t.Fatalf("subscriber %d terminal error: %v", index, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d timed out", index)
		}
	}

	journal.mu.Lock()
	readCalls := len(journal.limits)
	journal.mu.Unlock()
	if readCalls >= subscriberCount/2 {
		t.Fatalf("ReadAfter calls = %d for %d live subscribers; polling scaled with subscribers", readCalls, subscriberCount)
	}
}

func TestSharedTailerPreservesReplayToLiveOrdering(t *testing.T) {
	epoch := uuid.NewString()
	now := time.Now().UTC()
	journal := &subscriberJournal{bounds: datastore.NamespaceWatchBounds{
		Epoch: epoch, UpdatedAt: now, ProgressAt: now,
	}}
	subscriber := NewSubscriber(journal, SubscriberConfig{
		ReadBatchSize: 4, MaxReplayEvents: 100, BufferSize: 4,
		PollMin: time.Millisecond, PollMax: 5 * time.Millisecond,
	})

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first, err := subscriber.Subscribe(firstCtx, "")
	if err != nil {
		t.Fatalf("first Subscribe() error = %v", err)
	}
	appendSubscriberJournalEvent(journal, datastore.NamespaceWatchEvent{
		Epoch: epoch, Sequence: 1, Type: datastore.NamespaceWatchAdded, At: now,
	})
	requireSequence(t, first, 1)

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	second, err := subscriber.Subscribe(secondCtx, EncodeCursor(epoch, 0))
	if err != nil {
		t.Fatalf("second Subscribe() error = %v", err)
	}
	requireSequence(t, second, 1)

	appendSubscriberJournalEvent(journal, datastore.NamespaceWatchEvent{
		Epoch: epoch, Sequence: 2, Type: datastore.NamespaceWatchModified, At: now,
	})
	requireSequence(t, first, 2)
	requireSequence(t, second, 2)
}

func TestSharedTailerIsolatesSlowSubscriberOverflow(t *testing.T) {
	epoch := uuid.NewString()
	now := time.Now().UTC()
	journal := &subscriberJournal{bounds: datastore.NamespaceWatchBounds{
		Epoch: epoch, UpdatedAt: now, ProgressAt: now,
	}}
	subscriber := NewSubscriber(journal, SubscriberConfig{
		ReadBatchSize: 4, MaxReplayEvents: 100, BufferSize: 1,
		PollMin: time.Millisecond, PollMax: 5 * time.Millisecond,
		BackpressureTimeout: 20 * time.Millisecond,
	})

	slowCtx, cancelSlow := context.WithCancel(context.Background())
	defer cancelSlow()
	slow, err := subscriber.Subscribe(slowCtx, "")
	if err != nil {
		t.Fatalf("slow Subscribe() error = %v", err)
	}
	healthyCtx, cancelHealthy := context.WithCancel(context.Background())
	defer cancelHealthy()
	healthy, err := subscriber.Subscribe(healthyCtx, "")
	if err != nil {
		t.Fatalf("healthy Subscribe() error = %v", err)
	}

	for sequence := uint64(1); sequence <= 5; sequence++ {
		appendSubscriberJournalEvent(journal, datastore.NamespaceWatchEvent{
			Epoch: epoch, Sequence: sequence, Type: datastore.NamespaceWatchModified, At: now,
		})
	}
	for sequence := uint64(1); sequence <= 5; sequence++ {
		requireSequence(t, healthy, sequence)
	}
	select {
	case terminal := <-slow.Errors:
		assertTerminalReason(t, terminal, CodeExpired, ReasonSubscriberOverflow)
	case <-time.After(time.Second):
		t.Fatal("slow subscriber did not expire")
	}
}

func appendSubscriberJournalEvent(journal *subscriberJournal, event datastore.NamespaceWatchEvent) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.events = append(journal.events, event)
	journal.bounds.HighWater = event.Sequence
	journal.bounds.UpdatedAt = event.At
	journal.bounds.ProgressAt = event.At
}

func requireSequence(t *testing.T, stream *Stream, want uint64) {
	t.Helper()
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
