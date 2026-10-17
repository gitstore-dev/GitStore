// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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

func TestScyllaCapacityRealDatastorePages(t *testing.T) {
	store := newTestStore(t)
	namespace := "capacity-pages-" + newID()
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: newID(), Name: namespace, CreationTimestamp: time.Now().UTC(),
	}))
	for i := range 3 {
		repository := newRepository()
		repository.Namespace = namespace
		repository.CreationTimestamp = time.Now().UTC().AddDate(0, -i, 0).Truncate(time.Millisecond)
		require.NoError(t, store.CreateRepository(t.Context(), repository))
		product := newProduct(namespace, fmt.Sprintf("product-%d", i))
		product.CreationTimestamp = repository.CreationTimestamp
		require.NoError(t, store.CreateProduct(t.Context(), product))
	}
	assertDatastorePages(t, capacityConfig{Address: scyllaAddr, Keyspace: scyllaKeyspace, Namespace: namespace, PageSize: 1})
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
	assertDatastorePages(t, cfg)
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

type capacityQueryKey struct{}

type capacityQueryEvidence struct {
	mu                          sync.Mutex
	attempts                    int
	rows                        int
	retries                     int
	errors                      int
	filtered                    bool
	membershipUnbounded         bool
	tables                      map[string]int
	inserts                     map[string]int
	fileCASFields               map[int]int
	fileCASDuration             time.Duration
	fileCASAttempts             int
	fileProjectionReads         int
	fileProjectionKeys          int
	fileSnapshotLocatorMaxKeys  int
	fileSnapshotRowMaxKeys      int
	namespaceFenceReads         int
	namespaceReleaseCASAttempts int
	categoryProductQueries      int
	categoryProductRows         int
	categoryProductPartitions   map[string]int
	categoryProductLimit        int
	categoryProductUnbounded    bool
}

type capacityQueryObserver struct{}

func (capacityQueryObserver) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	evidence, ok := ctx.Value(capacityQueryKey{}).(*capacityQueryEvidence)
	if !ok {
		return
	}
	evidence.mu.Lock()
	defer evidence.mu.Unlock()
	evidence.attempts++
	evidence.rows += query.Rows
	if query.Attempt > 0 {
		evidence.retries++
	}
	if query.Err != nil {
		evidence.errors++
	}
	fields := strings.Fields(strings.ToLower(query.Statement))
	if strings.HasPrefix(query.Statement, "SELECT namespace, creation_timestamp, uid, name, repository_id FROM files_by_namespace ") {
		evidence.fileProjectionReads++
		evidence.fileProjectionKeys += (len(query.Values) - 1) / 2
	}
	if strings.HasPrefix(strings.ToLower(query.Statement), "select repository_creation_epoch, pending_repository_creations, deletion_timestamp, status, watch_committed from namespaces_by_uid ") {
		evidence.namespaceFenceReads++
	}
	if len(query.Values) == 4 && strings.HasPrefix(strings.ToLower(query.Statement), "update namespaces_by_uid set pending_repository_creations=") {
		evidence.namespaceReleaseCASAttempts++
	}
	if len(query.Values) == 25 && strings.HasPrefix(strings.ToLower(query.Statement), "update files_by_namespace set ") {
		evidence.fileCASDuration += query.End.Sub(query.Start)
		evidence.fileCASAttempts++
		if evidence.fileCASFields == nil {
			evidence.fileCASFields = make(map[int]int)
		}
		for i, value := range query.Values[:21] {
			if value != gocql.UnsetValue {
				evidence.fileCASFields[i]++
			}
		}
	}
	if len(fields) > 2 && fields[0] == "insert" && fields[1] == "into" {
		if evidence.inserts == nil {
			evidence.inserts = make(map[string]int)
		}
		evidence.inserts[fields[2]]++
	}
	for i, field := range fields {
		if field == "from" && i+1 < len(fields) {
			evidence.tables[fields[i+1]]++
			switch fields[i+1] {
			case "files_by_uid":
				evidence.fileSnapshotLocatorMaxKeys = max(evidence.fileSnapshotLocatorMaxKeys, len(query.Values))
			case "files_by_namespace":
				evidence.fileSnapshotRowMaxKeys = max(evidence.fileSnapshotRowMaxKeys, (len(query.Values)-1)/2)
			}
			if fields[i+1] == "category_products_by_category" {
				evidence.categoryProductQueries++
				evidence.categoryProductRows += query.Rows
				statement := strings.Join(fields, " ")
				if len(query.Values) < 4 ||
					!strings.HasPrefix(statement, "select creation_timestamp, product_uid from category_products_by_category where namespace=? and category_uid=? and shard=?") ||
					!strings.HasSuffix(statement, " limit ?") {
					evidence.categoryProductUnbounded = true
				} else {
					if evidence.categoryProductPartitions == nil {
						evidence.categoryProductPartitions = make(map[string]int)
					}
					partition := fmt.Sprintf("%v/%v/%v", query.Values[0], query.Values[1], query.Values[2])
					evidence.categoryProductPartitions[partition]++
					limit, err := strconv.Atoi(fmt.Sprint(query.Values[len(query.Values)-1]))
					if err != nil || limit < 1 || limit > datastore.DefaultPageSize+1 {
						evidence.categoryProductUnbounded = true
					}
					evidence.categoryProductLimit = max(evidence.categoryProductLimit, limit)
				}
			}
			break
		}
	}
	evidence.filtered = evidence.filtered || strings.Contains(strings.ToUpper(query.Statement), "ALLOW FILTERING")
	if strings.Contains(strings.ToLower(query.Statement), "from resources_by_repository") {
		statement := strings.Join(strings.Fields(strings.ToLower(query.Statement)), " ")
		evidence.membershipUnbounded = evidence.membershipUnbounded ||
			statement != "select uid from resources_by_repository where repository_id=? and shard=? limit 1"
	}
}

