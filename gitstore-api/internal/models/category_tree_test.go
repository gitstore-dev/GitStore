package models

import (
	"testing"
	"time"
)

func TestCategoryTreeBuild(t *testing.T) {
	tree := NewCategoryTree()

	// Create hierarchy: Electronics -> Computers -> Laptops
	electronics := &Category{
		ID:           "cat_electronics",
		Name:         "Electronics",
		Slug:         "electronics",
		DisplayOrder: 1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	computers := &Category{
		ID:           "cat_computers",
		Name:         "Computers",
		Slug:         "computers",
		ParentID:     stringPtr2("cat_electronics"),
		DisplayOrder: 1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	laptops := &Category{
		ID:           "cat_laptops",
		Name:         "Laptops",
		Slug:         "laptops",
		ParentID:     stringPtr2("cat_computers"),
		DisplayOrder: 1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	tree.AddCategory(electronics)
	tree.AddCategory(computers)
	tree.AddCategory(laptops)

	tree.Build()

	// Check roots
	roots := tree.GetRoots()
	if len(roots) != 1 {
		t.Fatalf("Expected 1 root, got %d", len(roots))
	}

	if roots[0].ID != "cat_electronics" {
		t.Errorf("Expected root to be electronics, got %s", roots[0].ID)
	}

	// Check hierarchy
	if len(electronics.Children) != 1 {
		t.Fatalf("Expected electronics to have 1 child, got %d", len(electronics.Children))
	}

	if electronics.Children[0].ID != "cat_computers" {
		t.Errorf("Expected child to be computers, got %s", electronics.Children[0].ID)
	}

	if len(computers.Children) != 1 {
		t.Fatalf("Expected computers to have 1 child, got %d", len(computers.Children))
	}

	if computers.Children[0].ID != "cat_laptops" {
		t.Errorf("Expected child to be laptops, got %s", computers.Children[0].ID)
	}

	// Check parent references
	if computers.Parent == nil || computers.Parent.ID != "cat_electronics" {
		t.Error("Expected computers parent to be electronics")
	}

	if laptops.Parent == nil || laptops.Parent.ID != "cat_computers" {
		t.Error("Expected laptops parent to be computers")
	}

	// Check depths
	if electronics.Depth != 0 {
		t.Errorf("Expected electronics depth 0, got %d", electronics.Depth)
	}

	if computers.Depth != 1 {
		t.Errorf("Expected computers depth 1, got %d", computers.Depth)
	}

	if laptops.Depth != 2 {
		t.Errorf("Expected laptops depth 2, got %d", laptops.Depth)
	}

	// Check paths
	if len(laptops.Path) != 2 {
		t.Fatalf("Expected laptops path length 2, got %d", len(laptops.Path))
	}

	if laptops.Path[0].ID != "cat_electronics" {
		t.Errorf("Expected first path element to be electronics, got %s", laptops.Path[0].ID)
	}

	if laptops.Path[1].ID != "cat_computers" {
		t.Errorf("Expected second path element to be computers, got %s", laptops.Path[1].ID)
	}
}

func TestCategoryTreeGetFlatList(t *testing.T) {
	tree := NewCategoryTree()

	root1 := &Category{ID: "cat_1", Name: "Root 1", DisplayOrder: 1}
	child1 := &Category{ID: "cat_2", Name: "Child 1", ParentID: stringPtr2("cat_1"), DisplayOrder: 1}
	root2 := &Category{ID: "cat_3", Name: "Root 2", DisplayOrder: 2}

	tree.AddCategory(root1)
	tree.AddCategory(child1)
	tree.AddCategory(root2)

	tree.Build()

	flatList := tree.GetFlatList()

	if len(flatList) != 3 {
		t.Fatalf("Expected 3 categories, got %d", len(flatList))
	}

	// Should be in depth-first order: root1, child1, root2
	if flatList[0].ID != "cat_1" {
		t.Errorf("Expected first to be cat_1, got %s", flatList[0].ID)
	}

	if flatList[1].ID != "cat_2" {
		t.Errorf("Expected second to be cat_2, got %s", flatList[1].ID)
	}

	if flatList[2].ID != "cat_3" {
		t.Errorf("Expected third to be cat_3, got %s", flatList[2].ID)
	}
}

func TestCategoryTreeOrphanedCategories(t *testing.T) {
	tree := NewCategoryTree()

	root := &Category{ID: "cat_1", Name: "Root"}
	orphan := &Category{ID: "cat_2", Name: "Orphan", ParentID: stringPtr2("cat_nonexistent")}

	tree.AddCategory(root)
	tree.AddCategory(orphan)

	tree.Build()

	// Orphan should not be in roots (has parent_id)
	roots := tree.GetRoots()
	if len(roots) != 1 {
		t.Fatalf("Expected 1 root, got %d", len(roots))
	}

	// But should still be accessible
	cat, ok := tree.GetCategory("cat_2")
	if !ok {
		t.Error("Expected orphan to be in tree")
	}

	// Parent should be nil (parent not found)
	if cat.Parent != nil {
		t.Error("Expected orphan parent to be nil")
	}
}

func stringPtr2(s string) *string {
	return &s
}
