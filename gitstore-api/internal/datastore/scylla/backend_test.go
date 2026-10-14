// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scylladb/gocqlx/v3/table"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var (
	scyllaAddr              string
	scyllaKeyspace          string
	scyllaReplicationFactor = 1
)

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
	scyllaKeyspace = fmt.Sprintf("gitstore_scylla_test_%d_%s", os.Getpid(), strings.ReplaceAll(uuid.NewString(), "-", "")[:8])

	provisionKeyspace(scyllaAddr, scyllaKeyspace)
	clockStarted := time.Now()
	clockStop, clockDone := make(chan struct{}), make(chan struct{})
	var invalidClock atomic.Bool
	go func() {
		defer close(clockDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-clockStop:
				return
			case <-ticker.C:
				now := time.Now()
				drift := now.Round(0).Sub(clockStarted.Round(0)) - now.Sub(clockStarted)
				if drift > time.Second || drift < -time.Second {
					if invalidClock.CompareAndSwap(false, true) {
						fmt.Fprintf(os.Stderr, "Scylla regression clock invalid: wall/monotonic drift %s; TTL, lease and CDC evidence cannot pass\n", drift)
					}
				}
			}
		}
	}()
	code := m.Run()
	close(clockStop)
	<-clockDone
	if invalidClock.Load() {
		code = 1
	}
	dropKeyspace(scyllaAddr, scyllaKeyspace)

	os.Exit(code)
}

// contactPointTranslator returns an AddressTranslator that redirects all peer
// addresses to the original contact point. This is needed when Scylla runs in
// a Docker container — its rpc_address is an internal Docker IP, but the host
// connects via a forwarded port on the contact-point address.
func contactPointTranslator(contactHost string, contactPort int) gocql.AddressTranslator {
	contactIP := net.ParseIP(contactHost)
	return gocql.AddressTranslatorFunc(func(_ net.IP, port int) (net.IP, int) {
		if contactPort > 0 {
			port = contactPort
		}
		return contactIP, port
	})
}

// provisionKeyspace creates the keyspace using a temporary no-keyspace session.
// This mirrors what the compose scylla-init service does for local/CI stacks.
// Retries for up to 30 s because ScyllaDB logs "Starting listening for CQL clients"
// slightly before it actually accepts connections.
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

func newTestStore(t *testing.T) datastore.Datastore {
	return newTestStoreWithWatchBucket(t, 0)
}

func newTestStoreWithWatchBucket(t *testing.T, watchBucketSize int) datastore.Datastore {
	t.Helper()
	store, err := scylla.New(testScyllaConfig(t), zap.NewNop(), watchBucketSize)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testScyllaConfig(t *testing.T) config.ScyllaConfig {
	t.Helper()
	host, portStr, splitErr := net.SplitHostPort(scyllaAddr)
	if splitErr != nil {
		host = scyllaAddr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	return config.ScyllaConfig{
		AutoMigrate:           true,
		Hosts:                 []string{scyllaAddr},
		Keyspace:              scyllaKeyspace,
		DisableShardAwarePort: true,
		IgnorePeerAddr:        true,
		AddressTranslator:     contactPointTranslator(host, port),
	}
}

func newTestStores(t *testing.T) (datastore.Datastore, datastore.Datastore) {
	t.Helper()
	return newTestStore(t), newTestStore(t)
}

func TestServiceAccountClusterConcurrentIdentityAndRepair(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	ctx := t.Context()
	for _, sameUID := range []bool{false, true} {
		name, uid := "sa-"+newID(), newID()
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for i := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				n, u := name, newID()
				if sameUID {
					n, u = "sa-"+newID(), uid
				}
				store := first
				if i%2 != 0 {
					store = second
				}
				results <- store.CreateServiceAccount(ctx, &datastore.ServiceAccount{UID: u, Name: n, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)})
			}()
		}
		wg.Wait()
		close(results)
		winners := 0
		for err := range results {
			if err == nil {
				winners++
			} else {
				require.ErrorIs(t, err, datastore.ErrAlreadyExists)
			}
		}
		require.Equal(t, 1, winners, "exactly one API may reserve a global name or UID")
	}
	name, uid := "interrupted-"+newID(), gocql.TimeUUID()
	created := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, raw.Query("INSERT INTO service_accounts_by_cluster_name (name,uid,creation_timestamp,resource_version,public_keys,enrollment_complete) VALUES (?,?,?,?,?,?)", name, uid, created, "1", "[]", false).Exec())
	_, err := first.GetServiceAccountBySubject(ctx, name)
	require.ErrorIs(t, err, datastore.ErrNotFound, "incomplete create must not authenticate")
	repair, err := scylla.OpenProjectionRepairService(testScyllaConfig(t))
	require.NoError(t, err)
	defer repair.Close()
	plan, err := repair.Audit(ctx)
	require.NoError(t, err)
	require.Len(t, plan.ServiceAccountEnrollments, 1)
	_, err = repair.Apply(ctx, plan)
	require.NoError(t, err)
	account, err := second.GetServiceAccountByUID(ctx, uid.String())
	require.NoError(t, err)
	require.Equal(t, name, account.Name)
	// An interrupted delete leaves projections behind, but neither can authenticate.
	require.NoError(t, raw.Query("DELETE FROM service_accounts_by_cluster_name WHERE name=?", name).Exec())
	_, err = second.GetServiceAccountByUID(ctx, uid.String())
	require.ErrorIs(t, err, datastore.ErrNotFound)
	replacement := &datastore.ServiceAccount{UID: newID(), Name: name, ResourceVersion: "1", CreationTimestamp: created.Add(time.Second)}
	require.NoError(t, first.CreateServiceAccount(ctx, replacement))
	plan, err = repair.Audit(ctx)
	require.NoError(t, err)
	_, err = repair.Apply(ctx, plan)
	require.NoError(t, err)
	_, err = second.GetServiceAccountByUID(ctx, uid.String())
	require.ErrorIs(t, err, datastore.ErrNotFound)
	got, err := second.GetServiceAccountByUID(ctx, replacement.UID)
	require.NoError(t, err)
	require.Equal(t, name, got.Name)
	// Stale listing rows must not turn a nonempty collection into an empty page.
	listed := &datastore.ServiceAccount{UID: newID(), Name: "listed-" + newID(), ResourceVersion: "1", CreationTimestamp: created.Add(2 * time.Hour)}
	require.NoError(t, first.CreateServiceAccount(ctx, listed))
	for i := range 4 {
		ts := listed.CreationTimestamp.Add(time.Duration(i+1) * time.Second)
		require.NoError(t, raw.Query("INSERT INTO service_accounts_by_cluster_bucket (bucket,creation_timestamp,uid,name) VALUES (?,?,?,?)", ts.Format("2006-01"), ts, gocql.TimeUUID(), "dangling").Exec())
	}
	page, err := second.ListServiceAccounts(ctx, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, listed.UID, page.Items[0].UID)
}

type sessionRevocationStore interface {
	RevokeSession(context.Context, string, time.Time) error
	IsSessionRevoked(context.Context, string) (bool, error)
	ConsumeSession(context.Context, string, time.Time) (bool, error)
}

func TestSessionRevocationsAreSharedAcrossReplicas(t *testing.T) {
	firstStore, secondStore := newTestStores(t)
	first := firstStore.(sessionRevocationStore)
	second := secondStore.(sessionRevocationStore)
	ctx := context.Background()
	jti := "logout-" + newID()
	require.NoError(t, first.RevokeSession(ctx, jti, time.Now().Add(time.Hour)))
	revoked, err := second.IsSessionRevoked(ctx, jti)
	require.NoError(t, err)
	assert.True(t, revoked)

	refreshJTI := "refresh-" + newID()
	consumed, err := first.ConsumeSession(ctx, refreshJTI, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, consumed)
	consumed, err = second.ConsumeSession(ctx, refreshJTI, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, consumed)
}

func newID() string { return uuid.New().String() }

