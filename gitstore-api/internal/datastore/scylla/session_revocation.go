// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"math"
	"time"

	"github.com/gocql/gocql"
)

// Scylla rejects TTLs above 20 years. Legacy static-admin tokens may have a
// much later expiry, so those revocations are stored without a TTL and removed
// lazily after expires_at instead of weakening their revocation window.
const maxRevocationTTL = 20 * 365 * 24 * time.Hour

// RevokeSession persists revocation state in the shared datastore so logout
// and refresh rotation take effect on every API replica.
func (s *scyllaDatastore) RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error {
	ttl, bounded := revocationTTL(expiresAt)
	if !bounded {
		return s.session.Session.Query(
			`INSERT INTO auth_session_revocations (jti, expires_at) VALUES (?, ?)`,
			jti, expiresAt.UTC(),
		).WithContext(ctx).Exec()
	}
	return s.session.Session.Query(
		`INSERT INTO auth_session_revocations (jti, expires_at) VALUES (?, ?) USING TTL ?`,
		jti, expiresAt.UTC(), ttl,
	).WithContext(ctx).Exec()
}

// ConsumeSession atomically claims a JTI for refresh rotation. Exactly one
// replica can consume a previously-unrevoked token.
func (s *scyllaDatastore) ConsumeSession(ctx context.Context, jti string, expiresAt time.Time) (bool, error) {
	dest := make(map[string]any)
	ttl, bounded := revocationTTL(expiresAt)
	if !bounded {
		return s.session.Session.Query(
			`INSERT INTO auth_session_revocations (jti, expires_at) VALUES (?, ?) IF NOT EXISTS`,
			jti, expiresAt.UTC(),
		).WithContext(ctx).MapScanCAS(dest)
	}
	return s.session.Session.Query(
		`INSERT INTO auth_session_revocations (jti, expires_at) VALUES (?, ?) IF NOT EXISTS USING TTL ?`,
		jti, expiresAt.UTC(), ttl,
	).WithContext(ctx).MapScanCAS(dest)
}

// IsSessionRevoked reads the shared JTI set. TTL expiry removes stale rows.
func (s *scyllaDatastore) IsSessionRevoked(ctx context.Context, jti string) (bool, error) {
	var expiresAt time.Time
	err := s.session.Session.Query(
		`SELECT expires_at FROM auth_session_revocations WHERE jti = ?`, jti,
	).WithContext(ctx).Scan(&expiresAt)
	if err == gocql.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if time.Now().Before(expiresAt) {
		return true, nil
	}
	// This is normally redundant with TTL expiry, but cleans up legacy-token
	// revocations whose expiry was too distant for Scylla's TTL range.
	if err := s.session.Session.Query(
		`DELETE FROM auth_session_revocations WHERE jti = ?`, jti,
	).WithContext(ctx).Exec(); err != nil {
		return false, err
	}
	return false, nil
}

func revocationTTL(expiresAt time.Time) (int, bool) {
	remaining := time.Until(expiresAt)
	if remaining > maxRevocationTTL {
		return 0, false
	}
	ttl := int(math.Ceil(remaining.Seconds()))
	if ttl < 1 {
		return 1, true
	}
	return ttl, true
}
