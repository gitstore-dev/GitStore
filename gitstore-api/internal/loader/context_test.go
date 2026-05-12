// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package loader

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"go.uber.org/zap"
)

func newTestStore(t *testing.T) datastore.Datastore {
	t.Helper()
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("Failed to create memdb store: %v", err)
	}
	return store
}

func TestLoadersCreation(t *testing.T) {
	store := newTestStore(t)
	logger := zap.NewNop()

	loaders := NewLoaders(store, logger)

	if loaders == nil {
		t.Fatal("Expected loaders, got nil")
	}

	if loaders.Product == nil {
		t.Error("Expected product loader, got nil")
	}

	if loaders.Category == nil {
		t.Error("Expected category loader, got nil")
	}

	if loaders.Collection == nil {
		t.Error("Expected collection loader, got nil")
	}
}

func TestLoadersFromContext(t *testing.T) {
	store := newTestStore(t)
	logger := zap.NewNop()

	middleware := Middleware(store, logger)
	ctx := middleware(context.Background())

	loaders := FromContext(ctx)

	if loaders == nil {
		t.Fatal("Expected loaders from context, got nil")
	}

	if loaders.Product == nil {
		t.Error("Expected product loader, got nil")
	}

	if loaders.Category == nil {
		t.Error("Expected category loader, got nil")
	}

	if loaders.Collection == nil {
		t.Error("Expected collection loader, got nil")
	}
}

func TestLoadersFromContextMissing(t *testing.T) {
	ctx := context.Background()

	loaders := FromContext(ctx)

	if loaders != nil {
		t.Errorf("Expected nil loaders from empty context, got %v", loaders)
	}
}

func TestLoadersClear(t *testing.T) {
	store := newTestStore(t)
	logger := zap.NewNop()

	loaders := NewLoaders(store, logger)

	loaders.Product.mu.Lock()
	loaders.Product.batch = []string{"prod_1"}
	loaders.Product.mu.Unlock()

	loaders.Category.mu.Lock()
	loaders.Category.batch = []string{"cat_1"}
	loaders.Category.mu.Unlock()

	loaders.Collection.mu.Lock()
	loaders.Collection.batch = []string{"coll_1"}
	loaders.Collection.mu.Unlock()

	loaders.Clear()

	loaders.Product.mu.Lock()
	productBatchLen := len(loaders.Product.batch)
	loaders.Product.mu.Unlock()

	loaders.Category.mu.Lock()
	categoryBatchLen := len(loaders.Category.batch)
	loaders.Category.mu.Unlock()

	loaders.Collection.mu.Lock()
	collectionBatchLen := len(loaders.Collection.batch)
	loaders.Collection.mu.Unlock()

	if productBatchLen != 0 {
		t.Errorf("Expected empty product batch, got %d items", productBatchLen)
	}

	if categoryBatchLen != 0 {
		t.Errorf("Expected empty category batch, got %d items", categoryBatchLen)
	}

	if collectionBatchLen != 0 {
		t.Errorf("Expected empty collection batch, got %d items", collectionBatchLen)
	}
}
