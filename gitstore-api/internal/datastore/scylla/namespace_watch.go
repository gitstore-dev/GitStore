// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

const namespaceWatchJournalName = "namespace"

func (s *scyllaDatastore) NamespaceWatchJournal() datastore.NamespaceWatchJournal { return s }

type namespaceWatchClockRow struct {
	Epoch     gocql.UUID `db:"epoch"`
	HighWater int64      `db:"high_water"`
	Oldest    int64      `db:"oldest"`
	UpdatedAt time.Time  `db:"updated_at"`
}

type namespaceWatchEventRow struct {
	Epoch            gocql.UUID `db:"epoch"`
	Bucket           int64      `db:"bucket"`
	Sequence         int64      `db:"sequence"`
	EventType        string     `db:"event_type"`
	Name             string     `db:"name"`
	Payload          string     `db:"payload"`
	DeduplicationKey string     `db:"deduplication_key"`
	EventAt          time.Time  `db:"event_at"`
}

type namespaceWatchLeaseRow struct {
	Holder       string    `db:"holder"`
	FencingToken int64     `db:"fencing_token"`
	ExpiresAt    time.Time `db:"expires_at"`
}

func (s *scyllaDatastore) ensureNamespaceWatchClock(ctx context.Context) (namespaceWatchClockRow, error) {
	epoch, err := gocql.RandomUUID()
	if err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: create Namespace watch epoch: %w", err)
	}
	_, err = s.session.Query(
		"INSERT INTO namespace_watch_clock (journal,epoch,high_water,oldest,updated_at) VALUES (?,?,?,?,?) IF NOT EXISTS",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName, epoch, int64(0), int64(0), time.Now().UTC()).ExecCASRelease()
	if err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: initialize Namespace watch clock: %w", err)
	}
	var row namespaceWatchClockRow
	if err := s.session.Query(
		"SELECT epoch,high_water,oldest,updated_at FROM namespace_watch_clock WHERE journal=?",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&row); err != nil {
		return namespaceWatchClockRow{}, fmt.Errorf("scylla: read Namespace watch clock: %w", err)
	}
	return row, nil
}

func (s *scyllaDatastore) Bounds(ctx context.Context) (datastore.NamespaceWatchBounds, error) {
	var row namespaceWatchClockRow
	err := s.session.Query(
		"SELECT epoch,high_water,oldest,updated_at FROM namespace_watch_clock WHERE journal=?",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&row)
	if errors.Is(err, gocql.ErrNotFound) {
		return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: Namespace watch materializer has not initialized the journal")
	}
	if err != nil {
		return datastore.NamespaceWatchBounds{}, fmt.Errorf("scylla: read Namespace watch clock: %w", err)
	}
	return datastore.NamespaceWatchBounds{Epoch: row.Epoch.String(), Oldest: uint64(maxInt64(row.Oldest, 0)), HighWater: uint64(maxInt64(row.HighWater, 0)), UpdatedAt: row.UpdatedAt}, nil
}

