// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
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

// RunMigrations ensures the migration lock table exists, acquires a distributed
// LWT lock, applies all pending CQL migrations via gocqlx/migrate, then
// releases the lock. The session must already be scoped to the target keyspace.
// instanceID should be a unique string per process (e.g. a UUID).
func RunMigrations(ctx context.Context, rawSession *gocql.Session, instanceID, keyspace string, log *zap.Logger) error {
	if err := rawSession.Query(
		`CREATE TABLE IF NOT EXISTS schema_migrations_lock ` +
			`(lock_key text PRIMARY KEY, holder text, acquired_at timestamp)`,
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

	log.Info("running CQL migrations")

	// migrate.FromFS expects a flat FS of *.cql files; sub into the migrations/ dir.
	migrationsFS, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("migrations sub-fs: %w", err)
	}

	// Register a callback for "-- CALL await_tables;" in the migration file.
	// On single-node --developer-mode=1 ScyllaDB, schema agreement returns
	// immediately, but the storage engine for new tables may not be ready;
	// we poll until a harmless SELECT succeeds on every table in the list.
	reg := migrate.CallbackRegister{}
	reg.Add(migrate.CallComment, "await_tables", awaitTablesCallback(rawSession, keyspace, log))
	migrate.Callback = reg.Callback

	session := gocqlx.NewSession(rawSession)
	if err := migrate.FromFS(ctx, session, migrationsFS); err != nil {
		migrate.Callback = nil
		return fmt.Errorf("apply migrations: %w", err)
	}
	migrate.Callback = nil

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

// awaitTablesCallback returns a migrate.CallbackFunc that polls each core table
// with a SELECT COUNT(*) until all of them respond without an error.
// On single-node --developer-mode=1 ScyllaDB, AwaitSchemaAgreement returns
// immediately while the storage engine is still initialising new tables.
func awaitTablesCallback(session *gocql.Session, keyspace string, log *zap.Logger) migrate.CallbackFunc {
	tables := []string{
		keyspace + ".products",
		keyspace + ".categories",
		keyspace + ".collections",
	}
	return func(ctx context.Context, _ gocqlx.Session, ev migrate.CallbackEvent, _ string) error {
		if ev != migrate.CallComment {
			return nil
		}
		deadline := time.Now().Add(30 * time.Second)
		for _, tbl := range tables {
			for {
				var count int
				err := session.Query("SELECT COUNT(*) FROM " + tbl).
					WithContext(ctx).Scan(&count)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("await_tables: table %s not ready after 30s: %w", tbl, err)
				}
				log.Debug("await_tables: table not ready, retrying",
					zap.String("table", tbl), zap.Error(err))
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
			}
		}
		return nil
	}
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
