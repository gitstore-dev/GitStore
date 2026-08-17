// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
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
		UID:        uid,
		Namespace:  namespace,
		Name:       name,
		APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind:       "Collection",
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

func TestMemdb_UpdateCategoryTaxonomyStatus_AppliesPatchAndAdvancesResourceVersion(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := categoryTaxonomyFixture("b0000000-0000-0000-0000-000000000010", "status-cat")
	require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

	generation := int64(1)
	updated, err := ds.UpdateCategoryTaxonomyStatus(ctx, c.Namespace, c.Name, datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion:    c.ResourceVersion,
		ObservedGeneration: &generation,
		Resolved: &catalog.ResolvedCategoryTaxonomy{
			Depth: 0,
			Path:  []string{"status-cat"},
		},
	})
	require.NoError(t, err)
	assert.NotEqual(t, c.ResourceVersion, updated.ResourceVersion)

	var status catalog.CategoryTaxonomyStatus
	require.NoError(t, json.Unmarshal(updated.Status, &status))
	assert.Equal(t, generation, status.ObservedGeneration)
	require.NotNil(t, status.Resolved)
	assert.Equal(t, []string{"status-cat"}, status.Resolved.Path)

	got, err := ds.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, updated.ResourceVersion, got.ResourceVersion)
}

func TestMemdb_UpdateCategoryTaxonomyStatus_StaleResourceVersionReturnsConflict(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := categoryTaxonomyFixture("b0000000-0000-0000-0000-000000000011", "conflict-cat")
	require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

	_, err := ds.UpdateCategoryTaxonomyStatus(ctx, c.Namespace, c.Name, datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion: "stale-version",
	})
	require.ErrorIs(t, err, datastore.ErrConflict)
}

func TestMemdb_UpdateCategoryTaxonomyStatus_NotFound(t *testing.T) {
	ds := newBackend(t)
	_, err := ds.UpdateCategoryTaxonomyStatus(context.Background(), "test-ns", "no-such-cat", datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion: "1",
	})
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestMemdb_UpdateCategoryTaxonomyStatus_PartialMergePreservesUnsetFields(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	c := categoryTaxonomyFixture("b0000000-0000-0000-0000-000000000012", "partial-cat")
	require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

	revision := "main@sha1:abc123"
	updated, err := ds.UpdateCategoryTaxonomyStatus(ctx, c.Namespace, c.Name, datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion:     c.ResourceVersion,
		LastAppliedRevision: &revision,
	})
	require.NoError(t, err)

	// Second patch only sets ObservedGeneration -- LastAppliedRevision must survive unchanged.
	generation := int64(2)
	updated2, err := ds.UpdateCategoryTaxonomyStatus(ctx, c.Namespace, c.Name, datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion:    updated.ResourceVersion,
		ObservedGeneration: &generation,
	})
	require.NoError(t, err)

	var status catalog.CategoryTaxonomyStatus
	require.NoError(t, json.Unmarshal(updated2.Status, &status))
	assert.Equal(t, revision, status.LastAppliedRevision)
	assert.Equal(t, generation, status.ObservedGeneration)
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
	require.NoError(t, ds.UpdateRepository(ctx, r, r.ResourceVersion))

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Name)
}

func TestMemdb_RepositoryContractRoundTrip(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	r := repoFixture(repoID1, nsID1, "contract-repo")
	r.Generation = 3
	r.ResourceVersion = "8"
	r.Status = json.RawMessage(`{"observedGeneration":2,"lastAppliedRevision":"main@sha1:abc","conditions":[]}`)

	require.NoError(t, ds.CreateRepository(ctx, r))

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.Generation)
	assert.Equal(t, "8", got.ResourceVersion)
	assert.JSONEq(t, string(r.Status), string(got.Status))

	got.Generation = 4
	expectedResourceVersion := got.ResourceVersion
	got.ResourceVersion = "9"
	got.Status = json.RawMessage(`{"observedGeneration":4,"conditions":[]}`)
	require.NoError(t, ds.UpdateRepository(ctx, got, expectedResourceVersion))

	updated, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, int64(4), updated.Generation)
	assert.Equal(t, "9", updated.ResourceVersion)
	assert.JSONEq(t, string(got.Status), string(updated.Status))
}

