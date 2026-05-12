// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

func newRawSession(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "scylladb/scylla:5.4",
		ExposedPorts: []string{"9042/tcp"},
		Cmd:          []string{"--developer-mode=1", "--overprovisioned=1"},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("9042/tcp"),
			wait.ForLog("Starting listening for CQL clients").
				WithStartupTimeout(120*time.Second),
		),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "9042")
	require.NoError(t, err)

	cluster := gocql.NewCluster(host + ":" + port.Port())
	cluster.Consistency = gocql.Quorum
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(session.Close)
	return session
}

func TestRunMigrations_AppliesSchema(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()

	err := scylla.RunMigrations(context.Background(), session, uuid.New().String(), log)
	require.NoError(t, err)

	// Verify keyspace exists.
	var ksName string
	err = session.Query(
		`SELECT keyspace_name FROM system_schema.keyspaces WHERE keyspace_name = 'gitstore'`,
	).Scan(&ksName)
	require.NoError(t, err)
	assert.Equal(t, "gitstore", ksName)

	// Verify products table exists.
	var tblName string
	err = session.Query(
		`SELECT table_name FROM system_schema.tables WHERE keyspace_name = 'gitstore' AND table_name = 'products'`,
	).Scan(&tblName)
	require.NoError(t, err)
	assert.Equal(t, "products", tblName)
}

func TestRunMigrations_Idempotent(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()
	ctx := context.Background()

	// Running migrations twice must not return an error.
	require.NoError(t, scylla.RunMigrations(ctx, session, uuid.New().String(), log))
	require.NoError(t, scylla.RunMigrations(ctx, session, uuid.New().String(), log))
}

func TestRunMigrations_LockReleasedAfterSuccess(t *testing.T) {
	session := newRawSession(t)
	log := zap.NewNop()
	ctx := context.Background()

	require.NoError(t, scylla.RunMigrations(ctx, session, uuid.New().String(), log))

	// After success the lock row must be gone (deleted by releaseLock).
	var holder string
	err := session.Query(
		`SELECT holder FROM gitstore.schema_migrations_lock WHERE lock_key = 'migration'`,
	).Scan(&holder)
	// ErrNotFound means the row was deleted, which is what we want.
	assert.ErrorIs(t, err, gocql.ErrNotFound)
}
