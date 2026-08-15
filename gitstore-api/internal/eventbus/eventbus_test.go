// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package eventbus_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/stretchr/testify/require"
)

func mustReceive(t *testing.T, ch <-chan eventbus.Event) eventbus.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return eventbus.Event{}
	}
}

func requireNoEvent(t *testing.T, ch <-chan eventbus.Event) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("expected no event, got %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPublishSubscribe_DeliversInOrder(t *testing.T) {
	bus := eventbus.New(10)

	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "")
	require.NoError(t, err)
	defer unsubscribe()

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: "1"})
	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: "2"})
	bus.Publish(eventbus.Event{Type: eventbus.Deleted, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: "3"})

	e1 := mustReceive(t, events)
	e2 := mustReceive(t, events)
	e3 := mustReceive(t, events)

	require.Equal(t, eventbus.Added, e1.Type)
	require.Equal(t, "1", e1.ResourceVersion)
	require.Equal(t, eventbus.Modified, e2.Type)
	require.Equal(t, "2", e2.ResourceVersion)
	require.Equal(t, eventbus.Deleted, e3.Type)
	require.Equal(t, "3", e3.ResourceVersion)
}

func TestSubscribe_PerKindIsolation(t *testing.T) {
	bus := eventbus.New(10)

	catEvents, unsubCat, err := bus.Subscribe("CategoryTaxonomy", "")
	require.NoError(t, err)
	defer unsubCat()

	prodEvents, unsubProd, err := bus.Subscribe("Product", "")
	require.NoError(t, err)
	defer unsubProd()

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: "1"})

	e := mustReceive(t, catEvents)
	require.Equal(t, "CategoryTaxonomy", e.Kind)

	requireNoEvent(t, prodEvents)
}

func TestSubscribe_ResumeFromValidCursor(t *testing.T) {
	bus := eventbus.New(10)

	for i := 1; i <= 3; i++ {
		bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: fmt.Sprintf("%d", i)})
	}

	// Resume after resourceVersion "2" -- only "3" should be replayed, not "1" or "2".
	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "2")
	require.NoError(t, err)
	defer unsubscribe()

	e := mustReceive(t, events)
	require.Equal(t, "3", e.ResourceVersion)
	requireNoEvent(t, events)
}

func TestSubscribe_ResumesUsingUniqueEventCursor(t *testing.T) {
	bus := eventbus.New(10)
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Name: "a", ResourceVersion: "1"})
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Name: "b", ResourceVersion: "1"})

	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "1")
	require.NoError(t, err)
	defer unsubscribe()
	e := mustReceive(t, events)
	require.Equal(t, "2", e.Cursor)
	require.Equal(t, "b", e.Name)
}

func TestSubscribe_ReplayLargerThanLiveBufferDoesNotBlock(t *testing.T) {
	bus := eventbus.New(1000)
	for i := 0; i < 1000; i++ {
		bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: fmt.Sprintf("c-%d", i), ResourceVersion: "1"})
	}
	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "1")
	require.NoError(t, err)
	defer unsubscribe()
	require.Equal(t, "2", mustReceive(t, events).Cursor)
}

func TestPublish_ExpiresSlowSubscriber(t *testing.T) {
	bus := eventbus.New(100)
	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "")
	require.NoError(t, err)
	defer unsubscribe()
	for i := 0; i < 65; i++ {
		bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: fmt.Sprintf("c-%d", i), ResourceVersion: "1"})
	}
	for range events {
	}
}

func TestSubscribe_EmptyCursorSkipsReplay(t *testing.T) {
	bus := eventbus.New(10)

	for i := 1; i <= 3; i++ {
		bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: fmt.Sprintf("%d", i)})
	}

	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "")
	require.NoError(t, err)
	defer unsubscribe()

	requireNoEvent(t, events)

	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: "4"})
	e := mustReceive(t, events)
	require.Equal(t, "4", e.ResourceVersion)
}

func TestSubscribeWithCursor_ReturnsCursorAtRegistration(t *testing.T) {
	bus := eventbus.New(10)
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "Product", Name: "widget"})

	events, unsubscribe, cursor, err := bus.SubscribeWithCursor("Product", "")
	require.NoError(t, err)
	defer unsubscribe()
	require.Equal(t, "1", cursor)

	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "Product", Name: "widget"})
	require.Equal(t, "2", mustReceive(t, events).Cursor)
}

func TestSubscribe_ZeroCursorReplaysFromBeginning(t *testing.T) {
	bus := eventbus.New(10)
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "Product", Name: "a"})
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "Product", Name: "b"})

	events, unsubscribe, err := bus.Subscribe("Product", "0")
	require.NoError(t, err)
	defer unsubscribe()
	require.Equal(t, "1", mustReceive(t, events).Cursor)
	require.Equal(t, "2", mustReceive(t, events).Cursor)
}

func TestSubscribe_ExpiredCursorReturnsErrWatchExpired(t *testing.T) {
	bus := eventbus.New(2) // small ring buffer to force eviction

	for i := 1; i <= 5; i++ {
		bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: fmt.Sprintf("%d", i)})
	}
	// Buffer capacity 2 retains only resourceVersion "4" and "5" now.

	_, _, err := bus.Subscribe("CategoryTaxonomy", "1")
	require.Error(t, err)
	require.True(t, errors.Is(err, eventbus.ErrWatchExpired))
}

func TestSubscribe_UnknownCursorOnEmptyBufferReturnsErrWatchExpired(t *testing.T) {
	bus := eventbus.New(10)

	_, _, err := bus.Subscribe("CategoryTaxonomy", "999")
	require.Error(t, err)
	require.True(t, errors.Is(err, eventbus.ErrWatchExpired))
}

func TestUnsubscribe_ClosesChannel(t *testing.T) {
	bus := eventbus.New(10)

	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "")
	require.NoError(t, err)

	unsubscribe()

	_, ok := <-events
	require.False(t, ok, "channel should be closed after unsubscribe")
}

func TestPublish_RingBufferEviction(t *testing.T) {
	bus := eventbus.New(2)

	for i := 1; i <= 5; i++ {
		bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Name: "electronics", ResourceVersion: fmt.Sprintf("%d", i)})
	}

	// Only the last 2 events ("4", "5") should be resumable from.
	events, unsubscribe, err := bus.Subscribe("CategoryTaxonomy", "4")
	require.NoError(t, err)
	defer unsubscribe()

	e := mustReceive(t, events)
	require.Equal(t, "5", e.ResourceVersion)
}