func TestScyllaCategoryProductsMembershipChurnAndQueryCost(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		count int
		hot   bool
	}{
		{name: "sparse", count: 1, hot: true},
		{name: "dense", count: 256},
		{name: "skewed", count: 65, hot: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = capacityQueryObserver{}
			clients := make([]datastore.Datastore, 2)
			indexes := make([]datastore.CategoryProductIndex, 2)
			for i := range clients {
				store, err := scylla.New(cfg, zap.NewNop())
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, store.Close()) })
				clients[i], indexes[i] = store, store.(datastore.CategoryProductIndex)
			}
			raw := newRawSession(t)
			require.NoError(t, raw.Query("INSERT INTO category_product_projection_state (projection,ready) VALUES (?,?)",
				"category-products", true).Exec())
			ctx := t.Context()
			namespace := "category-churn-" + newID()
			categories := []*datastore.CategoryTaxonomy{
				newCategoryTaxonomy(namespace, "primary"),
				newCategoryTaxonomy(namespace, "secondary"),
			}
			for _, category := range categories {
				category.ResourceVersion = "1"
				category.Status = json.RawMessage(`{}`)
				require.NoError(t, clients[0].CreateCategoryTaxonomy(ctx, category))
			}
			members := [2]map[string]*datastore.Product{{}, {}}
			products := make([]*datastore.Product, 0, fixture.count)
			created := time.Now().UTC().Truncate(time.Millisecond)
			prefix := newID()[:24]
			candidate := 0
			var distribution [32]int
			nextUID := func(balanced bool) string {
				t.Helper()
				for candidate < 65536 {
					candidate++
					uid := fmt.Sprintf("%s%012x", prefix, candidate)
					hash := fnv.New32a()
					_, _ = hash.Write([]byte(uid))
					shard := hash.Sum32() % 32
					if (fixture.hot && shard != 0) || (balanced && distribution[shard] >= 8) {
						continue
					}
					distribution[shard]++
					return uid
				}
				t.Fatal("bounded UID fixture cannot populate its declared shard distribution")
				return ""
			}
			for i := range fixture.count {
				product := newProduct(namespace, fmt.Sprintf("product-%d", i))
				product.UID = nextUID(!fixture.hot)
				product.ResourceVersion = "1"
				product.Generation = 1
				product.CreationTimestamp = created.Add(-time.Duration(i/8) * time.Second)
				product.Status = json.RawMessage(`{"resolved":{"category":{"path":["primary"]}}}`)
				require.NoError(t, clients[0].CreateProduct(ctx, product))
				require.NoError(t, indexes[0].ReplaceCategoryProductMembership(ctx, product, []string{categories[0].UID}))
				products = append(products, product)
				members[0][product.UID] = product
			}
			t.Logf("acknowledged fixture products=%d rows_by_shard=%v", fixture.count, distribution)
			if fixture.hot {
				require.Equal(t, fixture.count, distribution[0])
			} else {
				for _, count := range distribution {
					require.Equal(t, 8, count)
				}
			}
			verify := func(phase string) {
				t.Helper()
				for category, expected := range members {
					for replica, index := range indexes {
						for _, limit := range []int{7, datastore.DefaultPageSize} {
							assertCategoryProductMembershipPages(t,
								fmt.Sprintf("%s/%s/client-%d/%s", fixture.name, phase, replica, categories[category].Name),
								namespace, categories[category].UID, limit, expected, index.ListCategoryProducts)
						}
					}
				}
			}
			verify("initial")
			for i, product := range products {
				switch i % 3 {
				case 0:
					require.NoError(t, clients[1].DeleteProductWithResourceVersion(ctx, product.UID, product.ResourceVersion))
					delete(members[0], product.UID)
					replacement := newProduct(namespace, product.Name)
					replacement.UID = nextUID(false)
					replacement.ResourceVersion = "1"
					replacement.Generation = 1
					replacement.CreationTimestamp = created.Add(time.Duration(i/8+1) * time.Second)
					replacement.Status = product.Status
					require.NoError(t, clients[1].CreateProduct(ctx, replacement))
					require.NoError(t, indexes[1].ReplaceCategoryProductMembership(ctx, replacement, []string{categories[0].UID}))
					members[0][replacement.UID] = replacement
					require.NoError(t, indexes[0].ReplaceCategoryProductMembership(ctx, product, []string{categories[0].UID}))
					_, err := clients[0].GetProduct(ctx, product.UID)
					require.ErrorIs(t, err, datastore.ErrNotFound)
					found, err := clients[0].GetProductByName(ctx, namespace, product.Name)
					require.NoError(t, err)
					require.Equal(t, replacement.UID, found.UID, "stale membership cannot select a same-name replacement")
				case 1:
					var status catalog.ProductStatus
					require.NoError(t, json.Unmarshal([]byte(`{"resolved":{"category":{"path":["secondary"]}}}`), &status))
					moved, err := clients[1].UpdateProductStatus(ctx, namespace, product.Name,
						datastore.ProductStatusPatch{ResourceVersion: product.ResourceVersion, Resolved: status.Resolved})
					require.NoError(t, err)
					require.NotEqual(t, product.ResourceVersion, moved.ResourceVersion)
					require.Equal(t, product.Generation, moved.Generation)
					require.NoError(t, indexes[1].ReplaceCategoryProductMembership(ctx, moved, []string{categories[1].UID}))
					delete(members[0], product.UID)
					members[1][moved.UID] = moved
					require.NoError(t, indexes[0].ReplaceCategoryProductMembership(ctx, product, []string{categories[0].UID}))
				}
			}
			verify("churn")
			for _, expected := range members {
				for _, product := range expected {
					require.NoError(t, clients[1].DeleteProductWithResourceVersion(ctx, product.UID, product.ResourceVersion))
				}
				clear(expected)
			}
			require.NoError(t, indexes[0].ReplaceCategoryProductMembership(ctx, products[0],
				[]string{categories[0].UID, categories[1].UID}))
			verify("empty-after-delete")
		})
	}
}

func readyCategoryProductProjection(t *testing.T, raw *gocql.Session) {
	t.Helper()
	var originalReady bool
	markerErr := raw.Query("SELECT ready FROM category_product_projection_state WHERE projection=?",
		"category-products").Scan(&originalReady)
	require.True(t, markerErr == nil || errors.Is(markerErr, gocql.ErrNotFound))
	t.Cleanup(func() {
		if errors.Is(markerErr, gocql.ErrNotFound) {
			require.NoError(t, raw.Query("DELETE FROM category_product_projection_state WHERE projection=?",
				"category-products").Exec())
		} else {
			require.NoError(t, raw.Query("INSERT INTO category_product_projection_state (projection,ready) VALUES (?,?)",
				"category-products", originalReady).Exec())
		}
	})
	require.NoError(t, raw.Query("INSERT INTO category_product_projection_state (projection,ready) VALUES (?,?)",
		"category-products", true).Exec())
}

func TestScyllaCategoryProductsDuplicateProjectionFailsClosed(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	var clients []datastore.Datastore
	var indexes []datastore.CategoryProductIndex
	for range 2 {
		store, err := scylla.New(cfg, zap.NewNop())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		clients = append(clients, store)
		indexes = append(indexes, store.(datastore.CategoryProductIndex))
	}
	raw := newRawSession(t)
	readyCategoryProductProjection(t, raw)
	namespace, categoryUID := "category-duplicate-"+newID(), newID()
	product := newProduct(namespace, "acknowledged-member")
	product.ResourceVersion, product.Generation, product.Status = "1", 1, json.RawMessage(`{}`)
	require.NoError(t, clients[0].CreateProduct(t.Context(), product))
	uid, err := gocql.ParseUUID(product.UID)
	require.NoError(t, err)
	t.Cleanup(func() {
		for shard := range 32 {
			require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=?",
				namespace, categoryUID, shard).Exec())
		}
		require.NoError(t, raw.Query("DELETE FROM category_products_by_product WHERE namespace=? AND product_uid=?",
			namespace, uid).Exec())
		require.NoError(t, clients[0].DeleteProduct(context.Background(), product.UID))
	})
	require.NoError(t, indexes[0].ReplaceCategoryProductMembership(t.Context(), product, []string{categoryUID}))
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(product.UID))
	wrongShard := int8((hash.Sum32() + 1) % 32)
	require.NoError(t, raw.Query("INSERT INTO category_products_by_category (namespace,category_uid,shard,creation_timestamp,product_uid,resource_version) VALUES (?,?,?,?,?,?)",
		namespace, categoryUID, wrongShard, product.CreationTimestamp, uid, "1").Exec())
	for i, index := range indexes {
		for _, backward := range []bool{false, true} {
			t.Run(fmt.Sprintf("client-%d/backward-%t", i, backward), func(t *testing.T) {
				params := datastore.PageParams{First: 1}
				if backward {
					params = datastore.PageParams{Last: 1}
				}
				evidence := &capacityQueryEvidence{tables: map[string]int{}}
				ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), capacityQueryKey{}, evidence), 30*time.Second)
				defer cancel()
				page, err := index.ListCategoryProducts(ctx, namespace, categoryUID, params)
				require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
				require.ErrorContains(t, err, "duplicate")
				require.Nil(t, page)
				evidence.mu.Lock()
				defer evidence.mu.Unlock()
				require.Equal(t, 32, evidence.categoryProductQueries)
				require.Equal(t, 2, evidence.categoryProductRows)
				require.Equal(t, 2, evidence.tables["products_by_uid"])
				require.Equal(t, 2, evidence.tables["products_by_namespace"])
				require.Equal(t, 37, evidence.attempts)
				require.Equal(t, 7, evidence.rows)
				require.False(t, evidence.filtered)
				require.False(t, evidence.categoryProductUnbounded)
				require.Zero(t, evidence.errors)
				require.Zero(t, evidence.retries)
			})
		}
	}
	require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=?",
		namespace, categoryUID, wrongShard).Exec())
	require.NoError(t, indexes[0].ReplaceCategoryProductMembership(t.Context(), product, []string{categoryUID}))
	for _, index := range indexes {
		assertCategoryProductMembershipPages(t, "repaired-duplicate-fixture", namespace, categoryUID, 1,
			map[string]*datastore.Product{product.UID: product}, index.ListCategoryProducts)
	}
}

