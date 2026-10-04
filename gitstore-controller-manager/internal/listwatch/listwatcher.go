// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

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

// PagedListWatcher applies backpressure before fetching the next bounded page.
// A failed visit must abort listing without publishing its returned cursor.
type PagedListWatcher[T any] interface {
	ListPages(context.Context, func([]T) error) (string, error)
	Watch(context.Context, string) (Watcher[T], error)
}

func collectList[T any](ctx context.Context, lw PagedListWatcher[T]) (ListResponse[T], error) {
	var result ListResponse[T]
	cursor, err := lw.ListPages(ctx, func(page []T) error {
		result.Items = append(result.Items, page...)
		return nil
	})
	if err != nil {
		return ListResponse[T]{}, err
	}
	result.ResourceVersion = cursor
	return result, nil
}

func bootstrapCursor[T any](ctx context.Context, lw PagedListWatcher[T], sentinel string) (string, error) {
	w, err := lw.Watch(ctx, sentinel)
	if err != nil {
		return "", fmt.Errorf("listwatch: establish watch cursor: %w", err)
	}
	defer w.Stop()
	select {
	case event, ok := <-w.Events():
		if !ok {
			return "", errors.Join(errors.New("listwatch: watch closed before bootstrap bookmark"), w.Err())
		}
		if event.Type != Bookmark || event.ResourceVersion == "" {
			return "", errors.New("listwatch: watch did not return a bootstrap bookmark")
		}
		return event.ResourceVersion, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type watchStop struct {
	once sync.Once
	done chan struct{}
	stop func()
}

func newWatchStop(stop func()) *watchStop {
	return &watchStop{done: make(chan struct{}), stop: stop}
}

func (w *watchStop) Stop() {
	w.once.Do(func() {
		close(w.done)
		w.stop()
	})
}

func sendWatchEvent[T any](w *watchStop, events chan<- WatchEvent[T], event WatchEvent[T]) bool {
	select {
	case <-w.done:
		return false
	case events <- event:
		return true
	}
}

func nextListCursor(hasNext bool, next, previous *string) (*string, error) {
	if !hasNext {
		return nil, nil
	}
	if next == nil || *next == "" || previous != nil && *previous == *next {
		return nil, errors.New("listwatch: pagination did not advance")
	}
	return next, nil
}
