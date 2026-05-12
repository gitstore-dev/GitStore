// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

// Wires the contract suite against the ScyllaDB backend using testcontainers.

package datastore_contract_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

func newScyllaDatastore(t *testing.T) datastore.Datastore {
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

	cfg := config.ScyllaConfig{
		Hosts:    []string{host + ":" + port.Port()},
		Keyspace: "gitstore",
	}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestContractScylla(t *testing.T) {
	RunContractSuite(t, newScyllaDatastore(t))
}