func TestScyllaCategoryProductsWorkBudgetsFailClosed(t *testing.T) {
	const (
		shards       = 32
		queryBudget  = 256
		lookupBudget = 4096
	)
	for _, hydration := range []string{"missing-uid", "mismatched-key"} {
		t.Run(hydration, func(t *testing.T) {
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = capacityQueryObserver{}
			clients := make([]datastore.Datastore, 2)
			indexes := make([]datastore.CategoryProductIndex, 2)
			for i := range clients {
				store, err := scylla.New(cfg, zap.NewNop())
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, store.Close()) })
				clients[i], indexes[i] = store, store.(datastore.CategoryProductIndex)
			}
			raw := newRawSession(t)
			readyCategoryProductProjection(t, raw)
			namespace, categoryUID, unaffectedUID := "category-budget-"+newID(), newID(), newID()
			product := newProduct(namespace, "acknowledged-member")
			prefix := newID()[:24]
			foundHotUID := false
			for candidate := range 65536 {
				uid := fmt.Sprintf("%s%012x", prefix, candidate)
				hash := fnv.New32a()
				_, _ = hash.Write([]byte(uid))
				if hash.Sum32()%shards == 0 {
					product.UID, foundHotUID = uid, true
					break
				}
			}
			require.True(t, foundHotUID, "bounded fixture must place its live member in the same hot partition")
			product.ResourceVersion = "1"
			product.Generation = 1
			product.Status = json.RawMessage(`{}`)
			require.NoError(t, clients[0].CreateProduct(t.Context(), product))
			t.Cleanup(func() {
				for _, uid := range []string{categoryUID, unaffectedUID} {
					for shard := range shards {
						require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=?",
							namespace, uid, shard).Exec())
					}
				}
				uid, err := gocql.ParseUUID(product.UID)
				require.NoError(t, err)
				require.NoError(t, raw.Query("DELETE FROM category_products_by_product WHERE namespace=? AND product_uid=?",
					namespace, uid).Exec())
				require.NoError(t, clients[0].DeleteProduct(context.Background(), product.UID))
			})
			require.NoError(t, indexes[0].ReplaceCategoryProductMembership(t.Context(), product, []string{categoryUID, unaffectedUID}))
			liveUID, err := gocql.ParseUUID(product.UID)
			require.NoError(t, err)
			batch := raw.NewBatch(gocql.UnloggedBatch)
			for i := range lookupBudget + 1 {
				uid := liveUID
				if hydration == "missing-uid" {
					uid, err = gocql.ParseUUID(newID())
					require.NoError(t, err)
				}
				batch.Query("INSERT INTO category_products_by_category (namespace,category_uid,shard,creation_timestamp,product_uid,resource_version) VALUES (?,?,?,?,?,?)",
					namespace, categoryUID, 0, product.CreationTimestamp.Add(time.Duration(i+1)*time.Millisecond), uid, "1")
				if len(batch.Entries) == 64 || i == lookupBudget {
					require.NoError(t, raw.ExecuteBatch(batch.WithContext(t.Context())))
					batch = raw.NewBatch(gocql.UnloggedBatch)
				}
			}
			for i, index := range indexes {
				for _, fixture := range []struct {
					name    string
					limit   int
					queries int
					lookups int
				}{
					{name: "query", limit: 1, queries: queryBudget, lookups: (queryBudget - shards + 1) * 2},
					{name: "lookup", limit: 100, queries: shards - 1 + (lookupBudget+1+100)/101, lookups: lookupBudget},
				} {
					for _, backward := range []bool{false, true} {
						t.Run(fmt.Sprintf("client-%d/%s/backward-%t", i, fixture.name, backward), func(t *testing.T) {
							params := datastore.PageParams{First: fixture.limit}
							if backward {
								params = datastore.PageParams{Last: fixture.limit}
							}
							evidence := &capacityQueryEvidence{tables: map[string]int{}}
							ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), capacityQueryKey{}, evidence), 30*time.Second)
							defer cancel()
							started := time.Now()
							page, err := index.ListCategoryProducts(ctx, namespace, categoryUID, params)
							require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
							require.ErrorContains(t, err, fixture.name+" budget")
							require.Nil(t, page, "budget exhaustion must discard even an already hydrated live member")
							evidence.mu.Lock()
							defer evidence.mu.Unlock()
							require.False(t, evidence.filtered)
							require.False(t, evidence.categoryProductUnbounded)
							require.Zero(t, evidence.retries)
							require.Zero(t, evidence.errors)
							require.Len(t, evidence.categoryProductPartitions, shards)
							require.Equal(t, fixture.queries, evidence.categoryProductQueries)
							projectionRows := fixture.lookups
							if fixture.name == "lookup" {
								projectionRows = lookupBudget + 2
							}
							require.Equal(t, projectionRows, evidence.categoryProductRows)
							require.Equal(t, fixture.limit+1, evidence.categoryProductLimit)
							require.Equal(t, fixture.lookups, evidence.tables["products_by_uid"])
							canonical := 0
							if hydration == "mismatched-key" {
								canonical = fixture.lookups
							} else if backward {
								canonical = 1
							}
							require.Equal(t, canonical, evidence.tables["products_by_namespace"])
							require.Equal(t, 1, evidence.tables["category_product_projection_state"])
							require.Equal(t, 1+fixture.queries+fixture.lookups+canonical, evidence.attempts)
							require.Equal(t, 1+projectionRows+2*canonical, evidence.rows)
							t.Logf("budget=%s backward=%t partitions=%d projection_queries=%d projection_rows=%d uid_lookups=%d canonical_reads=%d attempts=%d fetched_rows=%d elapsed=%s",
								fixture.name, backward, len(evidence.categoryProductPartitions), evidence.categoryProductQueries,
								evidence.categoryProductRows, fixture.lookups, canonical, evidence.attempts, evidence.rows, time.Since(started))
						})
					}
				}
			}
			for _, index := range indexes {
				assertCategoryProductMembershipPages(t, "unaffected-budget-fixture", namespace, unaffectedUID, 1,
					map[string]*datastore.Product{product.UID: product}, index.ListCategoryProducts)
			}
			require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=?",
				namespace, categoryUID, 0).Exec())
			require.NoError(t, indexes[0].ReplaceCategoryProductMembership(t.Context(), product, []string{categoryUID, unaffectedUID}))
			for _, index := range indexes {
				for _, limit := range []int{1, 100} {
					assertCategoryProductMembershipPages(t, "repaired-budget-fixture", namespace, categoryUID, limit,
						map[string]*datastore.Product{product.UID: product}, index.ListCategoryProducts)
				}
			}
		})
	}
}

func assertCategoryProductMembershipPages(t *testing.T, name, namespace, categoryUID string, limit int,
	expected map[string]*datastore.Product,
	list func(context.Context, string, string, datastore.PageParams) (*datastore.PageResult[datastore.Product], error),
) {
	t.Helper()
	ordered := make([]*datastore.Product, 0, len(expected))
	for _, product := range expected {
		ordered = append(ordered, product)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		return left.CreationTimestamp.After(right.CreationTimestamp) ||
			(left.CreationTimestamp.Equal(right.CreationTimestamp) && left.UID > right.UID)
	})
	for _, backward := range []bool{false, true} {
		offset := 0
		if backward {
			offset = len(ordered)
		}
		cursor := ""
		for {
			params := datastore.PageParams{First: limit, After: cursor}
			start, end := offset, min(offset+limit, len(ordered))
			remaining := len(ordered) - offset
			if backward {
				params = datastore.PageParams{Last: limit, Before: cursor}
				start, end = max(0, offset-limit), offset
				remaining = offset
			}
			evidence := &capacityQueryEvidence{tables: map[string]int{}}
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), capacityQueryKey{}, evidence), 30*time.Second)
			started := time.Now()
			page, err := list(ctx, namespace, categoryUID, params)
			cancel()
			require.NoError(t, err, "%s window [%d,%d)", name, start, end)
			require.NotNil(t, page)
			require.Len(t, page.Items, end-start, "%s exact acknowledged membership window", name)
			require.Equal(t, start > 0, page.HasPrevious, "%s previous-page oracle", name)
			require.Equal(t, end < len(ordered), page.HasNext, "%s next-page oracle", name)
			for i, product := range page.Items {
				want := ordered[start+i]
				require.Equal(t, want.UID, product.UID)
				require.Equal(t, want.Name, product.Name)
				require.True(t, want.CreationTimestamp.Equal(product.CreationTimestamp))
				require.Equal(t, want.ResourceVersion, product.ResourceVersion)
				require.Equal(t, want.Generation, product.Generation)
				require.JSONEq(t, string(want.Status), string(product.Status))
			}
			func() {
				evidence.mu.Lock()
				defer evidence.mu.Unlock()
				require.False(t, evidence.filtered)
				require.False(t, evidence.categoryProductUnbounded)
				require.Zero(t, evidence.errors)
				require.Zero(t, evidence.retries)
				require.Len(t, evidence.categoryProductPartitions, 32)
				require.Equal(t, 32, evidence.categoryProductQueries, "clean membership needs only one bounded read per shard")
				require.Equal(t, limit+1, evidence.categoryProductLimit)
				lookups := min(remaining, limit+1)
				require.Equal(t, lookups, evidence.tables["products_by_uid"])
				require.Equal(t, lookups, evidence.tables["products_by_namespace"])
				require.Equal(t, 1, evidence.tables["category_product_projection_state"])
				require.Equal(t, 33+2*lookups, evidence.attempts)
				require.GreaterOrEqual(t, evidence.categoryProductRows, lookups)
				require.LessOrEqual(t, evidence.categoryProductRows, min(remaining, 32*(limit+1)))
				t.Logf("%s backward=%t limit=%d window=[%d,%d) returned=%d partitions=%d projection_queries=%d projection_rows=%d hydrated=%d attempts=%d fetched_rows=%d elapsed=%s",
					name, backward, limit, start, end, len(page.Items), len(evidence.categoryProductPartitions),
					evidence.categoryProductQueries, evidence.categoryProductRows, lookups, evidence.attempts, evidence.rows, time.Since(started))
			}()
			if (!backward && !page.HasNext) || (backward && !page.HasPrevious) {
				break
			}
			offset = end
			last := page.Items[len(page.Items)-1]
			if backward {
				offset, last = start, page.Items[0]
			}
			cursor = pageCursor(last.CreationTimestamp, last.UID)
		}
	}
}

