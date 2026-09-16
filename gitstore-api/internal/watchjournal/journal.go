// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
)

const (
	// CursorVersion deliberately describes the journal rather than a resource
	// kind. A cursor can therefore be replayed through any typed projection.
	CursorVersion        = "rwv1"
	BootstrapCursor      = "__resource_watch_bootstrap__"
	DefaultBucketSize    = 4096
	DefaultReadBatchSize = 256
	DefaultMaxReplay     = 100000
	DefaultBufferSize    = 64
)

// Reason is a bounded terminal continuity-failure classification.
type Reason string

const (
	ReasonRetentionExpired     Reason = "RETENTION_EXPIRED"
	ReasonEpochMismatch        Reason = "EPOCH_MISMATCH"
	ReasonIncompatibleCursor   Reason = "INCOMPATIBLE_CURSOR"
	ReasonInvalidCursor        Reason = "INVALID_CURSOR"
	ReasonReplayLimit          Reason = "REPLAY_LIMIT"
	ReasonSubscriberOverflow   Reason = "SUBSCRIBER_OVERFLOW"
	ReasonJournalDiscontinuity Reason = "JOURNAL_DISCONTINUITY"
	ReasonMaterializerNotReady Reason = "MATERIALIZER_NOT_READY"
)

var ErrIncompatibleCursor = errors.New("incompatible resource watch cursor")

// Cursor is the parsed generic resource journal cursor.
type Cursor struct {
	Epoch    string
	Sequence uint64
}

// Store is the backend-neutral shared resource journal contract.
type Store = datastore.ResourceWatchJournal

// CursorString returns the opaque external representation.
func CursorString(cursor Cursor) string { return EncodeCursor(cursor.Epoch, cursor.Sequence) }

// EncodeCursor creates a versioned, journal-wide opaque cursor.
func EncodeCursor(epoch string, sequence uint64) string {
	return CursorVersion + ":" + epoch + ":" + strconv.FormatUint(sequence, 36)
}

// ParseCursor validates and decodes a resource journal cursor.
func ParseCursor(raw string) (Cursor, error) {
	parts := strings.Split(raw, ":")
	if len(parts) == 3 && parts[0] != CursorVersion {
		return Cursor{}, fmt.Errorf("%w: version %q", ErrIncompatibleCursor, parts[0])
	}
	if len(parts) != 3 {
		return Cursor{}, fmt.Errorf("invalid resource watch cursor")
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return Cursor{}, fmt.Errorf("invalid resource watch cursor epoch: %w", err)
	}
	if parts[2] == "" || strings.HasPrefix(parts[2], "-") {
		return Cursor{}, fmt.Errorf("invalid resource watch cursor sequence")
	}
	sequence, err := strconv.ParseUint(parts[2], 36, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("invalid resource watch cursor sequence: %w", err)
	}
	return Cursor{Epoch: parts[1], Sequence: sequence}, nil
}

func (c Cursor) String() string { return EncodeCursor(c.Epoch, c.Sequence) }

func (c Cursor) After(other Cursor) bool {
	return c.Epoch == other.Epoch && c.Sequence > other.Sequence
}

// Clock is the time dependency used by leases, materialization, and polling.
type Clock interface{ Now() time.Time }

// MaterializerStore is the ordered write subset used by Materializer.
type MaterializerStore interface {
	Append(context.Context, datastore.ResourceWatchLease, datastore.ResourceWatchEvent, time.Duration) (datastore.ResourceWatchEvent, error)
	SaveProgress(context.Context, datastore.ResourceWatchLease, datastore.ResourceCDCProgress) error
}

// LeaseStore is the fenced ownership subset used by LeaseManager.
type LeaseStore interface {
	AcquireLease(context.Context, string, time.Time, time.Duration) (datastore.ResourceWatchLease, bool, error)
	RenewLease(context.Context, datastore.ResourceWatchLease, time.Time, time.Duration) (datastore.ResourceWatchLease, bool, error)
	ReleaseLease(context.Context, datastore.ResourceWatchLease) error
}
