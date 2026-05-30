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

func TestMemdb_ListProducts_Paginated(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()

	p1 := productFixture("a0000000-0000-0000-0000-000000000010", "SKU-010")
	p2 := productFixture("a0000000-0000-0000-0000-000000000011", "SKU-011")
	p3 := productFixture("a0000000-0000-0000-0000-000000000012", "SKU-012")

	require.NoError(t, ds.CreateProduct(ctx, p1))
	require.NoError(t, ds.CreateProduct(ctx, p2))
	require.NoError(t, ds.CreateProduct(ctx, p3))

	result, err := ds.ListProducts(ctx, datastore.PageParams{First: 2})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.True(t, result.HasNext)
}

func TestMemdb_ListProducts_ReturnsAll(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	for i, sku := range []string{"SKU-A", "SKU-B", "SKU-C"} {
		id := "a0000000-0000-0000-0000-00000000002" + string(rune('0'+i))
		require.NoError(t, ds.CreateProduct(ctx, productFixture(id, sku)))
	}

	result, err := ds.ListProducts(ctx, datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
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

// ── Repository tests ──────────────────────────────────────────────────────────

const (
	repoID1 = "01960000-0000-7000-8000-000000000001"
	repoID2 = "01960000-0000-7000-8000-000000000002"
	nsID1   = "01960000-0000-7000-8000-000000000010"
	nsID2   = "01960000-0000-7000-8000-000000000011"
)

func repoFixture(id, nsID, name string) *datastore.Repository {
	return &datastore.Repository{
		ID:            id,
		NamespaceID:   nsID,
		Name:          name,
		DefaultBranch: "main",
		StorageClass:  "default",
		CreatedAt:     time.Now(),
		CreatedBy:     "test",
		UpdatedAt:     time.Now(),
		UpdatedBy:     "test",
	}
}

func mappingFixture(nsID, name, repoID string) *datastore.NamespaceMapping {
	return &datastore.NamespaceMapping{
		NamespaceID: nsID,
		Name:        name,
		RepoID:      repoID,
	}
}

func TestMemdb_CreateAndGetRepository(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	r := repoFixture(repoID1, nsID1, "my-repo")

	require.NoError(t, ds.CreateRepository(ctx, r))

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, repoID1, got.ID)
	assert.Equal(t, "my-repo", got.Name)
	assert.Equal(t, nsID1, got.NamespaceID)
}

func TestMemdb_GetRepository_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetRepository(context.Background(), "01960000-0000-7000-8000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_CreateRepository_DuplicateReturnsAlreadyExists(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	r := repoFixture(repoID1, nsID1, "dup-repo")
	require.NoError(t, ds.CreateRepository(ctx, r))

	err := ds.CreateRepository(ctx, repoFixture(repoID1, nsID1, "dup-repo"))
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestMemdb_ListRepositoriesByNamespace(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()

	require.NoError(t, ds.CreateRepository(ctx, repoFixture(repoID1, nsID1, "repo-a")))
	require.NoError(t, ds.CreateRepository(ctx, repoFixture(repoID2, nsID1, "repo-b")))
	require.NoError(t, ds.CreateRepository(ctx, repoFixture("01960000-0000-7000-8000-000000000003", nsID2, "repo-c")))

	result, err := ds.ListRepositoriesByNamespace(ctx, nsID1, datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
}

func TestMemdb_UpdateRepository(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	r := repoFixture(repoID1, nsID1, "original-name")
	require.NoError(t, ds.CreateRepository(ctx, r))

	r.Name = "renamed"
	require.NoError(t, ds.UpdateRepository(ctx, r))

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Name)
}

func TestMemdb_DeleteRepository(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	r := repoFixture(repoID1, nsID1, "to-delete")
	require.NoError(t, ds.CreateRepository(ctx, r))
	require.NoError(t, ds.DeleteRepository(ctx, repoID1))

	_, err := ds.GetRepository(ctx, repoID1)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── NamespaceMapping tests ─────────────────────────────────────────────────────

func TestMemdb_CreateAndLookupMapping(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	m := mappingFixture(nsID1, "my-repo", repoID1)

	require.NoError(t, ds.CreateNamespaceMapping(ctx, m))

	got, err := ds.LookupRepository(ctx, nsID1, "my-repo")
	require.NoError(t, err)
	assert.Equal(t, repoID1, got.RepoID)
}

func TestMemdb_LookupRepository_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.LookupRepository(context.Background(), nsID1, "missing")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_LookupNamespaceByRepoID(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	require.NoError(t, ds.CreateNamespaceMapping(ctx, mappingFixture(nsID1, "configs", repoID1)))

	got, err := ds.LookupNamespaceByRepoID(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, nsID1, got.NamespaceID)
	assert.Equal(t, "configs", got.Name)
}

func TestMemdb_RenameRepository_OldNameNotFoundNewNameReturnsOriginalRepoID(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	require.NoError(t, ds.CreateNamespaceMapping(ctx, mappingFixture(nsID1, "old-name", repoID1)))

	require.NoError(t, ds.RenameRepository(ctx, nsID1, "old-name", "new-name"))

	_, err := ds.LookupRepository(ctx, nsID1, "old-name")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	got, err := ds.LookupRepository(ctx, nsID1, "new-name")
	require.NoError(t, err)
	assert.Equal(t, repoID1, got.RepoID)
}

func TestMemdb_TransferRepository_OldNSNotFoundNewNSReturnsSameRepoID(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	require.NoError(t, ds.CreateNamespaceMapping(ctx, mappingFixture(nsID1, "app", repoID1)))

	require.NoError(t, ds.TransferRepository(ctx, repoID1, nsID1, nsID2))

	_, err := ds.LookupRepository(ctx, nsID1, "app")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	got, err := ds.LookupRepository(ctx, nsID2, "app")
	require.NoError(t, err)
	assert.Equal(t, repoID1, got.RepoID)
}

func TestMemdb_DeleteNamespaceMapping(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	require.NoError(t, ds.CreateNamespaceMapping(ctx, mappingFixture(nsID1, "to-delete", repoID1)))
	require.NoError(t, ds.DeleteNamespaceMapping(ctx, nsID1, "to-delete"))

	_, err := ds.LookupRepository(ctx, nsID1, "to-delete")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}
