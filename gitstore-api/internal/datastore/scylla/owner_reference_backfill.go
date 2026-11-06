// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

const ownerReferenceBackfillJobID = "owner-references"

type ownerReferenceBackfillRow struct {
	Holder    string    `db:"lease_holder"`
	Fencing   int64     `db:"fencing_token"`
	ExpiresAt time.Time `db:"lease_expiration_timestamp"`
	Progress  string    `db:"progress_json"`
}

func (s *scyllaDatastore) loadOwnerReferenceBackfillRow(ctx context.Context) (ownerReferenceBackfillRow, error) {
	var row ownerReferenceBackfillRow
	err := s.session.Query(
		"SELECT lease_holder,fencing_token,lease_expiration_timestamp,progress_json FROM owner_reference_backfill_by_job WHERE job_id=?",
		nil,
	).WithContext(ctx).Bind(ownerReferenceBackfillJobID).GetRelease(&row)
	if errors.Is(err, gocql.ErrNotFound) {
		return ownerReferenceBackfillRow{}, datastore.ErrNotFound
	}
	if err != nil {
		return ownerReferenceBackfillRow{}, fmt.Errorf("scylla: load owner-reference backfill state: %w", err)
	}
	return row, nil
}

func (s *scyllaDatastore) AcquireOwnerReferenceBackfillLease(
	ctx context.Context,
	holder string,
	now time.Time,
	ttl time.Duration,
) (datastore.OwnerReferenceBackfillLease, bool, error) {
	if holder == "" || ttl <= 0 {
		return datastore.OwnerReferenceBackfillLease{}, false, datastore.ErrInvalidArgument
	}
	now = now.UTC().Truncate(time.Millisecond)
	expires := now.Add(ttl).UTC().Truncate(time.Millisecond)
	current, err := s.loadOwnerReferenceBackfillRow(ctx)
	if errors.Is(err, datastore.ErrNotFound) {
		applied, insertErr := s.session.Query(
			"INSERT INTO owner_reference_backfill_by_job (job_id,lease_holder,fencing_token,lease_expiration_timestamp,progress_json) VALUES (?,?,?,?,?) IF NOT EXISTS",
			nil,
		).WithContext(ctx).Bind(ownerReferenceBackfillJobID, holder, int64(1), expires, "").ExecCASRelease()
		if insertErr != nil {
			return datastore.OwnerReferenceBackfillLease{}, false, fmt.Errorf("scylla: initialize owner-reference backfill lease: %w", insertErr)
		}
		return datastore.OwnerReferenceBackfillLease{Holder: holder, FencingToken: 1, ExpiresAt: expires}, applied, nil
	}
	if err != nil {
		return datastore.OwnerReferenceBackfillLease{}, false, err
	}
	if current.Holder != "" && now.Before(current.ExpiresAt) {
		return datastore.OwnerReferenceBackfillLease{}, false, nil
	}
	next := current.Fencing + 1
	if next <= 0 {
		return datastore.OwnerReferenceBackfillLease{}, false, fmt.Errorf("scylla: owner-reference backfill fencing token exhausted")
	}
	applied, err := s.session.Query(
		"UPDATE owner_reference_backfill_by_job SET lease_holder=?,fencing_token=?,lease_expiration_timestamp=? WHERE job_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp=?",
		nil,
	).WithContext(ctx).Bind(holder, next, expires, ownerReferenceBackfillJobID, current.Holder, current.Fencing, current.ExpiresAt).ExecCASRelease()
	if err != nil {
		return datastore.OwnerReferenceBackfillLease{}, false, fmt.Errorf("scylla: acquire owner-reference backfill lease: %w", err)
	}
	return datastore.OwnerReferenceBackfillLease{Holder: holder, FencingToken: next, ExpiresAt: expires}, applied, nil
}

