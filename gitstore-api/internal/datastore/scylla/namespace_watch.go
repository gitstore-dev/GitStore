// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
)

const (
	namespaceWatchJournalName        = "namespace"
	namespaceWatchClockStream        = "__clock__"
	namespaceWatchRetentionScanLimit = 32
)

func (s *scyllaDatastore) NamespaceWatchJournal() datastore.NamespaceWatchJournal { return s }

type namespaceWatchClockRow struct {
	Epoch         gocql.UUID `db:"epoch"`
	HighWater     int64      `db:"high_water"`
	Oldest        int64      `db:"oldest"`
	UpdatedAt     time.Time  `db:"update_timestamp"`
	CDCProgressAt time.Time  `db:"cdc_progress_timestamp"`
	Holder        string     `db:"lease_holder"`
	FencingToken  int64      `db:"fencing_token"`
	ExpiresAt     time.Time  `db:"lease_expiration_timestamp"`
}

type namespaceWatchEventRow struct {
	Epoch            gocql.UUID        `db:"epoch"`
	Bucket           int64             `db:"bucket"`
	Sequence         int64             `db:"sequence"`
	EventType        string            `db:"event_type"`
	Name             string            `db:"name"`
	Payload          string            `db:"payload"`
	SelectorLabels   map[string]string `db:"labels"`
	DeduplicationKey string            `db:"deduplication_key"`
	FencingToken     int64             `db:"fencing_token"`
	EventAt          time.Time         `db:"event_timestamp"`
}

func (s *scyllaDatastore) ensureNamespaceWatchClock(ctx context.Context) (namespaceWatchClockRow, error) {
	epoch, err := gocql.RandomUUID()
	if err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: create Namespace watch epoch: %w", err)
	}
	zeroExpiry := time.Unix(0, 0).UTC()
	_, err = s.session.Query(
		"INSERT INTO namespace_watch_clock (journal,stream_id,epoch,high_water,oldest,update_timestamp,cdc_progress_timestamp,lease_holder,fencing_token,lease_expiration_timestamp) VALUES (?,?,?,?,?,?,?,?,?,?) IF NOT EXISTS",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName, namespaceWatchClockStream, epoch, int64(0), int64(0), time.Now().UTC(), zeroExpiry, "", int64(0), zeroExpiry).ExecCASRelease()
	if err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: initialize Namespace watch clock: %w", err)
	}
	var row namespaceWatchClockRow
	if err := s.session.Query(
		"SELECT epoch,high_water,oldest,update_timestamp,cdc_progress_timestamp,lease_holder,fencing_token,lease_expiration_timestamp FROM namespace_watch_clock WHERE journal=? LIMIT 1",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&row); err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: read Namespace watch clock: %w", err)
	}
	return row, nil
}

func (s *scyllaDatastore) Bounds(ctx context.Context) (datastore.NamespaceWatchBounds, error) {
	return s.namespaceWatchBounds(ctx, true)
}

func (s *scyllaDatastore) namespaceWatchBounds(ctx context.Context, refreshRetention bool) (datastore.NamespaceWatchBounds, error) {
	var row namespaceWatchClockRow
	err := s.session.Query(
		"SELECT epoch,high_water,oldest,update_timestamp,cdc_progress_timestamp FROM namespace_watch_clock WHERE journal=? LIMIT 1",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&row)
	if errors.Is(err, gocql.ErrNotFound) {
		return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: Namespace watch materializer has not initialized the journal")
	}
	if err != nil {
		return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: read Namespace watch clock: %w", err)
	}
	if refreshRetention {
		oldest, complete, oldestErr := s.namespaceWatchRetainedOldest(ctx, row, namespaceWatchRetentionScanLimit)
		if oldestErr != nil {
			return datastore.NamespaceWatchBounds{}, oldestErr
		}
		if oldest > row.Oldest {
			applied, updateErr := s.session.Query(
				"UPDATE namespace_watch_clock SET oldest=? WHERE journal=? IF epoch=? AND oldest=?",
				nil,
			).WithContext(ctx).Bind(oldest, namespaceWatchJournalName, row.Epoch, row.Oldest).ExecCASRelease()
			if updateErr != nil {
				return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: advance Namespace watch retained lower bound: %w", updateErr)
			}
			if !applied {
				var fresh namespaceWatchClockRow
				readErr := s.session.Query(
					"SELECT epoch,high_water,oldest,update_timestamp,cdc_progress_timestamp FROM namespace_watch_clock WHERE journal=? LIMIT 1",
					nil,
				).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&fresh)
				if readErr != nil {
					return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: reread Namespace watch retained lower bound: %w", readErr)
				}
				if fresh.Epoch != row.Epoch || fresh.Oldest < oldest {
					return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: advance Namespace watch retained lower bound: concurrent update did not reach %d", oldest)
				}
				row = fresh
			} else {
				row.Oldest = oldest
			}
		}
		if !complete {
			return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: Namespace watch retention reconciliation is incomplete")
		}
	}
	return datastore.NamespaceWatchBounds{
		Epoch: row.Epoch.String(), Oldest: uint64(maxInt64(row.Oldest, 0)), HighWater: uint64(maxInt64(row.HighWater, 0)),
		UpdatedAt: row.UpdatedAt, ProgressAt: row.CDCProgressAt,
	}, nil
}

