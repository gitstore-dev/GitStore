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
	CursorVersion        = "nwv1"
	BootstrapCursor      = "__namespace_watch_bootstrap__"
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

var ErrIncompatibleCursor = errors.New("incompatible Namespace watch cursor")

// Cursor is the parsed Namespace journal cursor.
type Cursor struct {
	Epoch    string
	Sequence uint64
}

// Store is the backend-neutral Namespace journal contract.
type Store = datastore.NamespaceWatchJournal

// CursorString returns the opaque external representation.
func CursorString(cursor Cursor) string { return EncodeCursor(cursor.Epoch, cursor.Sequence) }

// EncodeCursor creates a versioned, kind-specific opaque cursor.
func EncodeCursor(epoch string, sequence uint64) string {
	return CursorVersion + ":" + epoch + ":" + strconv.FormatUint(sequence, 36)
}

// ParseCursor validates and decodes a Namespace cursor.
func ParseCursor(raw string) (Cursor, error) {
	parts := strings.Split(raw, ":")
	if len(parts) == 3 && parts[0] != CursorVersion {
		return Cursor{}, fmt.Errorf("%w: version %q", ErrIncompatibleCursor, parts[0])
	}
	if len(parts) != 3 {
		return Cursor{}, fmt.Errorf("invalid Namespace watch cursor")
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return Cursor{}, fmt.Errorf("invalid Namespace watch cursor epoch: %w", err)
	}
	if parts[2] == "" || strings.HasPrefix(parts[2], "-") {
		return Cursor{}, fmt.Errorf("invalid Namespace watch cursor sequence")
	}
	sequence, err := strconv.ParseUint(parts[2], 36, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("invalid Namespace watch cursor sequence: %w", err)
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
	Append(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceWatchEvent, time.Duration) (datastore.NamespaceWatchEvent, error)
	SaveProgress(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceCDCProgress) error
}

// LeaseStore is the fenced ownership subset used by LeaseManager.
type LeaseStore interface {
	AcquireLease(context.Context, string, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error)
	RenewLease(context.Context, datastore.NamespaceWatchLease, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error)
	ReleaseLease(context.Context, datastore.NamespaceWatchLease) error
}
