// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3"
)

const (
	namespaceWatchJournalName        = "namespace"
	namespaceWatchClockStream        = "__clock__"
	namespaceWatchRetentionScanLimit = 32
	namespaceWatchBatchMaxStatements = 32
	namespaceWatchBatchMaxBytes      = 32 * 1024
	namespaceCDCProgressTTLSeconds   = 14 * 24 * 60 * 60
)

func (s *scyllaDatastore) NamespaceWatchJournal() datastore.NamespaceWatchJournal { return s }

type namespaceWatchClockRow struct {
	Epoch         gocql.UUID `db:"epoch"`
	HighWater     int64      `db:"high_water"`
	Oldest        int64      `db:"oldest"`
	BucketSize    int64      `db:"bucket_size"`
	UpdatedAt     time.Time  `db:"update_timestamp"`
	BookmarkAt    time.Time  `db:"bookmark_timestamp"`
	CDCProgressAt time.Time  `db:"cdc_progress_timestamp"`
	Holder        string     `db:"lease_holder"`
	FencingToken  int64      `db:"fencing_token"`
	ExpiresAt     time.Time  `db:"lease_expiration_timestamp"`
}

type namespaceWatchEventRow struct {
	Epoch                  gocql.UUID        `db:"epoch"`
	Bucket                 int64             `db:"bucket"`
	Sequence               int64             `db:"sequence"`
	EventType              string            `db:"event_type"`
	Name                   string            `db:"name"`
	Payload                string            `db:"payload"`
	SelectorLabels         map[string]string `db:"labels"`
	PreviousSelectorLabels map[string]string `db:"previous_labels"`
	DeduplicationKey       string            `db:"deduplication_key"`
	FencingToken           int64             `db:"fencing_token"`
	EventAt                time.Time         `db:"event_timestamp"`
}

func (s *scyllaDatastore) ensureNamespaceWatchClock(ctx context.Context) (namespaceWatchClockRow, error) {
	epoch, err := gocql.RandomUUID()
	if err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: create Namespace watch epoch: %w", err)
	}
	zeroExpiry := time.Unix(0, 0).UTC()
	_, err = s.session.Query(
		"INSERT INTO namespace_watch_clock (journal,stream_id,epoch,high_water,oldest,bucket_size,update_timestamp,bookmark_timestamp,cdc_progress_timestamp,lease_holder,fencing_token,lease_expiration_timestamp) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) IF NOT EXISTS",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName, namespaceWatchClockStream, epoch, int64(0), int64(0), s.namespaceWatchBucketSize, time.Now().UTC(), zeroExpiry, zeroExpiry, "", int64(0), zeroExpiry).ExecCASRelease()
	if err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: initialize Namespace watch clock: %w", err)
	}
	return s.loadNamespaceWatchClock(ctx)
}

func (s *scyllaDatastore) loadNamespaceWatchClock(ctx context.Context) (namespaceWatchClockRow, error) {
	var row namespaceWatchClockRow
	if err := s.session.Query(
		"SELECT epoch,high_water,oldest,bucket_size,update_timestamp,bookmark_timestamp,cdc_progress_timestamp,lease_holder,fencing_token,lease_expiration_timestamp FROM namespace_watch_clock WHERE journal=? LIMIT 1",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&row); err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: read Namespace watch clock: %w", err)
	}
	if err := s.ensureNamespaceWatchBucketSize(ctx, &row); err != nil {
		return namespaceWatchClockRow{}, err
	}
	return row, nil
}

