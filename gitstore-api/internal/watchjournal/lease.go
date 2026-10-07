// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// LeaseManager coordinates one fenced materializer owner.
type LeaseManager struct {
	store         LeaseStore
	holder        string
	ttl           time.Duration
	renewInterval time.Duration
	clock         Clock
}

func NewLeaseManager(store LeaseStore, holder string, ttl, renewInterval time.Duration, clock Clock) *LeaseManager {
	return &LeaseManager{store: store, holder: holder, ttl: ttl, renewInterval: renewInterval, clock: clock}
}

func (m *LeaseManager) Acquire(ctx context.Context) (datastore.NamespaceWatchLease, bool, error) {
	if m.store == nil || m.clock == nil || m.holder == "" {
		return datastore.NamespaceWatchLease{}, false, fmt.Errorf("namespace watch lease manager is not configured")
	}
	return m.store.AcquireLease(ctx, m.holder, m.clock.Now(), m.ttl)
}

func (m *LeaseManager) Renew(ctx context.Context, lease datastore.NamespaceWatchLease) (datastore.NamespaceWatchLease, bool, error) {
	return m.store.RenewLease(ctx, lease, m.clock.Now(), m.ttl)
}

func (m *LeaseManager) Release(ctx context.Context, lease datastore.NamespaceWatchLease) error {
	return m.store.ReleaseLease(ctx, lease)
}

// Maintain renews lease until cancellation or fencing loss. Fencing loss is
// returned to the caller so CDC consumption stops before another append.
func (m *LeaseManager) Maintain(ctx context.Context, lease datastore.NamespaceWatchLease) error {
	ticker := time.NewTicker(m.renewInterval)
	defer ticker.Stop()
	defer func() { _ = m.Release(context.Background(), lease) }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			renewed, ok, err := m.Renew(ctx, lease)
			if err != nil {
				return err
			}
			if !ok {
				return datastore.ErrStaleWatchLease
			}
			lease = renewed
		}
	}
}
