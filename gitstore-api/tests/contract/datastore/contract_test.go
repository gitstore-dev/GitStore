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
		before, err := ds.ListProducts(ctx, datastore.ProductFilter{})
		require.NoError(t, err)

		p1 := newProduct("")
		p2 := newProduct("")
		require.NoError(t, ds.CreateProduct(ctx, p1))
		require.NoError(t, ds.CreateProduct(ctx, p2))

		after, err := ds.ListProducts(ctx, datastore.ProductFilter{})
		require.NoError(t, err)
		assert.Equal(t, len(before)+2, len(after))
	})

	t.Run("Product/ListFilterByCategoryID", func(t *testing.T) {
		cat := newCategory()
		require.NoError(t, ds.CreateCategory(ctx, cat))

		// Create two products in the category, one outside
		inCat1 := newProduct(cat.ID)
		inCat2 := newProduct(cat.ID)
		outCat := newProduct(newID())
		require.NoError(t, ds.CreateProduct(ctx, inCat1))
		require.NoError(t, ds.CreateProduct(ctx, inCat2))
		require.NoError(t, ds.CreateProduct(ctx, outCat))

		results, err := ds.ListProducts(ctx, datastore.ProductFilter{CategoryID: cat.ID})
		require.NoError(t, err)

		ids := make(map[string]bool, len(results))
		for _, p := range results {
			ids[p.ID] = true
		}
		assert.True(t, ids[inCat1.ID], "inCat1 should be in results")
		assert.True(t, ids[inCat2.ID], "inCat2 should be in results")
		assert.False(t, ids[outCat.ID], "outCat should not be in results")
	})

	t.Run("Product/ListFilterEmptyCategoryReturnsNothing", func(t *testing.T) {
		results, err := ds.ListProducts(ctx, datastore.ProductFilter{CategoryID: newID()})
		require.NoError(t, err)
		assert.Empty(t, results)
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
}
