package loader

import (
	"context"
	"testing"
	"time"

	"github.com/yourorg/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

func TestCategoryLoaderLoad(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")

	cat1 := &catalog.Category{
		ID:        "cat_1",
		Name:      "Category 1",
		Slug:      "cat-1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	cat2 := &catalog.Category{
		ID:        "cat_2",
		Name:      "Category 2",
		Slug:      "cat-2",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	cat.AddCategory(cat1)
	cat.AddCategory(cat2)

	logger := zap.NewNop()
	loader := NewCategoryLoader(cat, logger)

	ctx := context.Background()

	// Load single category
	result, err := loader.Load(ctx, "cat_1")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("Expected category, got nil")
	}

	if result.ID != "cat_1" {
		t.Errorf("Expected cat_1, got %s", result.ID)
	}

	// Load non-existent category
	result, err = loader.Load(ctx, "cat_nonexistent")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result != nil {
		t.Errorf("Expected nil for non-existent category, got %v", result)
	}
}

func TestCategoryLoaderLoadMany(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")

	cat1 := &catalog.Category{
		ID:        "cat_1",
		Name:      "Category 1",
		Slug:      "cat-1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	cat2 := &catalog.Category{
		ID:        "cat_2",
		Name:      "Category 2",
		Slug:      "cat-2",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	cat.AddCategory(cat1)
	cat.AddCategory(cat2)

	logger := zap.NewNop()
	loader := NewCategoryLoader(cat, logger)

	ctx := context.Background()

	// Load multiple categories
	ids := []string{"cat_1", "cat_2", "cat_nonexistent"}
	results, errs := loader.LoadMany(ctx, ids)

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	if len(errs) != 3 {
		t.Fatalf("Expected 3 errors, got %d", len(errs))
	}

	// Check first result
	if results[0] == nil {
		t.Error("Expected cat_1, got nil")
	} else if results[0].ID != "cat_1" {
		t.Errorf("Expected cat_1, got %s", results[0].ID)
	}

	// Check second result
	if results[1] == nil {
		t.Error("Expected cat_2, got nil")
	} else if results[1].ID != "cat_2" {
		t.Errorf("Expected cat_2, got %s", results[1].ID)
	}

	// Check third result (non-existent)
	if results[2] != nil {
		t.Errorf("Expected nil for non-existent category, got %v", results[2])
	}
}

func TestCategoryLoaderClear(t *testing.T) {
	cat := catalog.NewCatalog("test-commit")
	logger := zap.NewNop()
	loader := NewCategoryLoader(cat, logger)

	// Add some state
	loader.mu.Lock()
	loader.batch = []string{"cat_1", "cat_2"}
	loader.waiting = make([]chan []*categoryResult, 2)
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
