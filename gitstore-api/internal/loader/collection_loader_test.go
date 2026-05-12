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
	collID1 = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	collID2 = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func TestCollectionLoaderLoad(t *testing.T) {
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	_ = store.CreateCollection(ctx, &datastore.Collection{ID: collID1, Name: "Collection 1", Slug: "coll-1", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	_ = store.CreateCollection(ctx, &datastore.Collection{ID: collID2, Name: "Collection 2", Slug: "coll-2", CreatedAt: time.Now(), UpdatedAt: time.Now()})

	loader := NewCollectionLoader(store, zap.NewNop())

	result, err := loader.Load(ctx, collID1)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected collection, got nil")
	}
	if result.ID != collID1 {
		t.Errorf("Expected %s, got %s", collID1, result.ID)
	}

	result, err = loader.Load(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("Expected nil for non-existent collection, got %v", result)
	}
}

func TestCollectionLoaderLoadMany(t *testing.T) {
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	_ = store.CreateCollection(ctx, &datastore.Collection{ID: collID1, Name: "Collection 1", Slug: "coll-1", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	_ = store.CreateCollection(ctx, &datastore.Collection{ID: collID2, Name: "Collection 2", Slug: "coll-2", CreatedAt: time.Now(), UpdatedAt: time.Now()})

	loader := NewCollectionLoader(store, zap.NewNop())

	ids := []string{collID1, collID2, "00000000-0000-0000-0000-000000000000"}
	results, errs := loader.LoadMany(ctx, ids)

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}
	if len(errs) != 3 {
		t.Fatalf("Expected 3 errors, got %d", len(errs))
	}
	if results[0] == nil || results[0].ID != collID1 {
		t.Errorf("Expected collID1 at index 0, got %v", results[0])
	}
	if results[1] == nil || results[1].ID != collID2 {
		t.Errorf("Expected collID2 at index 1, got %v", results[1])
	}
	if results[2] != nil {
		t.Errorf("Expected nil for non-existent collection, got %v", results[2])
	}
}

func TestCollectionLoaderClear(t *testing.T) {
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	loader := NewCollectionLoader(store, zap.NewNop())

	loader.mu.Lock()
	loader.batch = []string{collID1, collID2}
	loader.waiting = make([]chan []*collectionResult, 2)
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