func (s *scyllaDatastore) ensureNamespaceWatchBucketSize(ctx context.Context, row *namespaceWatchClockRow) error {
	if row.BucketSize == 0 {
		applied, err := s.session.Query(
			"UPDATE namespace_watch_clock SET bucket_size=? WHERE journal=? IF bucket_size=null",
			nil,
		).WithContext(ctx).Bind(s.namespaceWatchBucketSize, namespaceWatchJournalName).ExecCASRelease()
		if err != nil {
			return fmt.Errorf("scylla: initialize Namespace watch bucket size: %w", err)
		}
		if applied {
			row.BucketSize = s.namespaceWatchBucketSize
		} else if err := s.session.Query(
			"SELECT bucket_size FROM namespace_watch_clock WHERE journal=? LIMIT 1",
			nil,
		).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(row); err != nil {
			return fmt.Errorf("scylla: reread Namespace watch bucket size: %w", err)
		}
	}
	if row.BucketSize != s.namespaceWatchBucketSize {
		return fmt.Errorf("scylla: Namespace watch bucket size is %d, configured %d", row.BucketSize, s.namespaceWatchBucketSize)
	}
	return nil
}

func (s *scyllaDatastore) Bounds(ctx context.Context) (datastore.NamespaceWatchBounds, error) {
	return s.namespaceWatchBounds(ctx, true)
}

