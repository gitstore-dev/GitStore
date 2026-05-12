// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

func newScyllaContainer(t *testing.T) string {
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
	return host + ":" + port.Port()
}

func newTestStore(t *testing.T) datastore.Datastore {
	t.Helper()
	addr := newScyllaContainer(t)
	cfg := config.ScyllaConfig{
		Hosts:    []string{addr},
		Keyspace: "gitstore",
	}
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newID() string { return uuid.New().String() }

func ptr[T any](v T) *T { return &v }

// ── Product ───────────────────────────────────────────────────────────────────

func TestScylla_CreateGetProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &datastore.Product{
		ID: newID(), SKU: "SKU-001", Title: "Widget",
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

	p := &datastore.Product{ID: newID(), SKU: "SKU-D1", Title: "Dup"}
	require.NoError(t, store.CreateProduct(ctx, p))
	err := store.CreateProduct(ctx, p)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateProduct_DuplicateSKU(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p1 := &datastore.Product{ID: newID(), SKU: "DUPSKU"}
	require.NoError(t, store.CreateProduct(ctx, p1))
	p2 := &datastore.Product{ID: newID(), SKU: "DUPSKU"}
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

	p := &datastore.Product{ID: newID(), SKU: "FIND-ME", Title: "Findable"}
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductBySKU(ctx, "FIND-ME")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
}

func TestScylla_GetProductBySKU_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProductBySKU(context.Background(), "NO-SUCH-SKU")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListProducts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	catID := newID()
	p1 := &datastore.Product{ID: newID(), SKU: "LIST-1", CategoryID: catID}
	p2 := &datastore.Product{ID: newID(), SKU: "LIST-2", CategoryID: catID}
	p3 := &datastore.Product{ID: newID(), SKU: "LIST-3"}

	require.NoError(t, store.CreateProduct(ctx, p1))
	require.NoError(t, store.CreateProduct(ctx, p2))
	require.NoError(t, store.CreateProduct(ctx, p3))

	all, err := store.ListProducts(ctx, datastore.ProductFilter{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 3)

	byCat, err := store.ListProducts(ctx, datastore.ProductFilter{CategoryID: catID})
	require.NoError(t, err)
	assert.Len(t, byCat, 2)
}

func TestScylla_UpdateProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &datastore.Product{ID: newID(), SKU: "UPD-1", Title: "Before"}
	require.NoError(t, store.CreateProduct(ctx, p))
	p.Title = "After"
	require.NoError(t, store.UpdateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Title)
}

func TestScylla_UpdateProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	p := &datastore.Product{ID: newID(), SKU: "GHOST"}
	err := store.UpdateProduct(context.Background(), p)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &datastore.Product{ID: newID(), SKU: "DEL-1"}
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

	c := &datastore.Category{ID: newID(), Name: "Electronics", Slug: "electronics"}
	require.NoError(t, store.CreateCategory(ctx, c))

	got, err := store.GetCategory(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Slug, got.Slug)
}

func TestScylla_CreateCategory_DuplicateSlug(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c1 := &datastore.Category{ID: newID(), Slug: "duplicate"}
	require.NoError(t, store.CreateCategory(ctx, c1))
	c2 := &datastore.Category{ID: newID(), Slug: "duplicate"}
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

	c := &datastore.Category{ID: newID(), Slug: "slug-find"}
	require.NoError(t, store.CreateCategory(ctx, c))

	got, err := store.GetCategoryBySlug(ctx, "slug-find")
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
}

func TestScylla_GetCategoryBySlug_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCategoryBySlug(context.Background(), "missing-slug")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCategories(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c1 := &datastore.Category{ID: newID(), Slug: "cat-ls-1"}
	c2 := &datastore.Category{ID: newID(), Slug: "cat-ls-2"}
	require.NoError(t, store.CreateCategory(ctx, c1))
	require.NoError(t, store.CreateCategory(ctx, c2))

	all, err := store.ListCategories(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)
}

func TestScylla_UpdateCategory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Category{ID: newID(), Slug: "upd-cat", Name: "Before"}
	require.NoError(t, store.CreateCategory(ctx, c))
	c.Name = "After"
	require.NoError(t, store.UpdateCategory(ctx, c))

	got, err := store.GetCategory(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Name)
}

func TestScylla_UpdateCategory_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateCategory(context.Background(), &datastore.Category{ID: newID()})
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteCategory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Category{ID: newID(), Slug: "del-cat"}
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

	c := &datastore.Collection{ID: newID(), Name: "Summer Sale", Slug: "summer-sale"}
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Slug, got.Slug)
}

func TestScylla_CreateCollection_DuplicateSlug(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c1 := &datastore.Collection{ID: newID(), Slug: "dup-col"}
	require.NoError(t, store.CreateCollection(ctx, c1))
	c2 := &datastore.Collection{ID: newID(), Slug: "dup-col"}
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

	c := &datastore.Collection{ID: newID(), Slug: "find-col"}
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollectionBySlug(ctx, "find-col")
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
}

func TestScylla_GetCollectionBySlug_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollectionBySlug(context.Background(), "no-col")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCollections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c1 := &datastore.Collection{ID: newID(), Slug: "col-ls-1"}
	c2 := &datastore.Collection{ID: newID(), Slug: "col-ls-2"}
	require.NoError(t, store.CreateCollection(ctx, c1))
	require.NoError(t, store.CreateCollection(ctx, c2))

	all, err := store.ListCollections(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)
}

func TestScylla_UpdateCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Collection{ID: newID(), Slug: "upd-col", Name: "Before"}
	require.NoError(t, store.CreateCollection(ctx, c))
	c.Name = "After"
	require.NoError(t, store.UpdateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Name)
}

func TestScylla_UpdateCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateCollection(context.Background(), &datastore.Collection{ID: newID()})
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := &datastore.Collection{ID: newID(), Slug: "del-col"}
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

// suppress unused ptr helper warning
var _ = ptr[string]