// namespaceWatchRetainedOldest derives the actual lower bound from live TTL
// rows. Empty sequence buckets are skipped once and the static clock advances
// monotonically, so cleanup work is amortized instead of repeated per caller.
func (s *scyllaDatastore) namespaceWatchRetainedOldest(ctx context.Context, clock namespaceWatchClockRow, scanLimit int) (int64, bool, error) {
	if clock.HighWater <= 0 {
		return 0, true, nil
	}
	start := clock.Oldest
	if start < 1 {
		start = 1
	}
	bucketSize := s.namespaceWatchBucketSize
	if bucketSize <= 0 {
		bucketSize = int64(watchjournal.DefaultBucketSize)
	}
	if scanLimit < 1 {
		scanLimit = 1
	}
	scanned := 0
	for start <= clock.HighWater {
		if scanned == scanLimit {
			return start, false, nil
		}
		scanned++
		bucket := namespaceWatchBucket(uint64(start), bucketSize)
		bucketEnd := (bucket + 1) * bucketSize
		if bucketEnd > clock.HighWater {
			bucketEnd = clock.HighWater
		}
		var retained struct {
			Sequence int64 `db:"sequence"`
		}
		err := s.session.Query(
			"SELECT sequence FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence>=? AND sequence<=? LIMIT 1",
			nil,
		).WithContext(ctx).Bind(clock.Epoch, bucket, start, bucketEnd).GetRelease(&retained)
		if err == nil {
			return retained.Sequence, true, nil
		}
		if !errors.Is(err, gocql.ErrNotFound) {
			return 0, false, fmt.Errorf("scylla: derive Namespace watch retained lower bound: %w", err)
		}
		start = bucketEnd + 1
	}
	return clock.HighWater + 1, true, nil
}