func newProduct(namespace, name string) *datastore.Product {
	return &datastore.Product{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestCategoryProductMembershipRetryRepairsInterruptedForwardWrite(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	ctx := t.Context()
	product := newProduct("membership-"+newID(), "product")
	firstIndex := first.(datastore.CategoryProductIndex)
	secondIndex := second.(datastore.CategoryProductIndex)
	product.ResourceVersion = "1"
	product.Status = json.RawMessage(`{}`)
	require.NoError(t, first.CreateProduct(ctx, product))
	categoryUID := newID()
	require.NoError(t, firstIndex.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))

	var shard int
	require.NoError(t, raw.Query("SELECT shard FROM category_products_by_product WHERE namespace=? AND product_uid=? AND category_uid=?",
		product.Namespace, uuid.MustParse(product.UID), categoryUID).Scan(&shard))
	require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Exec())

	// A second replica retries the same version after the reverse write survived.
	require.NoError(t, secondIndex.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	var version string
	err := raw.Query("SELECT resource_version FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Scan(&version)
	require.NoError(t, err, "same-version retry must complete the interrupted forward write")
	require.Equal(t, product.ResourceVersion, version)

	require.NoError(t, raw.Query("UPDATE category_products_by_product SET resource_version=? WHERE namespace=? AND product_uid=? AND category_uid=?",
		"2", product.Namespace, uuid.MustParse(product.UID), categoryUID).Exec())
	require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Exec())
	require.NoError(t, firstIndex.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	err = raw.Query("SELECT resource_version FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
		product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Scan(&version)
	require.ErrorIs(t, err, gocql.ErrNotFound, "older retries must not restore superseded membership")
}

func TestCategoryProductDeletionCleansBothDirectionsAndFencesStaleRetry(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	ctx := t.Context()
	product := newProduct("membership-deletion-"+newID(), "product")
	product.ResourceVersion = "1"
	product.Status = json.RawMessage(`{}`)
	categoryUID := newID()
	index := first.(datastore.CategoryProductIndex)
	require.NoError(t, first.CreateProduct(ctx, product))
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	var shard int
	require.NoError(t, raw.Query("SELECT shard FROM category_products_by_product WHERE namespace=? AND product_uid=? AND category_uid=?",
		product.Namespace, uuid.MustParse(product.UID), categoryUID).Scan(&shard))
	require.NoError(t, second.DeleteProductWithResourceVersion(ctx, product.UID, product.ResourceVersion))
	assertAbsent := func() {
		t.Helper()
		var version string
		err := raw.Query("SELECT resource_version FROM category_products_by_product WHERE namespace=? AND product_uid=? AND category_uid=?",
			product.Namespace, uuid.MustParse(product.UID), categoryUID).Scan(&version)
		require.ErrorIs(t, err, gocql.ErrNotFound, "deleted Product reverse membership must be removed")
		err = raw.Query("SELECT resource_version FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?",
			product.Namespace, categoryUID, shard, product.CreationTimestamp, uuid.MustParse(product.UID)).Scan(&version)
		require.ErrorIs(t, err, gocql.ErrNotFound, "deleted Product forward membership must be removed")
	}
	assertAbsent()
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
	assertAbsent()
}

func TestScyllaCatalogListsHonorRequestCancellation(t *testing.T) {
	store := newTestStore(t)
	for name, list := range map[string]func(context.Context) error{
		"Product": func(ctx context.Context) error {
			_, err := store.ListProducts(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
		"CategoryTaxonomy": func(ctx context.Context) error {
			_, err := store.ListCategoryTaxonomies(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
		"Collection": func(ctx context.Context) error {
			_, err := store.ListCollections(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
		"ProductVariant": func(ctx context.Context) error {
			_, err := store.ListProductVariants(ctx, "cancelled", datastore.PageParams{First: 1})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, list(ctx), context.Canceled)
		})
	}
}

func TestScyllaCatalogPointsHonorRequestCancellation(t *testing.T) {
	store := newTestStore(t)
	uid := newID()
	for name, read := range map[string]func(context.Context) error{
		"Product/UID": func(ctx context.Context) error { _, err := store.GetProduct(ctx, uid); return err },
		"Product/name": func(ctx context.Context) error {
			_, err := store.GetProductByName(ctx, "cancelled", "product")
			return err
		},
		"CategoryTaxonomy/UID": func(ctx context.Context) error { _, err := store.GetCategoryTaxonomy(ctx, uid); return err },
		"CategoryTaxonomy/name": func(ctx context.Context) error {
			_, err := store.GetCategoryTaxonomyByName(ctx, "cancelled", "category")
			return err
		},
		"Collection/UID": func(ctx context.Context) error { _, err := store.GetCollection(ctx, uid); return err },
		"Collection/name": func(ctx context.Context) error {
			_, err := store.GetCollectionByName(ctx, "cancelled", "collection")
			return err
		},
		"ProductVariant/UID": func(ctx context.Context) error { _, err := store.GetProductVariant(ctx, uid); return err },
		"ProductVariant/name": func(ctx context.Context) error {
			_, err := store.GetProductVariantByName(ctx, "cancelled", "variant")
			return err
		},
		"ProductVariant/SKU": func(ctx context.Context) error {
			_, err := store.GetProductVariantBySKU(ctx, "cancelled", "sku")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, read(ctx), context.Canceled)
		})
	}
}

func TestCategoryProductsPagesRefillDanglingHotShard(t *testing.T) {
	first := newTestStore(t)
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	second, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	raw := newRawSession(t)
	ctx := t.Context()
	namespace, categoryUID := "category-page-"+newID(), newID()
	created := time.Now().UTC().Truncate(time.Millisecond)
	index := first.(datastore.CategoryProductIndex)
	var products []*datastore.Product
	t.Cleanup(func() {
		for shard := range 32 {
			require.NoError(t, raw.Query("DELETE FROM category_products_by_category WHERE namespace=? AND category_uid=? AND shard=?",
				namespace, categoryUID, shard).Exec())
		}
		for _, product := range products {
			require.NoError(t, raw.Query("DELETE FROM category_products_by_product WHERE namespace=? AND product_uid=?",
				namespace, uuid.MustParse(product.UID)).Exec())
			require.NoError(t, first.DeleteProduct(context.Background(), product.UID))
		}
	})
	for i := range 5 {
		product := newProduct(namespace, fmt.Sprintf("product-%d", i))
		product.CreationTimestamp = created.Add(-time.Duration(i+1) * time.Second)
		product.ResourceVersion = "1"
		product.Status = json.RawMessage(`{}`)
		require.NoError(t, first.CreateProduct(ctx, product))
		require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{categoryUID}))
		products = append(products, product)
	}
	for i := range 12 {
		require.NoError(t, raw.Query("INSERT INTO category_products_by_category (namespace,category_uid,shard,creation_timestamp,product_uid,resource_version) VALUES (?,?,?,?,?,?)",
			namespace, categoryUID, 0, created.Add(time.Duration(i)*time.Second), uuid.New(), "1").Exec())
	}
	require.NoError(t, raw.Query("INSERT INTO category_product_projection_state (projection,ready) VALUES (?,?)",
		"category-products", true).Exec())
	list := second.(datastore.CategoryProductIndex)
	page, err := list.ListCategoryProducts(ctx, namespace, categoryUID, datastore.PageParams{First: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.True(t, page.HasNext)
	require.Equal(t, products[0].UID, page.Items[0].UID)
	require.Equal(t, products[1].UID, page.Items[1].UID)
	next, err := list.ListCategoryProducts(ctx, namespace, categoryUID,
		datastore.PageParams{First: 2, After: pageCursor(products[1].CreationTimestamp, products[1].UID)})
	require.NoError(t, err)
	require.Len(t, next.Items, 2)
	require.Equal(t, products[2].UID, next.Items[0].UID)
	previous, err := list.ListCategoryProducts(ctx, namespace, categoryUID,
		datastore.PageParams{Last: 2, Before: pageCursor(products[2].CreationTimestamp, products[2].UID)})
	require.NoError(t, err)
	require.Equal(t, page.Items, previous.Items)
	require.False(t, previous.HasPrevious, "dangling projection rows cannot advertise live membership")
	assertCapacityDatastorePage(t, "Category.products/dangling-hot-shard", 2,
		func(ctx context.Context, params datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
			return list.ListCategoryProducts(ctx, namespace, categoryUID, params)
		}, func(product *datastore.Product) capacityPageKey {
			return capacityPageKey{uid: product.UID, created: product.CreationTimestamp}
		})
}

func newCollection(namespace, name string) *datastore.Collection {
	return &datastore.Collection{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Collection",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func newProductVariant(namespace, name, sku, productRefName string) *datastore.ProductVariant {
	return &datastore.ProductVariant{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "ProductVariant",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		SKU:               sku,
		ProductRefName:    productRefName,
	}
}

func newRepository() *datastore.Repository {
	now := time.Now().UTC().Truncate(time.Millisecond)
	uid := newID()
	namespace := "namespace-" + newID()[:8]
	return &datastore.Repository{
		UID:               uid,
		ID:                uid,
		Namespace:         namespace,
		NamespaceID:       namespace,
		Name:              "repo-" + newID()[:8],
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
	}
}

func setRepositoryContractFields(
	t *testing.T,
	repository *datastore.Repository,
	generation int64,
	resourceVersion string,
	status json.RawMessage,
) {
	t.Helper()
	value := reflect.ValueOf(repository).Elem()

	generationField := value.FieldByName("Generation")
	if !generationField.IsValid() {
		t.Fatal("datastore.Repository is missing Generation")
	}
	require.Equal(t, reflect.Int64, generationField.Kind())
	generationField.SetInt(generation)

	resourceVersionField := value.FieldByName("ResourceVersion")
	if !resourceVersionField.IsValid() {
		t.Fatal("datastore.Repository is missing ResourceVersion")
	}
	require.Equal(t, reflect.String, resourceVersionField.Kind())
	resourceVersionField.SetString(resourceVersion)

	statusField := value.FieldByName("Status")
	if !statusField.IsValid() {
		t.Fatal("datastore.Repository is missing Status")
	}
	require.Equal(t, reflect.TypeOf(json.RawMessage{}), statusField.Type())
	statusField.Set(reflect.ValueOf(status))
}

func repositoryContractFields(t *testing.T, repository *datastore.Repository) (int64, string, json.RawMessage) {
	t.Helper()
	value := reflect.ValueOf(repository).Elem()

	generationField := value.FieldByName("Generation")
	if !generationField.IsValid() {
		t.Fatal("datastore.Repository is missing Generation")
	}
	require.Equal(t, reflect.Int64, generationField.Kind())

	resourceVersionField := value.FieldByName("ResourceVersion")
	if !resourceVersionField.IsValid() {
		t.Fatal("datastore.Repository is missing ResourceVersion")
	}
	require.Equal(t, reflect.String, resourceVersionField.Kind())

	statusField := value.FieldByName("Status")
	if !statusField.IsValid() {
		t.Fatal("datastore.Repository is missing Status")
	}
	require.Equal(t, reflect.TypeOf(json.RawMessage{}), statusField.Type())

	return generationField.Int(), resourceVersionField.String(), statusField.Interface().(json.RawMessage)
}

// ── Repository ───────────────────────────────────────────────────────────────

func TestScylla_RepositoryResourceContractRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	initialStatus := json.RawMessage(`{
		"observedGeneration": 4,
		"lastAppliedRevision": "abc123",
		"conditions": [{
			"type": "Ready",
			"status": "True",
			"reason": "Reconciled",
			"message": "repository is ready",
			"lastTransitionTime": "2026-08-16T20:00:00Z",
			"observedGeneration": 4
		}]
	}`)
	setRepositoryContractFields(t, repository, 5, "12", initialStatus)

	require.NoError(t, store.CreateRepository(ctx, repository))

	created, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status := repositoryContractFields(t, created)
	assert.Equal(t, int64(5), generation)
	assert.Equal(t, "12", resourceVersion)
	assert.JSONEq(t, string(initialStatus), string(status))

	updatedStatus := json.RawMessage(`{
		"observedGeneration": 6,
		"lastAppliedRevision": "def456",
		"conditions": [{
			"type": "Ready",
			"status": "False",
			"reason": "Reconciling",
			"message": "repository update is pending",
			"lastTransitionTime": "2026-08-16T20:30:00Z",
			"observedGeneration": 6
		}]
	}`)
	setRepositoryContractFields(t, created, 6, "13", updatedStatus)
	require.NoError(t, store.UpdateRepository(ctx, created, "12"))

	updated, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status = repositoryContractFields(t, updated)
	assert.Equal(t, int64(6), generation)
	assert.Equal(t, "13", resourceVersion)
	assert.JSONEq(t, string(updatedStatus), string(status))
}

func TestScylla_RepositoryResourceContractLegacyNormalization(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	legacyRepository := newRepository()

	require.NoError(t, store.CreateRepository(ctx, legacyRepository))

	got, err := store.GetRepository(ctx, legacyRepository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status := repositoryContractFields(t, got)
	assert.Equal(t, int64(1), generation)
	assert.Equal(t, "1", resourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(status))
}

func TestScylla_RepositoryVersionTransitionsPersist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	require.NoError(t, store.CreateRepository(ctx, repository))

	expectedResourceVersion := repository.ResourceVersion
	datastore.AdvanceRepositorySpecVersion(repository)
	require.NoError(t, store.UpdateRepository(ctx, repository, expectedResourceVersion))

	afterSpec, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSpec.Generation)
	assert.Equal(t, "2", afterSpec.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(afterSpec.Status))

	afterSpec.Status = json.RawMessage(`{
		"observedGeneration": 2,
		"lastAppliedRevision": "main@sha1:abc",
		"conditions": [{
			"type": "Ready",
			"status": "True",
			"observedGeneration": 2,
			"lastTransitionTime": "2026-08-16T20:00:00Z"
		}]
	}`)
	expectedResourceVersion = afterSpec.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(afterSpec)
	require.NoError(t, store.UpdateRepository(ctx, afterSpec, expectedResourceVersion))

	afterSystem, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSystem.Generation)
	assert.Equal(t, "3", afterSystem.ResourceVersion)
	assert.JSONEq(t, string(afterSpec.Status), string(afterSystem.Status))
}

func TestScylla_RepositoryPushPolicyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	repository.MaxPackSizeBytes = 64 * 1024 * 1024
	repository.MaxFileSizeBytes = 8 * 1024 * 1024
	require.NoError(t, store.CreateRepository(ctx, repository))

	created, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, repository.MaxPackSizeBytes, created.MaxPackSizeBytes)
	assert.Equal(t, repository.MaxFileSizeBytes, created.MaxFileSizeBytes)

	expectedResourceVersion := created.ResourceVersion
	created.MaxPackSizeBytes *= 2
	created.MaxFileSizeBytes *= 2
	datastore.AdvanceRepositorySpecVersion(created)
	require.NoError(t, store.UpdateRepository(ctx, created, expectedResourceVersion))

	updated, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, created.MaxPackSizeBytes, updated.MaxPackSizeBytes)
	assert.Equal(t, created.MaxFileSizeBytes, updated.MaxFileSizeBytes)
}

func TestScylla_UpdateRepositoryRejectsStaleResourceVersion(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: repository.Namespace}))
	require.NoError(t, store.CreateRepository(ctx, repository))

	first, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	stale, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)

	first.Name = "first-writer"
	datastore.AdvanceRepositorySpecVersion(first)
	require.NoError(t, store.UpdateRepository(ctx, first, "1"))

	stale.Name = "stale-writer"
	datastore.AdvanceRepositorySpecVersion(stale)
	require.ErrorIs(t, store.UpdateRepository(ctx, stale, "1"), datastore.ErrConflict)

	got, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, "first-writer", got.Name)
	assert.Equal(t, "2", got.ResourceVersion)
}

func TestScylla_FileRoundTripAndIndexedLookup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	file := &datastore.File{
		UID: newID(), Namespace: "test-ns", Name: "hero-" + newID()[:8],
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		Spec: json.RawMessage(`{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero"}}`),
		Body: "alt text", OwnerReferences: json.RawMessage(`[{"kind":"Repository","name":"repo","uid":"owner"}]`),
		Status: json.RawMessage(`{"conditions":[]}`),
	}
	require.NoError(t, store.CreateFile(ctx, file))
	got, err := store.GetFileByName(ctx, file.Namespace, file.Name)
	require.NoError(t, err)
	assert.Equal(t, file.UID, got.UID)
	assert.JSONEq(t, string(file.Spec), string(got.Spec))
	assert.JSONEq(t, string(file.OwnerReferences), string(got.OwnerReferences))
	assert.Equal(t, file.Body, got.Body)
}