func (s *scyllaDatastore) Append(ctx context.Context, lease datastore.NamespaceWatchLease, event datastore.NamespaceWatchEvent, ttl time.Duration) (datastore.NamespaceWatchEvent, error) {
	valid, err := s.namespaceWatchLeaseValid(ctx, lease, time.Now())
	if err != nil {
		return datastore.NamespaceWatchEvent{}, err
	}
	if !valid {
		return datastore.NamespaceWatchEvent{}, datastore.ErrStaleWatchLease
	}

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
		next := clock.HighWater + 1
		candidate := event
		candidate.Epoch = clock.Epoch.String()
		candidate.Sequence = uint64(next)
		bucket := int64((candidate.Sequence - 1) / 4096)
		inserted, insertErr := s.session.Query(
			"INSERT INTO namespace_watch_events (epoch,bucket,sequence,event_type,name,payload,deduplication_key,event_at) VALUES (?,?,?,?,?,?,?,?) IF NOT EXISTS USING TTL ?",
			nil,
		).WithContext(ctx).Bind(clock.Epoch, bucket, next, string(candidate.Type), candidate.Name, string(candidate.Payload), candidate.DeduplicationKey, candidate.At, ttlSeconds).ExecCASRelease()
		if insertErr != nil {
			return datastore.NamespaceWatchEvent{}, fmt.Errorf("scylla: append Namespace journal event: %w", insertErr)
		}
		if !inserted {
			existing, loadErr := s.namespaceWatchEvent(ctx, clock.Epoch, bucket, next)
			if loadErr != nil && !errors.Is(loadErr, gocql.ErrNotFound) {
				return datastore.NamespaceWatchEvent{}, loadErr
			}
			// An event written before a crash is repaired into the visible clock
			// before allocating another sequence. If CDC redelivered the same
			// change, return that durable event and safely advance progress.
			_, _ = s.session.Query(
				"UPDATE namespace_watch_clock SET high_water=?,updated_at=? WHERE journal=? IF epoch=? AND high_water=?",
				nil,
			).WithContext(ctx).Bind(next, existing.At, namespaceWatchJournalName, clock.Epoch, clock.HighWater).ExecCASRelease()
			if existing.DeduplicationKey != "" && existing.DeduplicationKey == candidate.DeduplicationKey {
				return existing, nil
			}
			continue
		}

		applied, casErr := s.session.Query(
			"UPDATE namespace_watch_clock SET high_water=?,updated_at=? WHERE journal=? IF epoch=? AND high_water=?",
			nil,
		).WithContext(ctx).Bind(next, candidate.At, namespaceWatchJournalName, clock.Epoch, clock.HighWater).ExecCASRelease()
		if casErr != nil {
			return datastore.NamespaceWatchEvent{}, fmt.Errorf("scylla: publish Namespace journal sequence: %w", casErr)
		}
		if !applied {
			latest, latestErr := s.ensureNamespaceWatchClock(ctx)
			if latestErr != nil {
				return datastore.NamespaceWatchEvent{}, latestErr
			}
			if latest.Epoch == clock.Epoch && latest.HighWater >= next {
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

func (s *scyllaDatastore) namespaceWatchEvent(ctx context.Context, epoch gocql.UUID, bucket, sequence int64) (datastore.NamespaceWatchEvent, error) {
	var row namespaceWatchEventRow
	err := s.session.Query(
		"SELECT epoch,bucket,sequence,event_type,name,payload,deduplication_key,event_at FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence=?",
		nil,
	).WithContext(ctx).Bind(epoch, bucket, sequence).GetRelease(&row)
	if err != nil {
		return datastore.NamespaceWatchEvent{}, err
	}
	return datastore.NamespaceWatchEvent{
		Epoch: row.Epoch.String(), Sequence: uint64(row.Sequence), Type: datastore.NamespaceWatchEventType(row.EventType),
		Name: row.Name, Payload: []byte(row.Payload), DeduplicationKey: row.DeduplicationKey, At: row.EventAt,
	}, nil
}

func (s *scyllaDatastore) ReadAfter(ctx context.Context, cursor datastore.NamespaceWatchCursor, limit int) ([]datastore.NamespaceWatchEvent, error) {
	bounds, err := s.Bounds(ctx)
	if err != nil {
		return nil, err
	}
	if cursor.Epoch != "" && cursor.Epoch != bounds.Epoch {
		return nil, datastore.ErrWatchCursorEpoch
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
		bucket := int64((start - 1) / 4096)
		bucketEnd := uint64(bucket+1) * 4096
		if bucketEnd > bounds.HighWater {
			bucketEnd = bounds.HighWater
		}
		var rows []namespaceWatchEventRow
		err = s.session.Query(
			"SELECT epoch,bucket,sequence,event_type,name,payload,deduplication_key,event_at FROM namespace_watch_events WHERE epoch=? AND bucket=? AND sequence>=? AND sequence<=? LIMIT ?",
			nil,
		).WithContext(ctx).Bind(epoch, bucket, int64(start), int64(bucketEnd), limit-len(out)).SelectRelease(&rows)
		if err != nil {
			return nil, fmt.Errorf("scylla: read Namespace journal events: %w", err)
		}
		for _, row := range rows {
			out = append(out, datastore.NamespaceWatchEvent{Epoch: row.Epoch.String(), Sequence: uint64(row.Sequence), Type: datastore.NamespaceWatchEventType(row.EventType), Name: row.Name, Payload: []byte(row.Payload), DeduplicationKey: row.DeduplicationKey, At: row.EventAt})
		}
		start = bucketEnd + 1
	}
	return out, nil
}

func (s *scyllaDatastore) AcquireLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	expires := now.Add(ttl)
	applied, err := s.session.Query(
		"INSERT INTO namespace_watch_materializer_lease (lease_name,holder,fencing_token,expires_at) VALUES (?,?,?,?) IF NOT EXISTS",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName, holder, int64(1), expires).ExecCASRelease()
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("scylla: acquire Namespace materializer lease: %w", err)
	}
	if applied {
		return datastore.NamespaceWatchLease{Holder: holder, FencingToken: 1, ExpiresAt: expires}, true, nil
	}
	var current namespaceWatchLeaseRow
	if err := s.session.Query("SELECT holder,fencing_token,expires_at FROM namespace_watch_materializer_lease WHERE lease_name=?", nil).
		WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&current); err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("scylla: read Namespace materializer lease: %w", err)
	}
	if now.Before(current.ExpiresAt) {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	next := current.FencingToken + 1
	applied, err = s.session.Query(
		"UPDATE namespace_watch_materializer_lease SET holder=?,fencing_token=?,expires_at=? WHERE lease_name=? IF holder=? AND fencing_token=? AND expires_at=?",
		nil,
	).WithContext(ctx).Bind(holder, next, expires, namespaceWatchJournalName, current.Holder, current.FencingToken, current.ExpiresAt).ExecCASRelease()
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("scylla: take over Namespace materializer lease: %w", err)
	}
	return datastore.NamespaceWatchLease{Holder: holder, FencingToken: uint64(next), ExpiresAt: expires}, applied, nil
}

