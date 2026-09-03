// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package eventbus provides an in-process, per-kind change-notification
// fan-out from resource admission to GraphQL subscription resolvers. It is
// intentionally in-memory only (no durability across a process restart) —
// see specs/040-controller-watch-status-api/research.md R2/R3.
package eventbus

import (
	"errors"
	"fmt"
	"sync"
)

// EventType identifies the kind of change an Event carries.
type EventType int

const (
	Added EventType = iota
	Modified
	Deleted
	// Bookmark marks a synthetic cursor-only event with no real resource
	// change (e.g. the bootstrap cursor SubscribeWithCursor returns before
	// any real events exist). It must not be the enum's zero value —
	// EventType's zero value is Added — otherwise a caller that forgets to
	// set Type on a synthetic Event would silently be treated as a real
	// Added event by any switch keyed on EventType.
	Bookmark
)

// Event is a single change notification published after a successful
// admission (create/update/delete) of a resource.
type Event struct {
	// Cursor is a bus-assigned, per-kind monotonic event cursor used only for
	// resumable watches. ResourceVersion remains the resource's optimistic
	// concurrency version and is not unique across resources.
	Cursor          string
	Type            EventType
	Kind            string
	Namespace       string
	Name            string
	ResourceVersion string
	Object          any
}

// ErrWatchExpired signals that a requested resourceVersion cursor is no
// longer available because it predates the retained ring-buffer window.
var ErrWatchExpired = errors.New("eventbus: watch cursor expired")

type subscriber struct {
	ch chan Event
}

// kindBuffer is a fixed-capacity circular buffer of the most recent events
// for one kind, plus the set of currently-open subscriptions for it.
type kindBuffer struct {
	mu          sync.Mutex
	buf         []Event // capacity-sized slice, used circularly
	start       int     // index of the oldest retained event in buf
	size        int     // number of events currently held (<= capacity)
	subscribers map[*subscriber]struct{}
	nextCursor  uint64
}

// Bus is a bounded, per-kind, in-memory publish-subscribe event bus.
type Bus struct {
	capacity int

	mu     sync.Mutex
	byKind map[string]*kindBuffer
}

// New returns a Bus that retains up to capacity events per kind.
func New(capacity int) *Bus {
	if capacity <= 0 {
		capacity = 1
	}
	return &Bus{capacity: capacity, byKind: make(map[string]*kindBuffer)}
}

func (b *Bus) bufferFor(kind string) *kindBuffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	kb, ok := b.byKind[kind]
	if !ok {
		kb = &kindBuffer{
			buf:         make([]Event, b.capacity),
			subscribers: make(map[*subscriber]struct{}),
		}
		b.byKind[kind] = kb
	}
	return kb
}

// Publish appends ev to its kind's ring buffer and fans it out to every
// current subscriber for that kind. Publish never blocks on a slow
// subscriber beyond a buffered channel send; subscribers are expected to
// drain promptly (matching the existing listwatch.Watcher[T] contract on
// the controller-manager side).
func (b *Bus) Publish(ev Event) {
	kb := b.bufferFor(ev.Kind)

	kb.mu.Lock()
	kb.nextCursor++
	ev.Cursor = fmt.Sprintf("%d", kb.nextCursor)
	writeIdx := (kb.start + kb.size) % len(kb.buf)
	if kb.size == len(kb.buf) {
		// Full: overwrite the oldest slot and advance start (evict oldest).
		kb.buf[kb.start] = ev
		kb.start = (kb.start + 1) % len(kb.buf)
	} else {
		kb.buf[writeIdx] = ev
		kb.size++
	}
	for s := range kb.subscribers {
		select {
		case s.ch <- ev:
		default:
			// A live gap is unrecoverable without a relist. Close the
			// stream rather than silently allowing a stale cache to advance.
			delete(kb.subscribers, s)
			close(s.ch)
			EventsDroppedTotal.WithLabelValues(ev.Kind).Inc()
		}
	}
	kb.mu.Unlock()
}

// Subscribe opens a subscription for kind, replaying any retained events
// with a resourceVersion strictly after resourceVersion (opaque token,
// compared only for equality/position within the ring, never ordered).
// An empty resourceVersion skips replay and only delivers future events.
// If resourceVersion is non-empty and not found in the retained window,
// Subscribe returns ErrWatchExpired.
func (b *Bus) Subscribe(kind string, resourceVersion string) (<-chan Event, func(), error) {
	ch, unsubscribe, _, err := b.SubscribeWithCursor(kind, resourceVersion)
	return ch, unsubscribe, err
}

// SubscribeWithCursor opens a subscription and returns the bus cursor at the
// instant the subscriber is registered. Callers can use that cursor as the
// lower bound for a subsequent snapshot, avoiding a gap between snapshot and
// watch establishment.
func (b *Bus) SubscribeWithCursor(kind string, resourceVersion string) (<-chan Event, func(), string, error) {
	kb := b.bufferFor(kind)

	kb.mu.Lock()
	defer kb.mu.Unlock()

	retained := kb.retainedLocked()

	var replay []Event
	if resourceVersion == "" {
		// No replay -- only future events.
	} else if resourceVersion == "0" {
		// "0" is the cursor immediately before the first event. It is valid
		// while the ring still retains the complete stream; once the ring has
		// wrapped, the caller must relist instead.
		if kb.nextCursor > uint64(len(kb.buf)) {
			WatchExpiredTotal.WithLabelValues(kind).Inc()
			return nil, nil, "", ErrWatchExpired
		}
		replay = retained
	} else {
		idx := -1
		for i, e := range retained {
			if e.Cursor == resourceVersion {
				idx = i
				break
			}
		}
		if idx == -1 {
			WatchExpiredTotal.WithLabelValues(kind).Inc()
			return nil, nil, "", ErrWatchExpired
		}
		replay = retained[idx+1:]
	}
	SubscriptionsOpenedTotal.WithLabelValues(kind, boolLabel(resourceVersion != "")).Inc()

	// Replay is delivered before Subscribe returns. Size the buffer for the
	// complete retained suffix so this cannot block while holding kb.mu.
	s := &subscriber{ch: make(chan Event, max(64, len(replay)))}
	kb.subscribers[s] = struct{}{}

	for _, e := range replay {
		s.ch <- e
	}

	unsubscribe := func() {
		kb.mu.Lock()
		_, subscribed := kb.subscribers[s]
		if subscribed {
			delete(kb.subscribers, s)
		}
		kb.mu.Unlock()
		if subscribed {
			close(s.ch)
		}
	}

	return s.ch, unsubscribe, fmt.Sprintf("%d", kb.nextCursor), nil
}

func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// retainedLocked returns the currently retained events for a kind, oldest
// first. Caller must hold kb.mu.
func (kb *kindBuffer) retainedLocked() []Event {
	if kb.size == 0 {
		return nil
	}
	out := make([]Event, kb.size)
	for i := 0; i < kb.size; i++ {
		out[i] = kb.buf[(kb.start+i)%len(kb.buf)]
	}
	return out
}