func (s *scyllaDatastore) Append(ctx context.Context, lease datastore.NamespaceWatchLease, event datastore.NamespaceWatchEvent, ttl time.Duration) (datastore.NamespaceWatchEvent, error) {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	ttlSeconds := int(ttl / time.Second)
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	for attempts := 0; attempts < 32; attempts++ {
		clock, clockErr := s.ensureNamespaceWatchClock(ctx)
		if clockErr != nil {
			return datastore.NamespaceWatchEvent{}, clockErr
		}
		if !namespaceWatchLeaseMatches(clock, lease, time.Now()) {
			return datastore.NamespaceWatchEvent{}, datastore.ErrStaleWatchLease
		}
		next := clock.HighWater + 1
		candidate := event
		candidate.Epoch = clock.Epoch.String()
		candidate.Sequence = uint64(next)
		candidate.FencingToken = lease.FencingToken
		bucket := namespaceWatchBucket(candidate.Sequence, s.namespaceWatchBucketSize)
		inserted, insertErr := s.session.Query(
			"INSERT INTO namespace_watch_events (epoch,bucket,sequence,event_type,name,payload,labels,deduplication_key,fencing_token,event_timestamp) VALUES (?,?,?,?,?,?,?,?,?,?) IF NOT EXISTS USING TTL ?",
			nil,
		).WithContext(ctx).Bind(clock.Epoch, bucket, next, string(candidate.Type), candidate.Name, string(candidate.Payload), candidate.SelectorLabels, candidate.DeduplicationKey, int64(lease.FencingToken), candidate.At, ttlSeconds).ExecCASRelease()
		if insertErr != nil {
			return datastore.NamespaceWatchEvent{}, fmt.Errorf("scylla: append Namespace journal event: %w", insertErr)
		}
		if !inserted {
			existing, loadErr := s.namespaceWatchEvent(ctx, clock.Epoch, bucket, next)
			if loadErr != nil && !errors.Is(loadErr, gocql.ErrNotFound) {
				return datastore.NamespaceWatchEvent{}, loadErr
			}
			if uint64(existing.FencingToken) < lease.FencingToken {
				removed, removeErr := s.session.Query(
					"DELETE FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence=? IF fencing_token<?",
					nil,
				).WithContext(ctx).Bind(clock.Epoch, bucket, next, int64(lease.FencingToken)).ExecCASRelease()
				if removeErr != nil {
					return datastore.NamespaceWatchEvent{}, fmt.Errorf("scylla: remove stale Namespace journal event: %w", removeErr)
				}
				if removed {
					continue
				}
			}
			if uint64(existing.FencingToken) > lease.FencingToken {
				return datastore.NamespaceWatchEvent{}, datastore.ErrStaleWatchLease
			}
			if existing.DeduplicationKey != "" && existing.DeduplicationKey == candidate.DeduplicationKey {
				published, publishErr := s.publishNamespaceWatchSequence(ctx, clock, lease, next, existing.At)
				if publishErr != nil {
					return datastore.NamespaceWatchEvent{}, publishErr
				}
				if published {
					return existing, nil
				}
			}
			continue
		}

		applied, casErr := s.publishNamespaceWatchSequence(ctx, clock, lease, next, candidate.At)
		if casErr != nil {
			return datastore.NamespaceWatchEvent{}, fmt.Errorf("scylla: publish Namespace journal sequence: %w", casErr)
		}
		if !applied {
			latest, latestErr := s.ensureNamespaceWatchClock(ctx)
			if latestErr != nil {
				return datastore.NamespaceWatchEvent{}, latestErr
			}
			if !namespaceWatchLeaseMatches(latest, lease, time.Now()) {
				return datastore.NamespaceWatchEvent{}, datastore.ErrStaleWatchLease
			}
			if latest.Epoch == clock.Epoch && latest.HighWater >= next {
				existing, loadErr := s.namespaceWatchEvent(ctx, clock.Epoch, bucket, next)
				if loadErr != nil {
					return datastore.NamespaceWatchEvent{}, loadErr
				}
				if existing.DeduplicationKey == candidate.DeduplicationKey {
					return existing, nil
				}
			}
			continue
		}
		if candidate.Sequence == 1 {
			_ = s.session.Query("UPDATE namespace_watch_clock SET oldest=? WHERE journal=?", nil).
				WithContext(ctx).Bind(int64(1), namespaceWatchJournalName).ExecRelease()
		}
		return candidate, nil
	}
	return datastore.NamespaceWatchEvent{}, fmt.Errorf("scylla: append Namespace journal event: contention limit reached")
}

func (s *scyllaDatastore) publishNamespaceWatchSequence(ctx context.Context, clock namespaceWatchClockRow, lease datastore.NamespaceWatchLease, next int64, at time.Time) (bool, error) {
	applied, err := s.session.Query(
		"UPDATE namespace_watch_clock SET high_water=?,update_timestamp=? WHERE journal=? IF epoch=? AND high_water=? AND lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?",
		nil,
	).WithContext(ctx).Bind(next, at, namespaceWatchJournalName, clock.Epoch, clock.HighWater, lease.Holder, int64(lease.FencingToken), time.Now().UTC()).ExecCASRelease()
	if err != nil {
		return false, fmt.Errorf("scylla: publish Namespace journal sequence: %w", err)
	}
	return applied, nil
}

