package loader

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

func TestCollectionLoaderLoad(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")

	coll1 := &catalog.Collection{
		ID:         "coll_1",
		Name:       "Collection 1",
		Slug:       "coll-1",
		ProductIDs: []string{"prod_1"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	coll2 := &catalog.Collection{
		ID:         "coll_2",
		Name:       "Collection 2",
		Slug:       "coll-2",
		ProductIDs: []string{"prod_2"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	cat.AddCollection(coll1)
	cat.AddCollection(coll2)

	logger := zap.NewNop()
	loader := NewCollectionLoader(cat, logger)

	ctx := context.Background()

	// Load single collection
	result, err := loader.Load(ctx, "coll_1")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("Expected collection, got nil")
	}

	if result.ID != "coll_1" {
		t.Errorf("Expected coll_1, got %s", result.ID)
	}

	// Load non-existent collection
	result, err = loader.Load(ctx, "coll_nonexistent")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result != nil {
		t.Errorf("Expected nil for non-existent collection, got %v", result)
	}
}

func TestCollectionLoaderLoadMany(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")

	coll1 := &catalog.Collection{
		ID:         "coll_1",
		Name:       "Collection 1",
		Slug:       "coll-1",
		ProductIDs: []string{"prod_1"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	coll2 := &catalog.Collection{
		ID:         "coll_2",
		Name:       "Collection 2",
		Slug:       "coll-2",
		ProductIDs: []string{"prod_2"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	cat.AddCollection(coll1)
	cat.AddCollection(coll2)

	logger := zap.NewNop()
	loader := NewCollectionLoader(cat, logger)

	ctx := context.Background()

	// Load multiple collections
	ids := []string{"coll_1", "coll_2", "coll_nonexistent"}
	results, errs := loader.LoadMany(ctx, ids)

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	if len(errs) != 3 {
		t.Fatalf("Expected 3 errors, got %d", len(errs))
	}

	// Check first result
	if results[0] == nil {
		t.Error("Expected coll_1, got nil")
	} else if results[0].ID != "coll_1" {
		t.Errorf("Expected coll_1, got %s", results[0].ID)
	}

	// Check second result
	if results[1] == nil {
		t.Error("Expected coll_2, got nil")
	} else if results[1].ID != "coll_2" {
		t.Errorf("Expected coll_2, got %s", results[1].ID)
	}

	// Check third result (non-existent)
	if results[2] != nil {
		t.Errorf("Expected nil for non-existent collection, got %v", results[2])
	}
}

func TestCollectionLoaderClear(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")
	logger := zap.NewNop()
	loader := NewCollectionLoader(cat, logger)

	// Add some state
	loader.mu.Lock()
	loader.batch = []string{"coll_1", "coll_2"}
	loader.waiting = make([]chan []*collectionResult, 2)
	loader.mu.Unlock()

	// Clear
	loader.Clear()

	// Verify cleared
	loader.mu.Lock()
	defer loader.mu.Unlock()

	if len(loader.batch) != 0 {
		t.Errorf("Expected empty batch after clear, got %d items", len(loader.batch))
	}

	if len(loader.waiting) != 0 {
		t.Errorf("Expected empty waiting list after clear, got %d items", len(loader.waiting))
	}
}