func (s *scyllaDatastore) RenewLease(ctx context.Context, lease datastore.NamespaceWatchLease, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	expires := now.Add(ttl)
	applied, err := s.session.Query(
		"UPDATE namespace_watch_materializer_lease SET expires_at=? WHERE lease_name=? IF holder=? AND fencing_token=?",
		nil,
	).WithContext(ctx).Bind(expires, namespaceWatchJournalName, lease.Holder, int64(lease.FencingToken)).ExecCASRelease()
	if err != nil {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("scylla: renew Namespace materializer lease: %w", err)
	}
	lease.ExpiresAt = expires
	return lease, applied, nil
}

func (s *scyllaDatastore) ReleaseLease(ctx context.Context, lease datastore.NamespaceWatchLease) error {
	_, err := s.session.Query(
		"DELETE FROM namespace_watch_materializer_lease WHERE lease_name=? IF holder=? AND fencing_token=?",
		nil,
	).WithContext(ctx).Bind(namespaceWatchJournalName, lease.Holder, int64(lease.FencingToken)).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: release Namespace materializer lease: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) LoadProgress(ctx context.Context, streamID string) (datastore.NamespaceCDCProgress, error) {
	var row struct {
		StreamID  string    `db:"stream_id"`
		Position  []byte    `db:"position"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	err := s.session.Query("SELECT stream_id,position,updated_at FROM namespace_cdc_progress WHERE stream_id=?", nil).
		WithContext(ctx).Bind(streamID).GetRelease(&row)
	if errors.Is(err, gocql.ErrNotFound) {
		return datastore.NamespaceCDCProgress{}, datastore.ErrNotFound
	}
	if err != nil {
		return datastore.NamespaceCDCProgress{}, fmt.Errorf("scylla: load Namespace CDC progress: %w", err)
	}
	return datastore.NamespaceCDCProgress{StreamID: row.StreamID, Position: row.Position, UpdatedAt: row.UpdatedAt}, nil
}

func (s *scyllaDatastore) SaveProgress(ctx context.Context, lease datastore.NamespaceWatchLease, progress datastore.NamespaceCDCProgress) error {
	valid, err := s.namespaceWatchLeaseValid(ctx, lease, time.Now())
	if err != nil {
		return err
	}
	if !valid {
		return datastore.ErrStaleWatchLease
	}
	err = s.session.Query(
		"INSERT INTO namespace_cdc_progress (stream_id,position,fencing_token,updated_at) VALUES (?,?,?,?)",
		nil,
	).WithContext(ctx).Bind(progress.StreamID, progress.Position, int64(lease.FencingToken), progress.UpdatedAt).ExecRelease()
	if err != nil {
		return fmt.Errorf("scylla: save Namespace CDC progress: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) namespaceWatchLeaseValid(ctx context.Context, lease datastore.NamespaceWatchLease, now time.Time) (bool, error) {
	var current namespaceWatchLeaseRow
	err := s.session.Query("SELECT holder,fencing_token,expires_at FROM namespace_watch_materializer_lease WHERE lease_name=?", nil).
		WithContext(ctx).Bind(namespaceWatchJournalName).GetRelease(&current)
	if errors.Is(err, gocql.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scylla: validate Namespace materializer lease: %w", err)
	}
	return current.Holder == lease.Holder && uint64(current.FencingToken) == lease.FencingToken && now.Before(current.ExpiresAt), nil
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}