func (s *scyllaDatastore) RenewOwnerReferenceBackfillLease(
	ctx context.Context,
	lease datastore.OwnerReferenceBackfillLease,
	now time.Time,
	ttl time.Duration,
) (datastore.OwnerReferenceBackfillLease, bool, error) {
	if lease.Holder == "" || lease.FencingToken <= 0 || ttl <= 0 {
		return datastore.OwnerReferenceBackfillLease{}, false, datastore.ErrInvalidArgument
	}
	current, err := s.loadOwnerReferenceBackfillRow(ctx)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.OwnerReferenceBackfillLease{}, false, nil
	}
	if err != nil {
		return datastore.OwnerReferenceBackfillLease{}, false, err
	}
	now = now.UTC().Truncate(time.Millisecond)
	if current.Holder != lease.Holder || current.Fencing != lease.FencingToken || !now.Before(current.ExpiresAt) {
		return datastore.OwnerReferenceBackfillLease{}, false, nil
	}
	expires := now.Add(ttl).UTC().Truncate(time.Millisecond)
	applied, err := s.session.Query(
		"UPDATE owner_reference_backfill_by_job SET lease_expiration_timestamp=? WHERE job_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp=?",
		nil,
	).WithContext(ctx).Bind(expires, ownerReferenceBackfillJobID, lease.Holder, lease.FencingToken, current.ExpiresAt).ExecCASRelease()
	if err != nil {
		return datastore.OwnerReferenceBackfillLease{}, false, fmt.Errorf("scylla: renew owner-reference backfill lease: %w", err)
	}
	lease.ExpiresAt = expires
	return lease, applied, nil
}

func (s *scyllaDatastore) ReleaseOwnerReferenceBackfillLease(
	ctx context.Context,
	lease datastore.OwnerReferenceBackfillLease,
) error {
	if lease.Holder == "" || lease.FencingToken <= 0 {
		return datastore.ErrInvalidArgument
	}
	applied, err := s.session.Query(
		"UPDATE owner_reference_backfill_by_job SET lease_holder=?,lease_expiration_timestamp=? WHERE job_id=? IF lease_holder=? AND fencing_token=?",
		nil,
	).WithContext(ctx).Bind("", time.Unix(0, 0).UTC(), ownerReferenceBackfillJobID, lease.Holder, lease.FencingToken).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: release owner-reference backfill lease: %w", err)
	}
	if !applied {
		return datastore.ErrStaleBackfillLease
	}
	return nil
}

func (s *scyllaDatastore) LoadOwnerReferenceBackfillProgress(
	ctx context.Context,
) (datastore.OwnerReferenceBackfillProgress, error) {
	row, err := s.loadOwnerReferenceBackfillRow(ctx)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return datastore.OwnerReferenceBackfillProgress{}, datastore.ErrNotFound
		}
		return datastore.OwnerReferenceBackfillProgress{}, err
	}
	if row.Progress == "" {
		return datastore.OwnerReferenceBackfillProgress{}, datastore.ErrNotFound
	}
	var progress datastore.OwnerReferenceBackfillProgress
	if err := json.Unmarshal([]byte(row.Progress), &progress); err != nil {
		return datastore.OwnerReferenceBackfillProgress{}, fmt.Errorf("scylla: decode owner-reference backfill progress: %w", err)
	}
	if err := datastore.ValidateOwnerReferenceBackfillProgress(progress); err != nil {
		return datastore.OwnerReferenceBackfillProgress{}, fmt.Errorf("scylla: invalid owner-reference backfill progress: %w", err)
	}
	return progress, nil
}

func (s *scyllaDatastore) SaveOwnerReferenceBackfillProgress(
	ctx context.Context,
	lease datastore.OwnerReferenceBackfillLease,
	progress datastore.OwnerReferenceBackfillProgress,
) error {
	if lease.Holder == "" || lease.FencingToken <= 0 {
		return datastore.ErrInvalidArgument
	}
	if err := datastore.ValidateOwnerReferenceBackfillProgress(progress); err != nil {
		return err
	}
	raw, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("scylla: encode owner-reference backfill progress: %w", err)
	}
	current, err := s.loadOwnerReferenceBackfillRow(ctx)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.ErrStaleBackfillLease
	}
	if err != nil {
		return err
	}
	if current.Holder != lease.Holder || current.Fencing != lease.FencingToken || !time.Now().UTC().Before(current.ExpiresAt) {
		return datastore.ErrStaleBackfillLease
	}
	applied, err := s.session.Query(
		"UPDATE owner_reference_backfill_by_job SET progress_json=? WHERE job_id=? IF lease_holder=? AND fencing_token=? AND lease_expiration_timestamp=?",
		nil,
	).WithContext(ctx).Bind(string(raw), ownerReferenceBackfillJobID, lease.Holder, lease.FencingToken, current.ExpiresAt).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: save owner-reference backfill progress: %w", err)
	}
	if !applied {
		return datastore.ErrStaleBackfillLease
	}
	return nil
}
