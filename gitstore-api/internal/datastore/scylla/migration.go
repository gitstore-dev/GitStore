// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/scylladb/gocqlx/v3"
	"github.com/scylladb/gocqlx/v3/migrate"
	"go.uber.org/zap"
)

const (
	lockKey             = "migration"
	lockTable           = "schema_migrations_lock"
	lockTTL             = 120 // seconds — self-expiry if holder crashes
	lockMaxRetries      = 3
	lockRetryBase       = 2 * time.Second
	schemaReadyTimeout  = 60 * time.Second
	schemaRetryBase     = 100 * time.Millisecond
	schemaRetryMaxDelay = 2 * time.Second
)

var migrationLibraryMu sync.Mutex

// Migrate prepares an existing keyspace without starting API listeners, CDC
// readers, or controllers. It uses the same fenced startup migration path.
func Migrate(ctx context.Context, cfg config.ScyllaConfig, log *zap.Logger) error {
	session, err := openSession(cfg)
	if err != nil {
		return err
	}
	defer session.Close()
	if err := RunMigrations(ctx, session, cfg.Keyspace, uuid.New().String(), log); err != nil {
		return err
	}
	return ValidateSchema(ctx, session)
}

// ValidateSchema is read-only. migrate.List/Pending cannot be used here because
// even listing through that library first executes CREATE TABLE IF NOT EXISTS.
func ValidateSchema(ctx context.Context, session *gocql.Session) error {
	files, err := fs.Glob(migrations.Files, "*.cql")
	if err != nil {
		return fmt.Errorf("scylla: read embedded migrations: %w", err)
	}
	if err := session.AwaitSchemaAgreement(ctx); err != nil {
		return fmt.Errorf("scylla: schema agreement: %w", err)
	}
	var history []migrate.Info
	query := fmt.Sprintf("SELECT name, checksum, done, start_time, end_time FROM gocqlx_migrate LIMIT %d", len(files)+1)
	if err := gocqlx.NewSession(session).ContextQuery(ctx, query, nil).SelectRelease(&history); err != nil {
		return fmt.Errorf("scylla: read migration history (prepare the keyspace with gitctl migrate before API startup): %w", err)
	}
	if err := validateMigrationHistory(history, migrations.Files); err != nil {
		return fmt.Errorf("scylla: incompatible schema: %w; prepare a compatible keyspace with the matching gitctl migrate", err)
	}
	return nil
}

func validateMigrationHistory(history []migrate.Info, bundle fs.FS) error {
	files, err := fs.Glob(bundle, "*.cql")
	if err != nil {
		return err
	}
	if len(files) == 0 || len(history) != len(files) {
		return fmt.Errorf("expected %d completed migrations, found %d", len(files), len(history))
	}
	sort.Strings(files)
	sort.Slice(history, func(i, j int) bool { return history[i].Name < history[j].Name })
	for i, name := range files {
		data, err := fs.ReadFile(bundle, name)
		if err != nil {
			return err
		}
		// Match gocqlx's ledger format, not a security/integrity signature.
		checksum := fmt.Sprintf("%x", md5.Sum(data))
		statements := strings.Count(string(data), ";")
		if strings.TrimSpace(string(data)[strings.LastIndex(string(data), ";")+1:]) != "" {
			statements++
		}
		applied := history[i]
		if applied.Name != name || applied.Checksum != checksum || applied.Done != statements || applied.EndTime.IsZero() {
			return fmt.Errorf("migration %s is missing, changed, or incomplete", name)
		}
	}
	return nil
}

// RunMigrations ensures the migration lock table exists, acquires a distributed
// LWT lock, applies all pending CQL migrations via gocqlx/migrate, then
// releases the lock. The session must already be scoped to the target keyspace.
//
// ScyllaDB schema changes are distributed metadata updates. The runner keeps
// migration authoring delegated to gocqlx/migrate, while enabling the library's
// per-statement schema-agreement wait for Scylla's distributed schema metadata.
// instanceID should be a unique string per process (e.g. a UUID).
func RunMigrations(ctx context.Context, rawSession *gocql.Session, keyspace, instanceID string, log *zap.Logger) error {
	return RunMigrationsWithFS(ctx, rawSession, keyspace, instanceID, log, migrations.Files)
}

