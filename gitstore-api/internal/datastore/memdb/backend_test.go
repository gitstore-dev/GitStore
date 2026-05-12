// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBackend(t *testing.T) datastore.Datastore {
	t.Helper()
	ds, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close() }) //nolint:errcheck
	return ds
}

func productFixture(id, sku string) *datastore.Product {
	return &datastore.Product{
		ID:        id,
		SKU:       sku,
		Title:     "Test Product",
		Price:     9.99,
		Currency:  "USD",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func categoryFixture(id, slug string) *datastore.Category {
	return &datastore.Category{
		ID:        id,
		Name:      "Test Category",
		Slug:      slug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func collectionFixture(id, slug string) *datastore.Collection {
	return &datastore.Collection{
		ID:        id,
		Name:      "Test Collection",
		Slug:      slug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// ── Product tests ─────────────────────────────────────────────────────────────

func TestMemdb_CreateAndGetProduct(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000001", "SKU-001")

	require.NoError(t, ds.CreateProduct(ctx, p))

	got, err := ds.GetProduct(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, p.SKU, got.SKU)
}

func TestMemdb_GetProduct_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetProduct(context.Background(), "a0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_GetProductBySKU(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000002", "SKU-002")
	require.NoError(t, ds.CreateProduct(ctx, p))

	got, err := ds.GetProductBySKU(ctx, "SKU-002")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
}

func TestMemdb_GetProductBySKU_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetProductBySKU(context.Background(), "MISSING")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_CreateProduct_DuplicateIDReturnsAlreadyExists(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000003", "SKU-003")
	require.NoError(t, ds.CreateProduct(ctx, p))

	p2 := productFixture("a0000000-0000-0000-0000-000000000003", "SKU-004")
	err := ds.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestMemdb_CreateProduct_DuplicateSKUReturnsAlreadyExists(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p1 := productFixture("a0000000-0000-0000-0000-000000000004", "SKU-DUP")
	p2 := productFixture("a0000000-0000-0000-0000-000000000005", "SKU-DUP")
	require.NoError(t, ds.CreateProduct(ctx, p1))

	err := ds.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestMemdb_ListProducts_FilterByCategoryID(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()

	p1 := productFixture("a0000000-0000-0000-0000-000000000010", "SKU-010")
	p1.CategoryID = "cat-A"
	p2 := productFixture("a0000000-0000-0000-0000-000000000011", "SKU-011")
	p2.CategoryID = "cat-B"
	p3 := productFixture("a0000000-0000-0000-0000-000000000012", "SKU-012")
	p3.CategoryID = "cat-A"

	require.NoError(t, ds.CreateProduct(ctx, p1))
	require.NoError(t, ds.CreateProduct(ctx, p2))
	require.NoError(t, ds.CreateProduct(ctx, p3))

	results, err := ds.ListProducts(ctx, datastore.ProductFilter{CategoryID: "cat-A"})
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestMemdb_ListProducts_EmptyFilterReturnsAll(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	for i, sku := range []string{"SKU-A", "SKU-B", "SKU-C"} {
		id := "a0000000-0000-0000-0000-00000000002" + string(rune('0'+i))
		require.NoError(t, ds.CreateProduct(ctx, productFixture(id, sku)))
	}

	results, err := ds.ListProducts(ctx, datastore.ProductFilter{})
	require.NoError(t, err)
	assert.Len(t, results, 3)
}

func TestMemdb_UpdateProduct(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000030", "SKU-030")
	require.NoError(t, ds.CreateProduct(ctx, p))

	p.Title = "Updated"
	require.NoError(t, ds.UpdateProduct(ctx, p))

	got, err := ds.GetProduct(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated", got.Title)
}

func TestMemdb_UpdateProduct_NotFound(t *testing.T) {
	ds := newBackend(t)
	p := productFixture("a0000000-0000-0000-0000-000000000099", "SKU-NX")
	err := ds.UpdateProduct(context.Background(), p)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_DeleteProduct(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000040", "SKU-040")
	require.NoError(t, ds.CreateProduct(ctx, p))
	require.NoError(t, ds.DeleteProduct(ctx, p.ID))

	_, err := ds.GetProduct(ctx, p.ID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_DeleteProduct_NotFound(t *testing.T) {
	ds := newBackend(t)
	err := ds.DeleteProduct(context.Background(), "a0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Category tests ────────────────────────────────────────────────────────────

func TestMemdb_CreateAndGetCategory(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := categoryFixture("b0000000-0000-0000-0000-000000000001", "test-cat")
	require.NoError(t, ds.CreateCategory(ctx, c))

	got, err := ds.GetCategory(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Slug, got.Slug)
}

func TestMemdb_GetCategory_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetCategory(context.Background(), "b0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_GetCategoryBySlug(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := categoryFixture("b0000000-0000-0000-0000-000000000002", "slug-lookup")
	require.NoError(t, ds.CreateCategory(ctx, c))

	got, err := ds.GetCategoryBySlug(ctx, "slug-lookup")
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
}

func TestMemdb_GetCategoryBySlug_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetCategoryBySlug(context.Background(), "no-such-slug")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_CreateCategory_DuplicateSlugReturnsAlreadyExists(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c1 := categoryFixture("b0000000-0000-0000-0000-000000000003", "dupe-slug")
	c2 := categoryFixture("b0000000-0000-0000-0000-000000000004", "dupe-slug")
	require.NoError(t, ds.CreateCategory(ctx, c1))
	err := ds.CreateCategory(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestMemdb_DeleteCategory_NotFound(t *testing.T) {
	ds := newBackend(t)
	err := ds.DeleteCategory(context.Background(), "b0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Collection tests ──────────────────────────────────────────────────────────

func TestMemdb_CreateAndGetCollection(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := collectionFixture("c0000000-0000-0000-0000-000000000001", "col-slug")
	require.NoError(t, ds.CreateCollection(ctx, c))

	got, err := ds.GetCollection(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Slug, got.Slug)
}

func TestMemdb_GetCollection_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetCollection(context.Background(), "c0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_GetCollectionBySlug(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := collectionFixture("c0000000-0000-0000-0000-000000000002", "col-slug-lkp")
	require.NoError(t, ds.CreateCollection(ctx, c))

	got, err := ds.GetCollectionBySlug(ctx, "col-slug-lkp")
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
}

func TestMemdb_GetCollectionBySlug_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetCollectionBySlug(context.Background(), "not-there")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_DataIsGoneAfterNewInstance(t *testing.T) {
	ctx := context.Background()
	ds1, _ := memdb.New()
	p := productFixture("a0000000-0000-0000-0000-000000000050", "SKU-050")
	require.NoError(t, ds1.CreateProduct(ctx, p))
	ds1.Close() //nolint:errcheck

	ds2, _ := memdb.New()
	defer ds2.Close() //nolint:errcheck
	_, err := ds2.GetProduct(ctx, p.ID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}
