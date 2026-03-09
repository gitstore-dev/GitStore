package models

import (
	"testing"
)

func TestCategoryIsRoot(t *testing.T) {
	root := &Category{ID: "cat_1", ParentID: nil}
	child := &Category{ID: "cat_2", ParentID: stringPtr("cat_1")}

	if !root.IsRoot() {
		t.Error("Expected root category to return true for IsRoot()")
	}

	if child.IsRoot() {
		t.Error("Expected child category to return false for IsRoot()")
	}
}

func TestCategoryHasChildren(t *testing.T) {
	parent := &Category{
		ID:       "cat_1",
		Children: []*Category{{ID: "cat_2"}},
	}

	noChildren := &Category{
		ID:       "cat_3",
		Children: []*Category{},
	}

	if !parent.HasChildren() {
		t.Error("Expected category with children to return true")
	}

	if noChildren.HasChildren() {
		t.Error("Expected category without children to return false")
	}
}

func TestCategoryGetAncestorIDs(t *testing.T) {
	root := &Category{ID: "cat_1"}
	child1 := &Category{ID: "cat_2"}
	child2 := &Category{ID: "cat_3", Path: []*Category{root, child1}}

	ids := child2.GetAncestorIDs()

	if len(ids) != 2 {
		t.Fatalf("Expected 2 ancestors, got %d", len(ids))
	}

	if ids[0] != "cat_1" || ids[1] != "cat_2" {
		t.Errorf("Expected [cat_1, cat_2], got %v", ids)
	}
}

func TestCategoryGetDescendantIDs(t *testing.T) {
	grandchild := &Category{ID: "cat_3", Children: []*Category{}}
	child := &Category{ID: "cat_2", Children: []*Category{grandchild}}
	root := &Category{ID: "cat_1", Children: []*Category{child}}

	ids := root.GetDescendantIDs()

	if len(ids) != 2 {
		t.Fatalf("Expected 2 descendants, got %d", len(ids))
	}

	if ids[0] != "cat_2" || ids[1] != "cat_3" {
		t.Errorf("Expected [cat_2, cat_3], got %v", ids)
	}
}

func stringPtr(s string) *string {
	return &s
}
