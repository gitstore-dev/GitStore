// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

func (m *memdbDatastore) AcquireOwnerReferenceBackfillLease(
	ctx context.Context,
	holder string,
	now time.Time,
	ttl time.Duration,
) (datastore.OwnerReferenceBackfillLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return datastore.OwnerReferenceBackfillLease{}, false, err
	}
	if holder == "" || ttl <= 0 {
		return datastore.OwnerReferenceBackfillLease{}, false, datastore.ErrInvalidArgument
	}
	m.ownerBackfillMu.Lock()
	defer m.ownerBackfillMu.Unlock()
	if m.ownerBackfillLease.Holder != "" && now.Before(m.ownerBackfillLease.ExpiresAt) {
		return datastore.OwnerReferenceBackfillLease{}, false, nil
	}
	if m.ownerBackfillLease.FencingToken == math.MaxInt64 {
		return datastore.OwnerReferenceBackfillLease{}, false, fmt.Errorf("memdb: owner-reference backfill fencing token exhausted")
	}
	m.ownerBackfillLease = datastore.OwnerReferenceBackfillLease{
		Holder:       holder,
		FencingToken: m.ownerBackfillLease.FencingToken + 1,
		ExpiresAt:    now.Add(ttl),
	}
	return m.ownerBackfillLease, true, nil
}

func (m *memdbDatastore) RenewOwnerReferenceBackfillLease(
	ctx context.Context,
	lease datastore.OwnerReferenceBackfillLease,
	now time.Time,
	ttl time.Duration,
) (datastore.OwnerReferenceBackfillLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return datastore.OwnerReferenceBackfillLease{}, false, err
	}
	if lease.Holder == "" || lease.FencingToken <= 0 || ttl <= 0 {
		return datastore.OwnerReferenceBackfillLease{}, false, datastore.ErrInvalidArgument
	}
	m.ownerBackfillMu.Lock()
	defer m.ownerBackfillMu.Unlock()
	if !m.ownerBackfillLeaseMatches(lease, now) {
		return datastore.OwnerReferenceBackfillLease{}, false, nil
	}
	m.ownerBackfillLease.ExpiresAt = now.Add(ttl)
	return m.ownerBackfillLease, true, nil
}

func (m *memdbDatastore) ReleaseOwnerReferenceBackfillLease(
	ctx context.Context,
	lease datastore.OwnerReferenceBackfillLease,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.ownerBackfillMu.Lock()
	defer m.ownerBackfillMu.Unlock()
	if !m.ownerBackfillLeaseMatches(lease, time.Now()) {
		return datastore.ErrStaleBackfillLease
	}
	m.ownerBackfillLease.Holder = ""
	m.ownerBackfillLease.ExpiresAt = time.Time{}
	return nil
}

func (m *memdbDatastore) LoadOwnerReferenceBackfillProgress(
	ctx context.Context,
) (datastore.OwnerReferenceBackfillProgress, error) {
	if err := ctx.Err(); err != nil {
		return datastore.OwnerReferenceBackfillProgress{}, err
	}
	m.ownerBackfillMu.Lock()
	defer m.ownerBackfillMu.Unlock()
	if m.ownerBackfillProgress == nil {
		return datastore.OwnerReferenceBackfillProgress{}, datastore.ErrNotFound
	}
	return cloneOwnerReferenceBackfillProgress(*m.ownerBackfillProgress), nil
}

func (m *memdbDatastore) SaveOwnerReferenceBackfillProgress(
	ctx context.Context,
	lease datastore.OwnerReferenceBackfillLease,
	progress datastore.OwnerReferenceBackfillProgress,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateOwnerReferenceBackfillProgress(progress); err != nil {
		return err
	}
	m.ownerBackfillMu.Lock()
	defer m.ownerBackfillMu.Unlock()
	if !m.ownerBackfillLeaseMatches(lease, time.Now()) {
		return datastore.ErrStaleBackfillLease
	}
	copy := cloneOwnerReferenceBackfillProgress(progress)
	m.ownerBackfillProgress = &copy
	return nil
}

func (m *memdbDatastore) ownerBackfillLeaseMatches(lease datastore.OwnerReferenceBackfillLease, now time.Time) bool {
	return lease.Holder != "" &&
		lease.Holder == m.ownerBackfillLease.Holder &&
		lease.FencingToken == m.ownerBackfillLease.FencingToken &&
		now.Before(m.ownerBackfillLease.ExpiresAt)
}

func cloneOwnerReferenceBackfillProgress(progress datastore.OwnerReferenceBackfillProgress) datastore.OwnerReferenceBackfillProgress {
	result := progress
	if progress.Kinds != nil {
		result.Kinds = make(map[string]datastore.OwnerReferenceBackfillKindProgress, len(progress.Kinds))
		for kind, state := range progress.Kinds {
			result.Kinds[kind] = state
		}
	}
	return result
}