func TestScylla_FilePointReadsPreserveAuthoritativeReadback(t *testing.T) {
	first, second := newTestStores(t)
	readers := []datastore.Datastore{first, second}
	readFiles := func(store datastore.Datastore, uids []string) []*datastore.File {
		t.Helper()
		files := make([]*datastore.File, len(uids))
		for i, uid := range uids {
			file, err := store.GetFile(t.Context(), uid)
			if errors.Is(err, datastore.ErrNotFound) {
				continue
			}
			require.NoError(t, err)
			files[i] = file
		}
		return files
	}
	files := make([]*datastore.File, 7)
	for i := range files {
		namespace := "file-snapshots-" + strconv.Itoa(i%2) + "-" + newID()[:8]
		if i >= 2 {
			namespace = files[i%2].Namespace
		} else {
			require.NoError(t, first.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
		}
		files[i] = &datastore.File{UID: newID(), Namespace: namespace, Name: "file-" + strconv.Itoa(i),
			Kind: "File", APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Annotations:       map[string]string{"sequence": "1"}, Spec: json.RawMessage(`{"contentType":"image/jpeg"}`)}
		require.NoError(t, first.CreateFile(t.Context(), files[i]))
	}
	uids := []string{files[6].UID, "invalid", files[3].UID, newID(), files[0].UID, files[3].UID, files[1].UID, files[5].UID}
	for _, reader := range readers {
		snapshots := readFiles(reader, uids)
		bulk, err := reader.GetFileSnapshots(t.Context(), uids)
		require.NoError(t, err)
		require.Equal(t, snapshots, bulk)
		bulk[2].Annotations["sequence"] = "mutated-clone"
		bulk[2].Spec[0] = '!'
		require.Equal(t, "1", bulk[5].Annotations["sequence"])
		require.JSONEq(t, `{"contentType":"image/jpeg"}`, string(bulk[5].Spec))
		require.Len(t, snapshots, len(uids))
		for i, snapshot := range snapshots {
			if i == 1 || i == 3 {
				require.Nil(t, snapshot)
				continue
			}
			require.NotNil(t, snapshot)
			require.Equal(t, uids[i], snapshot.UID)
			point, err := second.GetFile(t.Context(), uids[i])
			require.NoError(t, err)
			require.Equal(t, point, snapshot)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = reader.GetFile(ctx, uids[0])
		require.ErrorIs(t, err, context.Canceled)
		_, err = reader.GetFileSnapshots(ctx, uids)
		require.ErrorIs(t, err, context.Canceled)
	}
	updated, err := second.GetFile(t.Context(), files[3].UID)
	require.NoError(t, err)
	updated.ResourceVersion, updated.Annotations["sequence"] = "2", "2"
	require.NoError(t, second.UpdateFile(t.Context(), updated, "1"))
	snapshots := readFiles(readers[0], uids)
	require.Equal(t, "2", snapshots[2].ResourceVersion)
	require.Equal(t, "2", snapshots[5].Annotations["sequence"])
	bulk, err := first.GetFileSnapshots(t.Context(), uids)
	require.NoError(t, err)
	require.Equal(t, snapshots, bulk)

	require.NoError(t, first.DeleteFile(t.Context(), files[3].UID))
	replacement := *files[3]
	replacement.UID, replacement.CreationTimestamp = newID(), time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, second.CreateFile(t.Context(), &replacement))
	snapshots = readFiles(readers[0], []string{files[3].UID, replacement.UID})
	require.Nil(t, snapshots[0])
	require.Equal(t, replacement.UID, snapshots[1].UID)
	bulk, err = first.GetFileSnapshots(t.Context(), []string{files[3].UID, replacement.UID})
	require.NoError(t, err)
	require.Equal(t, snapshots, bulk)

	raw := newRawSession(t)
	parsedUID, err := gocql.ParseUUID(files[0].UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("UPDATE files_by_uid SET namespace=? WHERE uid=?", "", parsedUID).Exec())
	bulk, err = second.GetFileSnapshots(t.Context(), []string{files[1].UID, files[0].UID})
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	require.Nil(t, bulk, "a corrupt locator cannot return partially authorized snapshots")
	require.NoError(t, raw.Query("UPDATE files_by_uid SET namespace=? WHERE uid=?", files[0].Namespace, parsedUID).Exec())
	require.NoError(t, raw.Query("DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
		files[0].Namespace, files[0].CreationTimestamp, parsedUID).Exec())
	snapshots = readFiles(readers[1], []string{files[0].UID})
	require.Nil(t, snapshots[0], "a retained UID locator is not authoritative resource data")
	bulk, err = second.GetFileSnapshots(t.Context(), []string{files[0].UID})
	require.NoError(t, err)
	require.Equal(t, snapshots, bulk)
}

func TestScylla_FileNameSnapshotsUseTwoPartitionReadsAndFreshAuthoritativeFacts(t *testing.T) {
	type readKey struct{}
	var queries atomic.Int64
	var invalidQuery atomic.Bool
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
		if ctx.Value(readKey{}) == true && strings.HasPrefix(query.Statement, "SELECT ") {
			if query.Err != nil || strings.Contains(query.Statement, "ALLOW FILTERING") ||
				!(strings.Contains(query.Statement, " FROM files_by_name WHERE namespace=? AND name IN (") ||
					strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND (creation_timestamp,uid) IN (")) {
				invalidQuery.Store(true)
			}
			queries.Add(1)
		}
	})
	first, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := newTestStore(t)
	namespace := "file-name-snapshots-" + newID()[:8]
	require.NoError(t, first.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: namespace}))
	files := make([]*datastore.File, datastore.FileNameSnapshotBatchLimit)
	names := make([]string, len(files))
	for i := range files {
		names[i] = fmt.Sprintf("file-%d", i)
		files[i] = &datastore.File{UID: newID(), Namespace: namespace, Name: names[i],
			ResourceVersion: "1", CreationActor: "alice", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Annotations: map[string]string{"sequence": "1"}, Spec: json.RawMessage(`{"contentType":"image/jpeg"}`)}
		require.NoError(t, first.CreateFile(t.Context(), files[i]))
	}
	ctx := context.WithValue(t.Context(), readKey{}, true)
	for _, count := range []int{2, 3, 4} {
		before := queries.Load()
		bulk, err := first.GetFileSnapshotsByName(ctx, namespace, names[:count])
		require.NoError(t, err)
		require.Len(t, bulk, count)
		require.EqualValues(t, 2, queries.Load()-before, "two sequential same-partition queries, independent of File count")
		require.False(t, invalidQuery.Load())
		for i, file := range bulk {
			point, err := second.GetFileByName(t.Context(), namespace, names[i])
			require.NoError(t, err)
			require.Equal(t, point, file)
		}
	}
	input := []string{names[3], names[0], "absent", names[0]}
	for _, reader := range []datastore.Datastore{first, second} {
		bulk, err := reader.GetFileSnapshotsByName(t.Context(), namespace, input)
		require.NoError(t, err)
		require.Len(t, bulk, len(input))
		require.Equal(t, files[3].UID, bulk[0].UID)
		require.Nil(t, bulk[2])
		bulk[1].Annotations["sequence"], bulk[1].Spec[0] = "mutated", '!'
		require.Equal(t, "1", bulk[3].Annotations["sequence"])
		require.JSONEq(t, `{"contentType":"image/jpeg"}`, string(bulk[3].Spec))
	}
	updated, err := second.GetFile(t.Context(), files[0].UID)
	require.NoError(t, err)
	updated.ResourceVersion, updated.Body = "2", "fresh"
	updated.Annotations[datastore.OwnerAnnotationKey] = "charlie"
	require.NoError(t, second.UpdateFile(t.Context(), updated, "1"))
	bulk, err := first.GetFileSnapshotsByName(t.Context(), namespace, input)
	require.NoError(t, err)
	require.Equal(t, "2", bulk[1].ResourceVersion)
	require.Equal(t, "charlie", bulk[1].EffectiveOwnerSub())
	require.Equal(t, "alice", bulk[1].CreationActor)
	require.Equal(t, "fresh", bulk[1].Body)
	require.NoError(t, second.DeleteFile(t.Context(), files[0].UID))
	replacement := *files[0]
	replacement.UID, replacement.CreationTimestamp = newID(), time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, second.CreateFile(t.Context(), &replacement))
	bulk, err = first.GetFileSnapshotsByName(t.Context(), namespace, input)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, bulk[1].UID)
	require.Equal(t, replacement.UID, bulk[3].UID)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	bulk, err = first.GetFileSnapshotsByName(cancelled, namespace, names)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, bulk)
	for _, invalid := range []struct {
		namespace string
		names     []string
	}{{namespace, append(names, "extra")}, {"", names}, {namespace, []string{""}}} {
		bulk, err = first.GetFileSnapshotsByName(t.Context(), invalid.namespace, invalid.names)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		require.Nil(t, bulk)
	}
	bulk, err = first.GetFileSnapshotsByName(t.Context(), namespace, nil)
	require.NoError(t, err)
	require.Empty(t, bulk)

	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(files[3].UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
		namespace, files[3].CreationTimestamp, uid).Exec())
	bulk, err = second.GetFileSnapshotsByName(t.Context(), namespace, input)
	require.NoError(t, err)
	require.Nil(t, bulk[0], "a retained name locator cannot substitute for an authoritative row")
	otherUID, err := gocql.ParseUUID(files[2].UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("UPDATE files_by_name SET uid=?,creation_timestamp=? WHERE namespace=? AND name=?",
		otherUID, files[2].CreationTimestamp, namespace, names[1]).Exec())
	bulk, err = first.GetFileSnapshotsByName(t.Context(), namespace, []string{names[1]})
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "a wrong-name authoritative row must fail closed")
	require.Nil(t, bulk)
}

func TestScylla_FileOwnerReferenceProjectionUsesOwnerRepositoryScope(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	file := &datastore.File{
		UID: newID(), Namespace: "file-owner-" + newID()[:8], Name: "owned-" + newID()[:8],
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		RepositoryID:    newID(),
		OwnerReferences: json.RawMessage(`[{"kind":"Repository","uid":"owner","repositoryID":"00000000-0000-0000-0000-000000000003","blockOwnerDeletion":true}]`),
	}
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: file.Namespace}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: file.RepositoryID, Namespace: file.Namespace, Name: "source"}))
	require.NoError(t, store.CreateFile(ctx, file))
	owners := store.(datastore.OwnerReferenceStore)
	blocked, err := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{Namespace: file.Namespace, RepositoryID: "00000000-0000-0000-0000-000000000003"}, "owner")
	require.NoError(t, err)
	assert.True(t, blocked)
}

// ── Product ───────────────────────────────────────────────────────────────────

func TestScylla_CreateGetProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, p.Namespace, got.Namespace)
}

func TestScylla_CreateProduct_DuplicateUID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "dup-uid-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	err := store.CreateProduct(ctx, p)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateProduct_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-name-" + newID()[:8]
	p1 := newProduct("test-ns", name)
	require.NoError(t, store.CreateProduct(ctx, p1))
	p2 := newProduct("test-ns", name)
	err := store.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetProductByName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "findable-" + newID()[:8]
	p := newProduct("test-ns", name)
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductByName(ctx, "test-ns", name)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
}

func TestScylla_GetProductByName_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProductByName(context.Background(), "test-ns", "no-such-product-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListProducts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "list-ns-" + newID()[:8]
	p1 := newProduct(ns, "p1-"+newID()[:8])
	p2 := newProduct(ns, "p2-"+newID()[:8])
	p3 := newProduct(ns, "p3-"+newID()[:8])

	require.NoError(t, store.CreateProduct(ctx, p1))
	require.NoError(t, store.CreateProduct(ctx, p2))
	require.NoError(t, store.CreateProduct(ctx, p3))

	result, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.Items), 3)
}

func TestScylla_UpdateProductVariant_UpdatesSKUIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "variant-ns-" + newID()[:8]
	name := "variant-" + newID()[:8]
	oldSKU := "sku-old-" + newID()[:8]
	newSKU := "sku-new-" + newID()[:8]

	v := newProductVariant(ns, name, oldSKU, "product-a")
	v.ResourceVersion = "1"
	require.NoError(t, store.CreateProductVariant(ctx, v))

	v.SKU = newSKU
	v.Generation = 2
	v.ResourceVersion = "2"
	require.NoError(t, store.UpdateProductVariant(ctx, v, "1"))

	_, err := store.GetProductVariantBySKU(ctx, ns, oldSKU)
	require.ErrorIs(t, err, datastore.ErrNotFound)

	got, err := store.GetProductVariantBySKU(ctx, ns, newSKU)
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, newSKU, got.SKU)
}

func TestScylla_UpdateProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "upd-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	p.GitRef = "main"
	expected := p.ResourceVersion
	datastore.AdvanceProductSystemVersion(p)
	require.NoError(t, store.UpdateProduct(ctx, p, expected))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "main", got.GitRef)
}

func TestScylla_UpdateProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	p := newProduct("test-ns", "ghost-"+newID()[:8])
	p.UID = newID() // does not exist
	err := store.UpdateProduct(context.Background(), p, "1")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "del-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	require.NoError(t, store.DeleteProduct(ctx, p.UID))

	_, err := store.GetProduct(ctx, p.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── CategoryTaxonomy ──────────────────────────────────────────────────────────

func newCategoryTaxonomy(ns, name string) *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		UID:             newID(),
		Namespace:       ns,
		Name:            name,
		APIVersion:      "catalog.gitstore.dev/v1beta1",
		Kind:            "CategoryTaxonomy",
		Generation:      1,
		ResourceVersion: "1",
	}
}

func TestScylla_CreateGetCategoryTaxonomy(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCategoryTaxonomy("test-ns", "cat-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))

	got, err := store.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)

	gotByUID, err := store.GetCategoryTaxonomy(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.Name, gotByUID.Name)
}

func TestScylla_CreateCategoryTaxonomy_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-cat-" + newID()[:8]
	c1 := newCategoryTaxonomy("test-ns", name)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c1))
	c2 := newCategoryTaxonomy("test-ns", name)
	err := store.CreateCategoryTaxonomy(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCategoryTaxonomy_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCategoryTaxonomyByName(context.Background(), "test-ns", "no-such-cat-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)

	_, err = store.GetCategoryTaxonomy(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCategoryTaxonomies(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCategoryTaxonomies(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)

	c1 := newCategoryTaxonomy("test-ns", "catls1-"+newID()[:8])
	c2 := newCategoryTaxonomy("test-ns", "catls2-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c1))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c2))

	after, err := store.ListCategoryTaxonomies(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Equal(t, len(before.Items)+2, len(after.Items))
}

func TestScylla_UpdateCategoryTaxonomy(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCategoryTaxonomy("test-ns", "upd-cat-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))
	c.AncestorPath = "electronics"
	expected := c.ResourceVersion
	datastore.AdvanceCategoryTaxonomySystemVersion(c)
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, c, expected))

	got, err := store.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, "electronics", got.AncestorPath)
}

func TestScylla_UpdateCategoryTaxonomy_NotFound(t *testing.T) {
	store := newTestStore(t)
	c := newCategoryTaxonomy("test-ns", "ghost-cat-"+newID()[:8])
	err := store.UpdateCategoryTaxonomy(context.Background(), c, "1")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Collection ────────────────────────────────────────────────────────────────

func TestScylla_CreateGetCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "col-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
	assert.Equal(t, c.Name, got.Name)
	assert.Equal(t, c.Namespace, got.Namespace)
}

func TestScylla_CreateCollection_DuplicateUID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "dup-uid-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c))
	err := store.CreateCollection(ctx, c)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateCollection_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-col-" + newID()[:8]
	c1 := newCollection("test-ns", name)
	require.NoError(t, store.CreateCollection(ctx, c1))
	c2 := newCollection("test-ns", name)
	err := store.CreateCollection(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollection(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetCollectionByName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "find-col-" + newID()[:8]
	c := newCollection("test-ns", name)
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollectionByName(ctx, "test-ns", name)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
}

func TestScylla_GetCollectionByName_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollectionByName(context.Background(), "test-ns", "no-col-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCollections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCollections(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)

	c1 := newCollection("test-ns", "colls1-"+newID()[:8])
	c2 := newCollection("test-ns", "colls2-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c1))
	require.NoError(t, store.CreateCollection(ctx, c2))

	after, err := store.ListCollections(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Equal(t, len(before.Items)+2, len(after.Items))
}

func TestScylla_UpdateCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "upd-col-"+newID()[:8])
	c.Body = "Before"
	require.NoError(t, store.CreateCollection(ctx, c))
	c.Body = "After"
	expected := c.ResourceVersion
	datastore.AdvanceCollectionSystemVersion(c)
	require.NoError(t, store.UpdateCollection(ctx, c, expected))

	got, err := store.GetCollection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Body)
}

func TestScylla_UpdateCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateCollection(context.Background(), newCollection("test-ns", "ghost-col-"+newID()[:8]), "1")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Product: three-table schema tests (016-product-spec-hydration) ────────────

func productCursor(p *datastore.Product) string {
	return pageCursor(p.CreationTimestamp, p.UID)
}

func pageCursor(created time.Time, uid string) string {
	raw := fmt.Sprintf("keyset|%s|%s",
		created.UTC().Format(time.RFC3339Nano), uid)
	return base64Encode(raw)
}

func base64Encode(s string) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(s)
	dst := make([]byte, (len(src)+2)/3*4)
	n := 0
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		b[0] = src[i]
		if i+1 < len(src) {
			b[1] = src[i+1]
		}
		if i+2 < len(src) {
			b[2] = src[i+2]
		}
		remaining := len(src) - i
		dst[n] = enc[(b[0]>>2)&0x3f]
		dst[n+1] = enc[((b[0]&0x03)<<4)|((b[1]>>4)&0x0f)]
		if remaining > 1 {
			dst[n+2] = enc[((b[1]&0x0f)<<2)|((b[2]>>6)&0x03)]
		} else {
			dst[n+2] = '='
		}
		if remaining > 2 {
			dst[n+3] = enc[b[2]&0x3f]
		} else {
			dst[n+3] = '='
		}
		n += 4
	}
	return string(dst[:n])
}

func TestScylla_GetProductByName_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "rt-ns-" + newID()[:8]
	p := newProduct(ns, "findable-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductByName(ctx, ns, p.Name)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, ns, got.Namespace)
}

func TestScylla_GetProductByUID_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "uid-rt-ns-" + newID()[:8]
	p := newProduct(ns, "uid-rt-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, ns, got.Namespace)
}

func TestScylla_UpdateProduct_BatchFanOut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "upd-fan-" + newID()[:8]
	p := newProduct(ns, "widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	p.GitRef = "main"
	p.GitCommitSHA = "abc123"
	expected := p.ResourceVersion
	datastore.AdvanceProductSystemVersion(p)
	require.NoError(t, store.UpdateProduct(ctx, p, expected))

	byUID, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "main", byUID.GitRef)
	assert.Equal(t, "abc123", byUID.GitCommitSHA)

	byName, err := store.GetProductByName(ctx, ns, p.Name)
	require.NoError(t, err)
	assert.Equal(t, "main", byName.GitRef)

	page, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "main", page.Items[0].GitRef)
}

