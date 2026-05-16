// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

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
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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
	scyllaKeyspace = fmt.Sprintf("gitstore_scylla_test_%d", os.Getpid())

	provisionKeyspace(scyllaAddr, scyllaKeyspace)
	code := m.Run()
	dropKeyspace(scyllaAddr, scyllaKeyspace)

	os.Exit(code)
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
	return cluster.CreateSession()
}

func newTestStore(t *testing.T) datastore.Datastore {
	t.Helper()
	cfg := config.ScyllaConfig{
		Hosts:                 []string{scyllaAddr},
		Keyspace:              scyllaKeyspace,
		DisableShardAwarePort: true,
	}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newID() string { return uuid.New().String() }

// ── Product ───────────────────────────────────────────────────────────────────

func TestScylla_CreateGetProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &datastore.Product{
		ID: newID(), SKU: "SKU-" + newID()[:8], Title: "Widget",
		Price: 9.99, Currency: "USD", InventoryStatus: "in_stock",
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		UpdatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, p.SKU, got.SKU)
	assert.Equal(t, p.Price, got.Price)
}

func TestScylla_CreateProduct_DuplicateID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &datastore.Product{ID: newID(), SKU: "D1-" + newID()[:8]}
	require.NoError(t, store.CreateProduct(ctx, p))
	err := store.CreateProduct(ctx, p)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateProduct_DuplicateSKU(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sku := "DUPSKU-" + newID()[:8]
	p1 := &datastore.Product{ID: newID(), SKU: sku}
	require.NoError(t, store.CreateProduct(ctx, p1))
	p2 := &datastore.Product{ID: newID(), SKU: sku}
	err := store.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetProductBySKU(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sku := "FIND-" + newID()[:8]
	p := &datastore.Product{ID: newID(), SKU: sku, Title: "Findable"}
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductBySKU(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
}

func TestScylla_GetProductBySKU_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProductBySKU(context.Background(), "NO-SUCH-SKU-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListProducts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	catID := newID()
	p1 := &datastore.Product{ID: newID(), SKU: "LS1-" + newID()[:8], CategoryID: catID}
	p2 := &datastore.Product{ID: newID(), SKU: "LS2-" + newID()[:8], CategoryID: catID}
	p3 := &datastore.Product{ID: newID(), SKU: "LS3-" + newID()[:8]}

	require.NoError(t, store.CreateProduct(ctx, p1))
	require.NoError(t, store.CreateProduct(ctx, p2))
	require.NoError(t, store.CreateProduct(ctx, p3))

	byCat, err := store.ListProducts(ctx, datastore.ProductFilter{CategoryID: catID})
	require.NoError(t, err)
	assert.Len(t, byCat, 2)
}

func TestScylla_UpdateProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &datastore.Product{ID: newID(), SKU: "UPD-" + newID()[:8], Title: "Before"}
	require.NoError(t, store.CreateProduct(ctx, p))
	p.Title = "After"
	require.NoError(t, store.UpdateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Title)
}

func TestScylla_UpdateProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	p := &datastore.Product{ID: newID(), SKU: "GHOST-" + newID()[:8]}
	err := store.UpdateProduct(context.Background(), p)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &datastore.Product{ID: newID(), SKU: "DEL-" + newID()[:8]}
	require.NoError(t, store.CreateProduct(ctx, p))
	require.NoError(t, store.DeleteProduct(ctx, p.ID))

	_, err := store.GetProduct(ctx, p.ID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Category ──────────────────────────────────────────────────────────────────

func TestScylla_CreateGetCategory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Category{ID: newID(), Name: "Electronics", Slug: "cat-" + newID()[:8]}
	require.NoError(t, store.CreateCategory(ctx, c))

	got, err := store.GetCategory(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Slug, got.Slug)
}

func TestScylla_CreateCategory_DuplicateSlug(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	slug := "dup-cat-" + newID()[:8]
	c1 := &datastore.Category{ID: newID(), Slug: slug}
	require.NoError(t, store.CreateCategory(ctx, c1))
	c2 := &datastore.Category{ID: newID(), Slug: slug}
	err := store.CreateCategory(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCategory_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCategory(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetCategoryBySlug(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	slug := "slug-" + newID()[:8]
	c := &datastore.Category{ID: newID(), Slug: slug}
	require.NoError(t, store.CreateCategory(ctx, c))

	got, err := store.GetCategoryBySlug(ctx, slug)
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
}

func TestScylla_GetCategoryBySlug_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCategoryBySlug(context.Background(), "missing-slug-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCategories(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCategories(ctx)
	require.NoError(t, err)

	c1 := &datastore.Category{ID: newID(), Slug: "catls1-" + newID()[:8]}
	c2 := &datastore.Category{ID: newID(), Slug: "catls2-" + newID()[:8]}
	require.NoError(t, store.CreateCategory(ctx, c1))
	require.NoError(t, store.CreateCategory(ctx, c2))

	after, err := store.ListCategories(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(before)+2, len(after))
}

func TestScylla_UpdateCategory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Category{ID: newID(), Slug: "upd-cat-" + newID()[:8], Name: "Before"}
	require.NoError(t, store.CreateCategory(ctx, c))
	c.Name = "After"
	require.NoError(t, store.UpdateCategory(ctx, c))

	got, err := store.GetCategory(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Name)
}

func TestScylla_UpdateCategory_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateCategory(context.Background(), &datastore.Category{ID: newID(), Slug: "ghost-" + newID()[:8]})
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteCategory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Category{ID: newID(), Slug: "del-cat-" + newID()[:8]}
	require.NoError(t, store.CreateCategory(ctx, c))
	require.NoError(t, store.DeleteCategory(ctx, c.ID))
	_, err := store.GetCategory(ctx, c.ID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteCategory_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteCategory(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Collection ────────────────────────────────────────────────────────────────

func TestScylla_CreateGetCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Collection{ID: newID(), Name: "Summer Sale", Slug: "col-" + newID()[:8]}
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Slug, got.Slug)
}

func TestScylla_CreateCollection_DuplicateSlug(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	slug := "dup-col-" + newID()[:8]
	c1 := &datastore.Collection{ID: newID(), Slug: slug}
	require.NoError(t, store.CreateCollection(ctx, c1))
	c2 := &datastore.Collection{ID: newID(), Slug: slug}
	err := store.CreateCollection(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollection(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetCollectionBySlug(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	slug := "find-col-" + newID()[:8]
	c := &datastore.Collection{ID: newID(), Slug: slug}
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollectionBySlug(ctx, slug)
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
}

func TestScylla_GetCollectionBySlug_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollectionBySlug(context.Background(), "no-col-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCollections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCollections(ctx)
	require.NoError(t, err)

	c1 := &datastore.Collection{ID: newID(), Slug: "colls1-" + newID()[:8]}
	c2 := &datastore.Collection{ID: newID(), Slug: "colls2-" + newID()[:8]}
	require.NoError(t, store.CreateCollection(ctx, c1))
	require.NoError(t, store.CreateCollection(ctx, c2))

	after, err := store.ListCollections(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(before)+2, len(after))
}

func TestScylla_UpdateCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Collection{ID: newID(), Slug: "upd-col-" + newID()[:8], Name: "Before"}
	require.NoError(t, store.CreateCollection(ctx, c))
	c.Name = "After"
	require.NoError(t, store.UpdateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Name)
}

func TestScylla_UpdateCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateCollection(context.Background(), &datastore.Collection{ID: newID(), Slug: "ghost-col-" + newID()[:8]})
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Collection{ID: newID(), Slug: "del-col-" + newID()[:8]}
	require.NoError(t, store.CreateCollection(ctx, c))
	require.NoError(t, store.DeleteCollection(ctx, c.ID))
	_, err := store.GetCollection(ctx, c.ID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteCollection(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}
