// Category hierarchy builder

package catalog

// BuildCategoryHierarchy builds parent-child relationships and computes path/depth
// for all categories in the catalog
func (c *Catalog) BuildCategoryHierarchy() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// First pass: Build ID -> Category map for quick lookups
	categoryMap := make(map[string]*Category)
	for _, cat := range c.categories {
		categoryMap[cat.ID] = cat
	}

	// Second pass: Set parent pointers and collect children
	for _, cat := range c.categories {
		if cat.ParentID != nil {
			if parent, ok := categoryMap[*cat.ParentID]; ok {
				if parent.Children == nil {
					parent.Children = []*Category{}
				}
				parent.Children = append(parent.Children, cat)
				cat.Parent = parent
			}
		}
	}

	// Third pass: Compute depth and path for each category
	for _, cat := range c.categories {
		computeCategoryPathAndDepth(cat)
	}
}

// computeCategoryPathAndDepth recursively computes the path and depth for a category
func computeCategoryPathAndDepth(cat *Category) {
	if cat.Path != nil {
		// Already computed
		return
	}

	if cat.Parent == nil {
		// Root category
		cat.Depth = 0
		cat.Path = []*Category{cat}
		return
	}

	// Ensure parent's path is computed first
	computeCategoryPathAndDepth(cat.Parent)

	// Build path from parent's path + this category
	cat.Path = make([]*Category, len(cat.Parent.Path)+1)
	copy(cat.Path, cat.Parent.Path)
	cat.Path[len(cat.Parent.Path)] = cat

	// Depth is parent's depth + 1
	cat.Depth = cat.Parent.Depth + 1
}
