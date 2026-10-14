// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestCategoryProductIndex_ReplacesAncestorMembership(t *testing.T) {
	ds, err := New()
	if err != nil {
		t.Fatal(err)
	}
	index := ds.(datastore.CategoryProductIndex)
	p := &datastore.Product{UID: "00000000-0000-0000-0000-000000000001", Namespace: "shop", Name: "p", CreationTimestamp: time.Now()}
	if err := ds.CreateProduct(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := index.ReplaceCategoryProductMembership(context.Background(), p, []string{"root", "leaf"}); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{"root", "leaf"} {
		page, err := index.ListCategoryProducts(context.Background(), "shop", category, datastore.PageParams{First: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].UID != p.UID {
			t.Fatalf("%s membership = %#v, %v", category, page, err)
		}
	}
	if err := index.ReplaceCategoryProductMembership(context.Background(), p, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	page, err := index.ListCategoryProducts(context.Background(), "shop", "root", datastore.PageParams{First: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("stale root membership = %#v, %v", page, err)
	}
}

func TestCategoryProductIndex_StaleWritersCannotRestoreMembership(t *testing.T) {
	ds, err := New()
	require.NoError(t, err)
	index := ds.(datastore.CategoryProductIndex)
	ctx := t.Context()
	product := &datastore.Product{
		UID: "00000000-0000-0000-0000-000000000001", Namespace: "shop",
		Name: "p", CreationTimestamp: time.Now(), ResourceVersion: "1",
	}
	require.NoError(t, ds.CreateProduct(ctx, product))
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{"old"}))
	current := *product
	datastore.AdvanceProductSystemVersion(&current)
	require.NoError(t, ds.UpdateProduct(ctx, &current, product.ResourceVersion))
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, &current, []string{"current"}))
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, product, []string{"old"}))
	old, err := index.ListCategoryProducts(ctx, "shop", "old", datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Empty(t, old.Items, "an obsolete writer must not restore the old category")
	page, err := index.ListCategoryProducts(ctx, "shop", "current", datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "an obsolete writer must not remove current membership")

	require.NoError(t, ds.DeleteProduct(ctx, product.UID))
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, &current, []string{"current"}))
	store := ds.(*memdbDatastore)
	store.categoryProductsMu.RLock()
	defer store.categoryProductsMu.RUnlock()
	require.Empty(t, store.productCategories[product.UID], "a deleted UID cannot regain projection entries")
	require.Empty(t, store.categoryProducts[categoryProductKey("shop", "current")])
}
