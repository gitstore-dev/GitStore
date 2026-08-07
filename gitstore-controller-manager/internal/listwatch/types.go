// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package listwatch implements a generic list-then-watch bootstrap and
// resume loop that populates a per-kind informer cache and drives the
// controller manager's work queue, checkpointing its resourceVersion cursor
// so a restart can resume without a full re-list.
package listwatch

import "errors"

// EventType identifies the kind of change a WatchEvent carries.
type EventType int

const (
	// Added indicates a new resource was created.
	Added EventType = iota
	// Modified indicates an existing resource changed.
	Modified
	// Deleted indicates a resource was removed.
	Deleted
	// Bookmark carries only a resourceVersion cursor update; Object is the
	// zero value and MUST NOT be interpreted.
	Bookmark
)

// WatchEvent is a single streaming change notification delivered by a
// Watcher after a List. Every variant carries ResourceVersion; Bookmark
// carries only the version.
type WatchEvent[T any] struct {
	Type            EventType
	Object          T // zero value when Type == Bookmark
	ResourceVersion string
}

// ListResponse is the result of a full list request for a given kind: a
// snapshot of all current resources and the resourceVersion at the time of
// the snapshot.
type ListResponse[T any] struct {
	Items           []T
	ResourceVersion string
}

// ErrWatchExpired signals that a requested watch cursor is no longer
// available because the event log has been compacted. Watcher.Err() MUST
// satisfy errors.Is(err, ErrWatchExpired) when this condition occurs.
var ErrWatchExpired = errors.New("watch cursor expired: event log compacted")
