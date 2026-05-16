// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

// migration_test.go shares the TestMain / scyllaAddr from backend_test.go.

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
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
	session, sessErr := cluster.CreateSession()
	require.NoError(t, sessErr)
	t.Cleanup(session.Close)
	return session
}

func TestRunMigrations_AppliesSchema(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()

	err := scylla.RunMigrations(context.Background(), session, uuid.New().String(), scyllaKeyspace, log)
	require.NoError(t, err)

	// Verify keyspace exists.
	var ksName string
	err = session.Query(`SELECT keyspace_name FROM system_schema.keyspaces WHERE keyspace_name = ?`, scyllaKeyspace).Scan(&ksName)
	require.NoError(t, err)
	assert.Equal(t, scyllaKeyspace, ksName)

	// Verify products table exists.
	var tblName string
	err = session.Query(
		`SELECT table_name FROM system_schema.tables WHERE keyspace_name = ? AND table_name = 'products'`,
		scyllaKeyspace,
	).Scan(&tblName)
	require.NoError(t, err)
	assert.Equal(t, "products", tblName)
}

func TestRunMigrations_Idempotent(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()
	ctx := context.Background()

	// Running migrations twice must not return an error.
	require.NoError(t, scylla.RunMigrations(ctx, session, uuid.New().String(), scyllaKeyspace, log))
	require.NoError(t, scylla.RunMigrations(ctx, session, uuid.New().String(), scyllaKeyspace, log))
}

func TestRunMigrations_LockReleasedAfterSuccess(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()
	ctx := context.Background()

	require.NoError(t, scylla.RunMigrations(ctx, session, uuid.New().String(), scyllaKeyspace, log))

	// After success the lock row must be gone (deleted by releaseLock).
	var holder string
	err := session.Query(
		fmt.Sprintf(`SELECT holder FROM %s.schema_migrations_lock WHERE lock_key = 'migration'`, scyllaKeyspace),
	).Scan(&holder)
	// ErrNotFound means the row was deleted, which is what we want.
	assert.ErrorIs(t, err, gocql.ErrNotFound)
}
