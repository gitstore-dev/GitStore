// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

// Wires the contract suite against the ScyllaDB backend using testcontainers.
// A single container is started in TestMain and shared across all tests.

package datastore_contract_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

// scyllaContainerAddr is set by TestMain before any test runs.
var scyllaContainerAddr string

func TestMain(m *testing.M) {
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "scylladb/scylla:5.4",
		ExposedPorts: []string{"9042/tcp"},
		Cmd:          []string{"--developer-mode=1", "--overprovisioned=1", "--smp=1"},
		// ForExec verifies cqlsh can actually connect, not just that the log appeared.
		WaitingFor: wait.ForExec([]string{"cqlsh", "-e", "describe cluster"}).
			WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		panic("failed to start ScyllaDB container: " + err.Error())
	}

	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9042")
	scyllaContainerAddr = host + ":" + port.Port()

	// Provision keyspace — mirrors the compose scylla-init service.
	provisionKeyspace(scyllaContainerAddr, "gitstore")

	code := m.Run()
	_ = c.Terminate(ctx)
	os.Exit(code)
}

func provisionKeyspace(addr, keyspace string) {
	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cluster := gocql.NewCluster(host)
	if port > 0 {
		cluster.Port = port
	}
	cluster.Consistency = gocql.Quorum
	cluster.ConnectTimeout = 5 * time.Second
	cluster.Timeout = 5 * time.Second
	cluster.DisableShardAwarePort = true

	var session *gocql.Session
	var err error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		session, err = cluster.CreateSession()
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		panic("provisionKeyspace: open session: " + err.Error())
	}
	defer session.Close()

	stmt := fmt.Sprintf(
		`CREATE KEYSPACE IF NOT EXISTS %s `+
			`WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '1'} `+
			`AND durable_writes = true`,
		keyspace,
	)
	if err := session.Query(stmt).Exec(); err != nil {
		panic("provisionKeyspace: create keyspace: " + err.Error())
	}
}

func newScyllaDatastore(t *testing.T) datastore.Datastore {
	t.Helper()
	cfg := config.ScyllaConfig{
		Hosts:                 []string{scyllaContainerAddr},
		Keyspace:              "gitstore",
		DisableShardAwarePort: true,
	}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestContractScylla(t *testing.T) {
	RunContractSuite(t, newScyllaDatastore(t))
}
