// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

// migration_test.go shares the TestMain / scyllaAddr from backend_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newRawSession(t *testing.T) *gocql.Session {
	return newRawSessionForKeyspace(t, scyllaKeyspace)
}

func newRawSessionForKeyspace(t *testing.T, keyspace string) *gocql.Session {
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
	cluster.Keyspace = keyspace
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
		"products_by_key",
		"products_by_identity",
		"products_by_namespace_shard",
		"namespace_catalog_fences_by_uid",
		"products_by_uid",
		"category_taxonomies_by_uid",
		"namespaces_by_uid",
		"repositories_by_uid",
		"repositories_by_namespace",
		"repositories_by_bucket",
		"namespace_mappings_by_repository",
		"service_account_assertion_replays",
		"service_accounts_by_cluster_bucket",
		"category_ancestor_index",
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
		"products_by_key",
		"product_variants_by_namespace",
		"collections_by_namespace",
		"category_taxonomies_by_namespace",
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
		scyllaKeyspace, "service_accounts_by_cluster_name",
	).Iter()
	var columnName, columnType string
	seen := map[string]string{}
	for iter.Scan(&columnName, &columnType) {
		seen[columnName] = columnType
	}
	require.NoError(t, iter.Close())

	for column, wantType := range wantColumnTypes {
		gotType, ok := seen[column]
		require.Truef(t, ok, "expected column %q on service_accounts_by_cluster_name", column)
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
		if strings.HasSuffix(tableName, "_scylla_cdc_log") {
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

func TestSchemaValidationDoesNotWriteOrIgnorePartialMigrations(t *testing.T) {
	session := newRawSession(t)
	cfg := testScyllaConfig(t)
	require.NoError(t, scylla.Migrate(t.Context(), cfg, zap.NewNop()))
	cfg.AutoMigrate = false
	readWrites := func() map[string]int64 {
		result := make(map[string]int64)
		iter := session.Query("SELECT name, writetime(done) FROM gocqlx_migrate").Iter()
		var name string
		var written int64
		for iter.Scan(&name, &written) {
			result[name] = written
		}
		require.NoError(t, iter.Close())
		return result
	}
	before := readWrites()
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.Equal(t, before, readWrites(), "disabled auto-migration must not rewrite the ledger")

	const name = "009_service_account.cql"
	var done int
	require.NoError(t, session.Query("SELECT done FROM gocqlx_migrate WHERE name=?", name).Scan(&done))
	require.NoError(t, session.Query("UPDATE gocqlx_migrate SET done=? WHERE name=?", done-1, name).Exec())
	t.Cleanup(func() {
		assert.NoError(t, session.Query("UPDATE gocqlx_migrate SET done=? WHERE name=?", done, name).Exec())
	})
	store, err = scylla.New(cfg, zap.NewNop())
	require.ErrorContains(t, err, "incompatible schema")
	require.Nil(t, store)
}

func TestServiceAccountCDCUpgradePreservesDataAndMigrationHistory(t *testing.T) {
	root, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(root.Close)
	keyspace := "gitstore_scylla_test_cdc_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	require.NoError(t, root.Query("CREATE KEYSPACE "+keyspace+
		" WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '1'}").Exec())
	t.Cleanup(func() {
		assert.NoError(t, root.Query("DROP KEYSPACE "+keyspace).Exec())
	})
	session := newRawSessionForKeyspace(t, keyspace)
	baseline := migrationSetThrough(t, "012_repository_resource_membership.cql")
	require.NoError(t, scylla.RunMigrationsWithFS(t.Context(), session, keyspace, uuid.NewString(), zap.NewNop(), baseline))

	type ledgerEntry struct {
		checksum string
		done     int
		written  int64
	}

	readHistory := func() map[string]ledgerEntry {
		history := make(map[string]ledgerEntry)
		iter := session.Query("SELECT name, checksum, done, writetime(done) FROM gocqlx_migrate").Iter()
		var name string
		var entry ledgerEntry
		for iter.Scan(&name, &entry.checksum, &entry.done, &entry.written) {
			history[name] = entry
		}
		require.NoError(t, iter.Close())
		return history
	}
	before := readHistory()
	require.Len(t, before, 12)
	uid := gocql.TimeUUID()
	require.NoError(t, session.Query(`INSERT INTO service_accounts_by_cluster_name
		(name, uid, generation, resource_version, enrollment_complete, public_keys)
		VALUES (?, ?, ?, ?, ?, ?)`, "existing-ci-bot", uid, int64(1), "1", true, "[]").Exec())
	require.ErrorContains(t, scylla.ValidateSchema(t.Context(), session), "incompatible schema")
	require.Equal(t, before, readHistory(), "read-only validation must not apply the pending migration")

	require.NoError(t, scylla.RunMigrations(t.Context(), session, keyspace, uuid.NewString(), zap.NewNop()))
	after := readHistory()
	require.Len(t, after, 14)
	for name, entry := range before {
		require.Equal(t, entry, after[name], "upgrade must preserve the applied ledger for %s", name)
	}
	require.Contains(t, after, "013_service_account_cdc.cql")
	require.Contains(t, after, "014_rbac.cql")
	var retained gocql.UUID
	require.NoError(t, session.Query("SELECT uid FROM service_accounts_by_cluster_name WHERE name=?", "existing-ci-bot").Scan(&retained))
	require.Equal(t, uid, retained, "enabling CDC must not replace existing identities")
	var logTable string
	require.NoError(t, session.Query("SELECT table_name FROM system_schema.tables WHERE keyspace_name=? AND table_name=?",
		keyspace, "service_accounts_by_cluster_name_scylla_cdc_log").Scan(&logTable))

	cfg := testScyllaConfig(t)
	cfg.Keyspace, cfg.AutoMigrate = keyspace, false
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.Equal(t, after, readHistory(), "read-only startup must not rewrite migration history")
	require.ErrorContains(t, scylla.RunMigrationsWithFS(t.Context(), session, keyspace, uuid.NewString(), zap.NewNop(), baseline),
		"database is ahead")
	require.NoError(t, scylla.RunMigrations(t.Context(), session, keyspace, uuid.NewString(), zap.NewNop()))
	require.Equal(t, after, readHistory(), "rerunning the forward bundle must preserve applied history")
}