// RunMigrationsWithFS runs the normal startup migration path with an explicit
// embedded migration bundle. Release tooling and tests use it to verify that a
// behavior rollback artifact retained every already-applied forward migration.
func RunMigrationsWithFS(
	ctx context.Context,
	rawSession *gocql.Session,
	keyspace, instanceID string,
	log *zap.Logger,
	migrationFiles fs.FS,
) error {
	if log == nil {
		log = zap.NewNop()
	}
	if migrationFiles == nil {
		return errors.New("migration files are required")
	}

	if err := execSchemaStatement(ctx, rawSession, log,
		`CREATE TABLE IF NOT EXISTS schema_migrations_lock (
			lock_key    text PRIMARY KEY,
			holder      text,
			acquired_at timestamp
		)`,
	); err != nil {
		return fmt.Errorf("create lock table: %w", err)
	}
	if err := waitForTable(ctx, rawSession, keyspace, lockTable); err != nil {
		return fmt.Errorf("wait for lock table: %w", err)
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

	session := gocqlx.NewSession(rawSession)

	log.Info("running CQL migrations")
	if err := applyGocqlxMigrations(ctx, session, migrationFiles); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	log.Info("migrations complete")
	return nil
}

func applyGocqlxMigrations(ctx context.Context, session gocqlx.Session, migrationFiles fs.FS) error {
	migrationLibraryMu.Lock()
	defer migrationLibraryMu.Unlock()

	previousAwait := migrate.DefaultAwaitSchemaAgreement
	migrate.DefaultAwaitSchemaAgreement = migrate.AwaitSchemaAgreementBeforeEachStatement
	defer func() {
		migrate.DefaultAwaitSchemaAgreement = previousAwait
	}()

	return migrate.FromFS(ctx, session, migrationFiles)
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

func execSchemaStatement(ctx context.Context, session *gocql.Session, log *zap.Logger, stmt string, args ...any) error {
	retryCtx, cancel := context.WithTimeout(ctx, schemaReadyTimeout)
	defer cancel()

	if err := session.AwaitSchemaAgreement(retryCtx); err != nil {
		return fmt.Errorf("await schema agreement before statement: %w", err)
	}

	if err := execTransientStatement(retryCtx, session, log, stmt, args...); err != nil {
		return err
	}

	if err := session.AwaitSchemaAgreement(retryCtx); err != nil {
		return fmt.Errorf("await schema agreement after statement: %w", err)
	}
	return nil
}

func execTransientStatement(ctx context.Context, session *gocql.Session, log *zap.Logger, stmt string, args ...any) error {
	delay := schemaRetryBase
	var lastErr error

	for {
		err := session.Query(stmt, args...).WithContext(ctx).RetryPolicy(nil).Exec()
		if err == nil {
			return nil
		}
		lastErr = err

		if !isTransientSchemaError(err) {
			return err
		}

		log.Debug("transient schema visibility error, retrying statement",
			zap.Error(err),
			zap.Duration("delay", delay),
		)

		if awaitErr := session.AwaitSchemaAgreement(ctx); awaitErr != nil && ctx.Err() != nil {
			return awaitErr
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: last schema error: %v", ctx.Err(), lastErr)
		case <-timer.C:
		}

		delay *= 2
		if delay > schemaRetryMaxDelay {
			delay = schemaRetryMaxDelay
		}
	}
}

func waitForTable(ctx context.Context, session *gocql.Session, keyspace, table string) error {
	retryCtx, cancel := context.WithTimeout(ctx, schemaReadyTimeout)
	defer cancel()

	delay := schemaRetryBase
	for {
		if err := session.AwaitSchemaAgreement(retryCtx); err != nil && retryCtx.Err() != nil {
			return err
		}

		var tableName string
		err := session.Query(
			`SELECT table_name FROM system_schema.tables WHERE keyspace_name = ? AND table_name = ?`,
			keyspace,
			table,
		).WithContext(retryCtx).Scan(&tableName)
		if err == nil && tableName == table {
			return nil
		}
		if err != nil && !errors.Is(err, gocql.ErrNotFound) && !isTransientSchemaError(err) {
			return err
		}

		timer := time.NewTimer(delay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return fmt.Errorf("timed out waiting for table %s.%s: %w", keyspace, table, retryCtx.Err())
		case <-timer.C:
		}

		delay *= 2
		if delay > schemaRetryMaxDelay {
			delay = schemaRetryMaxDelay
		}
	}
}

func isTransientSchemaError(err error) bool {
	var requestErr gocql.RequestError
	if errors.As(err, &requestErr) {
		switch requestErr.Code() {
		case gocql.ErrCodeInvalid, gocql.ErrCodeConfig, gocql.ErrCodeServer:
		default:
			return false
		}
		return isTransientSchemaMessage(requestErr.Message())
	}

	return isTransientSchemaMessage(err.Error())
}

func isTransientSchemaMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "unconfigured table") ||
		strings.Contains(msg, "unconfigured columnfamily") ||
		strings.Contains(msg, "unconfigured keyspace")
}
