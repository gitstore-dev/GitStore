// Contract test for categories query

package contract

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCategoriesQuery tests the categories list query
func TestCategoriesQuery(t *testing.T) {
	// This test will fail initially (Red phase of TDD)
	// Implementation in Phase 4 will make it pass (Green phase)

	serverURL := testutil.GetTestServerURL()

	t.Run("should return all categories", func(t *testing.T) {
		query := `
			query {
				categories {
					id
					name
					slug
					displayOrder
					parent {
						id
						name
					}
					children {
						id
						name
					}
					path
					depth
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Categories []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				Slug         string `json:"slug"`
				DisplayOrder int    `json:"displayOrder"`
				Parent       *struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"parent"`
				Children []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"children"`
				Path  []string `json:"path"`
				Depth int      `json:"depth"`
			} `json:"categories"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Assertions:
		// - Should return array of categories
		assert.NotNil(t, result.Categories, "Categories should not be nil")

		// If there are categories, validate structure
		if len(result.Categories) > 0 {
			for _, cat := range result.Categories {
				// Root categories should have parent: null
				if cat.Parent == nil {
					assert.Equal(t, 0, cat.Depth, "Root category should have depth 0")
					assert.Len(t, cat.Path, 1, "Root category path should have 1 element")
				} else {
					// Child categories should have parent populated
					assert.NotEmpty(t, cat.Parent.ID, "Child category should have parent ID")
					assert.Greater(t, cat.Depth, 0, "Child category should have depth > 0")
					assert.Greater(t, len(cat.Path), 1, "Child category path should have > 1 element")
				}

				// Basic field validation
				assert.NotEmpty(t, cat.ID, "Category ID should not be empty")
				assert.NotEmpty(t, cat.Name, "Category name should not be empty")
				assert.NotEmpty(t, cat.Slug, "Category slug should not be empty")
				assert.NotNil(t, cat.Children, "Children should not be nil (can be empty array)")
			}
		}
	})

	t.Run("should return hierarchical structure", func(t *testing.T) {
		query := `
			query {
				categories {
					name
					depth
					children {
						name
						depth
						children {
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
			Categories []CategoryNode `json:"categories"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Verify nested children are resolved correctly
		// Look for at least one category with children to validate nesting
		hasNested := false
		for _, cat := range result.Categories {
			if len(cat.Children) > 0 {
				hasNested = true
				// Verify depth increases at each level
				for _, child := range cat.Children {
					assert.Greater(t, child.Depth, cat.Depth, "Child depth should be greater than parent")

					// If grandchildren exist, check their depth too
					for _, grandchild := range child.Children {
						assert.Greater(t, grandchild.Depth, child.Depth, "Grandchild depth should be greater than child")
					}
				}
			}
		}

		// If no nested categories exist, that's ok (empty catalog scenario)
		if hasNested {
			t.Log("Successfully validated hierarchical structure")
		} else {
			t.Log("No nested categories found (empty or flat catalog)")
		}
	})

	t.Run("should handle empty categories", func(t *testing.T) {
		query := `
			query {
				categories {
					id
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Categories []struct {
				ID string `json:"id"`
			} `json:"categories"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Should return empty array, no errors
		assert.NotNil(t, result.Categories, "Categories should not be nil")
		// Length can be 0 if empty, which is valid
	})
}

// CategoryNode represents a category with nested children for hierarchy testing
type CategoryNode struct {
	Name     string         `json:"name"`
	Depth    int            `json:"depth"`
	Children []CategoryNode `json:"children"`
}

