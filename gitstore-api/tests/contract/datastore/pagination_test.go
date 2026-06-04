// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Pagination contract tests — verify keyset cursor-based pagination works
// correctly for all connection types (products, categories, collections,
// namespaces, repositories).

package datastore_contract_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodeCursor creates an opaque cursor matching the format used by memdb and scylla backends.
func encodeCursor(createdAt time.Time, id string) string {
	payload := fmt.Sprintf("keyset|%s|%s", createdAt.Format(time.RFC3339Nano), id)
	return base64.StdEncoding.EncodeToString([]byte(payload))
}

// RunPaginationSuite exercises keyset pagination across all list operations.
func RunPaginationSuite(t *testing.T, _ datastore.Datastore) {
	t.Helper()

	t.Run("Products/ForwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		products := make([]*datastore.Product, 5)
		for i := range 5 {
			products[i] = newProduct()
			products[i].CreationTimestamp = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateProduct(ctx, products[i]))
		}

		page1, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{First: 2})
		require.NoError(t, err)
		assert.Len(t, page1.Items, 2)
		assert.True(t, page1.HasNext)
		assert.False(t, page1.HasPrevious)

		assert.True(t, page1.Items[0].CreationTimestamp.After(page1.Items[1].CreationTimestamp) ||
			page1.Items[0].CreationTimestamp.Equal(page1.Items[1].CreationTimestamp))

		cursor := encodeCursor(page1.Items[1].CreationTimestamp, page1.Items[1].UID)
		page2, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{First: 2, After: cursor})
		require.NoError(t, err)
		assert.Len(t, page2.Items, 2)
		assert.True(t, page2.HasNext)
		assert.True(t, page2.HasPrevious)

		assert.True(t, page1.Items[1].CreationTimestamp.After(page2.Items[0].CreationTimestamp) ||
			(page1.Items[1].CreationTimestamp.Equal(page2.Items[0].CreationTimestamp) &&
				page1.Items[1].UID > page2.Items[0].UID))

		cursor2 := encodeCursor(page2.Items[1].CreationTimestamp, page2.Items[1].UID)
		page3, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{First: 2, After: cursor2})
		require.NoError(t, err)
		assert.Len(t, page3.Items, 1)
		assert.False(t, page3.HasNext)
		assert.True(t, page3.HasPrevious)
	})

	t.Run("Products/BackwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 5 {
			p := newProduct()
			p.CreationTimestamp = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateProduct(ctx, p))
		}

		result, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{Last: 2})
		require.NoError(t, err)
		assert.Len(t, result.Items, 2)
		assert.False(t, result.HasNext)
		assert.True(t, result.HasPrevious)

		assert.True(t, result.Items[0].CreationTimestamp.After(result.Items[1].CreationTimestamp) ||
			result.Items[0].CreationTimestamp.Equal(result.Items[1].CreationTimestamp))
	})

	t.Run("Products/BackwardWithBefore", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 5 {
			p := newProduct()
			p.CreationTimestamp = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateProduct(ctx, p))
		}

		page1, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{First: 3})
		require.NoError(t, err)
		require.Len(t, page1.Items, 3)

		startCursor := encodeCursor(page1.Items[2].CreationTimestamp, page1.Items[2].UID)
		backward, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{Last: 2, Before: startCursor})
		require.NoError(t, err)
		assert.Len(t, backward.Items, 2)
		assert.True(t, backward.HasNext)
	})

	t.Run("Categories/ForwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 4 {
			c := newCategory()
			c.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateCategory(ctx, c))
		}

		page1, err := ds.ListCategories(ctx, datastore.PageParams{First: 2})
		require.NoError(t, err)
		assert.Len(t, page1.Items, 2)
		assert.True(t, page1.HasNext)
		assert.False(t, page1.HasPrevious)

		cursor := encodeCursor(page1.Items[1].CreatedAt, page1.Items[1].ID)
		page2, err := ds.ListCategories(ctx, datastore.PageParams{First: 2, After: cursor})
		require.NoError(t, err)
		assert.Len(t, page2.Items, 2)
		assert.False(t, page2.HasNext)
		assert.True(t, page2.HasPrevious)
	})

	t.Run("Categories/BackwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 4 {
			c := newCategory()
			c.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateCategory(ctx, c))
		}

		result, err := ds.ListCategories(ctx, datastore.PageParams{Last: 2})
		require.NoError(t, err)
		assert.Len(t, result.Items, 2)
		assert.False(t, result.HasNext)
		assert.True(t, result.HasPrevious)
	})

	t.Run("Collections/ForwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 4 {
			c := newCollection()
			c.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateCollection(ctx, c))
		}

		page1, err := ds.ListCollections(ctx, datastore.PageParams{First: 2})
		require.NoError(t, err)
		assert.Len(t, page1.Items, 2)
		assert.True(t, page1.HasNext)
		assert.False(t, page1.HasPrevious)

		cursor := encodeCursor(page1.Items[1].CreatedAt, page1.Items[1].ID)
		page2, err := ds.ListCollections(ctx, datastore.PageParams{First: 2, After: cursor})
		require.NoError(t, err)
		assert.Len(t, page2.Items, 2)
		assert.False(t, page2.HasNext)
		assert.True(t, page2.HasPrevious)
	})

	t.Run("Collections/BackwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 4 {
			c := newCollection()
			c.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateCollection(ctx, c))
		}

		result, err := ds.ListCollections(ctx, datastore.PageParams{Last: 2})
		require.NoError(t, err)
		assert.Len(t, result.Items, 2)
		assert.False(t, result.HasNext)
		assert.True(t, result.HasPrevious)
	})

	t.Run("Namespaces/ForwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 4 {
			ns := newNamespace(datastore.NamespaceTierUser)
			ns.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateNamespace(ctx, ns))
		}

		page1, err := ds.ListNamespaces(ctx, datastore.PageParams{First: 2})
		require.NoError(t, err)
		assert.Len(t, page1.Items, 2)
		assert.True(t, page1.HasNext)
		assert.False(t, page1.HasPrevious)

		cursor := encodeCursor(page1.Items[1].CreatedAt, page1.Items[1].ID)
		page2, err := ds.ListNamespaces(ctx, datastore.PageParams{First: 2, After: cursor})
		require.NoError(t, err)
		assert.Len(t, page2.Items, 2)
		assert.False(t, page2.HasNext)
		assert.True(t, page2.HasPrevious)
	})

	t.Run("Namespaces/BackwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 4 {
			ns := newNamespace(datastore.NamespaceTierUser)
			ns.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateNamespace(ctx, ns))
		}

		result, err := ds.ListNamespaces(ctx, datastore.PageParams{Last: 2})
		require.NoError(t, err)
		assert.Len(t, result.Items, 2)
		assert.False(t, result.HasNext)
		assert.True(t, result.HasPrevious)
	})

	t.Run("Repositories/ForwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		for i := range 5 {
			r := newRepository(ns.ID)
			r.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateRepository(ctx, r))
		}

		page1, err := ds.ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{First: 2})
		require.NoError(t, err)
		assert.Len(t, page1.Items, 2)
		assert.True(t, page1.HasNext)
		assert.False(t, page1.HasPrevious)

		// Results should be newest first
		assert.True(t, page1.Items[0].CreatedAt.After(page1.Items[1].CreatedAt) ||
			page1.Items[0].CreatedAt.Equal(page1.Items[1].CreatedAt))

		cursor := encodeCursor(page1.Items[1].CreatedAt, page1.Items[1].ID)
		page2, err := ds.ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{First: 2, After: cursor})
		require.NoError(t, err)
		assert.Len(t, page2.Items, 2)
		assert.True(t, page2.HasNext)
		assert.True(t, page2.HasPrevious)

		cursor2 := encodeCursor(page2.Items[1].CreatedAt, page2.Items[1].ID)
		page3, err := ds.ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{First: 2, After: cursor2})
		require.NoError(t, err)
		assert.Len(t, page3.Items, 1)
		assert.False(t, page3.HasNext)
		assert.True(t, page3.HasPrevious)
	})

	t.Run("Repositories/BackwardPagination", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		for i := range 5 {
			r := newRepository(ns.ID)
			r.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateRepository(ctx, r))
		}

		// last:2 without before — oldest 2
		result, err := ds.ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{Last: 2})
		require.NoError(t, err)
		assert.Len(t, result.Items, 2)
		assert.False(t, result.HasNext)
		assert.True(t, result.HasPrevious)
	})

	t.Run("Repositories/BackwardWithBefore", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		repos := make([]*datastore.Repository, 5)
		for i := range 5 {
			repos[i] = newRepository(ns.ID)
			repos[i].CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateRepository(ctx, repos[i]))
		}

		// Get page to find a mid-point cursor
		page1, err := ds.ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{First: 3})
		require.NoError(t, err)
		require.Len(t, page1.Items, 3)

		// Go backward from the 3rd item
		beforeCursor := encodeCursor(page1.Items[2].CreatedAt, page1.Items[2].ID)
		backward, err := ds.ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{Last: 2, Before: beforeCursor})
		require.NoError(t, err)
		assert.Len(t, backward.Items, 2)
		assert.True(t, backward.HasNext) // items exist after the before cursor
	})

	t.Run("Repositories/IsolatedByNamespace", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		ns1 := newNamespace(datastore.NamespaceTierUser)
		ns2 := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns1))
		require.NoError(t, ds.CreateNamespace(ctx, ns2))

		// 3 repos in ns1, 2 in ns2
		for range 3 {
			require.NoError(t, ds.CreateRepository(ctx, newRepository(ns1.ID)))
		}
		for range 2 {
			require.NoError(t, ds.CreateRepository(ctx, newRepository(ns2.ID)))
		}

		r1, err := ds.ListRepositoriesByNamespace(ctx, ns1.ID, datastore.PageParams{})
		require.NoError(t, err)
		assert.Len(t, r1.Items, 3)

		r2, err := ds.ListRepositoriesByNamespace(ctx, ns2.ID, datastore.PageParams{})
		require.NoError(t, err)
		assert.Len(t, r2.Items, 2)
	})

	t.Run("EmptyResult/Products", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		result, err := ds.ListProducts(context.Background(), "test-ns", datastore.PageParams{First: 10})
		require.NoError(t, err)
		assert.Empty(t, result.Items)
		assert.False(t, result.HasNext)
		assert.False(t, result.HasPrevious)
	})

	t.Run("EmptyResult/Repositories", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		result, err := ds.ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{First: 10})
		require.NoError(t, err)
		assert.Empty(t, result.Items)
		assert.False(t, result.HasNext)
		assert.False(t, result.HasPrevious)
	})

	t.Run("TotalCount", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for range 5 {
			require.NoError(t, ds.CreateProduct(ctx, newProduct()))
		}

		result, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{First: 2})
		require.NoError(t, err)
		assert.Len(t, result.Items, 2)
		// memdb returns exact count; scylla may return -1
		if result.TotalCount >= 0 {
			assert.Equal(t, int32(5), result.TotalCount)
		}
	})

	t.Run("Ordering/NewestFirst", func(t *testing.T) {
		ds := newMemdbDatastore(t)
		ctx := context.Background()

		for i := range 5 {
			p := newProduct()
			p.CreationTimestamp = time.Now().Add(time.Duration(i) * time.Second)
			require.NoError(t, ds.CreateProduct(ctx, p))
		}

		result, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{})
		require.NoError(t, err)
		require.Len(t, result.Items, 5)

		for i := 0; i < len(result.Items)-1; i++ {
			assert.True(t,
				result.Items[i].CreationTimestamp.After(result.Items[i+1].CreationTimestamp) ||
					result.Items[i].CreationTimestamp.Equal(result.Items[i+1].CreationTimestamp),
				"items[%d].CreationTimestamp (%v) should be >= items[%d].CreationTimestamp (%v)",
				i, result.Items[i].CreationTimestamp, i+1, result.Items[i+1].CreationTimestamp,
			)
		}
	})
}

func newRepository(namespaceID string) *datastore.Repository {
	now := time.Now()
	return &datastore.Repository{
		ID:            newID(),
		NamespaceID:   namespaceID,
		Name:          "repo-" + newID()[:8],
		DefaultBranch: "main",
		StorageClass:  "local",
		CreatedAt:     now,
		CreatedBy:     "test-user",
		UpdatedAt:     now,
		UpdatedBy:     "test-user",
	}
}

func TestPaginationMemdb(t *testing.T) {
	RunPaginationSuite(t, newMemdbDatastore(t))
}