func TestScylla_DeleteProduct_BatchFanOut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "del-fan-" + newID()[:8]
	p := newProduct(ns, "del-widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	require.NoError(t, store.DeleteProduct(ctx, p.UID))

	_, errUID := store.GetProduct(ctx, p.UID)
	assert.ErrorIs(t, errUID, datastore.ErrNotFound)

	_, errName := store.GetProductByName(ctx, ns, p.Name)
	assert.ErrorIs(t, errName, datastore.ErrNotFound)

	page, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestScylla_Product_SpecStatus_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "spec-rt-" + newID()[:8]
	p := newProduct(ns, "spec-"+newID()[:8])
	p.Spec = []byte(`{"title":"Widget Pro","tags":["sale"]}`)
	p.Status = []byte(`{"observedGeneration":1,"conditions":[{"type":"READY","status":"TRUE","lastTransitionTime":"2026-01-01T00:00:00Z"}]}`)
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, string(p.Spec), string(got.Spec))
	assert.Equal(t, string(p.Status), string(got.Status))
}

func TestScylla_ListProducts_ThreePageForwardCursor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "paginate-" + newID()[:8]
	total := 25
	for i := 0; i < total; i++ {
		p := newProduct(ns, fmt.Sprintf("item-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	const pageSize = 10
	seen := make(map[string]bool)

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize})
	require.NoError(t, err)
	require.Len(t, r1.Items, 10)
	assert.True(t, r1.HasNext)
	for _, p := range r1.Items {
		require.False(t, seen[p.UID], "duplicate on page 1: %s", p.UID)
		seen[p.UID] = true
	}

	cursor1 := productCursor(r1.Items[len(r1.Items)-1])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize, After: cursor1})
	require.NoError(t, err)
	require.Len(t, r2.Items, 10)
	assert.True(t, r2.HasNext)
	for _, p := range r2.Items {
		require.False(t, seen[p.UID], "duplicate on page 2: %s", p.UID)
		seen[p.UID] = true
	}

	cursor2 := productCursor(r2.Items[len(r2.Items)-1])
	r3, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize, After: cursor2})
	require.NoError(t, err)
	require.Len(t, r3.Items, 5)
	assert.False(t, r3.HasNext)
	for _, p := range r3.Items {
		require.False(t, seen[p.UID], "duplicate on page 3: %s", p.UID)
		seen[p.UID] = true
	}

	assert.Len(t, seen, total)
}

