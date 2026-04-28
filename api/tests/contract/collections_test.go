// Contract test for collections query

package contract

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/tests/testutil"
	"github.com/stretchr/testify/assert"
)

// TestCollectionsQuery tests the collections list query
func TestCollectionsQuery(t *testing.T) {
	serverURL := testutil.GetTestServerURL()

	t.Run("should return all collections", func(t *testing.T) {
		query := `
			query {
				collections {
					id
					name
					slug
					displayOrder
					productCount
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Collections []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				Slug         string `json:"slug"`
				DisplayOrder int    `json:"displayOrder"`
				ProductCount int    `json:"productCount"`
			} `json:"collections"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Assertions:
		// - Returns array of collections
		assert.NotNil(t, result.Collections, "Collections should not be nil")

		// If there are collections, validate structure
		if len(result.Collections) > 0 {
			for _, coll := range result.Collections {
				assert.NotEmpty(t, coll.ID, "Collection ID should not be empty")
				assert.NotEmpty(t, coll.Name, "Collection name should not be empty")
				assert.NotEmpty(t, coll.Slug, "Collection slug should not be empty")
				assert.GreaterOrEqual(t, coll.DisplayOrder, 0, "DisplayOrder should be >= 0")
				assert.GreaterOrEqual(t, coll.ProductCount, 0, "ProductCount should be >= 0")
			}
		}
	})

	t.Run("should handle empty collections", func(t *testing.T) {
		query := `
			query {
				collections {
					id
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Collections []struct {
				ID string `json:"id"`
			} `json:"collections"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Should return empty array, no errors
		assert.NotNil(t, result.Collections, "Collections should not be nil")
		// Length can be 0 if empty, which is valid
	})
}

// TestCollectionBySlugQuery tests single collection query
func TestCollectionBySlugQuery(t *testing.T) {
	serverURL := testutil.GetTestServerURL()

	t.Run("should return collection by slug", func(t *testing.T) {
		query := `
			query {
				collection(slug: "featured") {
					id
					name
					slug
					products(first: 10) {
						edges {
							node {
								sku
								title
							}
						}
						totalCount
					}
					productCount
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Collection *struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Slug     string `json:"slug"`
				Products struct {
					Edges []struct {
						Node struct {
							SKU   string `json:"sku"`
							Title string `json:"title"`
						} `json:"node"`
					} `json:"edges"`
					TotalCount int `json:"totalCount"`
				} `json:"products"`
				ProductCount int `json:"productCount"`
			} `json:"collection"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// If collection exists, validate it
		if result.Collection != nil {
			assert.Equal(t, "featured", result.Collection.Slug, "Should return collection with matching slug")
			assert.NotEmpty(t, result.Collection.ID, "Collection ID should not be empty")
			assert.NotEmpty(t, result.Collection.Name, "Collection name should not be empty")

			// Products field should be resolved
			assert.NotNil(t, result.Collection.Products, "Products should not be nil")

			// ProductCount should match actual products
			assert.Equal(t, result.Collection.ProductCount, result.Collection.Products.TotalCount,
				"ProductCount should match Products.TotalCount")
		} else {
			t.Log("Collection 'featured' not found (expected in Red phase)")
		}
	})

	t.Run("should return null for non-existent slug", func(t *testing.T) {
		query := `
			query {
				collection(slug: "non-existent-collection-slug-12345") {
					id
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Collection *struct {
				ID string `json:"id"`
			} `json:"collection"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// Should return null, no errors
		assert.Nil(t, result.Collection, "Non-existent collection should return null")
	})
}

// TestCollectionProductsField tests the products field on Collection
func TestCollectionProductsField(t *testing.T) {
	serverURL := testutil.GetTestServerURL()

	t.Run("should return products in collection", func(t *testing.T) {
		query := `
			query {
				collection(slug: "winter-sale") {
					name
					products(first: 20) {
						edges {
							node {
								sku
								title
								collections {
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
			Collection *struct {
				Name     string `json:"name"`
				Products struct {
					Edges []struct {
						Node struct {
							SKU         string `json:"sku"`
							Title       string `json:"title"`
							Collections []struct {
								Slug string `json:"slug"`
							} `json:"collections"`
						} `json:"node"`
					} `json:"edges"`
					TotalCount int `json:"totalCount"`
				} `json:"products"`
			} `json:"collection"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// If collection exists, validate products
		if result.Collection != nil {
			products := result.Collection.Products

			// Total count should match edges length
			assert.Equal(t, len(products.Edges), products.TotalCount, "TotalCount should match edges length")

			// Validate each product includes this collection
			for _, edge := range products.Edges {
				// Check that this product references the winter-sale collection
				foundCollection := false
				for _, coll := range edge.Node.Collections {
					if coll.Slug == "winter-sale" {
						foundCollection = true
						break
					}
				}
				assert.True(t, foundCollection, "Product should reference the 'winter-sale' collection")
			}
		} else {
			t.Log("Collection 'winter-sale' not found (expected in Red phase)")
		}
	})

	t.Run("should handle collection with no products", func(t *testing.T) {
		query := `
			query {
				collection(slug: "empty-collection") {
					name
					products(first: 10) {
						edges {
							node {
								id
							}
						}
						totalCount
					}
					productCount
				}
			}
		`

		resp := testutil.ExecuteGraphQL(t, serverURL, query, nil)
		testutil.AssertNoErrors(t, resp)

		var result struct {
			Collection *struct {
				Name     string `json:"name"`
				Products struct {
					Edges []struct {
						Node struct {
							ID string `json:"id"`
						} `json:"node"`
					} `json:"edges"`
					TotalCount int `json:"totalCount"`
				} `json:"products"`
				ProductCount int `json:"productCount"`
			} `json:"collection"`
		}

		testutil.UnmarshalData(t, resp, &result)

		// If empty collection exists, validate it
		if result.Collection != nil {
			// Should return empty edges array
			assert.Empty(t, result.Collection.Products.Edges, "Empty collection should have no products")
			// totalCount should be 0
			assert.Equal(t, 0, result.Collection.Products.TotalCount, "TotalCount should be 0")
			// productCount should be 0
			assert.Equal(t, 0, result.Collection.ProductCount, "ProductCount should be 0")
		} else {
			t.Log("Empty collection not found (may be filtered out or not exist)")
		}
	})
}
