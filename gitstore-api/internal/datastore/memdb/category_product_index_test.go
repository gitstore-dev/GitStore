// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
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