func assertDatastorePages(t *testing.T, cfg capacityConfig) {
	t.Helper()
	require.LessOrEqual(t, cfg.PageSize, datastore.DefaultPageSize, "capacity reads must preserve the 250-row connection maximum")
	host, portString, err := net.SplitHostPort(cfg.Address)
	require.NoError(t, err)
	port, err := strconv.Atoi(portString)
	require.NoError(t, err)
	store, err := scylla.New(config.ScyllaConfig{
		Hosts: []string{cfg.Address}, Keyspace: cfg.Keyspace,
		DisableShardAwarePort: true, IgnorePeerAddr: true,
		AddressTranslator: contactPointTranslator(host, port),
		QueryObserver:     capacityQueryObserver{},
	}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	global, ok := store.(datastore.GlobalRepositoryLister)
	require.True(t, ok)
	repositoryKey := func(r *datastore.Repository) capacityPageKey {
		return capacityPageKey{r.UID, r.CreationTimestamp}
	}
	assertCapacityDatastorePage(t, "Repository/global", cfg.PageSize, global.ListRepositories, repositoryKey)
	assertCapacityDatastorePage(t, "Repository/namespace", cfg.PageSize,
		func(ctx context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
			return store.ListRepositoriesByNamespace(ctx, cfg.Namespace, page)
		}, repositoryKey)
	assertCapacityDatastorePage(t, "Product/namespace", cfg.PageSize,
		func(ctx context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
			return store.ListProducts(ctx, cfg.Namespace, page)
		}, func(p *datastore.Product) capacityPageKey { return capacityPageKey{p.UID, p.CreationTimestamp} })
}

type capacityPageKey struct {
	uid     string
	created time.Time
}

func assertCapacityDatastorePage[T any](t *testing.T, name string, limit int,
	fetch func(context.Context, datastore.PageParams) (*datastore.PageResult[T], error),
	key func(*T) capacityPageKey,
) {
	t.Helper()
	read := func(page datastore.PageParams) *datastore.PageResult[T] {
		evidence := &capacityQueryEvidence{tables: map[string]int{}}
		ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), capacityQueryKey{}, evidence), 30*time.Second)
		defer cancel()
		start := time.Now()
		result, err := fetch(ctx, page)
		require.NoError(t, err, "%s real datastore page", name)
		require.NotNil(t, result)
		require.LessOrEqual(t, len(result.Items), limit)
		evidence.mu.Lock()
		defer evidence.mu.Unlock()
		require.Positive(t, evidence.attempts, "a real page must observe datastore I/O")
		require.False(t, evidence.filtered, "capacity access paths must not use ALLOW FILTERING")
		require.Zero(t, evidence.errors, "query errors cannot pass as successful page evidence")
		t.Logf("%s page first=%d last=%d returned=%d attempts=%d fetched_rows=%d retries=%d tables=%v elapsed=%s",
			name, page.First, page.Last, len(result.Items), evidence.attempts, evidence.rows, evidence.retries, evidence.tables, time.Since(start))
		for i, item := range result.Items {
			current := key(item)
			require.NotEmpty(t, current.uid)
			if i > 0 {
				previous := key(result.Items[i-1])
				require.True(t, previous.created.After(current.created) ||
					(previous.created.Equal(current.created) && previous.uid > current.uid), "%s page must be strictly ordered", name)
			}
		}
		return result
	}
	first := read(datastore.PageParams{First: limit})
	require.NotEmpty(t, first.Items, "%s requires a populated, acknowledged fixture", name)
	if !first.HasNext {
		return
	}
	last := key(first.Items[len(first.Items)-1])
	cursor := func(k capacityPageKey) string {
		return pageCursor(k.created, k.uid)
	}
	second := read(datastore.PageParams{First: limit, After: cursor(last)})
	require.NotEmpty(t, second.Items, "HasNext must lead to a nonempty page")
	next := key(second.Items[0])
	require.True(t, last.created.After(next.created) ||
		(last.created.Equal(next.created) && last.uid > next.uid), "%s pages must not overlap", name)
	backward := read(datastore.PageParams{Last: limit, Before: cursor(next)})
	require.Equal(t, len(first.Items), len(backward.Items), "%s backward page must preserve the same window", name)
	for i := range first.Items {
		require.Equal(t, key(first.Items[i]), key(backward.Items[i]))
	}
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

func TestScylla_FileSnapshotHydrationTopology(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	namespace := &datastore.Namespace{UID: newID(), Name: "file-bulk-" + newID()}
	require.NoError(t, store.CreateNamespace(t.Context(), namespace))
	allUIDs := make([]string, datastore.FileSnapshotBatchLimit)
	for i := range allUIDs {
		file := &datastore.File{
			UID: newID(), Namespace: namespace.Name, Name: fmt.Sprintf("file-%d", i),
			Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Body:              strings.Repeat("bounded hydration fixture ", 16),
		}
		require.NoError(t, store.CreateFile(t.Context(), file))
		allUIDs[i] = file.UID
	}
	uids := allUIDs[:100]
	points := func() ([]*datastore.File, error) {
		files := make([]*datastore.File, len(uids))
		errs := make([]error, len(uids))
		var next atomic.Int64
		var workers sync.WaitGroup
		for range 8 {
			workers.Go(func() {
				for i := int(next.Add(1) - 1); i < len(uids); i = int(next.Add(1) - 1) {
					files[i], errs[i] = store.GetFile(t.Context(), uids[i])
				}
			})
		}
		workers.Wait()
		return files, errors.Join(errs...)
	}
	expected, err := points()
	require.NoError(t, err)
	current, err := store.GetFileSnapshots(t.Context(), uids)
	require.NoError(t, err)
	require.Equal(t, expected, current)
	for round := range 6 {
		modes := []string{"point", "namespace"}
		if round%2 != 0 {
			modes[0], modes[1] = modes[1], modes[0]
		}
		for _, mode := range modes {
			start := time.Now()
			var snapshots []*datastore.File
			if mode == "point" {
				snapshots, err = points()
			} else {
				snapshots, err = store.GetFileSnapshots(t.Context(), uids)
			}
			elapsed := time.Since(start)
			require.NoError(t, err)
			require.Equal(t, expected, snapshots)
			t.Logf("diagnostic-only File hydration: round=%d mode=%s files=%d point_fanout=8 locator_batch=16 RF=%d elapsed=%s",
				round, mode, len(uids), scyllaReplicationFactor, elapsed)
		}
	}
	evidence := &capacityQueryEvidence{tables: map[string]int{}}
	ctx := context.WithValue(t.Context(), capacityQueryKey{}, evidence)
	current, err = store.GetFileSnapshots(ctx, allUIDs)
	require.NoError(t, err)
	require.Len(t, current, datastore.FileSnapshotBatchLimit)
	for i, file := range current {
		require.NotNil(t, file)
		require.Equal(t, allUIDs[i], file.UID)
	}
	evidence.mu.Lock()
	queriesPerStage := (datastore.FileSnapshotBatchLimit + 15) / 16
	require.Equal(t, queriesPerStage, evidence.tables["files_by_uid"])
	require.Equal(t, queriesPerStage, evidence.tables["files_by_namespace"])
	require.Equal(t, 2*queriesPerStage, evidence.attempts)
	require.Equal(t, 16, evidence.fileSnapshotLocatorMaxKeys)
	require.Equal(t, 16, evidence.fileSnapshotRowMaxKeys)
	require.Equal(t, 2*datastore.FileSnapshotBatchLimit, evidence.rows)
	require.Zero(t, evidence.retries)
	require.Zero(t, evidence.errors)
	require.False(t, evidence.filtered)
	evidence.mu.Unlock()
	evidence = &capacityQueryEvidence{tables: map[string]int{}}
	ctx = context.WithValue(t.Context(), capacityQueryKey{}, evidence)
	current, err = store.GetFileSnapshots(ctx, []string{allUIDs[0], allUIDs[0], "invalid"})
	require.NoError(t, err)
	require.Equal(t, current[0], current[1])
	require.Nil(t, current[2])
	evidence.mu.Lock()
	require.Equal(t, 1, evidence.tables["files_by_uid"])
	require.Equal(t, 1, evidence.tables["files_by_namespace"])
	evidence.mu.Unlock()
	_, err = store.GetFileSnapshots(t.Context(), make([]string, 251))
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.GetFileSnapshots(ctx, uids)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, store.Close())
	current, err = store.GetFileSnapshots(t.Context(), uids)
	require.Error(t, err)
	require.Nil(t, current, "storage failures must not return partial success")
}

