// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package loader

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"go.uber.org/zap"
)

const (
	catID1 = "11111111-1111-1111-1111-111111111111"
	catID2 = "22222222-2222-2222-2222-222222222222"
)

func TestCategoryLoaderLoad(t *testing.T) {
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	_ = store.CreateCategory(ctx, &datastore.Category{ID: catID1, Name: "Category 1", Slug: "cat-1", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	_ = store.CreateCategory(ctx, &datastore.Category{ID: catID2, Name: "Category 2", Slug: "cat-2", CreatedAt: time.Now(), UpdatedAt: time.Now()})

	loader := NewCategoryLoader(store, zap.NewNop())

	result, err := loader.Load(ctx, catID1)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected category, got nil")
	}
	if result.ID != catID1 {
		t.Errorf("Expected %s, got %s", catID1, result.ID)
	}

	result, err = loader.Load(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("Expected nil for non-existent category, got %v", result)
	}
}

func TestCategoryLoaderLoadMany(t *testing.T) {
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	_ = store.CreateCategory(ctx, &datastore.Category{ID: catID1, Name: "Category 1", Slug: "cat-1", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	_ = store.CreateCategory(ctx, &datastore.Category{ID: catID2, Name: "Category 2", Slug: "cat-2", CreatedAt: time.Now(), UpdatedAt: time.Now()})

	loader := NewCategoryLoader(store, zap.NewNop())

	ids := []string{catID1, catID2, "00000000-0000-0000-0000-000000000000"}
	results, errs := loader.LoadMany(ctx, ids)

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}
	if len(errs) != 3 {
		t.Fatalf("Expected 3 errors, got %d", len(errs))
	}
	if results[0] == nil || results[0].ID != catID1 {
		t.Errorf("Expected catID1 at index 0, got %v", results[0])
	}
	if results[1] == nil || results[1].ID != catID2 {
		t.Errorf("Expected catID2 at index 1, got %v", results[1])
	}
	if results[2] != nil {
		t.Errorf("Expected nil for non-existent category, got %v", results[2])
	}
}

func TestCategoryLoaderClear(t *testing.T) {
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	loader := NewCategoryLoader(store, zap.NewNop())

	loader.mu.Lock()
	loader.batch = []string{catID1, catID2}
	loader.waiting = make([]chan []*categoryResult, 2)
	loader.mu.Unlock()

	loader.Clear()

	loader.mu.Lock()
	defer loader.mu.Unlock()

	if len(loader.batch) != 0 {
		t.Errorf("Expected empty batch after clear, got %d items", len(loader.batch))
	}
	if len(loader.waiting) != 0 {
		t.Errorf("Expected empty waiting list after clear, got %d items", len(loader.waiting))
	}
}
