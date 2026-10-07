// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package serviceaccountjwt

import (
	"sync"
	"time"
)

// revocationList is an optional, in-memory, per-token jti blacklist used as
// defense-in-depth (RevokeSession). It is never the authoritative
// revocation mechanism — ServiceAccount Disabled/DeletionTimestamp,
// checked on every Authenticate call against the persistent datastore
// record, is authoritative and survives API restarts; this in-memory list
// does not.
type revocationList struct {
	mu      sync.RWMutex
	entries map[string]time.Time
	stop    chan struct{}
}

func newRevocationList() *revocationList {
	return &revocationList{
		entries: make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
}

func (b *revocationList) add(jti string, expiresAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[jti] = expiresAt
}

func (b *revocationList) isRevoked(jti string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.entries[jti]
	return ok
}

func (b *revocationList) pruneLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			b.prune()
		case <-b.stop:
			return
		}
	}
}

func (b *revocationList) shutdown() {
	close(b.stop)
}

func (b *revocationList) prune() {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for jti, exp := range b.entries {
		if now.After(exp) {
			delete(b.entries, jti)
		}
	}
}
