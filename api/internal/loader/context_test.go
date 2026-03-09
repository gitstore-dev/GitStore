package loader

import (
	"context"
	"testing"

	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

func TestLoadersCreation(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")
	logger := zap.NewNop()

	loaders := NewLoaders(cat, logger)

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
	cat := catalog.NewCatalog("test-commit")
	logger := zap.NewNop()

	// Create context with loaders
	middleware := Middleware(cat, logger)
	ctx := middleware(context.Background())

	// Retrieve loaders
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

	// Try to retrieve loaders from empty context
	loaders := FromContext(ctx)

	if loaders != nil {
		t.Errorf("Expected nil loaders from empty context, got %v", loaders)
	}
}

func TestLoadersClear(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")
	logger := zap.NewNop()

	loaders := NewLoaders(cat, logger)

	// Add some state to loaders
	loaders.Product.mu.Lock()
	loaders.Product.batch = []string{"prod_1"}
	loaders.Product.mu.Unlock()

	loaders.Category.mu.Lock()
	loaders.Category.batch = []string{"cat_1"}
	loaders.Category.mu.Unlock()

	loaders.Collection.mu.Lock()
	loaders.Collection.batch = []string{"coll_1"}
	loaders.Collection.mu.Unlock()

	// Clear all loaders
	loaders.Clear()

	// Verify all cleared
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
