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

func productFixture(uid, namespace, name string) *datastore.Product {
	return &datastore.Product{
		UID:               uid,
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: time.Now(),
	}
}

func categoryTaxonomyFixture(uid, name string) *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		UID:               uid,
		Namespace:         "test-ns",
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: time.Now(),
	}
}

func collectionFixture(uid, namespace, name string) *datastore.Collection {
	return &datastore.Collection{
		UID:       uid,
		Namespace: namespace,
		Name:      name,
		APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind:      "Collection",
	}
}

// ── Product tests ─────────────────────────────────────────────────────────────

func TestMemdb_CreateAndGetProduct(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000001", "my-store", "macbook-pro")

	require.NoError(t, ds.CreateProduct(ctx, p))

	got, err := ds.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, p.Namespace, got.Namespace)
}

func TestMemdb_GetProduct_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetProduct(context.Background(), "a0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_GetProductByName(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000002", "my-store", "iphone-16")
	require.NoError(t, ds.CreateProduct(ctx, p))

	got, err := ds.GetProductByName(ctx, "my-store", "iphone-16")
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
}

func TestMemdb_GetProductByName_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetProductByName(context.Background(), "my-store", "missing")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_CreateProduct_DuplicateUIDReturnsAlreadyExists(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000003", "my-store", "product-a")
	require.NoError(t, ds.CreateProduct(ctx, p))

	p2 := productFixture("a0000000-0000-0000-0000-000000000003", "my-store", "product-b")
	err := ds.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestMemdb_CreateProduct_DuplicateNameReturnsAlreadyExists(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p1 := productFixture("a0000000-0000-0000-0000-000000000004", "my-store", "dup-name")
	p2 := productFixture("a0000000-0000-0000-0000-000000000005", "my-store", "dup-name")
	require.NoError(t, ds.CreateProduct(ctx, p1))

	err := ds.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestMemdb_ListProducts_Paginated(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()

	p1 := productFixture("a0000000-0000-0000-0000-000000000010", "my-store", "product-10")
	p2 := productFixture("a0000000-0000-0000-0000-000000000011", "my-store", "product-11")
	p3 := productFixture("a0000000-0000-0000-0000-000000000012", "my-store", "product-12")

	require.NoError(t, ds.CreateProduct(ctx, p1))
	require.NoError(t, ds.CreateProduct(ctx, p2))
	require.NoError(t, ds.CreateProduct(ctx, p3))

	result, err := ds.ListProducts(ctx, "my-store", datastore.PageParams{First: 2})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.True(t, result.HasNext)
}

func TestMemdb_ListProducts_ReturnsAll(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	names := []string{"product-a", "product-b", "product-c"}
	for i, name := range names {
		uid := "a0000000-0000-0000-0000-00000000002" + string(rune('0'+i))
		require.NoError(t, ds.CreateProduct(ctx, productFixture(uid, "my-store", name)))
	}

	result, err := ds.ListProducts(ctx, "my-store", datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
}

func TestMemdb_UpdateProduct(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000030", "my-store", "product-30")
	require.NoError(t, ds.CreateProduct(ctx, p))

	p.GitRef = "main"
	require.NoError(t, ds.UpdateProduct(ctx, p))

	got, err := ds.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "main", got.GitRef)
}

func TestMemdb_UpdateProduct_NotFound(t *testing.T) {
	ds := newBackend(t)
	p := productFixture("a0000000-0000-0000-0000-000000000099", "my-store", "no-such")
	err := ds.UpdateProduct(context.Background(), p)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_DeleteProduct(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	p := productFixture("a0000000-0000-0000-0000-000000000040", "my-store", "product-40")
	require.NoError(t, ds.CreateProduct(ctx, p))
	require.NoError(t, ds.DeleteProduct(ctx, p.UID))

	_, err := ds.GetProduct(ctx, p.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_DeleteProduct_NotFound(t *testing.T) {
	ds := newBackend(t)
	err := ds.DeleteProduct(context.Background(), "a0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── CategoryTaxonomy tests ────────────────────────────────────────────────────

func TestMemdb_CreateAndGetCategoryTaxonomy(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := categoryTaxonomyFixture("b0000000-0000-0000-0000-000000000001", "electronics")
	require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

	got, err := ds.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)

	gotByUID, err := ds.GetCategoryTaxonomy(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.Name, gotByUID.Name)
}

func TestMemdb_GetCategoryTaxonomy_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetCategoryTaxonomyByName(context.Background(), "test-ns", "no-such-cat")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	_, err = ds.GetCategoryTaxonomy(context.Background(), "b0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_CreateCategoryTaxonomy_DuplicateNameReturnsAlreadyExists(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c1 := categoryTaxonomyFixture("b0000000-0000-0000-0000-000000000003", "dupe-cat")
	c2 := categoryTaxonomyFixture("b0000000-0000-0000-0000-000000000004", "dupe-cat")
	require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c1))
	err := ds.CreateCategoryTaxonomy(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestMemdb_UpdateCategoryTaxonomy_NotFound(t *testing.T) {
	ds := newBackend(t)
	c := categoryTaxonomyFixture("b0000000-0000-0000-0000-000000000099", "ghost-cat")
	err := ds.UpdateCategoryTaxonomy(context.Background(), c)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Collection tests ──────────────────────────────────────────────────────────

func TestMemdb_CreateAndGetCollection(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := collectionFixture("c0000000-0000-0000-0000-000000000001", "my-store", "summer-sale")
	require.NoError(t, ds.CreateCollection(ctx, c))

	got, err := ds.GetCollection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
	assert.Equal(t, c.Name, got.Name)
}

func TestMemdb_GetCollection_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetCollection(context.Background(), "c0000000-0000-0000-0000-000000000099")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_GetCollectionByName(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := collectionFixture("c0000000-0000-0000-0000-000000000002", "my-store", "winter-sale")
	require.NoError(t, ds.CreateCollection(ctx, c))

	got, err := ds.GetCollectionByName(ctx, "my-store", "winter-sale")
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
}

func TestMemdb_GetCollectionByName_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.GetCollectionByName(context.Background(), "my-store", "not-there")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_DataIsGoneAfterNewInstance(t *testing.T) {
	ctx := context.Background()
	ds1, _ := memdb.New()
	p := productFixture("a0000000-0000-0000-0000-000000000050", "my-store", "product-50")
	require.NoError(t, ds1.CreateProduct(ctx, p))
	ds1.Close() //nolint:errcheck

	ds2, _ := memdb.New()
	defer ds2.Close() //nolint:errcheck
	_, err := ds2.GetProduct(ctx, p.UID)
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
