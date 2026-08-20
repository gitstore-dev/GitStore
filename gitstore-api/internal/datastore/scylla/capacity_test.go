// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

const (
	partitionHardCeilingBytes = int64(100 * 1024 * 1024)
	hotPartitionTargetBytes   = int64(10 * 1024 * 1024)
)

type capacityConfig struct {
	Address       string
	Keyspace      string
	Namespace     string
	Bucket        string
	Products      int
	PageSize      int
	Mutations     int
	Concurrency   int
	SoakDuration  time.Duration
	StrictHotSize bool
}

func loadCapacityConfig(t *testing.T) capacityConfig {
	t.Helper()
	address := os.Getenv("SCYLLA_TEST_ADDR")
	if address == "" {
		address = os.Getenv("GITSTORE_TEST_SCYLLA_ADDR")
	}
	soakDuration := envDuration(t, "GITSTORE_SCYLLA_CAPACITY_SOAK_DURATION", 0)
	return capacityConfig{
		Address:       address,
		Keyspace:      envString("GITSTORE_SCYLLA_CAPACITY_KEYSPACE", "gitstore"),
		Namespace:     envString("GITSTORE_SCYLLA_CAPACITY_NAMESPACE", "gitstore-capacity"),
		Bucket:        envString("GITSTORE_SCYLLA_CAPACITY_BUCKET", time.Now().UTC().Format("2006-01")),
		Products:      envInt(t, "GITSTORE_SCYLLA_CAPACITY_PRODUCTS", 5_000_000),
		PageSize:      envInt(t, "GITSTORE_SCYLLA_CAPACITY_PAGE_SIZE", 100),
		Mutations:     envInt(t, "GITSTORE_SCYLLA_CAPACITY_MUTATIONS", 1_000),
		Concurrency:   envInt(t, "GITSTORE_SCYLLA_CAPACITY_CONCURRENCY", 2),
		SoakDuration:  soakDuration,
		StrictHotSize: os.Getenv("GITSTORE_SCYLLA_CAPACITY_ALLOW_HOT_PARTITIONS") != "1",
	}
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s must be a positive integer", key)
	}
	return value
}

func envDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		t.Fatalf("%s must be a non-negative duration: %v", key, err)
	}
	return value
}

func TestScyllaCapacityThresholds(t *testing.T) {
	if partitionHardCeilingBytes != 100*1024*1024 {
		t.Fatalf("partition hard ceiling = %d, want 100 MiB", partitionHardCeilingBytes)
	}
	if hotPartitionTargetBytes != 10*1024*1024 {
		t.Fatalf("hot partition target = %d, want 10 MiB", hotPartitionTargetBytes)
	}
}

func TestScyllaCapacity(t *testing.T) {
	cfg := loadCapacityConfig(t)
	if cfg.Address == "" {
		t.Skip("set SCYLLA_TEST_ADDR to a dedicated Scylla capacity environment")
	}
	if os.Getenv("GITSTORE_SCYLLA_CAPACITY_RUN") != "1" {
		t.Skip("set GITSTORE_SCYLLA_CAPACITY_RUN=1 after preloading the capacity dataset")
	}
	if cfg.Products < 5_000_000 {
		t.Fatalf("GITSTORE_SCYLLA_CAPACITY_PRODUCTS=%d, want at least 5000000", cfg.Products)
	}
	if cfg.Concurrency < 2 {
		t.Fatalf("GITSTORE_SCYLLA_CAPACITY_CONCURRENCY=%d, want at least 2 clients", cfg.Concurrency)
	}

	sessions := []*gocql.Session{
		openCapacitySession(t, cfg),
		openCapacitySession(t, cfg),
	}
	t.Cleanup(func() {
		for _, session := range sessions {
			session.Close()
		}
	})

	t.Logf(
		"capacity configuration: address=%s keyspace=%s products=%d page=%d mutations=%d concurrency=%d soak=%s",
		cfg.Address, cfg.Keyspace, cfg.Products, cfg.PageSize, cfg.Mutations, cfg.Concurrency, cfg.SoakDuration,
	)
	assertPartitionSizes(t, sessions[0], cfg)
	assertBoundedPage(t, sessions[0], cfg)
	runMutationLoad(t, sessions, cfg, cfg.Mutations)
	if cfg.SoakDuration > 0 {
		runSoak(t, sessions, cfg)
	}
}

func openCapacitySession(t *testing.T, cfg capacityConfig) *gocql.Session {
	t.Helper()
	host, portString, err := net.SplitHostPort(cfg.Address)
	if err != nil {
		t.Fatalf("SCYLLA_TEST_ADDR must be host:port: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse SCYLLA_TEST_ADDR port: %v", err)
	}
	cluster := gocql.NewCluster(host)
	cluster.Port = port
	cluster.Keyspace = cfg.Keyspace
	cluster.Consistency = gocql.Quorum
	session, err := cluster.CreateSession()
	if err != nil {
		t.Fatalf("open Scylla capacity session: %v", err)
	}
	return session
}