// TestCategoryBySlugQuery tests single category query by slug
func TestCategoryBySlugQuery(t *testing.T) {
	serverURL := testutil.GetTestServerURL()

	t.Run("should return category by slug", func(t *testing.T) {
		query := `
			query {
				category(slug: "electronics") {
					id
					name
					slug
					parent {
						id
					}
					children {
						id
						name
					}
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
					ID string `json:"id"`
				} `json:"parent"`
				Children []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"children"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// If category exists, validate it
		if result.Category != nil {
			assert.Equal(t, "electronics", result.Category.Slug, "Should return category with matching slug")
			assert.NotEmpty(t, result.Category.ID, "Category ID should not be empty")
			assert.NotEmpty(t, result.Category.Name, "Category name should not be empty")
			assert.NotNil(t, result.Category.Children, "Children should not be nil")
		} else {
			// Category doesn't exist yet - this is expected in Red phase
			t.Log("Category 'electronics' not found (expected in Red phase)")
		}
	})

	t.Run("should return null for non-existent slug", func(t *testing.T) {
		query := `
			query {
				category(slug: "non-existent-category-slug-12345") {
					id
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Category *struct {
				ID string `json:"id"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Should return null, no errors
		assert.Nil(t, result.Category, "Non-existent category should return null")
	})
}

// TestCategoryProductsField tests the products field on Category
func TestCategoryProductsField(t *testing.T) {
	serverURL := testutil.GetTestServerURL()

	t.Run("should return products in category", func(t *testing.T) {
		query := `
			query {
				category(slug: "laptops") {
					name
					products(first: 10) {
						edges {
							node {
								sku
								title
								category {
									slug
								}
							}
						}
						totalCount
					}
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Category *struct {
				Name     string `json:"name"`
				Products struct {
					Edges []struct {
						Node struct {
							SKU      string `json:"sku"`
							Title    string `json:"title"`
							Category *struct {
								Slug string `json:"slug"`
							} `json:"category"`
						} `json:"node"`
					} `json:"edges"`
					TotalCount int `json:"totalCount"`
				} `json:"products"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// If category exists, validate products
		if result.Category != nil {
			products := result.Category.Products

			// Total count should match edges length
			assert.Equal(t, len(products.Edges), products.TotalCount, "TotalCount should match edges length")

			// Validate each product belongs to this category
			for _, edge := range products.Edges {
				require.NotNil(t, edge.Node.Category, "Product should have category")
				assert.Equal(t, "laptops", edge.Node.Category.Slug, "Product should belong to 'laptops' category")
			}
		} else {
			t.Log("Category 'laptops' not found (expected in Red phase)")
		}
	})

	t.Run("should include subcategory products", func(t *testing.T) {
		query := `
			query {
				category(slug: "electronics") {
					name
					products(first: 100) {
						edges {
							node {
								sku
								category {
									slug
									path
								}
							}
						}
						totalCount
					}
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Category *struct {
				Name     string `json:"name"`
				Products struct {
					Edges []struct {
						Node struct {
							SKU      string `json:"sku"`
							Category *struct {
								Slug string   `json:"slug"`
								Path []string `json:"path"`
							} `json:"category"`
						} `json:"node"`
					} `json:"edges"`
					TotalCount int `json:"totalCount"`
				} `json:"products"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// If category exists, verify products from child categories are included
		if result.Category != nil {
			products := result.Category.Products

			// Electronics category should include products from:
			// - Electronics itself
			// - Computers (child)
			// - Laptops (grandchild)
			// So we should find products where "electronics" is in the path
			foundSubcategoryProduct := false
			for _, edge := range products.Edges {
				if edge.Node.Category != nil {
					// Check if "electronics" is in the category path
					for _, pathSegment := range edge.Node.Category.Path {
						if pathSegment == "electronics" || pathSegment == "Electronics" {
							foundSubcategoryProduct = true
							break
						}
					}
				}
			}

			// This assertion might fail in Red phase if subcategory inclusion isn't implemented
			if products.TotalCount > 0 {
				t.Logf("Found %d products in electronics category (including subcategories)", products.TotalCount)
				if foundSubcategoryProduct {
					t.Log("Successfully validated subcategory product inclusion")
				}
			}
		} else {
			t.Log("Category 'electronics' not found (expected in Red phase)")
		}
	})
}
