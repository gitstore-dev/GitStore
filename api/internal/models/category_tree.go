package models

import "sort"

// CategoryTree builds and manages the hierarchical category structure
type CategoryTree struct {
	categories map[string]*Category
	roots      []*Category
}

// NewCategoryTree creates a new category tree builder
func NewCategoryTree() *CategoryTree {
	return &CategoryTree{
		categories: make(map[string]*Category),
		roots:      make([]*Category, 0),
	}
}

// AddCategory adds a category to the tree
func (t *CategoryTree) AddCategory(cat *Category) {
	t.categories[cat.ID] = cat
}

// Build constructs the hierarchical relationships
func (t *CategoryTree) Build() {
	// First pass: identify roots and link children to parents
	for _, cat := range t.categories {
		if cat.IsRoot() {
			t.roots = append(t.roots, cat)
		} else if cat.ParentID != nil {
			parent, exists := t.categories[*cat.ParentID]
			if exists {
				cat.Parent = parent
				parent.Children = append(parent.Children, cat)
			}
		}
	}

	// Sort roots by display order
	sort.Slice(t.roots, func(i, j int) bool {
		return t.roots[i].DisplayOrder < t.roots[j].DisplayOrder
	})

	// Second pass: build paths and depths recursively
	for _, root := range t.roots {
		t.buildPath(root, nil, 0)
	}

	// Sort children by display order for all categories
	for _, cat := range t.categories {
		sort.Slice(cat.Children, func(i, j int) bool {
			return cat.Children[i].DisplayOrder < cat.Children[j].DisplayOrder
		})
	}
}

// buildPath recursively builds the path and depth for each category
func (t *CategoryTree) buildPath(cat *Category, parentPath []*Category, depth int) {
	cat.Path = parentPath
	cat.Depth = depth

	// Build path for children
	childPath := make([]*Category, len(parentPath)+1)
	copy(childPath, parentPath)
	childPath[len(parentPath)] = cat

	for _, child := range cat.Children {
		t.buildPath(child, childPath, depth+1)
	}
}

// GetRoots returns all root categories sorted by display order
func (t *CategoryTree) GetRoots() []*Category {
	return t.roots
}

// GetCategory returns a category by ID
func (t *CategoryTree) GetCategory(id string) (*Category, bool) {
	cat, exists := t.categories[id]
	return cat, exists
}

// GetAll returns all categories
func (t *CategoryTree) GetAll() []*Category {
	cats := make([]*Category, 0, len(t.categories))
	for _, cat := range t.categories {
		cats = append(cats, cat)
	}
	return cats
}

// GetFlatList returns all categories in a flat list sorted by display order
func (t *CategoryTree) GetFlatList() []*Category {
	// Get roots first
	result := make([]*Category, 0, len(t.categories))

	// Add roots and their descendants in depth-first order
	for _, root := range t.roots {
		result = append(result, t.getFlatDescendants(root)...)
	}

	return result
}

// getFlatDescendants returns category and all its descendants in depth-first order
func (t *CategoryTree) getFlatDescendants(cat *Category) []*Category {
	result := []*Category{cat}

	for _, child := range cat.Children {
		result = append(result, t.getFlatDescendants(child)...)
	}

	return result
}
