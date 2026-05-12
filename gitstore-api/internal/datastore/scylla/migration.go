// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3"
	"github.com/scylladb/gocqlx/v3/migrate"
	"go.uber.org/zap"
)

//go:embed migrations/*.cql
var migrationFiles embed.FS

const (
	lockKey        = "migration"
	lockTTL        = 120 // seconds — self-expiry if holder crashes
	lockMaxRetries = 3
	lockRetryBase  = 2 * time.Second
)

// RunMigrations acquires a distributed LWT lock, applies all pending CQL
// migrations via gocqlx/migrate, then releases the lock.
// instanceID should be a unique string per process (e.g. a UUID).
func RunMigrations(ctx context.Context, rawSession *gocql.Session, instanceID string, log *zap.Logger) error {
	log.Info("acquiring migration lock", zap.String("instance", instanceID))

	acquired, err := acquireLockWithRetry(ctx, rawSession, instanceID, log)
	if err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	if !acquired {
		return errors.New("migration lock held by another instance after retries")
	}
	defer func() {
		if err := releaseLock(rawSession, instanceID); err != nil {
			log.Warn("failed to release migration lock", zap.Error(err))
		}
	}()

	log.Info("running CQL migrations")

	session := gocqlx.NewSession(rawSession)
	if err := migrate.FromFS(ctx, session, migrationFiles); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	log.Info("migrations complete")
	return nil
}

func acquireLockWithRetry(ctx context.Context, session *gocql.Session, instanceID string, log *zap.Logger) (bool, error) {
	for attempt := range lockMaxRetries {
		applied, err := acquireLock(session, instanceID)
		if err != nil {
			return false, err
		}
		if applied {
			return true, nil
		}

		wait := lockRetryBase * time.Duration(1<<attempt)
		log.Debug("migration lock held, retrying",
			zap.Int("attempt", attempt+1),
			zap.Duration("wait", wait),
		)

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(wait):
		}
	}
	return false, nil
}

func acquireLock(session *gocql.Session, instanceID string) (bool, error) {
	var holder string
	applied, err := session.Query(
		fmt.Sprintf(
			`INSERT INTO schema_migrations_lock (lock_key, holder, acquired_at)
			 VALUES (?, ?, toTimestamp(now()))
			 IF NOT EXISTS USING TTL %d`,
			lockTTL,
		),
		lockKey, instanceID,
	).ScanCAS(&holder)
	return applied, err
}

func releaseLock(session *gocql.Session, instanceID string) error {
	applied, err := session.Query(
		`DELETE FROM schema_migrations_lock WHERE lock_key = ? IF holder = ?`,
		lockKey, instanceID,
	).ScanCAS()
	if err != nil {
		return err
	}
	if !applied {
		return errors.New("lock not held by this instance")
	}
	return nil
}