func TestScylla_FileLocatorQueryTopology(t *testing.T) {
	const readers, rounds, fixtureSize = 8, 6, 64
	store := newTestStore(t)
	namespace := "locator-topology-" + newID()
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
	uids := make([]gocql.UUID, fixtureSize)
	created := time.Now().UTC().Truncate(time.Millisecond)
	for i := range uids {
		uids[i] = gocql.TimeUUID()
		require.NoError(t, store.CreateFile(t.Context(), &datastore.File{
			UID: uids[i].String(), Namespace: namespace, Name: fmt.Sprintf("locator-%d", i),
			Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1",
			ResourceVersion: "1", CreationTimestamp: created,
		}))
	}
	host, rawPort, err := net.SplitHostPort(scyllaAddr)
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	evidence := &capacityRoutingEvidence{hosts: make(map[string]int)}
	cluster := gocql.NewCluster(host)
	cluster.Port, cluster.Keyspace = port, scyllaKeyspace
	cluster.Consistency = gocql.Quorum
	cluster.Timeout = 5 * time.Second
	cluster.DisableShardAwarePort, cluster.IgnorePeerAddr = true, true
	cluster.AddressTranslator = contactPointTranslator(host, port)
	cluster.QueryObserver, cluster.StreamObserver = evidence, evidence
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	defer session.Close()

	read := func(ctx context.Context, keys []gocql.UUID, tracer gocql.Tracer) error {
		args := make([]any, len(keys))
		expected := make(map[gocql.UUID]bool, len(keys))
		for i, uid := range keys {
			args[i], expected[uid] = uid, true
		}
		statement := "SELECT uid, namespace, creation_timestamp FROM files_by_uid WHERE uid IN (" +
			strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",") + ")"
		iter := session.Query(statement, args...).WithContext(ctx).Trace(tracer).Iter()
		var uid gocql.UUID
		var actualNamespace string
		var actualCreated time.Time
		var identityErr error
		for iter.Scan(&uid, &actualNamespace, &actualCreated) {
			if !expected[uid] || actualNamespace != namespace || !actualCreated.Equal(created) {
				identityErr = errors.New("invalid File UID locator readback")
			}
			delete(expected, uid)
		}
		if err := errors.Join(identityErr, iter.Close()); err != nil {
			return err
		}
		if len(expected) != 0 {
			return errors.New("missing File UID locator readback")
		}
		return nil
	}
	execute := func(ctx context.Context, keys, batchSize int) error {
		batches := (keys + batchSize - 1) / batchSize
		errs := make([]error, batches)
		var next atomic.Int64
		var workers sync.WaitGroup
		for range min(readers, batches) {
			workers.Go(func() {
				for batch := int(next.Add(1) - 1); batch < batches; batch = int(next.Add(1) - 1) {
					start := batch * batchSize
					errs[batch] = read(ctx, uids[start:min(start+batchSize, keys)], nil)
				}
			})
		}
		workers.Wait()
		return errors.Join(errs...)
	}
	for _, keys := range []int{8, 16, fixtureSize} {
		batchSizes := []int{1, 4, 8, 16}
		for _, batchSize := range batchSizes {
			require.NoError(t, execute(t.Context(), keys, batchSize))
		}
		for round := range rounds {
			for offset := range batchSizes {
				batchSize := batchSizes[(offset+round)%len(batchSizes)]
				evidence.mu.Lock()
				driverBefore, streamBefore := evidence.driverDuration, evidence.streamDuration
				requestsBefore := evidence.requests
				startsBefore, finishesBefore := evidence.streamStarts, evidence.streamFinishes
				abandonedBefore, retriesBefore := evidence.streamAbandoned, evidence.attempts
				evidence.mu.Unlock()
				ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), capacityRoutingKey{}, evidence), 5*time.Second)
				start := time.Now()
				err := execute(ctx, keys, batchSize)
				elapsed := time.Since(start)
				cancel()
				require.NoError(t, err)
				evidence.streams.Wait()
				evidence.mu.Lock()
				driver, stream := evidence.driverDuration-driverBefore, evidence.streamDuration-streamBefore
				requests := evidence.requests - requestsBefore
				starts, finishes := evidence.streamStarts-startsBefore, evidence.streamFinishes-finishesBefore
				abandoned, retries := evidence.streamAbandoned-abandonedBefore, evidence.attempts-retriesBefore
				evidence.mu.Unlock()
				queries := (keys + batchSize - 1) / batchSize
				require.Equal(t, queries, requests)
				require.Equal(t, queries, starts)
				require.Equal(t, starts, finishes)
				require.Zero(t, abandoned)
				require.Zero(t, retries)
				t.Logf("diagnostic-only File UID locators: RF=%d round=%d keys=%d batch=%d max_readers=%d queries=%d rows=%d elapsed=%s driver_total=%s streams=%d stream_total=%s",
					scyllaReplicationFactor, round, keys, batchSize, readers, queries, keys, elapsed, driver, starts, stream)
			}
		}
	}
	if os.Getenv("GITSTORE_TEST_SCYLLA_SERVER_TRACE") == "1" {
		for _, keys := range []int{1, 16} {
			traceCapacityRoutingRequest(t, t.Context(), session, evidence, func(ctx context.Context, tracer gocql.Tracer) (bool, error) {
				return true, read(ctx, uids[:keys], tracer)
			})
		}
	}
}

func TestScylla_FileUpdateWriteAmplification(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	stores := make([]datastore.Datastore, 2)
	for i := range stores {
		var err error
		stores[i], err = scylla.New(cfg, zap.NewNop())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, stores[i].Close()) })
	}
	for round := range 3 {
		for _, workers := range []int{4, 16} {
			namespace := &datastore.Namespace{UID: newID(), Name: "file-update-" + newID()}
			require.NoError(t, stores[0].CreateNamespace(t.Context(), namespace))
			files := make([]*datastore.File, workers)
			for i := range files {
				files[i] = &datastore.File{
					UID: newID(), Namespace: namespace.Name, Name: fmt.Sprintf("file-%d", i),
					Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", Generation: 1, ResourceVersion: "1",
					CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
					Spec:              json.RawMessage(`{"path":"example"}`), Body: "initial",
				}
				require.NoError(t, stores[0].CreateFile(t.Context(), files[i]))
			}
			evidence := &capacityQueryEvidence{tables: make(map[string]int)}
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), capacityQueryKey{}, evidence), 30*time.Second)
			start := make(chan struct{})
			errs := make([]error, workers)
			var running sync.WaitGroup
			for i, file := range files {
				running.Go(func() {
					<-start
					for sequence := range 16 {
						expected := file.ResourceVersion
						file.Body = fmt.Sprintf("worker-%d-sequence-%d", i, sequence)
						file.GitCommitSHA = strconv.Itoa(sequence)
						file.Generation++
						datastore.AdvanceFileSystemVersion(file)
						if err := stores[i%len(stores)].UpdateFile(ctx, file, expected); err != nil {
							errs[i] = err
							return
						}
					}
				})
			}
			before := time.Now()
			close(start)
			running.Wait()
			elapsed := time.Since(before)
			cancel()
			require.NoError(t, errors.Join(errs...))
			evidence.mu.Lock()
			count, duration, written := evidence.fileCASAttempts, evidence.fileCASDuration, 0
			for _, n := range evidence.fileCASFields {
				written += n
			}
			evidence.mu.Unlock()
			require.Equal(t, workers*16, count)
			t.Logf("diagnostic-only actual File update: RF%d round=%d workers=%d clients=%d updates=%d elapsed=%s CASmean=%s setColumnsPerCAS=%.2f",
				scyllaReplicationFactor, round, workers, len(stores), count, elapsed, duration/time.Duration(count), float64(written)/float64(count))
			for _, file := range files {
				current, err := stores[1].GetFile(t.Context(), file.UID)
				require.NoError(t, err)
				require.Equal(t, file, current, "all actual update results must retain their full envelope")
				require.Equal(t, "17", current.ResourceVersion)
			}
		}
	}
}

