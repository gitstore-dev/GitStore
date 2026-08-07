// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import "context"

// Watcher is an open watch stream for a single kind.
type Watcher[T any] interface {
	// Events delivers watch notifications. Events for a single resource key
	// are delivered in resourceVersion order; events for different keys may
	// interleave. The channel is closed when the watch ends (error, expiry,
	// or Stop() called).
	Events() <-chan WatchEvent[T]

	// Err returns the reason Events() closed. Valid only after the channel
	// is closed. errors.Is(Err(), ErrWatchExpired) signals a compacted
	// cursor; any other non-nil value (or nil, e.g. a clean Stop()) signals
	// a transient/ordinary close.
	Err() error

	// Stop ends the watch. Safe to call multiple times. MUST cause Events()
	// to close if not already closed.
	Stop()
}

// ListWatcher is the transport abstraction a Runner depends on to bootstrap
// and resume a kind's cache. No concrete implementation ships in this
// package — production wiring is provided by whichever spec introduces the
// first concrete resource kind.
type ListWatcher[T any] interface {
	// List returns a full snapshot. Implementations do not retry internally
	// — the caller retries with exponential backoff on error.
	List(ctx context.Context) (ListResponse[T], error)

	// Watch opens a stream starting after resourceVersion.
	Watch(ctx context.Context, resourceVersion string) (Watcher[T], error)
}