func (s *scyllaDatastore) namespaceWatchBounds(ctx context.Context, refreshRetention bool) (datastore.NamespaceWatchBounds, error) {
	var row namespaceWatchClockRow
	err := s.session.Query(
		"SELECT epoch,high_water,oldest,bucket_size,update_timestamp,bookmark_timestamp,cdc_progress_timestamp FROM namespace_watch_clock WHERE journal=? LIMIT 1",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&row)
	if errors.Is(err, gocql.ErrNotFound) {
		return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: Namespace watch materializer has not initialized the journal")
	}
	if err != nil {
		return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: read Namespace watch clock: %w", err)
	}
	if err := s.ensureNamespaceWatchBucketSize(ctx, &row); err != nil {
		return datastore.NamespaceWatchBounds{}, err
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
					"SELECT epoch,high_water,oldest,bucket_size,update_timestamp,bookmark_timestamp,cdc_progress_timestamp FROM namespace_watch_clock WHERE journal=? LIMIT 1",
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
		UpdatedAt: row.UpdatedAt, BookmarkAt: row.BookmarkAt, ProgressAt: row.CDCProgressAt,
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
		// Lease acquisition initializes the clock. Avoid repeating its
		// INSERT-IF-NOT-EXISTS LWT for every materialized event.
		clock, clockErr := s.loadNamespaceWatchClock(ctx)
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
			"INSERT INTO namespace_watch_events (epoch,bucket,sequence,event_type,name,payload,labels,previous_labels,deduplication_key,fencing_token,event_timestamp) VALUES (?,?,?,?,?,?,?,?,?,?,?) IF NOT EXISTS USING TTL ?",
			nil,
		).WithContext(ctx).Bind(clock.Epoch, bucket, next, string(candidate.Type), candidate.Name, string(candidate.Payload), candidate.SelectorLabels, candidate.PreviousSelectorLabels, candidate.DeduplicationKey, int64(lease.FencingToken), candidate.At, ttlSeconds).ExecCASRelease()
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
				published, publishErr := s.publishNamespaceWatchSequence(ctx, clock, lease, next, existing.Type, existing.At)
				if published {
					return existing, nil
				}
				resolved, resolveErr := s.resolveNamespaceWatchPublishedRange(ctx, clock, lease, []datastore.NamespaceWatchEvent{existing}, publishErr)
				if resolveErr != nil {
					return datastore.NamespaceWatchEvent{}, resolveErr
				}
				if resolved {
					return existing, nil
				}
			}
			continue
		}

		applied, casErr := s.publishNamespaceWatchSequence(ctx, clock, lease, next, candidate.Type, candidate.At)
		if !applied || casErr != nil {
			resolved, resolveErr := s.resolveNamespaceWatchPublishedRange(ctx, clock, lease, []datastore.NamespaceWatchEvent{candidate}, casErr)
			if resolveErr != nil {
				return datastore.NamespaceWatchEvent{}, resolveErr
			}
			if resolved {
				return candidate, nil
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

// AppendBatch amortizes the two journal LWTs across each ordered group that
// fits in one event bucket. The conditional event batch remains fenced against
// a stale leader; the clock CAS publishes the entire contiguous range only
// after every row is durable. Any ambiguous/contended path falls back to the
// single-event recovery logic, which is idempotent by deduplication key.
func (s *scyllaDatastore) AppendBatch(ctx context.Context, lease datastore.NamespaceWatchLease, events []datastore.NamespaceWatchEvent, ttl time.Duration) ([]datastore.NamespaceWatchEvent, error) {
	appended := make([]datastore.NamespaceWatchEvent, 0, len(events))
	for len(events) > 0 {
		clock, err := s.loadNamespaceWatchClock(ctx)
		if err != nil {
			return nil, err
		}
		if !namespaceWatchLeaseMatches(clock, lease, time.Now()) {
			return nil, datastore.ErrStaleWatchLease
		}
		first := clock.HighWater + 1
		bucket := namespaceWatchBucket(uint64(first), s.namespaceWatchBucketSize)
		count := namespaceWatchBatchCount(events, first, s.namespaceWatchBucketSize)
		chunk := events[:count]
		batch, candidates, ttlSeconds := s.namespaceWatchEventBatch(clock, lease, bucket, chunk, ttl)
		batch.Batch = batch.Batch.WithContext(ctx)
		applied, iter, batchErr := s.session.MapExecuteBatchCAS(batch, make(map[string]any))
		if iter != nil {
			if closeErr := iter.Close(); batchErr == nil {
				batchErr = closeErr
			}
		}
		if batchErr == nil && applied {
			last := first + int64(count) - 1
			published, publishErr := s.publishNamespaceWatchSequence(ctx, clock, lease, last, candidates[len(candidates)-1].Type, candidates[len(candidates)-1].At)
			if publishErr == nil && published {
				if first == 1 {
					_ = s.session.Query("UPDATE namespace_watch_clock SET oldest=? WHERE journal=?", nil).
						WithContext(ctx).Bind(int64(1), namespaceWatchJournalName).ExecRelease()
				}
				appended = append(appended, candidates...)
				events = events[count:]
				continue
			}
			resolved, resolveErr := s.resolveNamespaceWatchPublishedRange(ctx, clock, lease, candidates, publishErr)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if resolved {
				if first == 1 {
					_ = s.session.Query("UPDATE namespace_watch_clock SET oldest=? WHERE journal=?", nil).
						WithContext(ctx).Bind(int64(1), namespaceWatchJournalName).ExecRelease()
				}
				appended = append(appended, candidates...)
				events = events[count:]
				continue
			}
		}
		// The conditional batch is atomic. If it was already present, or if
		// publishing its range was ambiguous, the established per-event path
		// identifies matching rows and advances the clock without gaps.
		for _, event := range chunk {
			result, appendErr := s.Append(ctx, lease, event, time.Duration(ttlSeconds)*time.Second)
			if appendErr != nil {
				if batchErr != nil {
					return nil, fmt.Errorf("scylla: batch append failed (%v), recovery failed: %w", batchErr, appendErr)
				}
				return nil, appendErr
			}
			appended = append(appended, result)
		}
		events = events[count:]
	}
	return appended, nil
}

func namespaceWatchBatchCount(events []datastore.NamespaceWatchEvent, first, bucketSize int64) int {
	count, encodedBytes := 0, 0
	for count < len(events) && count < namespaceWatchBatchMaxStatements &&
		namespaceWatchBucket(uint64(first+int64(count)), bucketSize) == namespaceWatchBucket(uint64(first), bucketSize) {
		eventBytes := namespaceWatchEventEncodedBytes(events[count])
		if count > 0 && encodedBytes+eventBytes > namespaceWatchBatchMaxBytes {
			break
		}
		encodedBytes += eventBytes
		count++
	}
	return count
}

func namespaceWatchEventEncodedBytes(event datastore.NamespaceWatchEvent) int {
	// Include fixed CQL value/framing overhead conservatively in addition to
	// variable-width strings and maps. One oversized event is still attempted
	// alone; the cap prevents unrelated events from amplifying that request.
	size := 256 + len(event.Type) + len(event.Name) + len(event.Payload) + len(event.DeduplicationKey)
	for key, value := range event.SelectorLabels {
		size += len(key) + len(value) + 16
	}
	for key, value := range event.PreviousSelectorLabels {
		size += len(key) + len(value) + 16
	}
	return size
}

func (s *scyllaDatastore) resolveNamespaceWatchPublishedRange(
	ctx context.Context,
	clock namespaceWatchClockRow,
	lease datastore.NamespaceWatchLease,
	candidates []datastore.NamespaceWatchEvent,
	primary error,
) (bool, error) {
	return runNamespaceWatchPublicationResolution(ctx, clock, lease, candidates, primary,
		func(resolveCtx context.Context) (namespaceWatchClockRow, error) {
			var state namespaceWatchClockRow
			err := s.session.Query(
				"SELECT epoch,high_water,lease_holder,fencing_token,lease_expiration_timestamp FROM namespace_watch_clock WHERE journal=? AND stream_id=?",
				nil,
			).Consistency(gocql.LocalSerial).WithContext(resolveCtx).Bind(namespaceWatchJournalName, namespaceWatchClockStream).GetRelease(&state)
			return state, err
		},
		func(resolveCtx context.Context, candidate datastore.NamespaceWatchEvent) (datastore.NamespaceWatchEvent, error) {
			bucket := namespaceWatchBucket(candidate.Sequence, s.namespaceWatchBucketSize)
			return s.namespaceWatchEvent(resolveCtx, clock.Epoch, bucket, int64(candidate.Sequence))
		},
	)
}

func runNamespaceWatchPublicationResolution(
	ctx context.Context,
	clock namespaceWatchClockRow,
	lease datastore.NamespaceWatchLease,
	candidates []datastore.NamespaceWatchEvent,
	primary error,
	readClock func(context.Context) (namespaceWatchClockRow, error),
	readEvent func(context.Context, datastore.NamespaceWatchEvent) (datastore.NamespaceWatchEvent, error),
) (bool, error) {
	resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), namespaceProjectionCleanupTimeout)
	defer cancel()
	latest, err := readClock(resolveCtx)
	if err != nil {
		if primary != nil {
			return false, fmt.Errorf("%w; resolve ambiguous Namespace journal publication: %v", primary, err)
		}
		return false, fmt.Errorf("scylla: resolve Namespace journal publication: %w", err)
	}
	if len(candidates) == 0 {
		return false, fmt.Errorf("scylla: resolve Namespace journal publication: empty candidate range")
	}
	last := int64(candidates[len(candidates)-1].Sequence)
	if latest.Epoch != clock.Epoch {
		return false, datastore.ErrStaleWatchLease
	}
	if latest.HighWater < last {
		if latest.Holder != lease.Holder || uint64(latest.FencingToken) != lease.FencingToken {
			return false, datastore.ErrStaleWatchLease
		}
		return false, nil
	}
	for _, candidate := range candidates {
		existing, readErr := readEvent(resolveCtx, candidate)
		if readErr != nil {
			return false, fmt.Errorf("scylla: resolve Namespace journal publication at sequence %d: %w", candidate.Sequence, readErr)
		}
		if !namespaceWatchEventsEqual(existing, candidate) {
			return false, fmt.Errorf("scylla: Namespace journal sequence %d was published with a different event", candidate.Sequence)
		}
	}
	return true, nil
}