func TestScylla_ListProducts_BackwardCursor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "back-page-" + newID()[:8]
	for i := 0; i < 15; i++ {
		p := newProduct(ns, fmt.Sprintf("bitem-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{Last: 5})
	require.NoError(t, err)
	require.Len(t, r1.Items, 5)
	assert.True(t, r1.HasPrevious)

	seen := make(map[string]bool)
	for _, p := range r1.Items {
		seen[p.UID] = true
	}

	cursorBefore := productCursor(r1.Items[0])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{Last: 5, Before: cursorBefore})
	require.NoError(t, err)
	require.Len(t, r2.Items, 5)
	for _, p := range r2.Items {
		require.False(t, seen[p.UID], "duplicate on backward page 2: %s", p.UID)
		seen[p.UID] = true
	}
}

func TestScylla_ListProducts_AfterLastCursor_ReturnsEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "empty-page-" + newID()[:8]
	for i := 0; i < 3; i++ {
		p := newProduct(ns, fmt.Sprintf("ep-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 10})
	require.NoError(t, err)
	require.Len(t, r1.Items, 3)
	assert.False(t, r1.HasNext)

	lastCursor := productCursor(r1.Items[len(r1.Items)-1])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 10, After: lastCursor})
	require.NoError(t, err)
	assert.Empty(t, r2.Items)
	assert.False(t, r2.HasNext)
}

func TestScylla_NamespaceDirectUID_FullEnvelopeAndBodyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Hour)
	uid := newID()
	namespace := &datastore.Namespace{
		APIVersion:        "gitstore.dev/v1beta1",
		Kind:              "Namespace",
		UID:               uid,
		ID:                uid,
		Name:              "namespace-envelope-" + newID()[:8],
		Generation:        7,
		ResourceVersion:   "11",
		Revision:          "main@sha1:namespace",
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now.Add(time.Minute),
		UpdateActor:       "bob",
		Labels:            map[string]string{"team": "catalog"},
		Annotations:       map[string]string{"gitstore.dev/note": "namespace"},
		OwnerReferences:   json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"owner","uid":"00000000-0000-0000-0000-000000000001"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		SourcePath:        "namespaces/example.md",
		GitCommitSHA:      "namespace-sha",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"title":"Canonical namespace","tier":"USER"}`),
		Body:              "# Namespace\n\nRaw **Markdown** body.\n",
		Status:            json.RawMessage(`{"observedGeneration":7,"conditions":[]}`),
		Title:             "Canonical namespace",
		Tier:              datastore.NamespaceTierUser,
	}

	require.NoError(t, store.CreateNamespace(ctx, namespace))

	got, err := store.GetNamespace(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, namespace, got)

	byName, err := store.GetNamespaceByName(ctx, namespace.Name)
	require.NoError(t, err)
	assert.Equal(t, namespace, byName)
	assert.Equal(t, uid, got.UID)
	assert.Equal(t, namespace.Body, got.Body)
	assert.JSONEq(t, string(namespace.OwnerReferences), string(got.OwnerReferences))
}

func TestScylla_RepositoryCatalogProjectionUpgradeRepair(t *testing.T) {
	ctx := context.Background()
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_catalog_repair"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	store, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ns := &datastore.Namespace{UID: newID(), Name: "upgrade", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, store.CreateNamespace(ctx, ns))
	repository := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog", CreationTimestamp: ns.CreationTimestamp}
	require.NoError(t, store.CreateRepository(ctx, repository))
	product := &datastore.Product{UID: newID(), Namespace: ns.Name, Name: "product", RepositoryID: repository.UID, ResourceVersion: "1"}
	require.NoError(t, store.CreateProduct(ctx, product))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	repositoryUID, err := gocql.ParseUUID(repository.UID)
	require.NoError(t, err)
	for shard := int8(0); shard < 32; shard++ {
		require.NoError(t, session.Query("DELETE FROM "+cfg.Keyspace+".resources_by_repository WHERE repository_id=? AND shard=?", repositoryUID, shard).Exec())
	}
	require.NoError(t, session.Query("DELETE FROM "+cfg.Keyspace+".repository_catalog_projection_state WHERE repository_id=?", repositoryUID).Exec())
	_, err = store.HasCatalogResources(ctx, repository.UID)
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
	repair, err := scylla.OpenProjectionRepairService(cfg)
	require.NoError(t, err)
	t.Cleanup(repair.Close)
	plan, err := repair.Audit(ctx)
	require.NoError(t, err)
	require.Len(t, plan.Actions, 1)
	_, err = repair.Apply(ctx, plan)
	require.NoError(t, err)
	blocked, err := store.HasCatalogResources(ctx, repository.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.NoError(t, store.DeleteProduct(ctx, product.UID))
	blocked, err = store.HasCatalogResources(ctx, repository.UID)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestScylla_ProjectionRepairPreservesProductResolvedPaths(t *testing.T) {
	ctx := t.Context()
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_paths"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	created := time.Now().UTC().Truncate(time.Millisecond)
	namespace := &datastore.Namespace{UID: newID(), Name: "paths", CreationTimestamp: created}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{UID: newID(), Namespace: namespace.Name, Name: "catalog", CreationTimestamp: created}
	require.NoError(t, store.CreateRepository(ctx, repository))
	root := &datastore.CategoryTaxonomy{
		UID: newID(), Namespace: namespace.Name, Name: "root", RepositoryID: repository.UID,
		ResourceVersion: "1", CreationTimestamp: created,
		Status: json.RawMessage(`{"resolved":{"path":["root"]}}`),
	}
	leaf := &datastore.CategoryTaxonomy{
		UID: newID(), Namespace: namespace.Name, Name: "leaf", RepositoryID: repository.UID,
		ResourceVersion: "1", CreationTimestamp: created.Add(time.Millisecond),
		Status: json.RawMessage(`{"resolved":{"path":["root","leaf"]}}`),
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, root))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, leaf))
	repair, err := scylla.OpenProjectionRepairService(cfg)
	require.NoError(t, err)
	t.Cleanup(repair.Close)
	for _, test := range []struct {
		name string
		path []string
		want map[string]bool
	}{
		{name: "direct", path: []string{"leaf"}, want: map[string]bool{leaf.UID: true}},
		{name: "ancestors", path: []string{"root", "leaf"}, want: map[string]bool{root.UID: true, leaf.UID: true}},
		{name: "pending-path", want: map[string]bool{leaf.UID: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, err := json.Marshal(map[string]any{
				"resolved": map[string]any{"category": map[string]any{"name": leaf.Name, "path": test.path}},
			})
			require.NoError(t, err)
			product := &datastore.Product{
				UID: newID(), Namespace: namespace.Name, Name: test.name, RepositoryID: repository.UID,
				ResourceVersion: "1", CreationTimestamp: created, Status: status,
			}
			require.NoError(t, store.CreateProduct(ctx, product))
			membership, ok := store.(datastore.CategoryProductIndex)
			require.True(t, ok)
			require.NoError(t, membership.ReplaceCategoryProductMembership(ctx, product, nil))
			plan, err := repair.Audit(ctx)
			require.NoError(t, err)
			got := map[string]bool{}
			for _, action := range plan.Actions {
				if action.UID == product.UID && action.After != nil && action.After.CategoryUID != "" {
					require.Equal(t, scylla.RepairInsert, action.Type)
					got[action.After.CategoryUID] = true
				}
			}
			require.Equal(t, test.want, got, "repair must not borrow the category's current ancestor path")
		})
	}
}

func TestRepositoryMembershipFiveKindsUseBoundedExactPartitionChecks(t *testing.T) {
	writer := newTestStore(t)
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	reader, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	namespace := &datastore.Namespace{UID: newID(), Name: "membership-matrix-" + newID(), CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, writer.CreateNamespace(t.Context(), namespace))
	repository := newRepository()
	repository.Namespace = namespace.Name
	require.NoError(t, writer.CreateRepository(t.Context(), repository))
	check := func(want bool) {
		t.Helper()
		evidence := &capacityQueryEvidence{tables: map[string]int{}}
		ctx := context.WithValue(t.Context(), capacityQueryKey{}, evidence)
		present, err := reader.HasCatalogResources(ctx, repository.UID)
		require.NoError(t, err)
		require.Equal(t, want, present)
		evidence.mu.Lock()
		defer evidence.mu.Unlock()
		require.Equal(t, 1, evidence.tables["repository_catalog_projection_state"])
		require.GreaterOrEqual(t, evidence.tables["resources_by_repository"], 1)
		require.LessOrEqual(t, evidence.tables["resources_by_repository"], 32)
		if !want {
			require.Equal(t, 32, evidence.tables["resources_by_repository"])
		}
		require.False(t, evidence.filtered)
		require.False(t, evidence.membershipUnbounded, "membership queries must retain full partition restrictions and LIMIT 1")
		require.Equal(t, evidence.tables["resources_by_repository"]+1, evidence.attempts)
	}
	check(false)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			uid := newID()
			when := time.Now().UTC().Truncate(time.Millisecond)
			var remove func(context.Context) error
			switch kind {
			case "Product":
				product := newProduct(namespace.Name, "product")
				product.UID, product.RepositoryID, product.ResourceVersion = uid, repository.UID, "1"
				require.NoError(t, writer.CreateProduct(t.Context(), product))
				remove = func(ctx context.Context) error { return writer.DeleteProduct(ctx, uid) }
			case "ProductVariant":
				variant := newProductVariant(namespace.Name, "variant", "sku", "parent")
				variant.UID, variant.RepositoryID, variant.ResourceVersion = uid, repository.UID, "1"
				require.NoError(t, writer.CreateProductVariant(t.Context(), variant))
				remove = func(ctx context.Context) error { return writer.DeleteProductVariant(ctx, uid) }
			case "CategoryTaxonomy":
				require.NoError(t, writer.CreateCategoryTaxonomy(t.Context(), &datastore.CategoryTaxonomy{
					UID: uid, Namespace: namespace.Name, Name: "category", RepositoryID: repository.UID,
					ResourceVersion: "1", CreationTimestamp: when,
				}))
				remove = func(ctx context.Context) error { return writer.DeleteCategoryTaxonomy(ctx, uid) }
			case "Collection":
				collection := newCollection(namespace.Name, "collection")
				collection.UID, collection.RepositoryID, collection.ResourceVersion = uid, repository.UID, "1"
				require.NoError(t, writer.CreateCollection(t.Context(), collection))
				remove = func(ctx context.Context) error { return writer.DeleteCollection(ctx, uid) }
			case "File":
				require.NoError(t, writer.CreateFile(t.Context(), &datastore.File{
					UID: uid, Namespace: namespace.Name, Name: "file", RepositoryID: repository.UID,
					ResourceVersion: "1", CreationTimestamp: when,
				}))
				remove = func(ctx context.Context) error { return writer.DeleteFile(ctx, uid) }
			}
			check(true)
			require.NoError(t, remove(t.Context()))
			check(false)
		})
	}
}

func TestScylla_RepositoryMembershipDoesNotEnumerateKinds(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	ns := &datastore.Namespace{UID: newID(), Name: "future-kinds-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, store.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog", CreationTimestamp: ns.CreationTimestamp}
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	repoUID, err := gocql.ParseUUID(repo.UID)
	require.NoError(t, err)
	memberUID, err := gocql.ParseUUID(newID())
	require.NoError(t, err)
	// Distinct future kinds may share a UID without colliding in this index.
	for i := range 40 {
		require.NoError(t, session.Query("INSERT INTO "+scyllaKeyspace+".resources_by_repository (repository_id,shard,kind,uid) VALUES (?,?,?,?)",
			repoUID, int8(31), fmt.Sprintf("FutureKind%d", i), memberUID).Exec())
	}
	blocked, err := store.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	for i := range 39 {
		require.NoError(t, session.Query("DELETE FROM "+scyllaKeyspace+".resources_by_repository WHERE repository_id=? AND shard=? AND kind=? AND uid=?",
			repoUID, int8(31), fmt.Sprintf("FutureKind%d", i), memberUID).Exec())
	}
	blocked, err = store.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked, "an unregistered kind must still block deletion")
	require.NoError(t, session.Query("DELETE FROM "+scyllaKeyspace+".resources_by_repository WHERE repository_id=? AND shard=? AND kind=? AND uid=?",
		repoUID, int8(31), "FutureKind39", memberUID).Exec())
	blocked, err = store.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestScylla_CatalogAdmissionsWaitForNamespaceFence(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	ns := &datastore.Namespace{UID: newID(), Name: "catalog-wait-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	setPending := func(pending int64) {
		t.Helper()
		require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", pending, nsUID).Exec())
	}
	setPending(1)
	t.Cleanup(func() { setPending(0) })

	canceledCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, storeB.CreateProduct(canceledCtx, &datastore.Product{
		UID: newID(), Namespace: ns.Name, Name: "canceled", RepositoryID: repo.UID,
	}), context.DeadlineExceeded)
	_, err = storeA.GetProductByName(ctx, ns.Name, "canceled")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	const writers = 8
	results := make(chan error, writers)
	products := make([]*datastore.Product, writers)
	for i := range writers {
		products[i] = &datastore.Product{UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("product-%d", i), RepositoryID: repo.UID}
		go func(i int) {
			writer := storeA
			if i%2 == 0 {
				writer = storeB
			}
			results <- writer.CreateProduct(ctx, products[i])
		}(i)
	}
	select {
	case err := <-results:
		t.Fatalf("admission returned before reservation release: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	setPending(0)
	for range writers {
		require.NoError(t, <-results)
	}
	for _, product := range products {
		got, err := storeA.GetProduct(ctx, product.UID)
		require.NoError(t, err)
		require.Equal(t, product.Name, got.Name)
	}
}

func TestScylla_FileCohortOrdersOverlappingNamesUnderOneFence(t *testing.T) {
	first, peer := newTestStores(t)
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "file-ordered-" + newID()[:8]}
	require.NoError(t, first.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, first.CreateRepositoryInActiveNamespace(ctx, repo))
	file := &datastore.File{
		UID: newID(), Namespace: ns.Name, Name: "shared", RepositoryID: repo.UID,
		ResourceVersion: "1", Generation: 1, CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, first.CreateFile(ctx, file))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+
			scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	beforeEpoch, pending := readFence()
	require.Zero(t, pending)
	initialStarted, releaseInitial := make(chan struct{}), make(chan struct{})
	firstStarted, releaseFirst := make(chan struct{}), make(chan struct{})
	var initialOnce, firstOnce sync.Once
	unblockInitial := func() { initialOnce.Do(func() { close(releaseInitial) }) }
	unblockFirst := func() { firstOnce.Do(func() { close(releaseFirst) }) }
	var running sync.WaitGroup
	t.Cleanup(func() {
		unblockInitial()
		unblockFirst()
		running.Wait()
	})
	initialDone := make(chan error, 1)
	running.Go(func() {
		initialDone <- first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
			close(initialStarted)
			<-releaseInitial
			return nil
		})
	})
	<-initialStarted
	var finished atomic.Int64
	results := make([]chan error, 4)
	for i := range results {
		results[i] = make(chan error, 1)
		running.Go(func() {
			results[i] <- first.(datastore.FileMutationBatcher).WithFileMutationBatch(ctx, ns.Name, repo.UID,
				[]string{file.Name}, func(batchCtx context.Context) error {
					assert.EqualValues(t, i, finished.Load(), "overlapping File mutations must finish in queue order")
					if i == 0 {
						close(firstStarted)
						<-releaseFirst
					}
					current, err := first.GetFile(batchCtx, file.UID)
					if err != nil {
						return err
					}
					expected := current.ResourceVersion
					current.Annotations = map[string]string{"sequence": fmt.Sprint(i)}
					datastore.AdvanceFileSystemVersion(current)
					if err := first.UpdateFile(batchCtx, current, expected); err != nil {
						return err
					}
					finished.Store(int64(i + 1))
					return nil
				})
		})
		require.Eventually(t, func() bool {
			return scylla.QueuedNamespaceCatalogWritesForTest(first, ns.UID) == i+2
		}, time.Second, time.Millisecond)
	}
	unblockInitial()
	require.NoError(t, <-initialDone)
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the selected File cohort did not start")
	}
	heldEpoch, held := readFence()
	require.Equal(t, beforeEpoch+2, heldEpoch)
	require.EqualValues(t, 1, held)
	terminating := *repo
	now := time.Now().UTC()
	terminating.DeletionTimestamp = &now
	datastore.AdvanceRepositorySystemVersion(&terminating)
	short, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	require.ErrorIs(t, peer.UpdateRepository(short, &terminating, repo.ResourceVersion), context.DeadlineExceeded)
	cancel()
	unblockFirst()
	for _, result := range results {
		require.NoError(t, <-result)
	}
	afterEpoch, afterPending := readFence()
	require.Equal(t, heldEpoch, afterEpoch, "all four ordered callbacks must share the confirmed reservation")
	require.Zero(t, afterPending)
	require.EqualValues(t, 4, finished.Load())
	current, err := peer.GetFile(ctx, file.UID)
	require.NoError(t, err)
	require.Equal(t, "5", current.ResourceVersion)
	require.EqualValues(t, 1, current.Generation)
	require.Equal(t, "3", current.Annotations["sequence"])
	blocked, err := peer.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Zero(t, scylla.QueuedNamespaceCatalogWritesForTest(first, ns.UID))
}

func TestScylla_FileCohortSharesOneFenceAndSurvivesLeaderCancellation(t *testing.T) {
	testFileCohortSharesOneFenceAndSurvivesLeaderCancellation(t, false)
}

func TestScylla_FileCohortDisjointSameRepositorySharesFence(t *testing.T) {
	testFileCohortSharesOneFenceAndSurvivesLeaderCancellation(t, true)
}

func testFileCohortSharesOneFenceAndSurvivesLeaderCancellation(t *testing.T, sharedRepository bool) {
	t.Helper()
	storeA, storeB := newTestStores(t)
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "file-cohort-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repositories := make([]*datastore.Repository, 4)
	for i := range repositories {
		if sharedRepository && i > 0 {
			repositories[i] = repositories[0]
			continue
		}
		repositories[i] = &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("catalog-%d", i)}
		require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repositories[i]))
	}
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+
			scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	beforeEpoch, beforePending := readFence()
	require.Zero(t, beforePending)
	started, releaseInitial := make(chan struct{}), make(chan struct{})
	var initialOnce sync.Once
	release := func() { initialOnce.Do(func() { close(releaseInitial) }) }
	t.Cleanup(release)
	initialDone := make(chan error, 1)
	var running sync.WaitGroup
	running.Go(func() {
		initialDone <- storeA.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error {
			close(started)
			<-releaseInitial
			return nil
		})
	})
	<-started
	allStarted, releaseMutations := make(chan struct{}), make(chan struct{})
	var mutationsOnce sync.Once
	proceed := func() { mutationsOnce.Do(func() { close(releaseMutations) }) }
	t.Cleanup(proceed)
	type actorKey struct{}
	var active atomic.Int64
	results := make([]chan error, 4)
	files := make([][]*datastore.File, 4)
	leaderCtx, cancelLeader := context.WithCancel(context.WithValue(ctx, actorKey{}, 0))
	defer cancelLeader()
	t.Cleanup(func() {
		release()
		proceed()
		cancelLeader()
		running.Wait()
	})
	for i, repo := range repositories {
		results[i] = make(chan error, 1)
		fileCtx := context.WithValue(ctx, actorKey{}, i)
		if i == 0 {
			fileCtx = leaderCtx
		}
		names := []string{fmt.Sprintf("cohort-%d-first", i), fmt.Sprintf("cohort-%d-second", i)}
		for _, name := range names {
			files[i] = append(files[i], &datastore.File{UID: newID(), Namespace: ns.Name, Name: name,
				RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()})
		}
		running.Go(func() {
			results[i] <- storeA.(datastore.FileMutationBatcher).WithFileMutationBatch(fileCtx, ns.Name, repo.UID, names, func(batchCtx context.Context) error {
				assert.Equal(t, i, batchCtx.Value(actorKey{}))
				if active.Add(1) == 4 {
					close(allStarted)
				}
				<-releaseMutations
				if err := batchCtx.Err(); err != nil {
					return err
				}
				errs := make([]error, len(files[i]))
				var writes sync.WaitGroup
				for j, file := range files[i] {
					writes.Go(func() { errs[j] = storeA.CreateFile(batchCtx, file) })
				}
				writes.Wait()
				return errors.Join(errs...)
			})
		})
		require.Eventually(t, func() bool {
			return scylla.QueuedNamespaceCatalogWritesForTest(storeA, ns.UID) == i+2
		}, time.Second, time.Millisecond)
	}
	release()
	require.NoError(t, <-initialDone)
	select {
	case <-allStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("four queued callbacks did not share the next exclusive reservation")
	}
	cohortEpoch, cohortPending := readFence()
	require.Equal(t, beforeEpoch+2, cohortEpoch)
	require.EqualValues(t, 1, cohortPending)
	terminating := *repositories[1]
	now := time.Now().UTC()
	terminating.DeletionTimestamp = &now
	datastore.AdvanceRepositorySystemVersion(&terminating)
	peerCtx, stopPeer := context.WithTimeout(ctx, 60*time.Millisecond)
	require.ErrorIs(t, storeB.UpdateRepository(peerCtx, &terminating, repositories[1].ResourceVersion), context.DeadlineExceeded,
		"another replica's lifecycle transition cannot enter the shared reservation")
	stopPeer()
	cancelLeader()
	proceed()
	require.ErrorIs(t, <-results[0], context.Canceled)
	for i := 1; i < 4; i++ {
		require.NoError(t, <-results[i])
		for _, file := range files[i] {
			current, err := storeB.GetFile(ctx, file.UID)
			require.NoError(t, err)
			require.Equal(t, file.Name, current.Name)
			require.Equal(t, repositories[i].UID, current.RepositoryID)
		}
		blocked, err := storeB.HasCatalogResources(ctx, repositories[i].UID)
		require.NoError(t, err)
		require.True(t, blocked)
	}
	for _, file := range files[0] {
		_, err := storeB.GetFile(ctx, file.UID)
		require.ErrorIs(t, err, datastore.ErrNotFound)
	}
	afterEpoch, afterPending := readFence()
	require.Equal(t, cohortEpoch, afterEpoch)
	require.Zero(t, afterPending)
	require.Zero(t, scylla.QueuedNamespaceCatalogWritesForTest(storeA, ns.UID))
}

func TestScylla_FileBatchActiveNamespaceProofBlocksPeerLifecycle(t *testing.T) {
	first, second := newTestStores(t)
	namespace := &datastore.Namespace{UID: newID(), Name: "batch-parent-" + newID()}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	version := namespace.ResourceVersion
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), namespace.Name, func(ctx context.Context) error {
		for _, intent := range []bool{false, true} {
			desired := *namespace
			if intent {
				desired.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
			} else {
				now := time.Now().UTC()
				desired.DeletionTimestamp = &now
			}
			datastore.AdvanceNamespaceSystemVersion(&desired)
			require.ErrorIs(t, second.UpdateNamespace(ctx, &desired, version), datastore.ErrNamespaceNotEmpty,
				"a held Namespace reservation must block peer lifecycle transitions even without repositories or children")
			current, err := second.GetNamespace(ctx, namespace.UID)
			require.NoError(t, err)
			require.Equal(t, version, current.ResourceVersion)
			require.Nil(t, current.DeletionTimestamp)
			require.False(t, datastore.HasDeletionIntent(current.Status))
		}
		return nil
	}))
	desired := *namespace
	now := time.Now().UTC()
	desired.DeletionTimestamp = &now
	datastore.AdvanceNamespaceSystemVersion(&desired)
	require.NoError(t, second.UpdateNamespace(t.Context(), &desired, version),
		"releasing the reservation must permit lifecycle transitions for an otherwise-empty Namespace")
	require.ErrorIs(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), namespace.Name, func(context.Context) error {
		t.Fatal("a terminating Namespace must not admit an active batch")
		return nil
	}), datastore.ErrNamespaceNotActive)
}

func TestScylla_FileUpdateSkipsUnchangedColumns(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	first, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := newTestStore(t)
	file := &datastore.File{
		UID: newID(), Namespace: "selective-file-" + newID(), Name: "file",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), CreationActor: "creator",
		Labels: map[string]string{"kept": "value"}, Annotations: map[string]string{"previous": "value"},
		Finalizers: []string{"example"}, OwnerReferences: json.RawMessage(`[]`),
		SourcePath: "file.json", GitCommitSHA: "before", GitRef: "main", Body: "before",
		Spec: json.RawMessage(`{"path":"example"}`), Status: json.RawMessage(`{"resolved":{}}`),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), &datastore.Namespace{UID: newID(), Name: file.Namespace}))
	require.NoError(t, first.CreateFile(t.Context(), file))
	before, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	desired := *before
	desired.Body, desired.GitCommitSHA = "after", "after"
	desired.Generation++
	datastore.AdvanceFileSystemVersion(&desired)
	evidence := &capacityQueryEvidence{tables: make(map[string]int)}
	ctx := context.WithValue(t.Context(), capacityQueryKey{}, evidence)
	require.NoError(t, first.UpdateFile(ctx, &desired, before.ResourceVersion))
	current, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Equal(t, &desired, current, "selective bindings must retain the complete current envelope")
	evidence.mu.Lock()
	written := make(map[int]int, len(evidence.fileCASFields))
	for column, count := range evidence.fileCASFields {
		written[column] = count
	}
	evidence.mu.Unlock()
	require.NotEmpty(t, written, "observe the actual authoritative CAS bindings")
	require.Zero(t, written[0], "unchanged name must not be rewritten")
	require.Zero(t, written[9], "unchanged labels must not be rewritten")
	require.Zero(t, written[12], "unchanged finalizers must not be rewritten")
	require.Equal(t, 1, written[4], "resourceVersion must remain an explicit CAS write")
	require.Equal(t, 1, written[16], "changed commit must be written")
	require.Equal(t, 1, written[19], "changed body must be written")
	require.Zero(t, written[11], "unchanged nonempty serialized JSON must not be rewritten")
	require.Zero(t, written[18])
	require.Zero(t, written[20])
	cleared := *current
	cleared.Labels, cleared.Annotations, cleared.Finalizers = nil, nil, nil
	cleared.OwnerReferences, cleared.Spec, cleared.Status = nil, nil, nil
	cleared.Body, cleared.GitCommitSHA = "", ""
	datastore.AdvanceFileSystemVersion(&cleared)
	require.NoError(t, first.UpdateFile(t.Context(), &cleared, current.ResourceVersion))
	after, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Equal(t, &cleared, after, "explicit removals must be written, never treated as unchanged")
	stale := *before
	stale.Body = "stale"
	datastore.AdvanceFileSystemVersion(&stale)
	require.ErrorIs(t, second.UpdateFile(t.Context(), &stale, before.ResourceVersion), datastore.ErrConflict)
	unchanged, err := first.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Equal(t, after, unchanged, "a stale version cannot partially change selective columns")
	raw := newRawSession(t)
	t.Cleanup(raw.Close)
	uid, err := gocql.ParseUUID(file.UID)
	require.NoError(t, err)
	require.NoError(t, raw.Query("UPDATE files_by_namespace SET owner_references=?,spec=?,status=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
		nil, nil, nil, file.Namespace, file.CreationTimestamp, uid).Exec())
	repair, err := second.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Nil(t, repair.OwnerReferences)
	require.Nil(t, repair.Spec)
	require.Nil(t, repair.Status)
	expected := repair.ResourceVersion
	datastore.AdvanceFileSystemVersion(repair)
	require.NoError(t, first.UpdateFile(t.Context(), repair, expected))
	var owners, spec, status *string
	require.NoError(t, raw.Query("SELECT owner_references,spec,status FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
		file.Namespace, file.CreationTimestamp, uid).Scan(&owners, &spec, &status))
	require.NotNil(t, owners, "unchanged logical JSON absence must still normalize CQL null to empty text")
	require.NotNil(t, spec)
	require.NotNil(t, status)
	require.Empty(t, *owners)
	require.Empty(t, *spec)
	require.Empty(t, *status)
}

func TestScylla_FileBatchReusesOneFenceWithoutWeakeningExclusion(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	storeA, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storeA.Close()) })
	storeB := newTestStore(t)
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "file-batch-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	epoch, pending := readFence()
	require.Zero(t, pending)
	files := []*datastore.File{
		{UID: newID(), Namespace: ns.Name, Name: "first", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()},
		{UID: newID(), Namespace: ns.Name, Name: "second", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()},
	}
	var retained context.Context
	registry := prometheus.NewRegistry()
	batcher := datastore.NewInstrumentedDatastoreWithRegistry(storeA, "scylla", zap.NewNop(), registry).(datastore.CatalogMutationBatcher)
	createEvidence := &capacityQueryEvidence{tables: map[string]int{}}
	createCtx := context.WithValue(ctx, capacityQueryKey{}, createEvidence)
	require.NoError(t, batcher.WithCatalogMutationBatch(createCtx, ns.Name, func(batchCtx context.Context) error {
		retained = batchCtx
		errs := make([]error, len(files))
		var wg sync.WaitGroup
		for i, file := range files {
			wg.Go(func() { errs[i] = storeA.CreateFile(batchCtx, file) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
		_, pending := readFence()
		require.EqualValues(t, 1, pending, "nested File writes must not release the outer reservation")
		terminating := *repo
		now := time.Now().UTC()
		terminating.DeletionTimestamp = &now
		datastore.AdvanceRepositorySystemVersion(&terminating)
		require.ErrorIs(t, storeA.UpdateRepository(batchCtx, &terminating, repo.ResourceVersion), datastore.ErrInvalidArgument,
			"parent lifecycle writes cannot reuse a child admission reservation")
		require.ErrorIs(t, storeA.DeleteFile(batchCtx, files[0].UID), datastore.ErrInvalidArgument)
		peerCtx, cancel := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer cancel()
		peer := &datastore.File{UID: newID(), Namespace: ns.Name, Name: "peer", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()}
		require.ErrorIs(t, storeB.CreateFile(peerCtx, peer), context.DeadlineExceeded,
			"a context issued by another datastore replica must not bypass its own fence")
		lifecycleCtx, stop := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer stop()
		require.ErrorIs(t, storeB.UpdateRepository(lifecycleCtx, &terminating, repo.ResourceVersion), context.DeadlineExceeded,
			"the held reservation must keep the checked Repository active across peer lifecycle writes")
		return nil
	}))
	afterEpoch, afterPending := readFence()
	require.Equal(t, epoch+1, afterEpoch, "one reservation must cover both File writes")
	require.Zero(t, afterPending)
	createEvidence.mu.Lock()
	require.Equal(t, 1, createEvidence.tables["namespaces_by_name"], "the held active Namespace reservation must avoid redundant parent-name lookups")
	require.Equal(t, 2, createEvidence.tables["repositories_by_uid"], "the File writes share one checked parent; the lifecycle rejection reads its own current row")
	require.Zero(t, createEvidence.errors)
	createEvidence.mu.Unlock()
	blocked, err := storeB.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked)
	beforeUpdateEpoch, _ := readFence()
	updateEvidence := &capacityQueryEvidence{tables: map[string]int{}}
	updateCtx := context.WithValue(ctx, capacityQueryKey{}, updateEvidence)
	require.NoError(t, batcher.WithCatalogMutationBatch(updateCtx, ns.Name, func(batchCtx context.Context) error {
		errs := make([]error, len(files))
		var wg sync.WaitGroup
		for i, file := range files {
			wg.Go(func() {
				desired := *file
				desired.Annotations = map[string]string{"sequence": "1"}
				desired.CreationTimestamp = file.CreationTimestamp.Add(time.Hour)
				datastore.AdvanceFileSystemVersion(&desired)
				errs[i] = storeA.UpdateFile(batchCtx, &desired, file.ResourceVersion)
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
		for _, file := range files {
			current, err := storeB.GetFile(batchCtx, file.UID)
			if err != nil {
				return err
			}
			require.Equal(t, "1", current.Annotations["sequence"])
			require.Equal(t, file.CreationTimestamp.UnixMilli(), current.CreationTimestamp.UnixMilli(), "updates preserve the immutable authoritative key")
			stale := *file
			datastore.AdvanceFileSystemVersion(&stale)
			require.ErrorIs(t, storeA.UpdateFile(batchCtx, &stale, file.ResourceVersion), datastore.ErrConflict)
		}
		return nil
	}))
	afterUpdateEpoch, afterUpdatePending := readFence()
	require.Equal(t, beforeUpdateEpoch+1, afterUpdateEpoch)
	require.Zero(t, afterUpdatePending)
	updateEvidence.mu.Lock()
	require.Equal(t, 1, updateEvidence.tables["namespaces_by_name"], "the held active Namespace reservation must avoid redundant parent-name lookups")
	require.Equal(t, 1, updateEvidence.tables["repositories_by_uid"], "updates and stale CAS attempts share only the parent eligibility check within the held reservation")
	require.Equal(t, 4, updateEvidence.tables["files_by_uid"], "each update and stale attempt resolves the UID once, then reads back its exact immutable authoritative key")
	require.Equal(t, 8, updateEvidence.tables["files_by_namespace"], "four attempts retain both authoritative reads")
	require.Equal(t, 4, updateEvidence.inserts["resources_by_repository"], "each update must repair its full conservative blocker once; identical readback must not rewrite it")
	require.Zero(t, updateEvidence.errors)
	updateEvidence.mu.Unlock()
	currentFiles := make([]*datastore.File, len(files))
	for i, file := range files {
		currentFiles[i], err = storeB.GetFile(ctx, file.UID)
		require.NoError(t, err)
		datastore.AdvanceFileSystemVersion(currentFiles[i])
	}
	keyEvidence := &capacityQueryEvidence{tables: map[string]int{}}
	keyCtx := context.WithValue(ctx, capacityQueryKey{}, keyEvidence)
	require.NoError(t, batcher.WithCatalogMutationBatch(keyCtx, ns.Name, func(batchCtx context.Context) error {
		errs := make([]error, len(currentFiles))
		var wg sync.WaitGroup
		for i, file := range currentFiles {
			wg.Go(func() { errs[i] = storeA.UpdateFile(batchCtx, file, "2") })
		}
		wg.Wait()
		return errors.Join(errs...)
	}))
	keyEvidence.mu.Lock()
	require.Equal(t, 1, keyEvidence.namespaceFenceReads, "a confirmed exclusive reservation must not reread its unchanged fence on release")
	require.Equal(t, 1, keyEvidence.namespaceReleaseCASAttempts, "release must use one conditional attempt when the held fence is unchanged")
	require.Equal(t, 2, keyEvidence.tables["files_by_uid"], "each update resolves its immutable target through the datastore-owned UID locator")
	require.Equal(t, 4, keyEvidence.tables["files_by_namespace"], "each update retains its fresh authoritative before/readback queries")
	require.Equal(t, 2, keyEvidence.inserts["resources_by_repository"], "each update still repairs its own conservative blocker")
	require.Zero(t, keyEvidence.errors)
	keyEvidence.mu.Unlock()
	for _, file := range currentFiles {
		current, err := storeB.GetFile(ctx, file.UID)
		require.NoError(t, err)
		require.Equal(t, "3", current.ResourceVersion)
		require.Equal(t, file.Generation, current.Generation)
		require.Equal(t, file.CreationTimestamp, current.CreationTimestamp)
		require.Equal(t, file.Annotations, current.Annotations)
	}
	families, err := registry.Gather()
	require.NoError(t, err)
	phaseCounts := make(map[string]uint64)
	for _, family := range families {
		if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "operation" {
					phaseCounts[label.GetValue()] = metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	require.EqualValues(t, 8, phaseCounts["CatalogParentCheck"])
	require.EqualValues(t, 8, phaseCounts["CatalogProjectionReadBefore"])
	require.EqualValues(t, 8, phaseCounts["CatalogProjectionReadAfter"])
	require.EqualValues(t, 10, phaseCounts["CatalogMembershipWrite"])
	require.EqualValues(t, 4, phaseCounts["FileAuthoritativeCAS"])
	require.EqualValues(t, 4, phaseCounts["FileOwnerReferenceSync"])
	require.EqualValues(t, 13, phaseCounts["FileUIDLocatorRead"], "include create/readback, updates, peer reads and the rejected delete's initial lookup")
	blocked, err = storeB.HasCatalogResources(ctx, repo.UID)
	require.NoError(t, err)
	require.True(t, blocked, "same-path CAS updates must retain repository deletion blockers")
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(1), nsUID).Exec())
	short, cancel := context.WithTimeout(retained, 60*time.Millisecond)
	err = storeA.CreateFile(short, &datastore.File{UID: newID(), Namespace: ns.Name, Name: "escaped", RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()})
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded, "a retained context must not outlive its batch reservation")
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(0), nsUID).Exec())
	sentinel := fmt.Errorf("batch operation failed")
	require.ErrorIs(t, batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error { return sentinel }), sentinel)
	_, pending = readFence()
	require.Zero(t, pending, "an unambiguous failure must release the parent reservation")
	canceled, stop := context.WithCancel(ctx)
	require.ErrorIs(t, batcher.WithCatalogMutationBatch(canceled, ns.Name, func(context.Context) error {
		stop()
		return canceled.Err()
	}), context.Canceled)
	_, pending = readFence()
	require.Zero(t, pending, "caller cancellation must not strand a confirmed reservation")
	repair := datastore.NewRepairRequiredError(datastore.MutationStep{Operation: "create_file", ResourceKind: "File"},
		fmt.Errorf("ambiguous File membership write"), nil)
	require.ErrorIs(t, batcher.WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error { return repair }), datastore.ErrRepairRequired)
	_, pending = readFence()
	require.EqualValues(t, 1, pending, "ambiguous mutation outcomes must retain the reservation for repair")
}

type namespaceReservationObserver func(context.Context, gocql.ObservedQuery)

func (observe namespaceReservationObserver) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	observe(ctx, query)
}

func TestScylla_FileConditionalBatchesPreserveEnvelopesAndIndependentConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict-%t", conflict), func(t *testing.T) {
			const count = 16
			type updateKey struct{}
			started, release := make(chan struct{}), make(chan struct{})
			var held, resumed sync.Once
			var reads atomic.Int64
			cfg := testScyllaConfig(t)
			cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
				if ctx.Value(updateKey{}) != true || query.Err != nil {
					return
				}
				if strings.Contains(query.Statement, " FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?") {
					reads.Add(1)
				}
				if strings.HasPrefix(query.Statement, "UPDATE files_by_namespace SET name=") {
					held.Do(func() {
						close(started)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
			})
			store, err := scylla.New(cfg, zap.NewNop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			peer := newTestStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ns := &datastore.Namespace{UID: newID(), Name: "conditional-files-" + newID()[:8]}
			require.NoError(t, store.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
			require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, repo))
			desired := make([]*datastore.File, count)
			created := time.Now().UTC().Truncate(time.Millisecond)
			for i := range desired {
				file := &datastore.File{
					UID: newID(), Namespace: ns.Name, Name: fmt.Sprintf("file-%d", i), RepositoryID: repo.UID,
					APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", Generation: 1, ResourceVersion: "1",
					CreationTimestamp: created, CreationActor: "creator", Labels: map[string]string{"stable": "label"},
					Annotations: map[string]string{"old": "annotation"}, Finalizers: []string{"old-finalizer"},
					Spec: json.RawMessage(`{"old":true}`), Status: json.RawMessage(`{"old":true}`),
					SourcePath: fmt.Sprintf("files/%d.yaml", i), GitCommitSHA: "old-sha", GitRef: "refs/heads/main",
					Body: "old-body",
				}
				require.NoError(t, store.CreateFile(ctx, file))
				current, err := peer.GetFile(ctx, file.UID)
				require.NoError(t, err)
				current.CreationTimestamp = created.Add(time.Hour)
				current.Generation++
				current.UpdateActor = fmt.Sprintf("actor-%d", i)
				current.Annotations = nil
				current.Finalizers = nil
				current.Spec, current.Status, current.OwnerReferences = nil, nil, nil
				current.Body = fmt.Sprintf("body-%d", i)
				current.GitCommitSHA = fmt.Sprintf("sha-%d", i)
				datastore.AdvanceFileSystemVersion(current)
				desired[i] = current
			}
			registry := prometheus.NewRegistry()
			batcher := datastore.NewInstrumentedDatastoreWithRegistry(store, "scylla", zap.NewNop(), registry).(datastore.CatalogMutationBatcher)
			resume := func() { resumed.Do(func() { close(release) }) }
			t.Cleanup(resume)
			var results [count]error
			err = batcher.WithCatalogMutationBatch(context.WithValue(ctx, updateKey{}, true), ns.Name, func(batchCtx context.Context) error {
				var writes sync.WaitGroup
				writes.Go(func() { results[0] = store.UpdateFile(batchCtx, desired[0], "1") })
				select {
				case <-started:
				case <-ctx.Done():
					resume()
					writes.Wait()
					return ctx.Err()
				}
				for i := 1; i < count; i++ {
					writes.Go(func() { results[i] = store.UpdateFile(batchCtx, desired[i], "1") })
				}
				ready := assert.Eventually(t, func() bool { return reads.Load() >= count }, 5*time.Second, time.Millisecond)
				if ready {
					// Hold only this test's first CAS response until peer writes queue.
					time.Sleep(50 * time.Millisecond)
					if conflict {
						file := desired[count-1]
						raw := newRawSession(t)
						uid, err := gocql.ParseUUID(file.UID)
						assert.NoError(t, err)
						applied, err := execCapacityCAS(raw.Query("UPDATE files_by_namespace SET resource_version=?,body=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
							"2", "peer-body", ns.Name, created, uid, "1").WithContext(ctx))
						assert.NoError(t, err)
						assert.True(t, applied, "the racing row must actually invalidate its queued condition")
					}
				}
				resume()
				writes.Wait()
				return errors.Join(results[:]...)
			})
			if conflict {
				require.ErrorIs(t, err, datastore.ErrConflict)
			} else {
				require.NoError(t, err)
			}
			for i, file := range desired {
				current, err := peer.GetFile(ctx, file.UID)
				require.NoError(t, err)
				if conflict && i == count-1 {
					require.ErrorIs(t, results[i], datastore.ErrConflict)
					require.Equal(t, "peer-body", current.Body)
				} else {
					require.NoError(t, results[i], "an unrelated row must not inherit another row's definite CAS conflict")
					file.CreationTimestamp = created
					require.Equal(t, file, current, "physical batches retain independent actors, clears, UNSET fields and immutable keys")
				}
			}
			families, err := registry.Gather()
			require.NoError(t, err)
			batches := uint64(0)
			rows := uint64(0)
			maximumBatch := 0
			for _, family := range families {
				if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
					continue
				}
				for _, metric := range family.GetMetric() {
					for _, label := range metric.GetLabel() {
						if label.GetName() != "operation" {
							continue
						}
						switch label.GetValue() {
						case "FileAuthoritativeCAS":
							rows = metric.GetHistogram().GetSampleCount()
						default:
							if strings.HasPrefix(label.GetValue(), "FileConditionalBatch") {
								batches += metric.GetHistogram().GetSampleCount()
								size, err := strconv.Atoi(strings.TrimPrefix(label.GetValue(), "FileConditionalBatch"))
								require.NoError(t, err)
								maximumBatch = max(maximumBatch, size)
							}
						}
					}
				}
			}
			require.Positive(t, batches, "the proof must execute actual full-binding conditional batches")
			require.Greater(t, maximumBatch, 4, "the proof must exercise larger same-partition physical batches")
			require.LessOrEqual(t, maximumBatch, count)
			require.EqualValues(t, count, rows, "row attempts remain individually measurable")
			blocked, err := peer.HasCatalogResources(ctx, repo.UID)
			require.NoError(t, err)
			require.True(t, blocked)
		})
	}
}

func TestScylla_FileCohortIncludesArrivalsDuringReservation(t *testing.T) {
	type reservationKey struct{}
	reserved, resume, peerLookup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, resumed, peerObserved sync.Once
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = namespaceReservationObserver(func(ctx context.Context, query gocql.ObservedQuery) {
		if ctx.Value(reservationKey{}) == "peer" && query.Err == nil &&
			strings.Contains(query.Statement, " FROM namespaces_by_uid WHERE ") {
			peerObserved.Do(func() { close(peerLookup) })
		}
		if ctx.Value(reservationKey{}) == true && query.Err == nil &&
			strings.HasPrefix(query.Statement, "UPDATE namespaces_by_uid SET repository_creation_epoch=") {
			once.Do(func() {
				close(reserved)
				<-resume
			})
		}
	})
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ns := &datastore.Namespace{UID: newID(), Name: "late-cohort-" + newID()[:8]}
	require.NoError(t, store.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, repo))
	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readEpoch := func() int64 {
		t.Helper()
		var epoch int64
		require.NoError(t, raw.Query("SELECT repository_creation_epoch FROM namespaces_by_uid WHERE uid=?", uid).Scan(&epoch))
		return epoch
	}
	before := readEpoch()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	release := func() { resumed.Do(func() { close(resume) }) }
	t.Cleanup(release)
	batcher := store.(datastore.FileMutationBatcher)
	create := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			return store.CreateFile(ctx, &datastore.File{UID: newID(), Namespace: ns.Name, Name: name,
				RepositoryID: repo.UID, ResourceVersion: "1", CreationTimestamp: time.Now().UTC()})
		}
	}
	go func() {
		firstDone <- batcher.WithFileMutationBatch(context.WithValue(ctx, reservationKey{}, true), ns.Name, repo.UID, []string{"first"}, create("first"))
	}()
	select {
	case <-reserved:
	case <-ctx.Done():
		t.Fatal("first cohort did not acquire its reservation")
	}
	go func() {
		secondDone <- batcher.WithFileMutationBatch(context.WithValue(ctx, reservationKey{}, "peer"), ns.Name, repo.UID, []string{"second"}, create("second"))
	}()
	select {
	case <-peerLookup:
	case <-ctx.Done():
		t.Fatal("peer did not complete its Namespace lookup while the reservation was held")
	}
	time.Sleep(30 * time.Millisecond)
	release()
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	require.Equal(t, before+1, readEpoch(), "consecutive callbacks queued during the reservation RPC must share that confirmed fence")
	for _, name := range []string{"first", "second"} {
		file, err := store.GetFileByName(ctx, ns.Name, name)
		require.NoError(t, err)
		require.Equal(t, "1", file.ResourceVersion)
		require.Equal(t, repo.UID, file.RepositoryID)
	}
}

func TestScylla_CatalogReleasePreservesPeerRepositoryReservation(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	first, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "release-peer-" + newID()[:8]}
	require.NoError(t, first.CreateNamespace(ctx, ns))
	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	readPending := func() int64 {
		t.Helper()
		var pending int64
		require.NoError(t, raw.Query("SELECT pending_repository_creations FROM namespaces_by_uid WHERE uid=?", uid).Scan(&pending))
		return pending
	}
	reserved, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cfg.QueryObserver = namespaceReservationObserver(func(_ context.Context, query gocql.ObservedQuery) {
		if query.Err == nil && strings.HasPrefix(query.Statement, "UPDATE namespaces_by_uid SET repository_creation_epoch=") {
			once.Do(func() {
				close(reserved)
				<-resume
			})
		}
	})
	second, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	peerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	peerDone := make(chan error, 1)
	peerStarted := false
	defer func() {
		close(resume)
		if peerStarted {
			require.NoError(t, <-peerDone)
			require.Zero(t, readPending(), "the peer must release only its remaining reservation")
		}
	}()
	evidence := &capacityQueryEvidence{tables: map[string]int{}}
	batchCtx := context.WithValue(ctx, capacityQueryKey{}, evidence)
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "peer"}
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(batchCtx, ns.Name, func(context.Context) error {
		peerStarted = true
		go func() { peerDone <- second.CreateRepositoryInActiveNamespace(peerCtx, repo) }()
		select {
		case <-reserved:
		case err := <-peerDone:
			peerDone <- err
			return errors.Join(errors.New("peer did not retain a reservation"), err)
		case <-peerCtx.Done():
			return peerCtx.Err()
		}
		require.EqualValues(t, 2, readPending())
		return nil
	}))
	require.EqualValues(t, 1, readPending(), "catalog release must preserve the still-active peer reservation")
	evidence.mu.Lock()
	require.Equal(t, 2, evidence.namespaceFenceReads, "a changed epoch requires the existing fresh-read release")
	require.Equal(t, 2, evidence.namespaceReleaseCASAttempts, "one definite miss may fall back to the existing conditional decrement")
	require.Zero(t, evidence.errors)
	evidence.mu.Unlock()
}

func TestScylla_CatalogReleaseFailureRetainsReservation(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.QueryObserver = capacityQueryObserver{}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	ns := &datastore.Namespace{UID: newID(), Name: "release-failure-" + newID()[:8]}
	require.NoError(t, store.CreateNamespace(t.Context(), ns))
	evidence := &capacityQueryEvidence{tables: map[string]int{}}
	ctx := context.WithValue(t.Context(), capacityQueryKey{}, evidence)
	err = store.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, ns.Name, func(context.Context) error { return store.Close() })
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	require.ErrorContains(t, err, "release namespace catalog reservation", "release errors must return without a fresh-read decrement")
	evidence.mu.Lock()
	require.Equal(t, 1, evidence.namespaceFenceReads)
	evidence.mu.Unlock()
	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	var pending int64
	require.NoError(t, raw.Query("SELECT pending_repository_creations FROM namespaces_by_uid WHERE uid=?", uid).Scan(&pending))
	require.EqualValues(t, 1, pending, "unconfirmed releases remain blocked for explicit repair")
}

func TestScylla_CheckedCatalogParentCannotMoveUntilReservationEnds(t *testing.T) {
	first, second := newTestStores(t)
	ctx := t.Context()
	original := &datastore.Namespace{UID: newID(), Name: "checked-parent-" + newID()[:8]}
	target := &datastore.Namespace{UID: newID(), Name: "checked-target-" + newID()[:8]}
	require.NoError(t, first.CreateNamespace(ctx, original))
	require.NoError(t, first.CreateNamespace(ctx, target))
	repo := &datastore.Repository{UID: newID(), Namespace: original.Name, Name: "catalog"}
	require.NoError(t, first.CreateRepositoryInActiveNamespace(ctx, repo))
	moved := *repo
	moved.Namespace = target.Name
	datastore.AdvanceRepositorySystemVersion(&moved)
	var retained context.Context
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, original.Name, func(batchCtx context.Context) error {
		retained = batchCtx
		require.NoError(t, scylla.CatalogParentsActiveForTest(first, batchCtx, original.Name, repo.UID))
		peerCtx, cancel := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, second.UpdateRepository(peerCtx, &moved, repo.ResourceVersion), context.DeadlineExceeded,
			"a checked parent's namespace cannot change while the reservation is held")
		require.NoError(t, scylla.CatalogParentsActiveForTest(first, batchCtx, original.Name, repo.UID))
		terminating := *repo
		terminating.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
		datastore.AdvanceRepositorySystemVersion(&terminating)
		lifecycleCtx, stop := context.WithTimeout(batchCtx, 60*time.Millisecond)
		defer stop()
		require.ErrorIs(t, second.UpdateRepository(lifecycleCtx, &terminating, repo.ResourceVersion), context.DeadlineExceeded,
			"deletion intent cannot invalidate a checked parent while the reservation is held")
		return nil
	}))
	require.NoError(t, second.UpdateRepository(ctx, &moved, repo.ResourceVersion))
	require.ErrorIs(t, scylla.CatalogParentsActiveForTest(first, retained, original.Name, repo.UID), datastore.ErrConflict,
		"a retained context must perform a fresh parent read after its reservation ends")
	require.NoError(t, first.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, target.Name, func(batchCtx context.Context) error {
		require.NoError(t, scylla.CatalogParentsActiveForTest(first, batchCtx, target.Name, repo.UID),
			"the new reservation checks the parent's current namespace independently")
		require.ErrorIs(t, scylla.CatalogParentsActiveForTest(first, batchCtx, original.Name, repo.UID), datastore.ErrConflict,
			"a checked Repository cannot be reused for a different Namespace")
		return nil
	}))
}

func TestScylla_ActiveRepositoryUpdatesDoNotAcquireCatalogFence(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := t.Context()
	ns := &datastore.Namespace{UID: newID(), Name: "repository-write-" + newID()[:8]}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	nsUID, err := gocql.ParseUUID(ns.UID)
	require.NoError(t, err)
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(1), nsUID).Exec())
	t.Cleanup(func() {
		require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(0), nsUID).Exec())
	})
	readFence := func() (int64, int64) {
		t.Helper()
		var epoch, pending int64
		require.NoError(t, session.Query("SELECT repository_creation_epoch,pending_repository_creations FROM "+scyllaKeyspace+".namespaces_by_uid WHERE uid=?", nsUID).Scan(&epoch, &pending))
		return epoch, pending
	}
	epoch, pending := readFence()
	version := repo.ResourceVersion
	repo.Annotations = map[string]string{"capacity.gitstore.dev/sequence": "metadata-1"}
	datastore.AdvanceRepositorySystemVersion(repo)
	writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err = storeB.UpdateRepository(writeCtx, repo, version)
	cancel()
	require.NoError(t, err, "an active same-path CAS update must not wait for unrelated catalog work")
	afterEpoch, afterPending := readFence()
	require.Equal(t, epoch, afterEpoch)
	require.Equal(t, pending, afterPending)
	current, err := storeA.GetRepository(ctx, repo.UID)
	require.NoError(t, err)
	require.Equal(t, repo.Annotations, current.Annotations)
	require.Equal(t, repo.ResourceVersion, current.ResourceVersion)
	require.Equal(t, repo.Generation, current.Generation)
	for _, intent := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletion-intent-%t", intent), func(t *testing.T) {
			desired := *current
			if intent {
				desired.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
			} else {
				now := time.Now().UTC()
				desired.DeletionTimestamp = &now
			}
			datastore.AdvanceRepositorySystemVersion(&desired)
			writeCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, storeB.UpdateRepository(writeCtx, &desired, current.ResourceVersion), context.DeadlineExceeded,
				"deletion transitions must still exclude child admission")
			unchanged, err := storeA.GetRepository(ctx, repo.UID)
			require.NoError(t, err)
			require.Equal(t, current.ResourceVersion, unchanged.ResourceVersion)
			require.Nil(t, unchanged.DeletionTimestamp)
			require.False(t, datastore.HasDeletionIntent(unchanged.Status))
		})
	}
	results := make(chan error, 2)
	for i, writer := range []datastore.Datastore{storeA, storeB} {
		desired := *current
		desired.Annotations = map[string]string{"writer": strconv.Itoa(i)}
		datastore.AdvanceRepositorySystemVersion(&desired)
		go func() {
			writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			results <- writer.UpdateRepository(writeCtx, &desired, current.ResourceVersion)
		}()
	}
	var successes, conflicts int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, datastore.ErrConflict)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	current, err = storeA.GetRepository(ctx, repo.UID)
	require.NoError(t, err)
	require.Equal(t, repo.Generation, current.Generation)
	afterEpoch, afterPending = readFence()
	require.Equal(t, epoch, afterEpoch)
	require.Equal(t, pending, afterPending)

	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(0), nsUID).Exec())
	previous := current.ResourceVersion
	now := time.Now().UTC()
	current.DeletionTimestamp = &now
	datastore.AdvanceRepositorySystemVersion(current)
	require.NoError(t, storeA.UpdateRepository(ctx, current, previous))
	require.NoError(t, session.Query("UPDATE "+scyllaKeyspace+".namespaces_by_uid SET pending_repository_creations=? WHERE uid=?", int64(1), nsUID).Exec())
	desired := *current
	desired.Status = json.RawMessage(`{"resolved":{"progress":"pending"}}`)
	datastore.AdvanceRepositorySystemVersion(&desired)
	writeCtx, cancel = context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, storeB.UpdateRepository(writeCtx, &desired, current.ResourceVersion), context.DeadlineExceeded,
		"terminating status writes must not race completion cleanup")
}

func TestScylla_RepositoryCompletionRestoresAllLookups(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	for _, stage := range []struct {
		name  string
		step  string
		after bool
	}{
		{"partial-cleanup", "complete-repository-projections", true},
		{"before-authoritative-delete", "complete-repository-authoritative", false},
		{"lost-authoritative-response", "complete-repository-authoritative", true},
	} {
		t.Run(stage.name, func(t *testing.T) {
			ns := &datastore.Namespace{UID: newID(), Name: "completion-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
			require.NoError(t, storeA.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "catalog", CreationTimestamp: ns.CreationTimestamp}
			require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
			require.NoError(t, storeA.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: ns.Name, Name: repo.Name, RepositoryID: repo.UID}))
			previous := repo.ResourceVersion
			now := time.Now().UTC().Truncate(time.Millisecond)
			repo.DeletionTimestamp = &now
			repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
			datastore.AdvanceRepositorySystemVersion(repo)
			require.NoError(t, storeA.UpdateRepository(ctx, repo, previous))
			restoreInjector := scylla.InjectRepositoryCompletionFailureForTest(storeA, stage.step, stage.after)
			t.Cleanup(restoreInjector)
			err := storeA.(datastore.RepositoryDeletionStore).CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion)
			if stage.name != "lost-authoritative-response" {
				require.Error(t, err)
				current, err := storeB.GetRepository(ctx, repo.UID)
				require.NoError(t, err)
				require.Equal(t, repo.ResourceVersion, current.ResourceVersion)
				mapping, err := storeB.LookupRepository(ctx, ns.Name, repo.Name)
				require.NoError(t, err)
				require.Equal(t, repo.UID, mapping.RepositoryID)
				reverse, err := storeB.LookupNamespaceByRepoID(ctx, repo.UID)
				require.NoError(t, err)
				require.Equal(t, repo.Name, reverse.Name)
				list, err := storeB.ListRepositoriesByNamespace(ctx, ns.Name, datastore.PageParams{First: 2})
				require.NoError(t, err)
				require.Len(t, list.Items, 1)
				require.Equal(t, repo.UID, list.Items[0].UID)
				require.NoError(t, storeB.(datastore.RepositoryDeletionStore).CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion))
			} else {
				require.NoError(t, err)
			}
			_, err = storeB.GetRepository(ctx, repo.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			_, err = storeB.LookupRepository(ctx, ns.Name, repo.Name)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			_, err = storeB.LookupNamespaceByRepoID(ctx, repo.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func TestScylla_CatalogAdmissionRacesDeletionIntentAcrossReplicas(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	for _, namespaceIntent := range []bool{false, true} {
		for round := 0; round < 4; round++ {
			ns := &datastore.Namespace{UID: newID(), Name: "catalog-race-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
			require.NoError(t, storeA.CreateNamespace(ctx, ns))
			repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "gitstore-system", CreationTimestamp: ns.CreationTimestamp}
			repo.RepositoryID = repo.UID
			require.NoError(t, storeA.CreateRepositoryInActiveNamespace(ctx, repo))
			product := &datastore.Product{UID: newID(), Namespace: ns.Name, Name: "product", RepositoryID: repo.UID, ResourceVersion: "1"}
			start := make(chan struct{})
			create := func() error { return storeB.CreateProduct(ctx, product) }
			remove := func() error { return storeB.DeleteProduct(ctx, product.UID) }
			if round%2 != 0 {
				file := &datastore.File{UID: product.UID, Namespace: ns.Name, Name: "file", RepositoryID: repo.UID, ResourceVersion: "1"}
				create = func() error { return storeB.CreateFile(ctx, file) }
				remove = func() error { return storeB.DeleteFile(ctx, file.UID) }
			}
			createResult, intentResult := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				createResult <- create()
			}()
			go func() {
				<-start
				intent := json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
				if namespaceIntent {
					expected := ns.ResourceVersion
					ns.Status = intent
					datastore.AdvanceNamespaceSystemVersion(ns)
					intentResult <- storeA.UpdateNamespace(ctx, ns, expected)
				} else {
					expected := repo.ResourceVersion
					repo.Status = intent
					datastore.AdvanceRepositorySystemVersion(repo)
					intentResult <- storeA.UpdateRepository(ctx, repo, expected)
				}
			}()
			close(start)
			createErr, intentErr := <-createResult, <-intentResult
			require.True(t, createErr == nil || intentErr == nil, "both operations failed: create=%v intent=%v", createErr, intentErr)
			require.False(t, createErr == nil && intentErr == nil, "child admission and empty-to-intent transition cannot both commit")
			if createErr == nil {
				blocked, err := storeA.HasCatalogResources(ctx, repo.UID)
				require.NoError(t, err)
				require.True(t, blocked)
				require.NoError(t, remove())
				blocked, err = storeA.HasCatalogResources(ctx, repo.UID)
				require.NoError(t, err)
				require.False(t, blocked)
			}
			if intentErr == nil {
				require.Error(t, create())
			}
		}
	}
}

func TestScylla_DeletionIntentAndGuardedCompletionAcrossReplicas(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	ns := &datastore.Namespace{UID: newID(), Name: "deletion-" + newID()[:8], CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, storeA.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "gitstore-system", CreationTimestamp: ns.CreationTimestamp}
	repo.RepositoryID = repo.UID
	require.NoError(t, storeB.CreateRepositoryInActiveNamespace(ctx, repo))
	require.NoError(t, storeB.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: ns.Name, Name: repo.Name, RepositoryID: repo.UID}))
	blocked, err := datastore.NamespaceDeletionBlocked(ctx, storeA, ns)
	require.NoError(t, err)
	require.False(t, blocked)
	ns.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
	previous := ns.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(ns)
	require.NoError(t, storeA.UpdateNamespace(ctx, ns, previous))
	late := &datastore.Repository{UID: newID(), Namespace: ns.Name, Name: "late"}
	require.ErrorIs(t, storeB.CreateRepositoryInActiveNamespace(ctx, late), datastore.ErrNamespaceNotActive)
	require.ErrorIs(t, storeB.CreateProduct(ctx, &datastore.Product{UID: newID(), Namespace: ns.Name, Name: "late", RepositoryID: repo.UID}), datastore.ErrNamespaceNotActive)
	now := time.Now().UTC().Truncate(time.Millisecond)
	previous = ns.ResourceVersion
	ns.DeletionTimestamp = &now
	datastore.AdvanceNamespaceSystemVersion(ns)
	require.NoError(t, storeA.MarkNamespaceDeletion(ctx, ns, previous))
	require.ErrorIs(t, storeB.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion), datastore.ErrNamespaceNotEmpty)
	repo.DeletionTimestamp = &now
	repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer, "other/finalizer"}
	previous = repo.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(repo)
	require.NoError(t, storeA.UpdateRepository(ctx, repo, previous))
	completion := storeB.(datastore.RepositoryDeletionStore)
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, previous), datastore.ErrConflict)
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion), datastore.ErrConflict)
	previous = repo.ResourceVersion
	repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
	datastore.AdvanceRepositorySystemVersion(repo)
	require.NoError(t, storeA.UpdateRepository(ctx, repo, previous))
	require.NoError(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion))
	_, err = storeA.LookupRepository(ctx, ns.Name, repo.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	require.NoError(t, storeB.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion))
}

func TestScylla_NamespaceRepositoryLifecycleCoordinationAcrossReplicas(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               newID(),
		Name:              "repository-lifecycle-" + newID()[:8],
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, storeA.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{
		UID:               newID(),
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}

	require.NoError(t, storeB.CreateRepositoryInActiveNamespace(ctx, repository))
	current, err := storeA.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	expectedResourceVersion := current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.ErrorIs(t, storeA.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrNamespaceNotEmpty)

	require.NoError(t, storeB.DeleteRepository(ctx, repository.UID))
	current, err = storeA.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion = current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, storeA.MarkNamespaceDeletion(ctx, current, expectedResourceVersion))

	late := *repository
	late.UID = newID()
	late.Name = "late"
	require.ErrorIs(t, storeB.CreateRepositoryInActiveNamespace(ctx, &late), datastore.ErrNamespaceNotActive)
}

func TestScylla_NamespaceRepositoryLifecycleRejectsUninitializedFence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               newID(),
		Name:              "invalid-fence-" + newID()[:8],
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	clearNamespaceRepositoryFence(t, namespace.UID)

	repository := &datastore.Repository{
		UID:               newID(),
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.ErrorIs(t, store.CreateRepositoryInActiveNamespace(ctx, repository), datastore.ErrRepairRequired)
	_, err := store.GetRepository(ctx, repository.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound, "an invalid fence cannot admit a repository")

	current, err := store.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion := current.ResourceVersion
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.ErrorIs(t, store.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrRepairRequired)
	current, err = store.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	require.Nil(t, current.DeletionTimestamp, "an invalid fence cannot start termination")
}

func clearNamespaceRepositoryFence(t *testing.T, namespaceUID string) {
	t.Helper()
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	uid, err := gocql.ParseUUID(namespaceUID)
	require.NoError(t, err)
	require.NoError(t, session.Query(
		fmt.Sprintf(
			"UPDATE %s.namespaces_by_uid SET repository_creation_epoch=null, pending_repository_creations=null WHERE uid=?",
			scyllaKeyspace,
		),
		uid,
	).Exec())
}

func TestScylla_RepositoryDirectUIDPathReversePath_FullEnvelopeAndBodyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Hour)
	uid := newID()
	namespace := "repository-envelope-" + newID()[:8]
	repository := &datastore.Repository{
		APIVersion:        "gitstore.dev/v1beta1",
		Kind:              "Repository",
		UID:               uid,
		ID:                uid,
		Namespace:         namespace,
		NamespaceID:       namespace,
		Name:              "canonical-" + newID()[:8],
		Generation:        9,
		ResourceVersion:   "13",
		Revision:          "main@sha1:repository",
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now.Add(time.Minute),
		UpdateActor:       "bob",
		Labels:            map[string]string{"team": "catalog"},
		Annotations:       map[string]string{"gitstore.dev/note": "repository"},
		OwnerReferences:   json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"owner","uid":"00000000-0000-0000-0000-000000000001"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		RepositoryID:      uid,
		SourcePath:        "repositories/canonical.md",
		GitCommitSHA:      "repository-sha",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"defaultBranch":"main","visibility":"PRIVATE"}`),
		Body:              "# Repository\n\nRaw **Markdown** body.\n",
		Status:            json.RawMessage(`{"observedGeneration":9,"conditions":[]}`),
		DefaultBranch:     "main",
		StorageClass:      "default",
		MaxPackSizeBytes:  64 * 1024 * 1024,
		MaxFileSizeBytes:  8 * 1024 * 1024,
	}
	mapping := &datastore.NamespaceMapping{
		Namespace:    namespace,
		NamespaceID:  namespace,
		Name:         repository.Name,
		RepositoryID: uid,
		RepoID:       uid,
	}

	require.NoError(t, store.CreateRepository(ctx, repository))
	require.NoError(t, store.CreateNamespaceMapping(ctx, mapping))

	got, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, repository, got)
	assert.Equal(t, uid, got.UID)
	assert.Equal(t, namespace, got.Namespace)
	assert.Equal(t, uid, got.RepositoryID)
	assert.Equal(t, repository.Body, got.Body)
	assert.JSONEq(t, string(repository.OwnerReferences), string(got.OwnerReferences))

	byPath, err := store.LookupRepository(ctx, namespace, repository.Name)
	require.NoError(t, err)
	assert.Equal(t, mapping, byPath)

	reverse, err := store.LookupNamespaceByRepoID(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, mapping, reverse)
}

