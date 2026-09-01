// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package serviceaccountassertion

import (
	"sync"
	"time"
)

// replayCache is the authoritative single-use enforcement mechanism for
// client assertions (data-model.md §3's "jti accepted once within the
// replay window"). Unlike serviceaccountjwt's revocationList — which is
// explicitly documented as non-authoritative, defense-in-depth only — this
// cache IS the primary control: an assertion's short exp-iat<=60s lifetime
// combined with "reject on second use" is what makes proof-of-possession
// exchange safe against replay, so TryConsume's return value must be
// trusted as the deciding factor, not merely advisory.
//
// In-memory, single-instance scope only (documented Assumption — a
// multi-replica gitstore-api deployment does not share this cache across
// instances; see spec 061's Assumptions section).
type replayCache struct {
	mu     sync.Mutex
	seen   map[string]time.Time // jti -> expiry (prune after expiry passes)
	stopCh chan struct{}
}

func newReplayCache() *replayCache {
	return &replayCache{
		seen:   make(map[string]time.Time),
		stopCh: make(chan struct{}),
	}
}

// TryConsume records jti as used and returns true if this is the first time
// it has been seen (i.e. this assertion may proceed), or false if jti was
// already recorded (replay — caller must Deny).
func (c *replayCache) TryConsume(jti string, expiresAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.seen[jti]; exists {
		return false
	}
	c.seen[jti] = expiresAt
	return true
}

// pruneLoop periodically evicts expired entries so the cache does not grow
// unbounded across the lifetime of the process. Mirrors
// serviceaccountjwt.revocationList's pruneLoop shape.
func (c *replayCache) pruneLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.prune()
		case <-c.stopCh:
			return
		}
	}
}

func (c *replayCache) prune() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for jti, exp := range c.seen {
		if now.After(exp) {
			delete(c.seen, jti)
		}
	}
}

func (c *replayCache) shutdown() {
	close(c.stopCh)
}