func namespaceWatchEventsEqual(left, right datastore.NamespaceWatchEvent) bool {
	return left.Epoch == right.Epoch &&
		left.Sequence == right.Sequence &&
		left.Type == right.Type &&
		left.Name == right.Name &&
		bytes.Equal(left.Payload, right.Payload) &&
		maps.Equal(left.SelectorLabels, right.SelectorLabels) &&
		maps.Equal(left.PreviousSelectorLabels, right.PreviousSelectorLabels) &&
		left.DeduplicationKey == right.DeduplicationKey &&
		left.FencingToken == right.FencingToken
}

func (s *scyllaDatastore) namespaceWatchEventBatch(clock namespaceWatchClockRow, lease datastore.NamespaceWatchLease, bucket int64, events []datastore.NamespaceWatchEvent, ttl time.Duration) (*gocqlx.Batch, []datastore.NamespaceWatchEvent, int) {
	ttlSeconds := max(1, int(ttl/time.Second))
	batch := s.session.Batch(gocql.LoggedBatch)
	candidates := make([]datastore.NamespaceWatchEvent, 0, len(events))
	for index, event := range events {
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		event.Epoch = clock.Epoch.String()
		event.Sequence = uint64(clock.HighWater + 1 + int64(index))
		event.FencingToken = lease.FencingToken
		batch.Entries = append(batch.Entries, gocql.BatchEntry{
			Stmt: "INSERT INTO namespace_watch_events (epoch,bucket,sequence,event_type,name,payload,labels,previous_labels,deduplication_key,fencing_token,event_timestamp) VALUES (?,?,?,?,?,?,?,?,?,?,?) IF NOT EXISTS USING TTL ?",
			Args: []any{clock.Epoch, bucket, int64(event.Sequence), string(event.Type), event.Name, string(event.Payload), event.SelectorLabels, event.PreviousSelectorLabels, event.DeduplicationKey, int64(lease.FencingToken), event.At, ttlSeconds},
		})
		candidates = append(candidates, event)
	}
	return batch, candidates, ttlSeconds
}

