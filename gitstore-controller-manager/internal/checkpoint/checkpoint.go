// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package checkpoint provides pluggable, atomic per-kind persistence of the
// last-processed resourceVersion so a controller can resume a watch stream
// after a restart without re-listing every resource.
package checkpoint

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// Record contains everything required to resume a kind without re-listing.
// Snapshot rebuilds the volatile informer cache, while ReplayKeys preserves
// work that has not completed, including deletion tombstones, across a crash.
type Record struct {
	Kind            string
	ResourceVersion string
	Snapshot        json.RawMessage
	ReplayKeys      []types.WorkItemKey
	WrittenAt       time.Time
}

// Store is the storage abstraction responsible for reading and writing
// checkpoint Records. Writes MUST be atomic: a concurrent Load for the same
// kind observes either the fully-written new Record or the previous one —
// never a partial write.
type Store interface {
	// Load returns the persisted Record for kind. Returns a non-nil error if
	// no record exists, the entry cannot be read, or its contents cannot be
	// parsed. Callers MUST treat every error identically — there is no
	// distinction between "missing" and "corrupt".
	Load(ctx context.Context, kind string) (Record, error)

	// Save persists rec, keyed by rec.Kind.
	Save(ctx context.Context, rec Record) error
}