func TestMemdb_RepositoryContractNormalizesLegacyRowsOnEveryReadPath(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	legacy := repoFixture(repoID1, nsID1, "legacy-contract")

	require.NoError(t, ds.CreateRepository(ctx, legacy))
	assert.Equal(t, int64(1), legacy.Generation)
	assert.Equal(t, "1", legacy.ResourceVersion)
	assert.NotEmpty(t, legacy.Status)

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.Generation)
	assert.Equal(t, "1", got.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(got.Status))

	listed, err := ds.ListRepositoriesByNamespace(ctx, nsID1, datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	assert.Equal(t, int64(1), listed.Items[0].Generation)
	assert.Equal(t, "1", listed.Items[0].ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(listed.Items[0].Status))
}

func TestMemdb_RepositoryVersionTransitionsPersist(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	repo := repoFixture(repoID1, nsID1, "versioned")
	require.NoError(t, ds.CreateRepository(ctx, repo))

	expectedResourceVersion := repo.ResourceVersion
	datastore.AdvanceRepositorySpecVersion(repo)
	require.NoError(t, ds.UpdateRepository(ctx, repo, expectedResourceVersion))

	afterSpec, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSpec.Generation)
	assert.Equal(t, "2", afterSpec.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(afterSpec.Status))

	afterSpec.Status = json.RawMessage(`{"observedGeneration":2,"lastAppliedRevision":"main@sha1:abc","conditions":[]}`)
	expectedResourceVersion = afterSpec.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(afterSpec)
	require.NoError(t, ds.UpdateRepository(ctx, afterSpec, expectedResourceVersion))

	afterSystem, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSystem.Generation)
	assert.Equal(t, "3", afterSystem.ResourceVersion)
	assert.JSONEq(t, string(afterSpec.Status), string(afterSystem.Status))
}

func TestMemdb_UpdateRepositoryRejectsStaleResourceVersion(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	repository := repoFixture(repoID1, nsID1, "versioned")
	require.NoError(t, ds.CreateRepository(ctx, repository))

	first, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	stale, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)

	first.Name = "first-writer"
	datastore.AdvanceRepositorySpecVersion(first)
	require.NoError(t, ds.UpdateRepository(ctx, first, "1"))

	stale.Name = "stale-writer"
	datastore.AdvanceRepositorySpecVersion(stale)
	require.ErrorIs(t, ds.UpdateRepository(ctx, stale, "1"), datastore.ErrConflict)

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, "first-writer", got.Name)
	assert.Equal(t, "2", got.ResourceVersion)
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

// T008: push policy fields round-trip through CreateRepository / GetRepository.
func TestMemdb_Repository_PushPolicyRoundTrip(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()

	r := repoFixture(repoID1, nsID1, "policy-repo")
	r.MaxPackSizeBytes = 0 // zero = unlimited (FR-015)
	r.MaxFileSizeBytes = 0 // zero = unlimited (FR-015)
	require.NoError(t, ds.CreateRepository(ctx, r))

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.MaxPackSizeBytes)
	assert.Equal(t, int64(0), got.MaxFileSizeBytes)
}

// T008 (non-zero): verify non-zero policy limits also round-trip.
func TestMemdb_Repository_PushPolicyNonZeroRoundTrip(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()

	r := repoFixture(repoID1, nsID1, "policy-repo-limits")
	r.MaxPackSizeBytes = 52428800 // 50 MiB
	r.MaxFileSizeBytes = 10485760 // 10 MiB
	require.NoError(t, ds.CreateRepository(ctx, r))

	got, err := ds.GetRepository(ctx, repoID1)
	require.NoError(t, err)
	assert.Equal(t, int64(52428800), got.MaxPackSizeBytes)
	assert.Equal(t, int64(10485760), got.MaxFileSizeBytes)
}
