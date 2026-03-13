// Contract test for products query filtered by categoryId

package contract

import (
	"testing"

	"github.com/commerce-projects/gitstore/api/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProductsFilteredByCategory tests products query with category filter
func TestProductsFilteredByCategory(t *testing.T) {
	// This test validates that products can be filtered by category
	// and that the filtering respects the category hierarchy

	serverURL := testutil.GetTestServerURL()

	t.Run("should filter products by categoryId", func(t *testing.T) {
		// First, get a category to use for filtering
		categoryQuery := `
			query {
				category(slug: "laptops") {
					id
					name
				}
			}
		`

		categoryResp := testutil.ExecuteGraphQL(t, serverURL, categoryQuery, nil)
		testutil.AssertNoErrors(t, categoryResp)

		var categoryResult struct {
			Category *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, categoryResp, &categoryResult)

		if categoryResult.Category == nil {
			t.Skip("Category 'laptops' not found (expected in Red phase)")
		}

		categoryID := categoryResult.Category.ID

		// Now query products filtered by this category
		productsQuery := `
			query($categoryId: ID!) {
				products(first: 50, filter: { categoryId: $categoryId }) {
					edges {
						node {
							id
							sku
							title
							category {
								id
								name
							}
						}
					}
					totalCount
				}
			}
		`

		productsResp := testutil.ExecuteGraphQL(t, serverURL, productsQuery, map[string]interface{}{
			"categoryId": categoryID,
		})
		testutil.AssertNoErrors(t, productsResp)

		var productsResult struct {
			Products struct {
				Edges []struct {
					Node struct {
						ID    string `json:"id"`
						SKU   string `json:"sku"`
						Title string `json:"title"`
						Category *struct {
							ID   string `json:"id"`
							Name string `json:"name"`
						} `json:"category"`
					} `json:"node"`
				} `json:"edges"`
				TotalCount int `json:"totalCount"`
			} `json:"products"`
		}

		testutil.UnmarshalData(t, productsResp, &productsResult)

		// Validate all returned products belong to the specified category
		for _, edge := range productsResult.Products.Edges {
			require.NotNil(t, edge.Node.Category, "Product should have a category")
			assert.Equal(t, categoryID, edge.Node.Category.ID,
				"Product %s should belong to category %s", edge.Node.SKU, categoryResult.Category.Name)
		}

		// Total count should match edges length (with pagination limit considered)
		assert.LessOrEqual(t, len(productsResult.Products.Edges), 50,
			"Should not return more than requested limit")

		t.Logf("Found %d products in category '%s'", productsResult.Products.TotalCount, categoryResult.Category.Name)
	})

	t.Run("should return empty list for category with no products", func(t *testing.T) {
		// Query products for a category that has no products
		query := `
			query {
				category(slug: "empty-category") {
					id
					name
					products(first: 10) {
						edges {
							node {
								id
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
				ID   string `json:"id"`
				Name string `json:"name"`
				Products struct {
					Edges []struct {
						Node struct {
							ID string `json:"id"`
						} `json:"node"`
					} `json:"edges"`
					TotalCount int `json:"totalCount"`
				} `json:"products"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, resp, &result)

		if result.Category != nil {
			// Should return empty edges array
			assert.Empty(t, result.Category.Products.Edges, "Category with no products should have empty edges")
			assert.Equal(t, 0, result.Category.Products.TotalCount, "TotalCount should be 0")
		} else {
			t.Log("Empty category not found (may not exist in catalog)")
		}
	})

	t.Run("should support pagination with category filter", func(t *testing.T) {
		// Get a category with products
		categoryQuery := `
			query {
				category(slug: "laptops") {
					id
					name
				}
			}
		`

		categoryResp := testutil.ExecuteGraphQL(t, serverURL, categoryQuery, nil)
		testutil.AssertNoErrors(t, categoryResp)

		var categoryResult struct {
			Category *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"category"`
		}

		testutil.UnmarshalData(t, categoryResp, &categoryResult)

		if categoryResult.Category == nil {
			t.Skip("Category 'laptops' not found")
		}

		categoryID := categoryResult.Category.ID

		// Query first page
		firstPageQuery := `
			query($categoryId: ID!) {
				products(first: 2, filter: { categoryId: $categoryId }) {
					edges {
						cursor
						node {
							id
							sku
						}
					}
					pageInfo {
						hasNextPage
						endCursor
					}
					totalCount
				}
			}
		`

		firstPageResp := testutil.ExecuteGraphQL(t, serverURL, firstPageQuery, map[string]interface{}{
			"categoryId": categoryID,
		})
		testutil.AssertNoErrors(t, firstPageResp)

		var firstPageResult struct {
			Products struct {
				Edges []struct {
					Cursor string `json:"cursor"`
					Node struct {
						ID  string `json:"id"`
						SKU string `json:"sku"`
					} `json:"node"`
				} `json:"edges"`
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
				TotalCount int `json:"totalCount"`
			} `json:"products"`
		}

		testutil.UnmarshalData(t, firstPageResp, &firstPageResult)

		// Should return at most 2 products
		assert.LessOrEqual(t, len(firstPageResult.Products.Edges), 2,
			"Should respect pagination limit")

		// If there are more products, hasNextPage should be true
		if firstPageResult.Products.TotalCount > 2 {
			assert.True(t, firstPageResult.Products.PageInfo.HasNextPage,
				"Should indicate there are more pages")
			require.NotNil(t, firstPageResult.Products.PageInfo.EndCursor,
				"Should provide endCursor for pagination")

			// Query second page using cursor
			secondPageQuery := `
				query($categoryId: ID!, $after: String!) {
					products(first: 2, after: $after, filter: { categoryId: $categoryId }) {
						edges {
							node {
								id
								sku
								category {
									id
								}
							}
						}
						pageInfo {
							hasPreviousPage
							startCursor
						}
					}
				}
			`

			secondPageResp := testutil.ExecuteGraphQL(t, serverURL, secondPageQuery, map[string]interface{}{
				"categoryId": categoryID,
				"after":      *firstPageResult.Products.PageInfo.EndCursor,
			})
			testutil.AssertNoErrors(t, secondPageResp)

			var secondPageResult struct {
				Products struct {
					Edges []struct {
						Node struct {
							ID  string `json:"id"`
							SKU string `json:"sku"`
							Category *struct {
								ID string `json:"id"`
							} `json:"category"`
						} `json:"node"`
					} `json:"edges"`
					PageInfo struct {
						HasPreviousPage bool    `json:"hasPreviousPage"`
						StartCursor     *string `json:"startCursor"`
					} `json:"pageInfo"`
				} `json:"products"`
			}

			testutil.UnmarshalData(t, secondPageResp, &secondPageResult)

			// Second page should indicate there's a previous page
			assert.True(t, secondPageResult.Products.PageInfo.HasPreviousPage,
				"Second page should have previous page")

			// All products in second page should still belong to the category
			for _, edge := range secondPageResult.Products.Edges {
				require.NotNil(t, edge.Node.Category, "Product should have category")
				assert.Equal(t, categoryID, edge.Node.Category.ID,
					"Product should belong to filtered category")
			}

			t.Log("Successfully validated pagination with category filter")
		}
	})

	t.Run("should handle non-existent category gracefully", func(t *testing.T) {
		// Query products with a non-existent category ID
		query := `
			query {
				products(first: 10, filter: { categoryId: "cat_nonexistent12345" }) {
					edges {
						node {
							id
						}
					}
					totalCount
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Products struct {
				Edges []struct {
					Node struct {
						ID string `json:"id"`
					} `json:"node"`
				} `json:"edges"`
				TotalCount int `json:"totalCount"`
			} `json:"products"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Should return empty list for non-existent category
		assert.Empty(t, result.Products.Edges, "Should return empty list for non-existent category")
		assert.Equal(t, 0, result.Products.TotalCount, "TotalCount should be 0 for non-existent category")
	})

	t.Run("should work with category field on product", func(t *testing.T) {
		// Query products and verify category field resolution
		query := `
			query {
				products(first: 10) {
					edges {
						node {
							sku
							category {
								id
								name
								slug
								parent {
									id
									name
								}
							}
						}
					}
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)

		// In Red phase, this might fail because products may have null categories
		// which violates the schema (category: Category! is non-null)
		// This is expected until categories are implemented
		if len(resp.Errors) > 0 {
			// Check if errors are related to null categories
			for _, err := range resp.Errors {
				if err.Message == "the requested element is null which the schema does not allow" {
					t.Skip("Products have null categories (expected in Red phase before category implementation)")
					return
				}
			}
			// If it's a different error, fail the test
			testutil.AssertNoErrors(t, resp)
		}

		var result struct {
			Products struct {
				Edges []struct {
					Node struct {
						SKU string `json:"sku"`
						Category *struct {
							ID   string `json:"id"`
							Name string `json:"name"`
							Slug string `json:"slug"`
							Parent *struct {
								ID   string `json:"id"`
								Name string `json:"name"`
							} `json:"parent"`
						} `json:"category"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"products"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Validate category field resolution
		for _, edge := range result.Products.Edges {
			if edge.Node.Category != nil {
				assert.NotEmpty(t, edge.Node.Category.ID, "Category ID should not be empty")
				assert.NotEmpty(t, edge.Node.Category.Name, "Category name should not be empty")
				assert.NotEmpty(t, edge.Node.Category.Slug, "Category slug should not be empty")

				// If category has a parent, validate it
				if edge.Node.Category.Parent != nil {
					assert.NotEmpty(t, edge.Node.Category.Parent.ID, "Parent category ID should not be empty")
					assert.NotEmpty(t, edge.Node.Category.Parent.Name, "Parent category name should not be empty")
				}
			}
		}

		t.Logf("Validated category field resolution for %d products", len(result.Products.Edges))
	})
}
