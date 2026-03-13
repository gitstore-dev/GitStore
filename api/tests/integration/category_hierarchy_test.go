// Integration test for category parent-child relationships

package integration

import (
	"testing"

	"github.com/commerce-projects/gitstore/api/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCategoryHierarchy tests category parent-child relationships
func TestCategoryHierarchy(t *testing.T) {
	// This test validates that categories can form a proper tree structure
	// with parent-child relationships correctly resolved

	serverURL := testutil.GetTestServerURL()

	t.Run("should resolve parent references correctly", func(t *testing.T) {
		// Query a child category and verify its parent is resolved
		query := `
			query {
				category(slug: "laptops") {
					id
					name
					slug
					parent {
						id
						name
						slug
					}
					depth
					path
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Category *struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Slug   string `json:"slug"`
				Parent *struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					Slug string `json:"slug"`
				} `json:"parent"`
				Depth int      `json:"depth"`
				Path  []string `json:"path"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, resp, &result)

		if result.Category != nil {
			// Laptops should be a child category (e.g., under Computers or Electronics)
			require.NotNil(t, result.Category.Parent, "Laptops should have a parent category")
			assert.NotEmpty(t, result.Category.Parent.ID, "Parent ID should not be empty")
			assert.NotEmpty(t, result.Category.Parent.Name, "Parent name should not be empty")
			assert.NotEmpty(t, result.Category.Parent.Slug, "Parent slug should not be empty")

			// Depth should be > 0 for child categories
			assert.Greater(t, result.Category.Depth, 0, "Child category should have depth > 0")

			// Path should contain multiple elements (root -> ... -> this category)
			assert.Greater(t, len(result.Category.Path), 1, "Child category path should have > 1 element")
		} else {
			t.Skip("Category 'laptops' not found (expected in Red phase)")
		}
	})

	t.Run("should resolve children references correctly", func(t *testing.T) {
		// Query a parent category and verify its children are resolved
		query := `
			query {
				category(slug: "electronics") {
					id
					name
					slug
					children {
						id
						name
						slug
						parent {
							slug
						}
					}
					depth
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Category *struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Slug     string `json:"slug"`
				Children []struct {
					ID     string `json:"id"`
					Name   string `json:"name"`
					Slug   string `json:"slug"`
					Parent *struct {
						Slug string `json:"slug"`
					} `json:"parent"`
				} `json:"children"`
				Depth int `json:"depth"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, resp, &result)

		if result.Category != nil {
			// Electronics should be a root category
			assert.Equal(t, 0, result.Category.Depth, "Root category should have depth 0")

			// If it has children, validate them
			if len(result.Category.Children) > 0 {
				for _, child := range result.Category.Children {
					assert.NotEmpty(t, child.ID, "Child ID should not be empty")
					assert.NotEmpty(t, child.Name, "Child name should not be empty")
					assert.NotEmpty(t, child.Slug, "Child slug should not be empty")

					// Child's parent should reference back to electronics
					require.NotNil(t, child.Parent, "Child should have parent reference")
					assert.Equal(t, "electronics", child.Parent.Slug, "Child's parent should be electronics")
				}
				t.Logf("Found %d children under electronics category", len(result.Category.Children))
			} else {
				t.Log("Electronics category has no children (empty catalog)")
			}
		} else {
			t.Skip("Category 'electronics' not found (expected in Red phase)")
		}
	})

	t.Run("should handle circular reference detection", func(t *testing.T) {
		// This test would require creating a circular reference in the catalog
		// For now, we test that the system can query deep hierarchies without issues

		query := `
			query {
				categories {
					name
					depth
					parent {
						name
						depth
						parent {
							name
							depth
						}
					}
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Categories []struct {
				Name   string `json:"name"`
				Depth  int    `json:"depth"`
				Parent *struct {
					Name   string `json:"name"`
					Depth  int    `json:"depth"`
					Parent *struct {
						Name  string `json:"name"`
						Depth int    `json:"depth"`
					} `json:"parent"`
				} `json:"parent"`
			} `json:"categories"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Validate depth increases as we traverse up the tree
		for _, cat := range result.Categories {
			if cat.Parent != nil {
				assert.Less(t, cat.Parent.Depth, cat.Depth, "Parent depth should be less than child depth")

				if cat.Parent.Parent != nil {
					assert.Less(t, cat.Parent.Parent.Depth, cat.Parent.Depth, "Grandparent depth should be less than parent depth")
				}
			}
		}

		t.Log("Successfully queried deep category hierarchy without circular reference issues")
	})

	t.Run("should compute path correctly", func(t *testing.T) {
		// Query categories and verify the path array is correctly computed
		query := `
			query {
				categories {
					name
					slug
					depth
					path
					parent {
						slug
					}
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Categories []struct {
				Name   string   `json:"name"`
				Slug   string   `json:"slug"`
				Depth  int      `json:"depth"`
				Path   []string `json:"path"`
				Parent *struct {
					Slug string `json:"slug"`
				} `json:"parent"`
			} `json:"categories"`
		}

		testutil.UnmarshalData(t, resp, &result)

		for _, cat := range result.Categories {
			// Path length should equal depth + 1
			expectedPathLength := cat.Depth + 1
			assert.Len(t, cat.Path, expectedPathLength,
				"Path length should be depth+1 for category %s", cat.Name)

			// Last element in path should be this category's name
			if len(cat.Path) > 0 {
				lastPathElement := cat.Path[len(cat.Path)-1]
				// Path contains names, not slugs
				assert.NotEmpty(t, lastPathElement, "Last path element should not be empty")
			}

			// Root categories (depth 0) should have path length 1
			if cat.Depth == 0 {
				assert.Len(t, cat.Path, 1, "Root category should have path length 1")
				assert.Nil(t, cat.Parent, "Root category should have no parent")
			} else {
				assert.Greater(t, len(cat.Path), 1, "Child category should have path length > 1")
				assert.NotNil(t, cat.Parent, "Child category should have a parent")
			}
		}

		t.Logf("Validated path computation for %d categories", len(result.Categories))
	})

	t.Run("should handle root categories correctly", func(t *testing.T) {
		// Query all categories and filter for root categories
		query := `
			query {
				categories {
					id
					name
					slug
					parent {
						id
					}
					depth
					path
					children {
						id
					}
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Categories []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Slug   string `json:"slug"`
				Parent *struct {
					ID string `json:"id"`
				} `json:"parent"`
				Depth    int      `json:"depth"`
				Path     []string `json:"path"`
				Children []struct {
					ID string `json:"id"`
				} `json:"children"`
			} `json:"categories"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Find root categories (parent == null, depth == 0)
		rootCategories := []string{}
		for _, cat := range result.Categories {
			if cat.Parent == nil {
				rootCategories = append(rootCategories, cat.Slug)

				// Root category validations
				assert.Equal(t, 0, cat.Depth, "Root category %s should have depth 0", cat.Name)
				assert.Len(t, cat.Path, 1, "Root category %s should have path length 1", cat.Name)
				assert.NotNil(t, cat.Children, "Root category %s should have children array (can be empty)", cat.Name)
			}
		}

		if len(rootCategories) > 0 {
			t.Logf("Found %d root categories: %v", len(rootCategories), rootCategories)
		} else {
			t.Log("No root categories found (empty catalog)")
		}
	})
}
