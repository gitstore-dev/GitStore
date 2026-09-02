// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

// migration_test.go shares the TestMain / scyllaAddr from backend_test.go.

import (
	"context"
	"io/fs"
	"net"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newRawSession(t *testing.T) *gocql.Session {
	t.Helper()
	host, portStr, err := net.SplitHostPort(scyllaAddr)
	if err != nil {
		host = scyllaAddr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cluster := gocql.NewCluster(host)
	if port > 0 {
		cluster.Port = port
	}
	cluster.Keyspace = scyllaKeyspace // keyspace provisioned by TestMain in backend_test.go
	cluster.Consistency = gocql.Quorum
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)
	session, sessErr := cluster.CreateSession()
	require.NoError(t, sessErr)
	t.Cleanup(session.Close)
	return session
}

func TestRunMigrations_AppliesSchema(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()

	err := scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), log)
	require.NoError(t, err)

	// Verify keyspace exists.
	var ksName string
	err = session.Query(`SELECT keyspace_name FROM system_schema.keyspaces WHERE keyspace_name = ?`, scyllaKeyspace).Scan(&ksName)
	require.NoError(t, err)
	assert.Equal(t, scyllaKeyspace, ksName)

	// Verify representative lookup tables exist.
	for _, expectedTable := range []string{
		"products_by_namespace",
		"products_by_name",
		"products_by_uid",
		"category_taxonomy_by_uid",
		"namespaces_by_uid",
		"repositories_by_uid",
		"repositories_by_namespace",
		"repositories_by_bucket",
		"namespace_mappings_by_repository",
		"service_account_assertion_replays",
	} {
		var tblName string
		err = session.Query(
			`SELECT table_name FROM system_schema.tables WHERE keyspace_name = ? AND table_name = ?`,
			scyllaKeyspace, expectedTable,
		).Scan(&tblName)
		require.NoError(t, err, "expected table %s to exist", expectedTable)
		assert.Equal(t, expectedTable, tblName)
	}
}

func TestRunMigrations_RepositoryResourceContractColumns(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()

	require.NoError(t, scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), log))

	expectedColumns := map[string]string{
		"api_version":         "text",
		"kind":                "text",
		"namespace":           "text",
		"uid":                 "uuid",
		"name":                "text",
		"creation_timestamp":  "timestamp",
		"creation_actor":      "text",
		"generation":          "bigint",
		"resource_version":    "text",
		"revision":            "text",
		"labels":              "map<text, text>",
		"annotations":         "map<text, text>",
		"owner_references":    "text",
		"finalizers":          "list<text>",
		"deletion_timestamp":  "timestamp",
		"repository_id":       "uuid",
		"source_path":         "text",
		"git_commit_sha":      "text",
		"git_ref":             "text",
		"spec":                "text",
		"body":                "text",
		"status":              "text",
		"update_timestamp":    "timestamp",
		"update_actor":        "text",
		"max_pack_size_bytes": "bigint",
		"max_file_size_bytes": "bigint",
	}

	for column, expectedType := range expectedColumns {
		t.Run(column, func(t *testing.T) {
			var columnName, columnType string
			err := session.Query(
				`SELECT column_name, type FROM system_schema.columns
				 WHERE keyspace_name = ? AND table_name = 'repositories_by_uid' AND column_name = ?`,
				scyllaKeyspace, column,
			).Scan(&columnName, &columnType)
			require.NoError(t, err, "expected repositories_by_uid.%s to exist", column)
			assert.Equal(t, column, columnName)
			assert.Equal(t, expectedType, columnType)
		})
	}
}

func TestRunMigrations_NamespaceRepositoryFenceColumns(t *testing.T) {
	session := newRawSession(t)
	require.NoError(t, scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), zap.NewNop()))

	for _, column := range []string{"repository_creation_epoch", "pending_repository_creations"} {
		var columnName, columnType string
		err := session.Query(
			`SELECT column_name, type FROM system_schema.columns
			 WHERE keyspace_name = ? AND table_name = 'namespaces_by_uid' AND column_name = ?`,
			scyllaKeyspace,
			column,
		).Scan(&columnName, &columnType)
		require.NoError(t, err)
		assert.Equal(t, column, columnName)
		assert.Equal(t, "bigint", columnType)
	}
}

