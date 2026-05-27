// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3"
	"github.com/scylladb/gocqlx/v3/migrate"
	"go.uber.org/zap"
)

const (
	lockKey        = "migration"
	lockTTL        = 120 // seconds — self-expiry if holder crashes
	lockMaxRetries = 3
	lockRetryBase  = 2 * time.Second
)

// RunMigrations ensures the migration lock table exists, acquires a distributed
// LWT lock, applies all pending CQL migrations via gocqlx/migrate, then
// releases the lock. The session must already be scoped to the target keyspace.
// instanceID should be a unique string per process (e.g. a UUID).
func RunMigrations(ctx context.Context, rawSession *gocql.Session, keyspace, instanceID string, log *zap.Logger) error {
	if err := rawSession.Query(
		`CREATE TABLE IF NOT EXISTS schema_migrations_lock (
			lock_key    text PRIMARY KEY,
			holder      text,
			acquired_at timestamp
		)`,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("create lock table: %w", err)
	}

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

	callbackLog := newMigrationCallbackLogger(log, keyspace)
	log.Info("running CQL migrations")

	// Register callbacks used in migration files.
	reg := migrate.CallbackRegister{}
	reg.Add(migrate.CallComment, "log_tables", callbackLog)
	// wait_schema: used between CREATE TABLE and CREATE INDEX in migration files.
	// On single-node Scylla with developer mode, AwaitSchemaAgreement returns
	// immediately (no peers to agree with), so gocql's schema cache may lag.
	// A brief sleep allows the schema event handler goroutine to process the
	// preceding CREATE TABLE before the CREATE INDEX references the new table.
	reg.Add(migrate.CallComment, "wait_schema", func(ctx context.Context, _ gocqlx.Session, _ migrate.CallbackEvent, _ string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return nil
		}
	})
	migrate.Callback = reg.Callback

	session := gocqlx.NewSession(rawSession)

	pending, err := migrate.Pending(ctx, session, migrations.Files)
	if err != nil {
		return fmt.Errorf("pending: %w", err)
	}
	log.Info("pending migrations", zap.Int("count", len(pending)))

	if err := migrate.FromFS(ctx, session, migrations.Files); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	log.Info("migrations complete")
	return nil
}

func newMigrationCallbackLogger(log *zap.Logger, keyspace string) migrate.CallbackFunc {
	return func(_ context.Context, _ gocqlx.Session, _ migrate.CallbackEvent, name string) error {
		log.Info("migration callback", zap.String("call", name), zap.String("keyspace", keyspace))
		return nil
	}
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
	// MapScanCAS handles variable result shapes: on success (applied=true) the
	// map is empty; on conflict (applied=false) it holds the existing row columns.
	dest := make(map[string]any)
	applied, err := session.Query(
		fmt.Sprintf(
			`INSERT INTO schema_migrations_lock (lock_key, holder, acquired_at)
			 VALUES (?, ?, ?)
			 IF NOT EXISTS USING TTL %d`,
			lockTTL,
		),
		lockKey, instanceID, time.Now(),
	).MapScanCAS(dest)
	return applied, err
}

func releaseLock(session *gocql.Session, instanceID string) error {
	// MapScanCAS handles variable result shapes on conditional DELETE.
	dest := make(map[string]any)
	applied, err := session.Query(
		`DELETE FROM schema_migrations_lock WHERE lock_key = ? IF holder = ?`,
		lockKey, instanceID,
	).MapScanCAS(dest)
	if err != nil {
		return err
	}
	if !applied {
		return errors.New("lock not held by this instance")
	}
	return nil
}