func (s *scyllaDatastore) publishNamespaceWatchSequence(ctx context.Context, clock namespaceWatchClockRow, lease datastore.NamespaceWatchLease, next int64, eventType datastore.NamespaceWatchEventType, at time.Time) (bool, error) {
	statement := "UPDATE namespace_watch_clock SET high_water=?,update_timestamp=? WHERE journal=? IF epoch=? AND high_water=? AND lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?"
	values := []any{next, at, namespaceWatchJournalName, clock.Epoch, clock.HighWater, lease.Holder, int64(lease.FencingToken), time.Now().UTC()}
	if eventType == datastore.NamespaceWatchBookmark {
		statement = "UPDATE namespace_watch_clock SET high_water=?,update_timestamp=?,bookmark_timestamp=? WHERE journal=? IF epoch=? AND high_water=? AND lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?"
		values = []any{next, at, at, namespaceWatchJournalName, clock.Epoch, clock.HighWater, lease.Holder, int64(lease.FencingToken), time.Now().UTC()}
	}
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(values...).ExecCASRelease()
	if err != nil {
		return false, fmt.Errorf("scylla: publish Namespace journal sequence: %w", err)
	}
	return applied, nil
}

func (s *scyllaDatastore) namespaceWatchEvent(ctx context.Context, epoch gocql.UUID, bucket, sequence int64) (datastore.NamespaceWatchEvent, error) {
	var row namespaceWatchEventRow
	err := s.session.Query(
		"SELECT epoch,bucket,sequence,event_type,name,payload,labels,previous_labels,deduplication_key,fencing_token,event_timestamp FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence=?",
		nil,
	).WithContext(ctx).Bind(epoch, bucket, sequence).GetRelease(&row)
	if err != nil {
		return datastore.NamespaceWatchEvent{}, err
	}
	return datastore.NamespaceWatchEvent{
		Epoch: row.Epoch.String(), Sequence: uint64(row.Sequence), Type: datastore.NamespaceWatchEventType(row.EventType),
		Name: row.Name, Payload: []byte(row.Payload), SelectorLabels: row.SelectorLabels, PreviousSelectorLabels: row.PreviousSelectorLabels,
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
			"SELECT epoch,bucket,sequence,event_type,name,payload,labels,previous_labels,deduplication_key,fencing_token,event_timestamp FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence>=? AND sequence<=? LIMIT ?",
			nil,
		).WithContext(ctx).Bind(epoch, bucket, int64(start), int64(bucketEnd), limit-len(out)).SelectRelease(&rows)
		if err != nil {
			return nil, fmt.Errorf("scylla: read Namespace journal events: %w", err)
		}
		for _, row := range rows {
			out = append(out, datastore.NamespaceWatchEvent{
				Epoch: row.Epoch.String(), Sequence: uint64(row.Sequence), Type: datastore.NamespaceWatchEventType(row.EventType),
				Name: row.Name, Payload: []byte(row.Payload), SelectorLabels: row.SelectorLabels, PreviousSelectorLabels: row.PreviousSelectorLabels,
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
	// Scylla timestamps have millisecond precision. Normalize before the LWT so
	// ambiguity resolution can compare the requested and persisted expirations
	// exactly.
	expires := now.Add(ttl).UTC().Truncate(time.Millisecond)
	next := current.FencingToken + 1
	applied, err := s.session.Query(
		"UPDATE namespace_watch_clock SET lease_holder=?,fencing_token=?,lease_expiration_timestamp=? WHERE journal=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp=?",
		nil,
	).WithContext(ctx).Bind(holder, next, expires, namespaceWatchJournalName, current.Holder, current.FencingToken, current.ExpiresAt).ExecCASRelease()
	if err != nil {
		primary := fmt.Errorf("scylla: acquire Namespace materializer lease: %w", err)
		return s.resolveNamespaceLeaseAcquisition(ctx, holder, next, primary)
	}
	return datastore.NamespaceWatchLease{Holder: holder, FencingToken: uint64(next), ExpiresAt: expires}, applied, nil
}

func (s *scyllaDatastore) resolveNamespaceLeaseAcquisition(
	ctx context.Context,
	holder string,
	fencingToken int64,
	primary error,
) (datastore.NamespaceWatchLease, bool, error) {
	return runNamespaceLeaseAcquisitionResolution(ctx, holder, fencingToken, primary, func(resolveCtx context.Context) (namespaceWatchClockRow, error) {
		var state namespaceWatchClockRow
		err := s.session.Query(
			"SELECT lease_holder,fencing_token,lease_expiration_timestamp FROM namespace_watch_clock WHERE journal=? AND stream_id=?",
			nil,
		).Consistency(gocql.LocalSerial).WithContext(resolveCtx).Bind(namespaceWatchJournalName, namespaceWatchClockStream).GetRelease(&state)
		return state, err
	})
}

func runNamespaceLeaseAcquisitionResolution(
	ctx context.Context,
	holder string,
	fencingToken int64,
	primary error,
	readState func(context.Context) (namespaceWatchClockRow, error),
) (datastore.NamespaceWatchLease, bool, error) {
	resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), namespaceProjectionCleanupTimeout)
	defer cancel()
	state, err := readState(resolveCtx)
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("%w; resolve ambiguous lease acquisition: %v", primary, err)
	}
	if state.Holder != holder || state.FencingToken != fencingToken {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	return datastore.NamespaceWatchLease{
		Holder: holder, FencingToken: uint64(fencingToken), ExpiresAt: state.ExpiresAt,
	}, true, nil
}

func (s *scyllaDatastore) RenewLease(ctx context.Context, lease datastore.NamespaceWatchLease, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	current, err := s.ensureNamespaceWatchClock(ctx)
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, err
	}
	if !namespaceWatchLeaseMatches(current, lease, now) {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	expires := now.Add(ttl).UTC().Truncate(time.Millisecond)
	applied, err := s.session.Query(
		"UPDATE namespace_watch_clock SET lease_expiration_timestamp=? WHERE journal=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp=?",
		nil,
	).WithContext(ctx).Bind(expires, namespaceWatchJournalName, lease.Holder, int64(lease.FencingToken), current.ExpiresAt).ExecCASRelease()
	if err != nil {
		primary := fmt.Errorf("scylla: renew Namespace materializer lease: %w", err)
		return s.resolveNamespaceLeaseRenewal(ctx, lease, expires, primary)
	}
	lease.ExpiresAt = expires
	return lease, applied, nil
}

func (s *scyllaDatastore) resolveNamespaceLeaseRenewal(
	ctx context.Context,
	lease datastore.NamespaceWatchLease,
	expires time.Time,
	primary error,
) (datastore.NamespaceWatchLease, bool, error) {
	return runNamespaceLeaseRenewalResolution(ctx, lease, expires, primary, func(resolveCtx context.Context) (namespaceWatchClockRow, error) {
		var state namespaceWatchClockRow
		err := s.session.Query(
			"SELECT lease_holder,fencing_token,lease_expiration_timestamp FROM namespace_watch_clock WHERE journal=? AND stream_id=?",
			nil,
		).Consistency(gocql.LocalSerial).WithContext(resolveCtx).Bind(namespaceWatchJournalName, namespaceWatchClockStream).GetRelease(&state)
		return state, err
	})
}

func runNamespaceLeaseRenewalResolution(
	ctx context.Context,
	lease datastore.NamespaceWatchLease,
	expires time.Time,
	primary error,
	readState func(context.Context) (namespaceWatchClockRow, error),
) (datastore.NamespaceWatchLease, bool, error) {
	resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), namespaceProjectionCleanupTimeout)
	defer cancel()
	state, err := readState(resolveCtx)
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("%w; resolve ambiguous lease renewal: %v", primary, err)
	}
	if state.Holder != lease.Holder || uint64(state.FencingToken) != lease.FencingToken || !state.ExpiresAt.Equal(expires) {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	lease.ExpiresAt = state.ExpiresAt
	return lease, true, nil
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
	if progress.StreamID == namespaceCDCPublishedFrontierProgress {
		applied, err := s.session.Query("UPDATE namespace_watch_clock SET position=?,progress_update_timestamp=?,cdc_progress_timestamp=? WHERE journal=? AND stream_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?", nil).
			WithContext(ctx).Bind(progress.Position, progress.UpdatedAt, progress.UpdatedAt, namespaceWatchJournalName, progress.StreamID, lease.Holder, int64(lease.FencingToken), time.Now().UTC()).ExecCASRelease()
		if err != nil {
			return fmt.Errorf("scylla: save Namespace CDC published frontier: %w", err)
		}
		if !applied {
			return datastore.ErrStaleWatchLease
		}
		return nil
	}
	if progress.StreamID == namespaceCDCGenerationProgress {
		applied, err := s.session.Query("UPDATE namespace_watch_clock SET position=?,progress_update_timestamp=? WHERE journal=? AND stream_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?", nil).
			WithContext(ctx).Bind(progress.Position, progress.UpdatedAt, namespaceWatchJournalName, progress.StreamID, lease.Holder, int64(lease.FencingToken), time.Now().UTC()).ExecCASRelease()
		if err != nil {
			return fmt.Errorf("scylla: save Namespace CDC progress: %w", err)
		}
		if !applied {
			return datastore.ErrStaleWatchLease
		}
		return nil
	}

	// Per-stream checkpoints are durable resume tokens only. Readiness is owned
	// by the separately fenced global published frontier, which represents the
	// minimum progress safe across every active CDC stream.
	applied, err := s.session.Query("UPDATE namespace_watch_clock USING TTL ? SET position=?,progress_update_timestamp=? WHERE journal=? AND stream_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp>?", nil).
		WithContext(ctx).Bind(namespaceCDCProgressTTLSeconds, progress.Position, progress.UpdatedAt, namespaceWatchJournalName, progress.StreamID, lease.Holder, int64(lease.FencingToken), time.Now().UTC()).ExecCASRelease()
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