func TestRunMigrations_CanonicalEnvelopeColumnsMatch(t *testing.T) {
	session := newRawSession(t)
	require.NoError(t, scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), zap.NewNop()))

	tables := []string{
		"products_by_namespace",
		"product_variant_by_namespace",
		"collection",
		"category_taxonomy",
		"repositories_by_uid",
	}
	columns := []string{
		"api_version", "kind", "namespace", "uid", "name", "generation",
		"resource_version", "revision", "creation_timestamp", "creation_actor",
		"update_timestamp", "update_actor", "labels", "annotations",
		"owner_references", "finalizers", "deletion_timestamp", "repository_id",
		"source_path", "git_commit_sha", "git_ref", "spec", "body", "status",
	}
	for _, tableName := range tables {
		for _, column := range columns {
			t.Run(tableName+"/"+column, func(t *testing.T) {
				var got string
				err := session.Query(
					`SELECT column_name FROM system_schema.columns
					 WHERE keyspace_name = ? AND table_name = ? AND column_name = ?`,
					scyllaKeyspace, tableName, column,
				).Scan(&got)
				require.NoError(t, err)
				assert.Equal(t, column, got)
			})
		}
	}
}

func TestRunMigrations_HasNoRepositorySecondaryIndexes(t *testing.T) {
	session := newRawSession(t)
	require.NoError(t, scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), zap.NewNop()))

	iter := session.Query(
		`SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ?`,
		scyllaKeyspace,
	).Iter()
	var index string
	for iter.Scan(&index) {
		assert.NotContains(t, index, "repositories")
		assert.NotContains(t, index, "mappings")
		assert.NotContains(t, index, "service_accounts")
	}
	require.NoError(t, iter.Close())
}

// TestRunMigrations_ServiceAccountSchemaMatchesEnvelopeConventions asserts
// spec 061's service_accounts_* tables (a) create no secondary index and
// (b) use the canonical envelope column names/types established by 002's
// query-first pattern (creation_timestamp/update_timestamp/deletion_timestamp,
// creation_actor/update_actor, generation bigint, resource_version text, and
// uid typed uuid rather than text) — the existing index test above is scoped
// to repositories/mappings names only, so it would not have caught a
// service_accounts index on its own.
func TestRunMigrations_ServiceAccountSchemaMatchesEnvelopeConventions(t *testing.T) {
	session := newRawSession(t)
	require.NoError(t, scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), zap.NewNop()))

	iter := session.Query(
		`SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ? AND table_name LIKE 'service_accounts%' ALLOW FILTERING`,
		scyllaKeyspace,
	).Iter()
	var index string
	count := 0
	for iter.Scan(&index) {
		count++
	}
	require.NoError(t, iter.Close())
	assert.Zero(t, count, "service_accounts_* tables must have zero secondary indexes (query-first pattern)")

	wantColumnTypes := map[string]string{
		"creation_timestamp": "timestamp",
		"update_timestamp":   "timestamp",
		"deletion_timestamp": "timestamp",
		"creation_actor":     "text",
		"update_actor":       "text",
		"generation":         "bigint",
		"resource_version":   "text",
		"uid":                "uuid",
	}

	iter = session.Query(
		`SELECT column_name, type FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?`,
		scyllaKeyspace, "service_accounts_by_namespace",
	).Iter()
	var columnName, columnType string
	seen := map[string]string{}
	for iter.Scan(&columnName, &columnType) {
		seen[columnName] = columnType
	}
	require.NoError(t, iter.Close())

	for column, wantType := range wantColumnTypes {
		gotType, ok := seen[column]
		require.Truef(t, ok, "expected column %q on service_accounts_by_namespace", column)
		assert.Equalf(t, wantType, gotType, "column %q type mismatch", column)
	}
}

func TestRunMigrations_DoesNotMaterializeProductLabelSelectors(t *testing.T) {
	session := newRawSession(t)
	require.NoError(t, scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), zap.NewNop()))

	iter := session.Query(
		`SELECT table_name FROM system_schema.tables WHERE keyspace_name = ?`,
		scyllaKeyspace,
	).Iter()
	var tableName string
	for iter.Scan(&tableName) {
		assert.NotContains(t, tableName, "product_label")
		assert.NotContains(t, tableName, "product_selector")
	}
	require.NoError(t, iter.Close())

	iter = session.Query(
		`SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ?`,
		scyllaKeyspace,
	).Iter()
	var indexName string
	for iter.Scan(&indexName) {
		assert.NotContains(t, indexName, "product_label")
		assert.NotContains(t, indexName, "product_selector")
	}
	require.NoError(t, iter.Close())
}