func TestRBACMigrationRetainsLegacyAuditPayloadAndLedger(t *testing.T) {
	root, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(root.Close)
	keyspace := "gitstore_scylla_test_audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, root.Query("CREATE KEYSPACE "+keyspace+
		" WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '1'}").Exec())
	t.Cleanup(func() {
		assert.NoError(t, root.Query("DROP KEYSPACE "+keyspace).SetRequestTimeout(30*time.Second).Exec())
	})
	session := newRawSessionForKeyspace(t, keyspace)
	require.NoError(t, scylla.RunMigrationsWithFS(t.Context(), session, keyspace, uuid.NewString(),
		zap.NewNop(), migrationSetThrough(t, "014_rbac.cql")))
	readLedger := func() map[string]string {
		result := map[string]string{}
		iter := session.Query("SELECT name,checksum,writetime(done) FROM gocqlx_migrate").Iter()
		var name, checksum string
		var written int64
		for iter.Scan(&name, &checksum, &written) {
			result[name] = fmt.Sprintf("%s/%d", checksum, written)
		}
		require.NoError(t, iter.Close())
		return result
	}
	before := readLedger()
	require.Len(t, before, 14)
	tier, uid := uuid.NewString(), uuid.NewString()
	created := time.Unix(1_700_000_000, 0).UTC()
	resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: tier,
		Metadata: rbac.Metadata{Name: "legacy", Namespace: "contract", UID: uid,
			Generation: 1, ResourceVersion: "1", CreationTimestamp: &created},
		Spec: json.RawMessage(`{"rules":[]}`)}
	payload, err := json.Marshal(resource)
	require.NoError(t, err)
	require.NoError(t, session.Query(`INSERT INTO rbac_resources_by_scope
		(tier,entry,uid,payload,revision) VALUES (?,?,?,?,?)`, tier, "Role/legacy", uid, payload, int64(1)).Exec())
	require.NoError(t, session.Query(`INSERT INTO rbac_resources_by_scope
		(tier,entry,revision,role_count,binding_count,payload_bytes,projection) VALUES (?,?,?,?,?,?,?)`,
		tier, "", int64(1), 1, 0, int64(len(payload)), []byte(`{}`)).Exec())
	require.NoError(t, scylla.RunMigrations(t.Context(), session, keyspace, uuid.NewString(), zap.NewNop()))
	after := readLedger()
	require.Len(t, after, 14)
	for name, entry := range before {
		require.Equal(t, entry, after[name], "applied migration ledger must remain untouched")
	}
	var retained []byte
	require.NoError(t, session.Query("SELECT payload FROM rbac_resources_by_scope WHERE tier=? AND entry=?",
		tier, "Role/legacy").Scan(&retained))
	require.Equal(t, payload, retained, "migration replay must not backfill or invent audit")
	cfg := testScyllaConfig(t)
	cfg.Keyspace, cfg.AutoMigrate = keyspace, false
	ds, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ds.Close()) })
	store := ds.(datastore.RBACCapable).RBACStore()
	key := rbac.ResourceKey{TierID: tier, Kind: rbac.Role, Name: "legacy"}
	current, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	require.Empty(t, current.Metadata.CreationActor)
	require.Nil(t, current.Metadata.UpdateTimestamp)
	current.Status = json.RawMessage(`{"observedGeneration":1}`)
	_, err = store.ApplyRBAC(datastore.ContextWithRBACAuditActor(t.Context(), "upgrade-controller"), rbac.TierWrite{
		TierID: tier, ExpectedRevision: 1,
		Changes: []rbac.Change{{Resource: current, ExpectedUID: uid, ExpectedResourceVersion: "1"}},
	})
	require.NoError(t, err)
	current, err = store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	require.Empty(t, current.Metadata.CreationActor)
	require.Equal(t, &created, current.Metadata.CreationTimestamp)
	require.Equal(t, "upgrade-controller", current.Metadata.UpdateActor)
	require.NotNil(t, current.Metadata.UpdateTimestamp)
	require.NoError(t, scylla.ValidateSchema(t.Context(), session))
	require.Equal(t, after, readLedger())
}

// The per-resource baseline has no supported rollback below itself: a binary
// embedding only a prefix of the baseline must refuse a newer keyspace, and
// the complete set must remain re-runnable.
func TestRunMigrations_BaselinePrefixRefusesNewerKeyspace(t *testing.T) {
	session := newRawSession(t)
	ctx := context.Background()
	log := zap.NewNop()

	require.NoError(t, scylla.RunMigrations(ctx, session, scyllaKeyspace, uuid.New().String(), log))

	err := scylla.RunMigrationsWithFS(ctx, session, scyllaKeyspace, uuid.New().String(), log,
		migrationSetThrough(t, "009_service_account.cql"))
	require.ErrorContains(t, err, "database is ahead")

	require.NoError(t, scylla.RunMigrationsWithFS(ctx, session, scyllaKeyspace, uuid.New().String(), log,
		migrations.Files))
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