func (s *scyllaDatastore) namespaceWatchEvent(ctx context.Context, epoch gocql.UUID, bucket, sequence int64) (datastore.NamespaceWatchEvent, error) {
	var row namespaceWatchEventRow
	err := s.session.Query(
		"SELECT epoch,bucket,sequence,event_type,name,payload,labels,deduplication_key,fencing_token,event_timestamp FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence=?",
		nil,
	).WithContext(ctx).Bind(epoch, bucket, sequence).GetRelease(&row)
	if err != nil {
		return datastore.NamespaceWatchEvent{}, err
	}
	return datastore.NamespaceWatchEvent{
		Epoch: row.Epoch.String(), Sequence: uint64(row.Sequence), Type: datastore.NamespaceWatchEventType(row.EventType),
		Name: row.Name, Payload: []byte(row.Payload), SelectorLabels: row.SelectorLabels,
		DeduplicationKey: row.DeduplicationKey, FencingToken: uint64(row.FencingToken), At: row.EventAt,
	}, nil
}

func (s *scyllaDatastore) ReadAfter(ctx context.Context, cursor datastore.NamespaceWatchCursor, limit int) ([]datastore.NamespaceWatchEvent, error) {
	// Retention is refreshed when a subscription validates its initial cursor
	// through Bounds. Tail polling only needs the clock, avoiding an extra
	// retained-row lookup for every idle subscriber poll.
	bounds, err := s.namespaceWatchBounds(ctx, false)
	if err != nil {
		return nil, err
	}
	if cursor.Epoch != "" && cursor.Epoch != bounds.Epoch {
		return nil, datastore.ErrWatchCursorEpoch
	}
	if bounds.Oldest > 0 && cursor.Sequence+1 < bounds.Oldest {
		return nil, datastore.ErrWatchRetentionExpired
	}
	if limit <= 0 || limit > 256 {
		limit = 256
	}
	epoch, err := gocql.ParseUUID(bounds.Epoch)
	if err != nil {
		return nil, fmt.Errorf("scylla: parse Namespace journal epoch: %w", err)
	}
	start := cursor.Sequence + 1
	if start == 0 {
		start = 1
	}
	out := make([]datastore.NamespaceWatchEvent, 0, limit)
	for start <= bounds.HighWater && len(out) < limit {
		bucketSize := s.namespaceWatchBucketSize
		if bucketSize <= 0 {
			bucketSize = int64(watchjournal.DefaultBucketSize)
		}
		bucket := namespaceWatchBucket(start, bucketSize)
		bucketEnd := uint64(bucket+1) * uint64(bucketSize)
		if bucketEnd > bounds.HighWater {
			bucketEnd = bounds.HighWater
		}
		var rows []namespaceWatchEventRow
		err = s.session.Query(
			"SELECT epoch,bucket,sequence,event_type,name,payload,labels,deduplication_key,fencing_token,event_timestamp FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence>=? AND sequence<=? LIMIT ?",
			nil,
		).WithContext(ctx).Bind(epoch, bucket, int64(start), int64(bucketEnd), limit-len(out)).SelectRelease(&rows)
		if err != nil {
			return nil, fmt.Errorf("scylla: read Namespace journal events: %w", err)
		}
		for _, row := range rows {
			out = append(out, datastore.NamespaceWatchEvent{
				Epoch: row.Epoch.String(), Sequence: uint64(row.Sequence), Type: datastore.NamespaceWatchEventType(row.EventType),
				Name: row.Name, Payload: []byte(row.Payload), SelectorLabels: row.SelectorLabels,
				DeduplicationKey: row.DeduplicationKey, FencingToken: uint64(row.FencingToken), At: row.EventAt,
			})
		}
		start = bucketEnd + 1
	}
	if cursor.Sequence < bounds.HighWater && (len(out) == 0 || out[0].Sequence != cursor.Sequence+1) {
		return nil, datastore.ErrWatchRetentionExpired
	}
	return out, nil
}

func (s *scyllaDatastore) AcquireLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	current, err := s.ensureNamespaceWatchClock(ctx)
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, err
	}
	if current.Holder != "" && now.Before(current.ExpiresAt) {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	expires := now.Add(ttl)
	next := current.FencingToken + 1
	applied, err := s.session.Query(
		"UPDATE namespace_watch_clock SET lease_holder=?,fencing_token=?,lease_expiration_timestamp=? WHERE journal=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp=?",
		nil,
	).WithContext(ctx).Bind(holder, next, expires, namespaceWatchJournalName, current.Holder, current.FencingToken, current.ExpiresAt).ExecCASRelease()
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("scylla: acquire Namespace materializer lease: %w", err)
	}
	return datastore.NamespaceWatchLease{Holder: holder, FencingToken: uint64(next), ExpiresAt: expires}, applied, nil
}