func TestRunMigrations_UsesTenDayGCGrace(t *testing.T) {
	session := newRawSession(t)
	require.NoError(t, scylla.RunMigrations(context.Background(), session, scyllaKeyspace, uuid.New().String(), zap.NewNop()))

	iter := session.Query(
		`SELECT table_name, gc_grace_seconds FROM system_schema.tables WHERE keyspace_name = ?`,
		scyllaKeyspace,
	).Iter()
	var tableName string
	var gcGraceSeconds int
	for iter.Scan(&tableName, &gcGraceSeconds) {
		if strings.HasSuffix(tableName, "$paxos") || strings.HasPrefix(tableName, "schema_migrations") {
			continue
		}
		if tableName == "namespaces_by_uid_scylla_cdc_log" {
			assert.Equalf(t, 0, gcGraceSeconds, "Scylla-managed CDC log %s", tableName)
			continue
		}
		assert.Equalf(t, 864000, gcGraceSeconds, "table %s", tableName)
	}
	require.NoError(t, iter.Close())
}

func TestRunMigrations_Idempotent(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()
	ctx := context.Background()

	// Running migrations twice must not return an error.
	require.NoError(t, scylla.RunMigrations(ctx, session, scyllaKeyspace, uuid.New().String(), log))
	require.NoError(t, scylla.RunMigrations(ctx, session, scyllaKeyspace, uuid.New().String(), log))
}

func TestRunMigrations_SupportedRollbackArtifactRetainsForwardMigrationSet(t *testing.T) {
	session := newRawSession(t)
	ctx := context.Background()
	log := zap.NewNop()

	require.NoError(t, scylla.RunMigrations(ctx, session, scyllaKeyspace, uuid.New().String(), log))

	legacyBinaryMigrations := migrationSetThrough(t, "004_file_resource.cql")
	err := scylla.RunMigrationsWithFS(
		ctx,
		session,
		scyllaKeyspace,
		uuid.New().String(),
		log,
		legacyBinaryMigrations,
	)
	require.ErrorContains(t, err, "database is ahead")

	preWatchBinaryMigrations := migrationSetThrough(t, "005_namespace_repository_fence.cql")
	err = scylla.RunMigrationsWithFS(
		ctx,
		session,
		scyllaKeyspace,
		uuid.New().String(),
		log,
		preWatchBinaryMigrations,
	)
	require.ErrorContains(t, err, "database is ahead")

	preServiceAccountBinaryMigrations := migrationSetThrough(t, "006_namespace_watch_cdc.cql")
	err = scylla.RunMigrationsWithFS(
		ctx,
		session,
		scyllaKeyspace,
		uuid.New().String(),
		log,
		preServiceAccountBinaryMigrations,
	)
	require.ErrorContains(t, err, "database is ahead")

	supportedRollbackMigrations := migrationSetThrough(t, "009_service_account_assertion_replay.cql")
	require.NoError(t, scylla.RunMigrationsWithFS(
		ctx,
		session,
		scyllaKeyspace,
		uuid.New().String(),
		log,
		supportedRollbackMigrations,
	))
}

func migrationSetThrough(t *testing.T, last string) fstest.MapFS {
	t.Helper()
	entries, err := fs.ReadDir(migrations.Files, ".")
	require.NoError(t, err)
	files := make(fstest.MapFS)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cql") || entry.Name() > last {
			continue
		}
		content, readErr := fs.ReadFile(migrations.Files, entry.Name())
		require.NoError(t, readErr)
		files[entry.Name()] = &fstest.MapFile{Data: content, Mode: 0o444}
	}
	require.Contains(t, files, last)
	return files
}

func TestRunMigrations_LockReleasedAfterSuccess(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()
	ctx := context.Background()

	require.NoError(t, scylla.RunMigrations(ctx, session, scyllaKeyspace, uuid.New().String(), log))

	// After success the lock row must be gone (deleted by releaseLock).
	var holder string
	err := session.Query(
		`SELECT holder FROM schema_migrations_lock WHERE lock_key = 'migration'`,
	).Scan(&holder)
	// ErrNotFound means the row was deleted, which is what we want.
	assert.ErrorIs(t, err, gocql.ErrNotFound)
}
