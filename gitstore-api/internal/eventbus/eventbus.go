// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package eventbus provides an in-process, per-kind change-notification
// fan-out from resource admission to GraphQL subscription resolvers. It is
// intentionally in-memory only (no durability across a process restart) —
// see specs/040-controller-watch-status-api/research.md R2/R3.
package eventbus

import (
	"errors"
	"sync"
)

// EventType identifies the kind of change an Event carries.
type EventType int

const (
	Added EventType = iota
	Modified
	Deleted
)

// Event is a single change notification published after a successful
// admission (create/update/delete) of a resource.
type Event struct {
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
	writeIdx := (kb.start + kb.size) % len(kb.buf)
	if kb.size == len(kb.buf) {
		// Full: overwrite the oldest slot and advance start (evict oldest).
		kb.buf[kb.start] = ev
		kb.start = (kb.start + 1) % len(kb.buf)
	} else {
		kb.buf[writeIdx] = ev
		kb.size++
	}
	subs := make([]*subscriber, 0, len(kb.subscribers))
	for s := range kb.subscribers {
		subs = append(subs, s)
	}
	kb.mu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			// Slow subscriber: drop rather than block Publish. The
			// subscriber's next resume will detect the gap via
			// ErrWatchExpired once its cursor falls outside the
			// retained window.
		}
	}
}

// Subscribe opens a subscription for kind, replaying any retained events
// with a resourceVersion strictly after resourceVersion (opaque token,
// compared only for equality/position within the ring, never ordered).
// An empty resourceVersion skips replay and only delivers future events.
// If resourceVersion is non-empty and not found in the retained window,
// Subscribe returns ErrWatchExpired.
func (b *Bus) Subscribe(kind string, resourceVersion string) (<-chan Event, func(), error) {
	kb := b.bufferFor(kind)

	kb.mu.Lock()
	defer kb.mu.Unlock()

	retained := kb.retainedLocked()

	var replay []Event
	if resourceVersion == "" {
		// No replay -- only future events.
	} else {
		idx := -1
		for i, e := range retained {
			if e.ResourceVersion == resourceVersion {
				idx = i
				break
			}
		}
		if idx == -1 {
			return nil, nil, ErrWatchExpired
		}
		replay = retained[idx+1:]
	}

	s := &subscriber{ch: make(chan Event, 64)}
	kb.subscribers[s] = struct{}{}

	for _, e := range replay {
		s.ch <- e
	}

	unsubscribe := func() {
		kb.mu.Lock()
		delete(kb.subscribers, s)
		kb.mu.Unlock()
		close(s.ch)
	}

	return s.ch, unsubscribe, nil
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
