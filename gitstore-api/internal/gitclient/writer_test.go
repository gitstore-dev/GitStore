package gitclient

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateProductMarkdown(t *testing.T) {
	t.Run("should generate product markdown with front-matter", func(t *testing.T) {
		frontMatter := ProductFrontMatter{
			ID:              "prod_test123",
			SKU:             "LAPTOP-001",
			Title:           "Premium Laptop",
			Price:           1299.99,
			Currency:        "USD",
			InventoryStatus: "in_stock",
			CategoryID:      "cat_electronics",
			CollectionIDs:   []string{"coll_featured"},
			CreatedAt:       "2026-03-10T00:00:00Z",
			UpdatedAt:       "2026-03-10T00:00:00Z",
		}

		body := "A high-quality laptop for professionals."

		markdown, err := GenerateProductMarkdown(frontMatter, body)
		require.NoError(t, err)

		// Verify front-matter delimiter
		assert.True(t, strings.HasPrefix(markdown, "---\n"))
		assert.Contains(t, markdown, "\n---\n\n")

		// Verify YAML fields
		assert.Contains(t, markdown, "id: prod_test123")
		assert.Contains(t, markdown, "sku: LAPTOP-001")
		assert.Contains(t, markdown, "title: Premium Laptop")
		assert.Contains(t, markdown, "price: 1299.99")
		assert.Contains(t, markdown, "currency: USD")
		assert.Contains(t, markdown, "inventory_status: in_stock")
		assert.Contains(t, markdown, "category_id: cat_electronics")

		// Verify body content
		assert.Contains(t, markdown, "A high-quality laptop for professionals.")

		// Verify ends with newline
		assert.True(t, strings.HasSuffix(markdown, "\n"))
	})

	t.Run("should add title header if body doesn't have one", func(t *testing.T) {
		frontMatter := ProductFrontMatter{
			ID:              "prod_test456",
			SKU:             "WIDGET-001",
			Title:           "Test Widget",
			Price:           29.99,
			Currency:        "USD",
			InventoryStatus: "in_stock",
			CategoryID:      "cat_test",
			CreatedAt:       "2026-03-10T00:00:00Z",
			UpdatedAt:       "2026-03-10T00:00:00Z",
		}

		body := "This is a simple description."

		markdown, err := GenerateProductMarkdown(frontMatter, body)
		require.NoError(t, err)

		// Should add title header
		assert.Contains(t, markdown, "# Test Widget\n\n")
		assert.Contains(t, markdown, "This is a simple description.")
	})

	t.Run("should not duplicate title if body starts with header", func(t *testing.T) {
		frontMatter := ProductFrontMatter{
			ID:              "prod_test789",
			SKU:             "GADGET-001",
			Title:           "Test Gadget",
			Price:           49.99,
			Currency:        "USD",
			InventoryStatus: "in_stock",
			CategoryID:      "cat_test",
			CreatedAt:       "2026-03-10T00:00:00Z",
			UpdatedAt:       "2026-03-10T00:00:00Z",
		}

		body := "# Custom Title\n\nCustom description here."

		markdown, err := GenerateProductMarkdown(frontMatter, body)
		require.NoError(t, err)

		// Should use body's title, not add duplicate
		assert.Contains(t, markdown, "# Custom Title")
		assert.NotContains(t, markdown, "# Test Gadget")
	})

	t.Run("should handle empty body", func(t *testing.T) {
		frontMatter := ProductFrontMatter{
			ID:              "prod_empty",
			SKU:             "EMPTY-001",
			Title:           "Empty Product",
			Price:           0.00,
			Currency:        "USD",
			InventoryStatus: "out_of_stock",
			CategoryID:      "cat_test",
			CreatedAt:       "2026-03-10T00:00:00Z",
			UpdatedAt:       "2026-03-10T00:00:00Z",
		}

		markdown, err := GenerateProductMarkdown(frontMatter, "")
		require.NoError(t, err)

		// Should have front-matter
		assert.Contains(t, markdown, "id: prod_empty")
		// Should add title
		assert.Contains(t, markdown, "# Empty Product")
	})

	t.Run("should handle optional fields", func(t *testing.T) {
		quantity := 100
		frontMatter := ProductFrontMatter{
			ID:                "prod_optional",
			SKU:               "OPTIONAL-001",
			Title:             "Product with Optional Fields",
			Price:             99.99,
			Currency:          "USD",
			InventoryStatus:   "in_stock",
			InventoryQuantity: &quantity,
			CategoryID:        "cat_test",
			CollectionIDs:     []string{"coll_featured", "coll_bestsellers"},
			Images:            []string{"https://cdn.example.com/image1.jpg", "https://cdn.example.com/image2.jpg"},
			Metadata: map[string]interface{}{
				"brand":     "TestBrand",
				"weight_kg": 1.5,
			},
			CreatedAt: "2026-03-10T00:00:00Z",
			UpdatedAt: "2026-03-10T00:00:00Z",
		}

		markdown, err := GenerateProductMarkdown(frontMatter, "Product with all optional fields.")
		require.NoError(t, err)

		// Verify optional fields are included
		assert.Contains(t, markdown, "inventory_quantity: 100")
		assert.Contains(t, markdown, "collection_ids:")
		assert.Contains(t, markdown, "- coll_featured")
		assert.Contains(t, markdown, "- coll_bestsellers")
		assert.Contains(t, markdown, "images:")
		assert.Contains(t, markdown, "metadata:")
		assert.Contains(t, markdown, "brand: TestBrand")
	})
}

