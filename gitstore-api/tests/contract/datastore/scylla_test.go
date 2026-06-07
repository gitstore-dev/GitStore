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
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var (
	scyllaAddr     string
	scyllaKeyspace string
)

func TestMain(m *testing.M) {
	scyllaAddr = os.Getenv("GITSTORE_TEST_SCYLLA_ADDR")
	if scyllaAddr == "" {
		scyllaAddr = "127.0.0.1:9042"
	}
	scyllaKeyspace = fmt.Sprintf("gitstore_contract_test_%d", os.Getpid())

	provisionKeyspace(scyllaAddr, scyllaKeyspace)
	code := m.Run()
	dropKeyspace(scyllaAddr, scyllaKeyspace)

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
	cluster.Timeout = 5 * time.Second
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
			`WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '1'} `+
			`AND durable_writes = true`,
		keyspace,
	)
	if err := session.Query(stmt).Exec(); err != nil {
		panic("provisionKeyspace: create keyspace: " + err.Error())
	}
	if err := session.AwaitSchemaAgreement(context.Background()); err != nil {
		panic("provisionKeyspace: await schema agreement: " + err.Error())
	}
}

func dropKeyspace(addr, keyspace string) {
	session, err := openRootSession(addr)
	if err != nil {
		return
	}
	defer session.Close()
	_ = session.Query(fmt.Sprintf(`DROP KEYSPACE IF EXISTS %s`, keyspace)).Exec()
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
	cluster.Timeout = 5 * time.Second
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)
	return cluster.CreateSession()
}

func newScyllaDatastore(t *testing.T) datastore.Datastore {
	t.Helper()
	host, portStr, splitErr := net.SplitHostPort(scyllaAddr)
	if splitErr != nil {
		host = scyllaAddr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cfg := config.ScyllaConfig{
		Hosts:                 []string{scyllaAddr},
		Keyspace:              scyllaKeyspace,
		DisableShardAwarePort: true,
		IgnorePeerAddr:        true,
		AddressTranslator:     contactPointTranslator(host, port),
	}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestContractScylla(t *testing.T) {
	RunContractSuite(t, newScyllaDatastore(t))
}

func TestPaginationScylla(t *testing.T) {
	RunPaginationSuite(t, newScyllaDatastore(t))
}