func (s *scyllaDatastore) RenewLease(ctx context.Context, lease datastore.NamespaceWatchLease, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	current, err := s.ensureNamespaceWatchClock(ctx)
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, err
	}
	if !namespaceWatchLeaseMatches(current, lease, now) {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	expires := now.Add(ttl)
	applied, err := s.session.Query(
		"UPDATE namespace_watch_clock SET lease_expiration_timestamp=? WHERE journal=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp=?",
		nil,
	).WithContext(ctx).Bind(expires, namespaceWatchJournalName, lease.Holder, int64(lease.FencingToken), current.ExpiresAt).ExecCASRelease()
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("scylla: renew Namespace materializer lease: %w", err)
	}
	lease.ExpiresAt = expires
	return lease, applied, nil
}

func (s *scyllaDatastore) ReleaseLease(ctx context.Context, lease datastore.NamespaceWatchLease) error {
	_, err := s.session.Query(
		"UPDATE namespace_watch_clock SET lease_holder=?,lease_expiration_timestamp=? WHERE journal=? IF lease_holder=? AND fencing_token=?",
		nil,
	).WithContext(ctx).Bind("", time.Unix(0, 0).UTC(), namespaceWatchJournalName, lease.Holder, int64(lease.FencingToken)).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: release Namespace materializer lease: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) LoadProgress(ctx context.Context, streamID string) (datastore.NamespaceCDCProgress, error) {
	var row struct {
		StreamID  string    `db:"stream_id"`
		Position  []byte    `db:"position"`
		UpdatedAt time.Time `db:"progress_update_timestamp"`
	}
	err := s.session.Query("SELECT stream_id,position,progress_update_timestamp FROM namespace_watch_clock WHERE journal=? AND stream_id=?", nil).
		WithContext(ctx).Bind(namespaceWatchJournalName, streamID).GetRelease(&row)
	if errors.Is(err, gocql.ErrNotFound) {
		return datastore.NamespaceCDCProgress{}, datastore.ErrNotFound
	}
	if err != nil {
		return datastore.NamespaceCDCProgress{}, fmt.Errorf("scylla: load Namespace CDC progress: %w", err)
	}
	return datastore.NamespaceCDCProgress{StreamID: row.StreamID, Position: row.Position, UpdatedAt: row.UpdatedAt}, nil
}

func (s *scyllaDatastore) SaveProgress(ctx context.Context, lease datastore.NamespaceWatchLease, progress datastore.NamespaceCDCProgress) error {
	if progress.StreamID == "" {
		return fmt.Errorf("%w: CDC stream id is required", datastore.ErrInvalidArgument)
	}
	query := "UPDATE namespace_watch_clock SET position=?,progress_update_timestamp=?,cdc_progress_timestamp=? WHERE journal=? AND stream_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?"
	values := []any{progress.Position, progress.UpdatedAt, progress.UpdatedAt, namespaceWatchJournalName, progress.StreamID, lease.Holder, int64(lease.FencingToken), time.Now().UTC()}
	if progress.StreamID == namespaceCDCGenerationProgress {
		query = "UPDATE namespace_watch_clock SET position=?,progress_update_timestamp=? WHERE journal=? AND stream_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?"
		values = []any{progress.Position, progress.UpdatedAt, namespaceWatchJournalName, progress.StreamID, lease.Holder, int64(lease.FencingToken), time.Now().UTC()}
	}
	applied, err := s.session.Query(query, nil).WithContext(ctx).Bind(values...).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: save Namespace CDC progress: %w", err)
	}
	if !applied {
		return datastore.ErrStaleWatchLease
	}
	return nil
}

func namespaceWatchBucket(sequence uint64, bucketSize int64) int64 {
	if bucketSize <= 0 {
		bucketSize = int64(watchjournal.DefaultBucketSize)
	}
	if sequence == 0 {
		return 0
	}
	return int64((sequence - 1) / uint64(bucketSize))
}

func namespaceWatchLeaseMatches(current namespaceWatchClockRow, lease datastore.NamespaceWatchLease, now time.Time) bool {
	return current.Holder == lease.Holder && uint64(current.FencingToken) == lease.FencingToken && now.Before(current.ExpiresAt)
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}