func assertPartitionSizes(t *testing.T, session *gocql.Session, cfg capacityConfig) {
	t.Helper()
	iter := session.Query("SELECT keyspace_name, table_name, partition_size FROM system.large_partitions").Iter()
	var keyspace, table string
	var size int64
	seen := 0
	for iter.Scan(&keyspace, &table, &size) {
		if keyspace != cfg.Keyspace {
			continue
		}
		seen++
		if size > partitionHardCeilingBytes {
			t.Errorf("%s partition size %d exceeds 100 MiB hard ceiling", table, size)
		}
		if cfg.StrictHotSize && size > hotPartitionTargetBytes {
			t.Errorf("%s partition size %d exceeds 10 MiB hot-partition target", table, size)
		}
	}
	if err := iter.Close(); err != nil {
		t.Fatalf("read system.large_partitions: %v", err)
	}
	t.Logf("checked %d recorded large partitions for keyspace %s", seen, cfg.Keyspace)
}

func assertBoundedPage(t *testing.T, session *gocql.Session, cfg capacityConfig) {
	t.Helper()
	assertQueryBound(t, session.Query(
		"SELECT creation_timestamp,uid FROM repositories_by_bucket WHERE bucket=? LIMIT ?",
		cfg.Bucket, cfg.PageSize,
	), cfg.PageSize, "repositories_by_bucket")
	assertQueryBound(t, session.Query(
		"SELECT creation_timestamp,uid FROM products_by_namespace WHERE namespace=? LIMIT ?",
		cfg.Namespace, cfg.PageSize,
	), cfg.PageSize, "products_by_namespace")
}

func assertQueryBound(t *testing.T, query *gocql.Query, limit int, table string) {
	t.Helper()
	iter := query.Iter()
	var created time.Time
	var uid gocql.UUID
	rows := 0
	for iter.Scan(&created, &uid) {
		rows++
	}
	if err := iter.Close(); err != nil {
		t.Fatalf("bounded page query %s: %v", table, err)
	}
	if rows > limit {
		t.Fatalf("%s returned %d rows for limit %d", table, rows, limit)
	}
	t.Logf("%s bounded page returned %d rows (limit %d)", table, rows, limit)
}

func runMutationLoad(t *testing.T, sessions []*gocql.Session, cfg capacityConfig, mutations int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var completed atomic.Int64
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()
	for worker := 0; worker < cfg.Concurrency; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			session := sessions[worker%len(sessions)]
			for sequence := worker; sequence < mutations; sequence += cfg.Concurrency {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if err := mutateCapacityMapping(session, cfg.Namespace, worker, sequence); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
				completed.Add(1)
			}
		}(worker)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("sustained mutation load: %v", firstErr)
	}
	if completed.Load() != int64(mutations) {
		t.Fatalf("completed mutations = %d, want %d", completed.Load(), mutations)
	}
	if len(sessions) < 2 {
		t.Fatal("capacity mutation load requires two independent clients")
	}
	t.Logf("completed %d projection mutation cycles in %s using %d clients", mutations, time.Since(start), len(sessions))
}

func mutateCapacityMapping(session *gocql.Session, namespace string, worker, sequence int) error {
	name := fmt.Sprintf("capacity-%d-%d-%d", time.Now().UnixNano(), worker, sequence)
	uid := gocql.TimeUUID()
	applied, err := execCapacityCAS(session.Query(
		"INSERT INTO namespace_mappings (namespace,name,repository_id) VALUES (?,?,?) IF NOT EXISTS",
		namespace, name, uid,
	))
	if err != nil {
		return fmt.Errorf("reserve forward mapping: %w", err)
	}
	if !applied {
		return errors.New("unexpected forward mapping conflict")
	}
	defer session.Query("DELETE FROM namespace_mappings WHERE namespace=? AND name=?", namespace, name).Exec()

	applied, err = execCapacityCAS(session.Query(
		"INSERT INTO namespace_mappings_by_repository (repository_id,namespace,name) VALUES (?,?,?) IF NOT EXISTS",
		uid, namespace, name,
	))
	if err != nil {
		return fmt.Errorf("reserve reverse mapping: %w", err)
	}
	if !applied {
		return errors.New("unexpected reverse mapping conflict")
	}
	if err := session.Query("DELETE FROM namespace_mappings_by_repository WHERE repository_id=?", uid).Exec(); err != nil {
		return fmt.Errorf("delete reverse mapping: %w", err)
	}
	if err := session.Query("DELETE FROM namespace_mappings WHERE namespace=? AND name=?", namespace, name).Exec(); err != nil {
		return fmt.Errorf("delete forward mapping: %w", err)
	}
	return nil
}

func runSoak(t *testing.T, sessions []*gocql.Session, cfg capacityConfig) {
	t.Helper()
	deadline := time.Now().Add(cfg.SoakDuration)
	cycles := 0
	for time.Now().Before(deadline) {
		runMutationLoad(t, sessions, cfg, cfg.Concurrency*10)
		cycles++
	}
	if cycles == 0 {
		t.Fatalf("soak duration %s completed no mutation cycles", cfg.SoakDuration)
	}
	t.Logf("soak completed %d batches over %s", cycles, cfg.SoakDuration)
}

func execCapacityCAS(query *gocql.Query) (bool, error) {
	return query.MapScanCAS(make(map[string]any))
}