func TestGenerateCategoryMarkdown(t *testing.T) {
	t.Run("should generate category markdown", func(t *testing.T) {
		desc := "Electronics category"
		frontMatter := CategoryFrontMatter{
			ID:           "cat_electronics",
			Name:         "Electronics",
			Slug:         "electronics",
			Description:  &desc,
			DisplayOrder: 1,
			CreatedAt:    "2026-03-10T00:00:00Z",
			UpdatedAt:    "2026-03-10T00:00:00Z",
		}

		markdown, err := GenerateCategoryMarkdown(frontMatter, "")
		require.NoError(t, err)

		assert.Contains(t, markdown, "id: cat_electronics")
		assert.Contains(t, markdown, "name: Electronics")
		assert.Contains(t, markdown, "slug: electronics")
		assert.Contains(t, markdown, "display_order: 1")
		assert.Contains(t, markdown, "Electronics category")
	})

	t.Run("should handle parent category", func(t *testing.T) {
		parentID := "cat_electronics"
		frontMatter := CategoryFrontMatter{
			ID:           "cat_laptops",
			Name:         "Laptops",
			Slug:         "laptops",
			ParentID:     &parentID,
			DisplayOrder: 1,
			CreatedAt:    "2026-03-10T00:00:00Z",
			UpdatedAt:    "2026-03-10T00:00:00Z",
		}

		markdown, err := GenerateCategoryMarkdown(frontMatter, "")
		require.NoError(t, err)

		assert.Contains(t, markdown, "parent_id: cat_electronics")
	})
}

func TestGenerateCollectionMarkdown(t *testing.T) {
	t.Run("should generate collection markdown", func(t *testing.T) {
		desc := "Featured products collection"
		frontMatter := CollectionFrontMatter{
			ID:           "coll_featured",
			Name:         "Featured Products",
			Slug:         "featured",
			Description:  &desc,
			ProductIDs:   []string{"prod_1", "prod_2", "prod_3"},
			DisplayOrder: 1,
			CreatedAt:    "2026-03-10T00:00:00Z",
			UpdatedAt:    "2026-03-10T00:00:00Z",
		}

		markdown, err := GenerateCollectionMarkdown(frontMatter, "")
		require.NoError(t, err)

		assert.Contains(t, markdown, "id: coll_featured")
		assert.Contains(t, markdown, "name: Featured Products")
		assert.Contains(t, markdown, "slug: featured")
		assert.Contains(t, markdown, "product_ids:")
		assert.Contains(t, markdown, "- prod_1")
		assert.Contains(t, markdown, "Featured products collection")
	})
}

func TestGetFilePaths(t *testing.T) {
	t.Run("should generate correct product file path", func(t *testing.T) {
		path := GetProductFilePath("LAPTOP-001", "electronics")
		assert.Equal(t, "products/electronics/LAPTOP-001.md", path)
	})

	t.Run("should handle uncategorized products", func(t *testing.T) {
		path := GetProductFilePath("WIDGET-001", "")
		assert.Equal(t, "products/uncategorized/WIDGET-001.md", path)
	})

	t.Run("should generate correct category file path", func(t *testing.T) {
		path := GetCategoryFilePath("electronics")
		assert.Equal(t, "categories/electronics.md", path)
	})

	t.Run("should generate correct collection file path", func(t *testing.T) {
		path := GetCollectionFilePath("featured")
		assert.Equal(t, "collections/featured.md", path)
	})
}