func TestScylla_NamespaceRepositoryQueryShapeMetadata(t *testing.T) {
	tests := []struct {
		name  string
		table interface {
			Metadata() table.Metadata
			Get(...string) (string, []string)
		}
		partKey  []string
		sortKey  []string
		whereKey string
	}{
		{"namespace UID", scylla.NamespaceByUID, []string{"uid"}, nil, "uid"},
		{"namespace name", scylla.NamespaceByName, []string{"name"}, nil, "name"},
		{"repository UID", scylla.RepositoryByUID, []string{"uid"}, nil, "uid"},
		{"repository path", scylla.NamespaceMapping, []string{"namespace"}, []string{"name"}, "namespace"},
		{"repository reverse path", scylla.NamespaceMappingByRepository, []string{"repository_id"}, nil, "repository_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := tt.table.Metadata()
			assert.Equal(t, tt.partKey, metadata.PartKey)
			assert.Equal(t, tt.sortKey, metadata.SortKey)

			statement, _ := tt.table.Get()
			lower := strings.ToLower(statement)
			assert.Contains(t, lower, "where "+tt.whereKey+"=")
			assert.NotContains(t, lower, "allow filtering")
			assert.NotContains(t, lower, " index ")
		})
	}

	store := newTestStore(t)
	require.NotNil(t, store)
	session := newRawSession(t)
	iter := session.Query(
		`SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ?`,
		scyllaKeyspace,
	).Iter()
	var indexName string
	for iter.Scan(&indexName) {
		lower := strings.ToLower(indexName)
		assert.NotContains(t, lower, "repository")
		assert.NotContains(t, lower, "namespace_mapping")
	}
	require.NoError(t, iter.Close())
}