func TestScylla_FileScopedUpdateWriteAmplification(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	for _, workers := range []int{4, 16} {
		namespace := &datastore.Namespace{UID: newID(), Name: "scoped-file-update-" + newID()}
		require.NoError(t, store.CreateNamespace(t.Context(), namespace))
		repository := &datastore.Repository{UID: newID(), Namespace: namespace.Name, Name: "catalog"}
		require.NoError(t, store.CreateRepositoryInActiveNamespace(t.Context(), repository))
		files := make([]*datastore.File, workers)
		for i := range files {
			files[i] = &datastore.File{
				UID: newID(), Namespace: namespace.Name, Name: fmt.Sprintf("file-%d", i), RepositoryID: repository.UID,
				Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", Generation: 1, ResourceVersion: "1",
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
				Spec:              json.RawMessage(`{"path":"example"}`), Body: "initial",
			}
			require.NoError(t, store.CreateFile(t.Context(), files[i]))
		}
		registry := prometheus.NewRegistry()
		batcher := datastore.NewInstrumentedDatastoreWithRegistry(store, "scylla", zap.NewNop(), registry).(datastore.CatalogMutationBatcher)
		evidence := &capacityQueryEvidence{tables: make(map[string]int)}
		ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), capacityQueryKey{}, evidence), 30*time.Second)
		before := time.Now()
		for sequence := range 16 {
			require.NoError(t, batcher.WithCatalogMutationBatch(ctx, namespace.Name, func(batchCtx context.Context) error {
				errs := make([]error, workers)
				var running sync.WaitGroup
				for i, file := range files {
					running.Go(func() {
						expected := file.ResourceVersion
						file.Body = fmt.Sprintf("worker-%d-sequence-%d", i, sequence)
						file.GitCommitSHA = strconv.Itoa(sequence)
						file.Generation++
						datastore.AdvanceFileSystemVersion(file)
						errs[i] = store.UpdateFile(batchCtx, file, expected)
					})
				}
				running.Wait()
				return errors.Join(errs...)
			}))
		}
		elapsed := time.Since(before)
		cancel()
		families, err := registry.Gather()
		require.NoError(t, err)
		var batches, batchedRows, rows uint64
		var rowDuration float64
		for _, family := range families {
			if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
				continue
			}
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() != "operation" {
						continue
					}
					histogram := metric.GetHistogram()
					switch label.GetValue() {
					case "FileConditionalBatch2", "FileConditionalBatch3", "FileConditionalBatch4":
						batches += histogram.GetSampleCount()
						batchedRows += histogram.GetSampleCount() * uint64(label.GetValue()[len(label.GetValue())-1]-'0')
					case "FileAuthoritativeCAS":
						rows = histogram.GetSampleCount()
						rowDuration = histogram.GetSampleSum()
					}
				}
			}
		}
		evidence.mu.Lock()
		singles := evidence.fileCASAttempts
		evidence.mu.Unlock()
		require.EqualValues(t, workers*16, rows)
		require.EqualValues(t, rows, uint64(singles)+batchedRows, "every row must reconcile to an actual single or batch CAS")
		t.Logf("diagnostic-only actual scoped File updates: RF=%d workers=%d updates=%d physicalCASRequests=%d batchRows=%d elapsed=%s containedRowCASMean=%s",
			scyllaReplicationFactor, workers, rows, uint64(singles)+batches, batchedRows, elapsed, time.Duration(rowDuration/float64(rows)*float64(time.Second)))
		for _, file := range files {
			current, err := store.GetFile(t.Context(), file.UID)
			require.NoError(t, err)
			require.Equal(t, file, current)
			require.Equal(t, "17", current.ResourceVersion)
		}
	}
}

func TestScylla_FileCASPartitionTopology(t *testing.T) {
	const maxWorkers, writesPerWorker = 32, 16
	store := newTestStore(t)
	sessions := []*gocql.Session{newRawSession(t), newRawSession(t)}
	for round := range 3 {
		for _, workers := range []int{4, 16, maxWorkers} {
			partitions := []int{1, workers}
			if round%2 != 0 {
				partitions[0], partitions[1] = partitions[1], partitions[0]
			}
			for _, partitionCount := range partitions {
				t.Run(fmt.Sprintf("round-%d/workers-%d/partitions-%d", round, workers, partitionCount), func(t *testing.T) {
					namespaces := make([]string, partitionCount)
					for i := range namespaces {
						namespaces[i] = "file-cas-" + newID()
						require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
							UID: newID(), Name: namespaces[i],
						}))
					}
					files := make([]*datastore.File, workers)
					uids := make([]gocql.UUID, workers)
					for worker := range workers {
						files[worker] = &datastore.File{
							UID: newID(), Namespace: namespaces[worker%partitionCount],
							Name: fmt.Sprintf("file-%d", worker), Kind: "File",
							APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
							CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
						}
						require.NoError(t, store.CreateFile(t.Context(), files[worker]))
						var err error
						uids[worker], err = gocql.ParseUUID(files[worker].UID)
						require.NoError(t, err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					var samples [maxWorkers][writesPerWorker]time.Duration
					failures := make(chan error, workers)
					start := make(chan struct{})
					var wg sync.WaitGroup
					for worker := range workers {
						wg.Go(func() {
							<-start
							file := files[worker]
							for sequence := range writesPerWorker {
								before := time.Now()
								applied, err := execCapacityCAS(sessions[worker%len(sessions)].Query(
									"UPDATE files_by_namespace SET resource_version=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
									strconv.Itoa(sequence+2), file.Namespace, file.CreationTimestamp,
									uids[worker], strconv.Itoa(sequence+1),
								).WithContext(ctx))
								samples[worker][sequence] = time.Since(before)
								if err != nil {
									failures <- fmt.Errorf("worker %d sequence %d: %w", worker, sequence, err)
									return
								}
								if !applied {
									failures <- fmt.Errorf("worker %d sequence %d: independent row CAS not applied", worker, sequence)
									return
								}
							}

						})
					}
					before := time.Now()
					close(start)
					wg.Wait()
					elapsed := time.Since(before)
					close(failures)
					for err := range failures {
						t.Error(err)
					}
					if t.Failed() {
						return
					}
					durations := make([]time.Duration, 0, workers*writesPerWorker)
					var total time.Duration
					for _, workerSamples := range samples[:workers] {
						for _, duration := range workerSamples {
							durations = append(durations, duration)
							total += duration
						}
					}
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					t.Logf("diagnostic-only native File CAS: RF%d test keyspace, partitions=%d clients=%d workers=%d writes=%d elapsed=%s mean=%s p95=%s p99=%s",
						scyllaReplicationFactor, partitionCount, len(sessions), workers, len(durations), elapsed, total/time.Duration(len(durations)),
						durations[(len(durations)*95+99)/100-1], durations[(len(durations)*99+99)/100-1])
					for _, file := range files {
						got, err := store.GetFile(t.Context(), file.UID)
						require.NoError(t, err)
						require.Equal(t, strconv.Itoa(writesPerWorker+1), got.ResourceVersion)
					}
				})
			}
		}
	}
}

func execCapacityBatchCAS(batch *gocql.Batch) (bool, error) {
	applied, iter, err := batch.GetSession().MapExecuteBatchCAS(batch, make(map[string]any))
	if iter != nil {
		err = errors.Join(err, iter.Close())
	}
	return applied, err
}

type capacityRoutingEvidence struct {
	mu              sync.Mutex
	hosts           map[string]int
	requests        int
	attempts        int
	driverDuration  time.Duration
	streamStarts    int
	streamFinishes  int
	streamAbandoned int
	streamDuration  time.Duration
	streams         sync.WaitGroup
}

type capacityRoutingKey struct{}

func (e *capacityRoutingEvidence) record(ctx context.Context, host *gocql.HostInfo, attempt int, elapsed time.Duration) {
	if ctx.Value(capacityRoutingKey{}) != e {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hosts[host.ConnectAddress().String()]++
	e.requests++
	e.attempts += attempt
	e.driverDuration += elapsed
}

func (e *capacityRoutingEvidence) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	e.record(ctx, query.Host, query.Attempt, query.End.Sub(query.Start))
}

func (e *capacityRoutingEvidence) ObserveBatch(ctx context.Context, batch gocql.ObservedBatch) {
	e.record(ctx, batch.Host, batch.Attempt, batch.End.Sub(batch.Start))
}

func (e *capacityRoutingEvidence) StreamContext(ctx context.Context) gocql.StreamObserverContext {
	if ctx.Value(capacityRoutingKey{}) != e {
		return nil
	}
	return &capacityRoutingStream{evidence: e}
}

type capacityRoutingStream struct {
	evidence *capacityRoutingEvidence
	started  time.Time
}

func (s *capacityRoutingStream) StreamStarted(gocql.ObservedStream) {
	started := time.Now()
	s.evidence.mu.Lock()
	s.started = started
	s.evidence.streamStarts++
	s.evidence.streams.Add(1)
	s.evidence.mu.Unlock()
}

func (s *capacityRoutingStream) StreamFinished(gocql.ObservedStream) {
	finished := time.Now()
	s.evidence.mu.Lock()
	s.evidence.streamFinishes++
	s.evidence.streamDuration += finished.Sub(s.started)
	s.evidence.mu.Unlock()
	s.evidence.streams.Done()
}

