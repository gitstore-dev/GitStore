// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLeaseStore struct {
	lease        datastore.NamespaceWatchLease
	acquired     bool
	renewed      bool
	released     bool
	renewalToken uint64
}

func (s *fakeLeaseStore) AcquireLease(_ context.Context, holder string, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	if !s.acquired {
		s.acquired = true
		s.lease = datastore.NamespaceWatchLease{Holder: holder, FencingToken: s.lease.FencingToken + 1, ExpiresAt: now.Add(ttl)}
		return s.lease, true, nil
	}
	return datastore.NamespaceWatchLease{}, false, nil
}

func (s *fakeLeaseStore) RenewLease(_ context.Context, lease datastore.NamespaceWatchLease, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	s.renewalToken = lease.FencingToken
	if lease.FencingToken != s.lease.FencingToken {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	s.renewed = true
	s.lease.ExpiresAt = now.Add(ttl)
	return s.lease, true, nil
}

func (s *fakeLeaseStore) ReleaseLease(_ context.Context, lease datastore.NamespaceWatchLease) error {
	s.released = lease.FencingToken == s.lease.FencingToken
	return nil
}

func TestLeaseManagerAcquireRenewRelease(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	store := &fakeLeaseStore{}
	manager := NewLeaseManager(store, "replica-a", 30*time.Second, 10*time.Second, clock)

	lease, acquired, err := manager.Acquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	assert.Equal(t, uint64(1), lease.FencingToken)

	clock.Advance(10 * time.Second)
	renewed, ok, err := manager.Renew(context.Background(), lease)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, renewed.ExpiresAt.After(lease.ExpiresAt))

	require.NoError(t, manager.Release(context.Background(), renewed))
	assert.True(t, store.released)
}

func TestLeaseManagerRejectsStaleFencingToken(t *testing.T) {
	clock := newFakeClock(time.Now())
	store := &fakeLeaseStore{acquired: true, lease: datastore.NamespaceWatchLease{Holder: "replica-b", FencingToken: 9}}
	manager := NewLeaseManager(store, "replica-a", 30*time.Second, 10*time.Second, clock)

	_, ok, err := manager.Renew(context.Background(), datastore.NamespaceWatchLease{Holder: "replica-a", FencingToken: 8})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, uint64(8), store.renewalToken)
}
