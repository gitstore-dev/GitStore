// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
)

func assertTerminalReason(t *testing.T, err error, code string, reason Reason) {
	t.Helper()
	terminal, ok := AsTerminal(err)
	if !ok {
		t.Fatalf("error %v is not TerminalError", err)
	}
	if terminal.Code != code || terminal.Reason != reason {
		t.Fatalf("terminal = (%s, %s), want (%s, %s)", terminal.Code, terminal.Reason, code, reason)
	}
}

func TestSubscriberRejectsUnprovableResumeCursors(t *testing.T) {
	t.Parallel()
	epoch := uuid.NewString()
	now := time.Now().UTC()
	base := &subscriberJournal{bounds: datastore.NamespaceWatchBounds{
		Epoch: epoch, Oldest: 10, HighWater: 20, UpdatedAt: now, ProgressAt: now,
	}}
	tests := []struct {
		name   string
		cursor string
		reason Reason
	}{
		{name: "retention", cursor: EncodeCursor(epoch, 8), reason: ReasonRetentionExpired},
		{name: "epoch", cursor: EncodeCursor(uuid.NewString(), 10), reason: ReasonEpochMismatch},
		{name: "incompatible version", cursor: "nwv2:" + epoch + ":a", reason: ReasonIncompatibleCursor},
		{name: "invalid", cursor: "not-a-cursor", reason: ReasonInvalidCursor},
		{name: "future", cursor: EncodeCursor(epoch, 21), reason: ReasonInvalidCursor},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSubscriber(base, SubscriberConfig{}).Subscribe(context.Background(), test.cursor)
			assertTerminalReason(t, err, CodeExpired, test.reason)
		})
	}
}

func TestSubscriberFailsOnJournalDiscontinuity(t *testing.T) {
	t.Parallel()
	epoch := uuid.NewString()
	journal := &subscriberJournal{bounds: datastore.NamespaceWatchBounds{
		Epoch: epoch, Oldest: 1, HighWater: 3, UpdatedAt: time.Now().UTC(), ProgressAt: time.Now().UTC(),
	}}
	journal.read = func(cursor datastore.NamespaceWatchCursor, _ int) ([]datastore.NamespaceWatchEvent, error) {
		if cursor.Sequence == 0 {
			return []datastore.NamespaceWatchEvent{{Epoch: epoch, Sequence: 1}, {Epoch: epoch, Sequence: 3}}, nil
		}
		return nil, nil
	}
	stream, err := NewSubscriber(journal, SubscriberConfig{BufferSize: 4}).Subscribe(context.Background(), EncodeCursor(epoch, 0))
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	<-stream.Events
	assertTerminalReason(t, <-stream.Errors, CodeExpired, ReasonJournalDiscontinuity)
}

func TestSubscriberOverflowIsTerminal(t *testing.T) {
	t.Parallel()
	epoch := uuid.NewString()
	journal := &subscriberJournal{
		bounds: datastore.NamespaceWatchBounds{Epoch: epoch, Oldest: 1, HighWater: 2, UpdatedAt: time.Now().UTC(), ProgressAt: time.Now().UTC()},
		events: []datastore.NamespaceWatchEvent{{Epoch: epoch, Sequence: 1}, {Epoch: epoch, Sequence: 2}},
	}
	stream, err := NewSubscriber(journal, SubscriberConfig{BufferSize: 1, BackpressureTimeout: 10 * time.Millisecond}).Subscribe(context.Background(), EncodeCursor(epoch, 0))
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	select {
	case terminal := <-stream.Errors:
		assertTerminalReason(t, terminal, CodeExpired, ReasonSubscriberOverflow)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for overflow")
	}
}

func TestAsTerminalUnwrapsWrappedErrors(t *testing.T) {
	t.Parallel()
	want := expired(ReasonReplayLimit, errors.New("too many"))
	terminal, ok := AsTerminal(fmt.Errorf("delivery: %w", want))
	if !ok || terminal.Reason != ReasonReplayLimit {
		t.Fatalf("AsTerminal() = (%v, %v), want replay terminal", terminal, ok)
	}
}

func TestUnavailableHasStableReason(t *testing.T) {
	t.Parallel()
	assertTerminalReason(t, unavailable(errors.New("not ready")), CodeUnavailable, ReasonMaterializerNotReady)
}