func (s *capacityRoutingStream) StreamAbandoned(gocql.ObservedStream) {
	s.evidence.mu.Lock()
	s.evidence.streamAbandoned++
	s.evidence.mu.Unlock()
	s.evidence.streams.Done()
}

type capacityServerTrace struct {
	mu      sync.Mutex
	id      gocql.UUID
	calls   int
	invalid bool
}

func (s *capacityServerTrace) Trace(id []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(id) != len(s.id) {
		s.invalid = true
		return
	}
	copy(s.id[:], id)
}

func traceCapacityRoutingRequest(t *testing.T, ctx context.Context, session *gocql.Session, evidence *capacityRoutingEvidence,
	execute func(context.Context, gocql.Tracer) (bool, error),
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	evidence.mu.Lock()
	driverBefore, streamBefore := evidence.driverDuration, evidence.streamDuration
	startsBefore, finishesBefore, abandonedBefore := evidence.streamStarts, evidence.streamFinishes, evidence.streamAbandoned
	attemptsBefore := evidence.attempts
	evidence.mu.Unlock()
	tracer := &capacityServerTrace{}
	started := time.Now()
	applied, err := execute(context.WithValue(ctx, capacityRoutingKey{}, evidence), tracer)
	elapsed := time.Since(started)
	require.NoError(t, err)
	require.True(t, applied)
	evidence.streams.Wait()
	evidence.mu.Lock()
	driver, stream := evidence.driverDuration-driverBefore, evidence.streamDuration-streamBefore
	starts, finishes, abandoned := evidence.streamStarts-startsBefore, evidence.streamFinishes-finishesBefore, evidence.streamAbandoned-abandonedBefore
	attempts := evidence.attempts - attemptsBefore
	evidence.mu.Unlock()
	require.Equal(t, 1, starts)
	require.Equal(t, starts, finishes)
	require.Zero(t, abandoned)
	require.Zero(t, attempts)
	tracer.mu.Lock()
	id, calls, invalid := tracer.id, tracer.calls, tracer.invalid
	tracer.mu.Unlock()
	require.Equal(t, 1, calls, "one explicit native trace must identify this exact request")
	require.False(t, invalid)
	require.NotEqual(t, gocql.UUID{}, id)
	t.Logf("Native server trace requested: id=%s", id)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var duration int64
		var coordinator net.IP
		err := session.Query("SELECT duration,coordinator FROM system_traces.sessions WHERE session_id=?", id).
			WithContext(ctx).Scan(&duration, &coordinator)
		if err != nil && !errors.Is(err, gocql.ErrNotFound) {
			require.NoError(t, err)
		}
		require.GreaterOrEqual(t, duration, int64(0))
		if err == nil && duration > 0 {
			require.NotEmpty(t, coordinator)
			t.Logf("diagnostic-only native server trace: id=%s server_coordinator=%s server_duration_us=%d caller=%s driver=%s stream=%s",
				id, coordinator, duration, elapsed, driver, stream)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native trace session did not publish a completed duration: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestScylla_ConditionalCoordinatorRouting(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_SCYLLA_DIRECT_PEERS") != "1" || scyllaReplicationFactor != 3 {
		t.Skip("requires an isolated RF3 cluster with directly reachable peer addresses")
	}
	control := os.Getenv("GITSTORE_TEST_SCYLLA_SERVER_TRACE_CONTROL_DIR")
	if control != "" {
		require.Equal(t, "1", os.Getenv("GITSTORE_TEST_SCYLLA_SERVER_TRACE"))
	}
	const warmup, writes = 8, 64
	store := newTestStore(t)
	host, rawPort, err := net.SplitHostPort(scyllaAddr)
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	for round := range 3 {
		for _, rows := range []int{1, 4, 16} {
			policies := []string{"default", "round-robin", "token-aware", "uncoalesced"}
			for offset := range policies {
				policy := policies[(offset+round)%len(policies)]
				if control != "" && (round != 0 || rows != 4 || policy != "default") {
					continue
				}
				t.Run(fmt.Sprintf("round-%d/rows-%d/policy-%s", round, rows, policy), func(t *testing.T) {
					evidence := &capacityRoutingEvidence{hosts: make(map[string]int)}
					cluster := gocql.NewCluster(host)
					cluster.Port, cluster.Keyspace = port, scyllaKeyspace
					cluster.Consistency = gocql.Quorum
					cluster.Timeout = 5 * time.Second
					cluster.DisableShardAwarePort = true
					cluster.QueryObserver, cluster.BatchObserver = evidence, evidence
					cluster.StreamObserver = evidence
					switch policy {
					case "round-robin":
						cluster.PoolConfig.HostSelectionPolicy = gocql.RoundRobinHostPolicy()
					case "token-aware":
						cluster.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(gocql.RoundRobinHostPolicy())
					case "uncoalesced":
						cluster.WriteCoalesceWaitTime = 0
					}
					session, err := cluster.CreateSession()
					require.NoError(t, err)
					defer session.Close()
					if round == 0 && policy == "default" && (rows == 1 || control != "") {
						var replication map[string]string
						require.NoError(t, session.Query("SELECT replication FROM system_schema.keyspaces WHERE keyspace_name=?", scyllaKeyspace).
							WithContext(t.Context()).Scan(&replication))
						require.Greater(t, len(replication), 1)
						for name, factor := range replication {
							if name != "class" {
								require.Equal(t, "3", factor)
							}
						}
						t.Logf("Native driver diagnostic keyspace %s replication=%v", scyllaKeyspace, replication)
					}
					namespace := "file-routing-" + newID()
					require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
					files := make([]*datastore.File, rows)
					uids := make([]gocql.UUID, rows)
					for i := range rows {
						files[i] = &datastore.File{
							UID: newID(), Namespace: namespace, Name: fmt.Sprintf("file-%d", i),
							Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
							CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
							Body:              strings.Repeat("routing fixture ", 32),
						}
						require.NoError(t, store.CreateFile(t.Context(), files[i]))
						uids[i], err = gocql.ParseUUID(files[i].UID)
						require.NoError(t, err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					const statement = "UPDATE files_by_namespace SET resource_version=?,body=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
					execute := func(queryCtx context.Context, sequence int, tracer gocql.Tracer) (bool, error) {
						if rows == 1 {
							file := files[0]
							return execCapacityCAS(session.Query(statement,
								strconv.Itoa(sequence+2), file.Body, namespace, file.CreationTimestamp,
								uids[0], strconv.Itoa(sequence+1)).WithContext(queryCtx).Trace(tracer))
						}
						batch := session.NewBatch(gocql.LoggedBatch).WithContext(queryCtx).RetryPolicy(&gocql.SimpleRetryPolicy{}).Trace(tracer)
						for i, file := range files {
							batch.Query(statement, strconv.Itoa(sequence+2), file.Body, namespace,
								file.CreationTimestamp, uids[i], strconv.Itoa(sequence+1))
						}
						return execCapacityBatchCAS(batch)
					}
					durations := make([]time.Duration, 0, writes)
					for sequence := range warmup + writes {
						queryCtx := ctx
						if sequence >= warmup {
							queryCtx = context.WithValue(ctx, capacityRoutingKey{}, evidence)
						}
						before := time.Now()
						applied, err := execute(queryCtx, sequence, nil)
						duration := time.Since(before)
						require.NoError(t, err)
						require.True(t, applied)
						if sequence >= warmup {
							durations = append(durations, duration)
						}
					}
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					evidence.streams.Wait()
					evidence.mu.Lock()
					hosts, attempts := evidence.hosts, evidence.attempts
					driverDuration, streamDuration := evidence.driverDuration, evidence.streamDuration
					streamStarts, streamFinishes, streamAbandoned := evidence.streamStarts, evidence.streamFinishes, evidence.streamAbandoned
					evidence.mu.Unlock()
					observations := 0
					for _, count := range hosts {
						observations += count
					}
					require.Equal(t, writes, observations)
					require.Zero(t, attempts)
					require.GreaterOrEqual(t, streamStarts, writes)
					require.Equal(t, streamStarts, streamFinishes)
					require.Zero(t, streamAbandoned)
					if policy == "round-robin" {
						require.Len(t, hosts, 3, "control must reach three distinct actual peer addresses, not a translated contact point")
					} else {
						require.Len(t, hosts, 1, "stable LWTs must retain their replica coordinator")
					}
					t.Logf("diagnostic-only RF3 routing: round=%d rows=%d policy=%s coalescing=%s writes=%d coordinators=%d p50=%s p95=%s p99=%s driver_total=%s streams=%d stream_total=%s",
						round, rows, policy, cluster.WriteCoalesceWaitTime, writes, len(hosts), durations[writes/2-1],
						durations[(writes*95+99)/100-1], durations[(writes*99+99)/100-1],
						driverDuration, streamFinishes, streamDuration)
					finalVersion := warmup + writes + 1
					if control != "" {
						waitCapacityServerTraceLoad(t, control)
						for probe := range 6 {
							started := time.Now().UTC()
							t.Logf("Loaded native server probe: probe=%d started_at=%s", probe, started.Format(time.RFC3339Nano))
							traceCapacityRoutingRequest(t, t.Context(), session, evidence,
								func(ctx context.Context, tracer gocql.Tracer) (bool, error) {
									return execute(ctx, warmup+writes+probe, tracer)
								})
							t.Logf("Loaded native server probe: probe=%d completed_at=%s", probe, time.Now().UTC().Format(time.RFC3339Nano))
							finalVersion++
							if probe != 5 {
								timer := time.NewTimer(5 * time.Second)
								select {
								case <-t.Context().Done():
									timer.Stop()
									t.Fatalf("loaded native probe cancelled: %v", t.Context().Err())
								case <-timer.C:
								}
							}
						}
					} else if os.Getenv("GITSTORE_TEST_SCYLLA_SERVER_TRACE") == "1" && round == 0 &&
						(policy == "default" || policy == "uncoalesced") {
						traceCapacityRoutingRequest(t, ctx, session, evidence,
							func(ctx context.Context, tracer gocql.Tracer) (bool, error) {
								return execute(ctx, warmup+writes, tracer)
							})
						finalVersion++
					}
					for _, file := range files {
						got, err := store.GetFile(t.Context(), file.UID)
						require.NoError(t, err)
						require.Equal(t, strconv.Itoa(finalVersion), got.ResourceVersion)
						require.Equal(t, file.Body, got.Body)
					}
					session.Close()
				})
			}
		}
	}
}

func waitCapacityServerTraceLoad(t *testing.T, control string) {
	t.Helper()
	require.True(t, filepath.IsAbs(control))
	info, err := os.Lstat(control)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	ready, err := os.OpenFile(filepath.Join(control, "ready"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	_, writeErr := ready.WriteString("ready\n")
	require.NoError(t, ready.Close())
	require.NoError(t, writeErr)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		path := filepath.Join(control, "start")
		info, err := os.Lstat(path)
		if err == nil {
			require.True(t, info.Mode().IsRegular())
			require.Equal(t, int64(len("start\n")), info.Size())
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "start\n", string(body))
			return
		}
		require.ErrorIs(t, err, os.ErrNotExist)
		select {
		case <-ctx.Done():
			t.Fatalf("native probe load start did not arrive: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestScylla_FileCASBatchTopology(t *testing.T) {
	const maxWorkers, writesPerWorker = 32, 16
	type batchMode struct{ batchSize, groupSize int }
	store := newTestStore(t)
	sessions := []*gocql.Session{newRawSession(t), newRawSession(t)}
	for _, workers := range []int{4, 16, maxWorkers} {
		modes := []batchMode{{1, 1}, {1, 4}, {4, 4}}
		if workers > 4 {
			modes = append(modes, batchMode{1, 8}, batchMode{8, 8})
		}
		for round := range 3 {
			for offset := range modes {
				mode := modes[(offset+round)%len(modes)]
				batchSize, groupSize := mode.batchSize, mode.groupSize
				t.Run(fmt.Sprintf("workers-%d/round-%d/batch-%d/inflight-%d", workers, round, batchSize, workers/groupSize), func(t *testing.T) {
					namespace := "file-cas-batch-" + newID()
					require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
						UID: newID(), Name: namespace,
					}))
					files := make([]*datastore.File, workers)
					uids := make([]gocql.UUID, workers)
					for worker := range workers {
						files[worker] = &datastore.File{
							UID: newID(), Namespace: namespace, Name: fmt.Sprintf("file-%d", worker),
							Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
							CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
							Body:              strings.Repeat("bounded conditional-write fixture ", 16),
						}
						require.NoError(t, store.CreateFile(t.Context(), files[worker]))
						var err error
						uids[worker], err = gocql.ParseUUID(files[worker].UID)
						require.NoError(t, err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
					defer cancel()
					var samples [maxWorkers][writesPerWorker]time.Duration
					failures := make(chan error, workers/groupSize)
					start := make(chan struct{})
					var wg sync.WaitGroup
					for first := 0; first < workers; first += groupSize {
						wg.Go(func() {
							<-start
							session := sessions[(first/groupSize)%len(sessions)]
							for sequence := range writesPerWorker {
								const statement = "UPDATE files_by_namespace SET resource_version=?,body=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
								var applied bool
								var err error
								if batchSize == 1 {
									for worker := first; worker < first+groupSize; worker++ {
										file := files[worker]
										before := time.Now()
										applied, err = execCapacityCAS(session.Query(statement,
											strconv.Itoa(sequence+2), file.Body, namespace, file.CreationTimestamp,
											uids[worker], strconv.Itoa(sequence+1)).WithContext(ctx))
										samples[worker][sequence] = time.Since(before)
										if err != nil || !applied {
											break
										}
									}
								} else {
									before := time.Now()
									batch := session.NewBatch(gocql.LoggedBatch).WithContext(ctx)
									for worker := first; worker < first+batchSize; worker++ {
										file := files[worker]
										batch.Query(statement, strconv.Itoa(sequence+2), file.Body,
											namespace, file.CreationTimestamp, uids[worker], strconv.Itoa(sequence+1))
									}
									applied, err = execCapacityBatchCAS(batch)
									duration := time.Since(before)
									for worker := first; worker < first+batchSize; worker++ {
										samples[worker][sequence] = duration
									}
								}
								if err != nil || !applied {
									failures <- fmt.Errorf("first worker %d sequence %d: applied=%t err=%v", first, sequence, applied, err)
									return
								}
							}
						})
					}
					before := time.Now()
					close(start)
					wg.Wait()
					elapsed := time.Since(before)
					close(failures)
					for err := range failures {
						t.Error(err)
					}
					if t.Failed() {
						return
					}
					durations := make([]time.Duration, 0, workers*writesPerWorker)
					var total time.Duration
					for _, workerSamples := range samples[:workers] {
						for _, duration := range workerSamples {
							durations = append(durations, duration)
							total += duration
						}
					}
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					t.Logf("diagnostic-only native File CAS batching: round=%d RF=%d partitions=1 clients=%d independent_rows=%d batch_rows=%d inflight_requests=%d writes=%d elapsed=%s mean=%s p95=%s p99=%s",
						round, scyllaReplicationFactor, len(sessions), workers, batchSize, workers/groupSize,
						len(durations), elapsed, total/time.Duration(len(durations)),
						durations[(len(durations)*95+99)/100-1], durations[(len(durations)*99+99)/100-1])
					for _, file := range files {
						got, err := store.GetFile(t.Context(), file.UID)
						require.NoError(t, err)
						require.Equal(t, strconv.Itoa(writesPerWorker+1), got.ResourceVersion)
						require.Equal(t, file.Body, got.Body)
					}
				})
			}
		}
	}
}

func TestScylla_FileCASBatchConflictIsAtomic(t *testing.T) {
	const rows = 4
	store := newTestStore(t)
	session := newRawSession(t)
	namespace := "file-cas-batch-conflict-" + newID()
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
	files := make([]*datastore.File, rows)
	uids := make([]gocql.UUID, rows)
	for i := range rows {
		files[i] = &datastore.File{
			UID: newID(), Namespace: namespace, Name: fmt.Sprintf("file-%d", i),
			Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), Body: "before",
		}
		require.NoError(t, store.CreateFile(t.Context(), files[i]))
		var err error
		uids[i], err = gocql.ParseUUID(files[i].UID)
		require.NoError(t, err)
	}
	const statement = "UPDATE files_by_namespace SET resource_version=?,body=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	for _, conflict := range []bool{true, false} {
		batch := session.NewBatch(gocql.LoggedBatch).WithContext(t.Context())
		for i, file := range files {
			expected := "1"
			if conflict && i == rows-1 {
				expected = "stale"
			}
			batch.Query(statement, "2", "after", namespace, file.CreationTimestamp, uids[i], expected)
		}
		applied, err := execCapacityBatchCAS(batch)
		require.NoError(t, err)
		require.Equal(t, !conflict, applied)
		for _, file := range files {
			got, err := store.GetFile(t.Context(), file.UID)
			require.NoError(t, err)
			if conflict {
				require.Equal(t, "1", got.ResourceVersion)
				require.Equal(t, "before", got.Body)
			} else {
				require.Equal(t, "2", got.ResourceVersion)
				require.Equal(t, "after", got.Body)
			}
		}
	}
}
