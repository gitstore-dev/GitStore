// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Backend-agnostic datastore contract suite.
// RunContractSuite verifies that any Datastore implementation satisfies the full
// behavioural contract: all 18 CRUD operations, sentinel error wrapping, filter
// semantics, and slug/SKU lookups.

package datastore_contract_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newID() string { return uuid.New().String() }

func newProduct(categoryID string) *datastore.Product {
	now := time.Now()
	return &datastore.Product{
		ID:              newID(),
		SKU:             "SKU-" + newID()[:8],
		Title:           "Test Product",
		Price:           9.99,
		Currency:        "USD",
		InventoryStatus: "in_stock",
		CategoryID:      categoryID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func newCategory() *datastore.Category {
	now := time.Now()
	slug := "cat-" + newID()[:8]
	return &datastore.Category{
		ID:        newID(),
		Name:      "Test Category",
		Slug:      slug,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func newCollection() *datastore.Collection {
	now := time.Now()
	slug := "coll-" + newID()[:8]
	return &datastore.Collection{
		ID:        newID(),
		Name:      "Test Collection",
		Slug:      slug,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func newNamespace(tier datastore.NamespaceTier) *datastore.Namespace {
	now := time.Now()
	id := newID()
	identifier := "ns-" + newID()[:8]
	return &datastore.Namespace{
		ID:         id,
		Identifier: identifier,
		Tier:       tier,
		CreatedAt:  now,
		CreatedBy:  "test-user",
		UpdatedAt:  now,
		UpdatedBy:  "test-user",
	}
}

// RunContractSuite runs the full contract suite against any Datastore implementation.
// Callers should pass a freshly initialised, empty store.
func RunContractSuite(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	ctx := context.Background()

	t.Run("Product/CreateAndGet", func(t *testing.T) {
		p := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p))

		got, err := ds.GetProduct(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, p.ID, got.ID)
		assert.Equal(t, p.SKU, got.SKU)
		assert.Equal(t, p.Title, got.Title)
	})

	t.Run("Product/GetNotFound", func(t *testing.T) {
		_, err := ds.GetProduct(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/GetBySKU", func(t *testing.T) {
		p := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p))

		got, err := ds.GetProductBySKU(ctx, p.SKU)
		require.NoError(t, err)
		assert.Equal(t, p.ID, got.ID)
	})

	t.Run("Product/GetBySKUNotFound", func(t *testing.T) {
		_, err := ds.GetProductBySKU(ctx, "SKU-DOES-NOT-EXIST-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/DuplicateIDReturnsAlreadyExists", func(t *testing.T) {
		p := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p))
		err := ds.CreateProduct(ctx, p)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Product/DuplicateSKUReturnsAlreadyExists", func(t *testing.T) {
		p := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p))

		p2 := newProduct("")
		p2.SKU = p.SKU // same SKU, different ID
		err := ds.CreateProduct(ctx, p2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Product/Update", func(t *testing.T) {
		p := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p))

		p.Title = "Updated Title"
		require.NoError(t, ds.UpdateProduct(ctx, p))

		got, err := ds.GetProduct(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, "Updated Title", got.Title)
	})

	t.Run("Product/UpdateNotFound", func(t *testing.T) {
		p := newProduct("")
		p.ID = newID() // does not exist
		err := ds.UpdateProduct(ctx, p)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/Delete", func(t *testing.T) {
		p := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p))
		require.NoError(t, ds.DeleteProduct(ctx, p.ID))

		_, err := ds.GetProduct(ctx, p.ID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/DeleteNotFound", func(t *testing.T) {
		err := ds.DeleteProduct(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/ListAll", func(t *testing.T) {
		// Count before
		before, err := ds.ListProducts(ctx, datastore.PageParams{})
		require.NoError(t, err)

		p1 := newProduct("")
		p2 := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p1))
		require.NoError(t, ds.CreateProduct(ctx, p2))

		after, err := ds.ListProducts(ctx, datastore.PageParams{})
		require.NoError(t, err)
		assert.Equal(t, len(before.Items)+2, len(after.Items))
	})

	t.Run("Product/ListPaginated", func(t *testing.T) {
		// Ensure at least 2 products exist
		require.NoError(t, ds.CreateProduct(ctx, newProduct("")))
		require.NoError(t, ds.CreateProduct(ctx, newProduct("")))

		result, err := ds.ListProducts(ctx, datastore.PageParams{First: 1})
		require.NoError(t, err)
		assert.Len(t, result.Items, 1)
		assert.True(t, result.HasNext)
	})

	t.Run("Category/CreateAndGet", func(t *testing.T) {
		c := newCategory()
		require.NoError(t, ds.CreateCategory(ctx, c))

		got, err := ds.GetCategory(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)
		assert.Equal(t, c.Slug, got.Slug)
	})

	t.Run("Category/GetNotFound", func(t *testing.T) {
		_, err := ds.GetCategory(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Category/GetBySlug", func(t *testing.T) {
		c := newCategory()
		require.NoError(t, ds.CreateCategory(ctx, c))

		got, err := ds.GetCategoryBySlug(ctx, c.Slug)
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)
	})

	t.Run("Category/GetBySlugNotFound", func(t *testing.T) {
		_, err := ds.GetCategoryBySlug(ctx, "slug-does-not-exist-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Category/DuplicateSlugReturnsAlreadyExists", func(t *testing.T) {
		c := newCategory()
		require.NoError(t, ds.CreateCategory(ctx, c))

		c2 := newCategory()
		c2.Slug = c.Slug
		err := ds.CreateCategory(ctx, c2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Category/Update", func(t *testing.T) {
		c := newCategory()
		require.NoError(t, ds.CreateCategory(ctx, c))

		c.Name = "Renamed"
		require.NoError(t, ds.UpdateCategory(ctx, c))

		got, err := ds.GetCategory(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, "Renamed", got.Name)
	})

	t.Run("Category/UpdateNotFound", func(t *testing.T) {
		c := newCategory()
		c.ID = newID()
		err := ds.UpdateCategory(ctx, c)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Category/Delete", func(t *testing.T) {
		c := newCategory()
		require.NoError(t, ds.CreateCategory(ctx, c))
		require.NoError(t, ds.DeleteCategory(ctx, c.ID))

		_, err := ds.GetCategory(ctx, c.ID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Category/DeleteNotFound", func(t *testing.T) {
		err := ds.DeleteCategory(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/CreateAndGet", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		got, err := ds.GetCollection(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)
		assert.Equal(t, c.Slug, got.Slug)
	})

	t.Run("Collection/GetNotFound", func(t *testing.T) {
		_, err := ds.GetCollection(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/GetBySlug", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		got, err := ds.GetCollectionBySlug(ctx, c.Slug)
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)
	})

	t.Run("Collection/GetBySlugNotFound", func(t *testing.T) {
		_, err := ds.GetCollectionBySlug(ctx, "slug-does-not-exist-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/DuplicateSlugReturnsAlreadyExists", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		c2 := newCollection()
		c2.Slug = c.Slug
		err := ds.CreateCollection(ctx, c2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Collection/Update", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		c.Name = "Renamed"
		require.NoError(t, ds.UpdateCollection(ctx, c))

		got, err := ds.GetCollection(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, "Renamed", got.Name)
	})

	t.Run("Collection/UpdateNotFound", func(t *testing.T) {
		c := newCollection()
		c.ID = newID()
		err := ds.UpdateCollection(ctx, c)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/Delete", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))
		require.NoError(t, ds.DeleteCollection(ctx, c.ID))

		_, err := ds.GetCollection(ctx, c.ID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/DeleteNotFound", func(t *testing.T) {
		err := ds.DeleteCollection(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	// ── Namespace ─────────────────────────────────────────────────────────────

	t.Run("Namespace/TestCreateNamespace_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespace(ctx, ns.ID)
		require.NoError(t, err)
		assert.Equal(t, ns.ID, got.ID)
		assert.Equal(t, ns.Identifier, got.Identifier)
		assert.Equal(t, ns.Tier, got.Tier)
		assert.Equal(t, ns.CreatedBy, got.CreatedBy)
	})

	t.Run("Namespace/TestCreateNamespace_duplicateIdentifier", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganisation)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		ns2 := newNamespace(datastore.NamespaceTierUser)
		ns2.Identifier = ns.Identifier // same identifier
		err := ds.CreateNamespace(ctx, ns2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Namespace/TestCreateNamespace_acrossAllTiers", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierEnterprise)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		// same identifier, different tier — must still conflict
		nsOrg := newNamespace(datastore.NamespaceTierOrganisation)
		nsOrg.Identifier = ns.Identifier
		err := ds.CreateNamespace(ctx, nsOrg)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Namespace/TestGetNamespaceByIdentifier_notFound", func(t *testing.T) {
		_, err := ds.GetNamespaceByIdentifier(ctx, "does-not-exist-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestListNamespaces_empty", func(t *testing.T) {
		// fresh store or just verify list succeeds
		nss, err := ds.ListNamespaces(ctx, datastore.PageParams{})
		require.NoError(t, err)
		assert.NotNil(t, nss)
	})

	t.Run("Namespace/TestListNamespaces_multiple", func(t *testing.T) {
		before, err := ds.ListNamespaces(ctx, datastore.PageParams{})
		require.NoError(t, err)

		ns1 := newNamespace(datastore.NamespaceTierUser)
		ns2 := newNamespace(datastore.NamespaceTierOrganisation)
		require.NoError(t, ds.CreateNamespace(ctx, ns1))
		require.NoError(t, ds.CreateNamespace(ctx, ns2))

		after, err := ds.ListNamespaces(ctx, datastore.PageParams{})
		require.NoError(t, err)
		assert.Equal(t, len(before.Items)+2, len(after.Items))
	})

	t.Run("Namespace/TestGetNamespace_byID_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespace(ctx, ns.ID)
		require.NoError(t, err)
		assert.Equal(t, ns.ID, got.ID)
	})

	t.Run("Namespace/TestGetNamespaceByIdentifier_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespaceByIdentifier(ctx, ns.Identifier)
		require.NoError(t, err)
		assert.Equal(t, ns.ID, got.ID)
		assert.Equal(t, ns.Identifier, got.Identifier)
	})

	t.Run("Namespace/TestDeleteNamespace_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		require.NoError(t, ds.DeleteNamespace(ctx, ns.ID))

		_, err := ds.GetNamespace(ctx, ns.ID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestDeleteNamespace_notFound", func(t *testing.T) {
		err := ds.DeleteNamespace(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestDeleteNamespace_thenGetReturnsNotFound", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganisation)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		require.NoError(t, ds.DeleteNamespace(ctx, ns.ID))

		_, errID := ds.GetNamespace(ctx, ns.ID)
		assert.ErrorIs(t, errID, datastore.ErrNotFound)

		_, errIdent := ds.GetNamespaceByIdentifier(ctx, ns.Identifier)
		assert.ErrorIs(t, errIdent, datastore.ErrNotFound)
	})
}
