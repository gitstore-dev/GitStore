// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

// Wires the contract suite against an externally managed ScyllaDB instance.

package datastore_contract_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var (
	scyllaAddr              string
	scyllaKeyspaceSequence  atomic.Uint64
	scyllaReplicationFactor = 1
)

const scyllaSchemaTimeout = time.Minute

func TestMain(m *testing.M) {
	scyllaAddr = os.Getenv("GITSTORE_TEST_SCYLLA_ADDR")
	if scyllaAddr == "" {
		scyllaAddr = "127.0.0.1:9042"
	}
	if raw := os.Getenv("GITSTORE_TEST_SCYLLA_REPLICATION_FACTOR"); raw != "" {
		factor, err := strconv.Atoi(raw)
		if err != nil || factor < 1 {
			panic("GITSTORE_TEST_SCYLLA_REPLICATION_FACTOR must be a positive integer")
		}
		scyllaReplicationFactor = factor
	}
	code := m.Run()
	os.Exit(code)
}

// contactPointTranslator redirects all peer addresses to the contact point,
// needed when Scylla runs behind Docker NAT where rpc_address is an internal IP.
func contactPointTranslator(contactHost string, contactPort int) gocql.AddressTranslator {
	contactIP := net.ParseIP(contactHost)
	return gocql.AddressTranslatorFunc(func(_ net.IP, port int) (net.IP, int) {
		if contactPort > 0 {
			port = contactPort
		}
		return contactIP, port
	})
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
	cluster.Timeout = scyllaSchemaTimeout
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)

	var session *gocql.Session
	var err error
	deadline := time.Now().Add(60 * time.Second)
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
			`WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '%d'} `+
			`AND durable_writes = true`,
		keyspace, scyllaReplicationFactor,
	)
	if err := session.Query(stmt).Exec(); err != nil {
		panic("provisionKeyspace: create keyspace: " + err.Error())
	}
	if err := session.AwaitSchemaAgreement(context.Background()); err != nil {
		panic("provisionKeyspace: await schema agreement: " + err.Error())
	}
	var replication map[string]string
	if err := session.Query("SELECT replication FROM system_schema.keyspaces WHERE keyspace_name = ?", keyspace).Scan(&replication); err != nil {
		panic("provisionKeyspace: read replication: " + err.Error())
	}
	if len(replication) < 2 {
		panic("provisionKeyspace: server returned no datacenter replication setting")
	}
	for datacenter, factor := range replication {
		if datacenter != "class" && factor != strconv.Itoa(scyllaReplicationFactor) {
			panic(fmt.Sprintf("provisionKeyspace: %s replication is %s, expected %d", datacenter, factor, scyllaReplicationFactor))
		}
	}
	fmt.Printf("Scylla contract keyspace %s replication=%v\n", keyspace, replication)
}

func dropKeyspace(addr, keyspace string) error {
	session, err := openRootSession(addr)
	if err != nil {
		return err
	}
	defer session.Close()
	if err := session.Query(fmt.Sprintf(`DROP KEYSPACE IF EXISTS %s`, keyspace)).Exec(); err != nil {
		return err
	}
	return session.AwaitSchemaAgreement(context.Background())
}

func openRootSession(addr string) (*gocql.Session, error) {
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
	cluster.Timeout = scyllaSchemaTimeout
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)
	return cluster.CreateSession()
}

func newScyllaDatastore(t *testing.T) datastore.Datastore {
	t.Helper()
	keyspace := newScyllaTestKeyspace(t)
	return newScyllaDatastoreInKeyspace(t, keyspace)
}

func newScyllaTestKeyspace(t *testing.T) string {
	t.Helper()
	keyspace := fmt.Sprintf("gitstore_contract_test_%s_%d", strings.ReplaceAll(uuid.NewString(), "-", "")[:8], scyllaKeyspaceSequence.Add(1))
	t.Cleanup(func() { require.NoError(t, dropKeyspace(scyllaAddr, keyspace)) })
	provisionKeyspace(scyllaAddr, keyspace)
	return keyspace
}

func newScyllaDatastoreInKeyspace(t *testing.T, keyspace string) datastore.Datastore {
	t.Helper()
	host, portStr, splitErr := net.SplitHostPort(scyllaAddr)
	if splitErr != nil {
		host = scyllaAddr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cfg := config.ScyllaConfig{
		AutoMigrate:           true,
		Hosts:                 []string{scyllaAddr},
		Keyspace:              keyspace,
		DisableShardAwarePort: true,
		IgnorePeerAddr:        true,
		AddressTranslator:     contactPointTranslator(host, port),
	}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newScyllaDatastores(t *testing.T) (datastore.Datastore, datastore.Datastore) {
	t.Helper()
	keyspace := newScyllaTestKeyspace(t)
	return newScyllaDatastoreInKeyspace(t, keyspace), newScyllaDatastoreInKeyspace(t, keyspace)
}

func TestContractScylla(t *testing.T) {
	RunContractSuite(t, newScyllaDatastore(t))
}

func TestPaginationScylla(t *testing.T) {
	RunPaginationSuite(t, newScyllaDatastore(t))
}

func TestCategoryAncestorIndexScylla(t *testing.T) {
	ds := newScyllaDatastore(t)
	t.Run("Contract", func(t *testing.T) { runCategoryAncestorIndexContract(t, ds) })
	t.Run("Pagination", func(t *testing.T) { runCategoryAncestorIndexPagination(t, ds) })
}
