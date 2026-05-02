package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateProductID(t *testing.T) {
	t.Run("should generate unique IDs", func(t *testing.T) {
		id1, err := GenerateProductID()
		require.NoError(t, err)

		id2, err := GenerateProductID()
		require.NoError(t, err)

		assert.NotEqual(t, id1, id2)
	})

	t.Run("should have correct prefix", func(t *testing.T) {
		id, err := GenerateProductID()
		require.NoError(t, err)

		assert.True(t, strings.HasPrefix(id, "prod_"))
	})

	t.Run("should have reasonable length", func(t *testing.T) {
		id, err := GenerateProductID()
		require.NoError(t, err)

		// prod_ (5) + base64 encoded 12 bytes (~16 chars) = ~21 chars
		assert.Greater(t, len(id), 15)
		assert.Less(t, len(id), 30)
	})
}

func TestNewProduct(t *testing.T) {
	t.Run("should create product with required fields", func(t *testing.T) {
		product, err := NewProduct("TEST-SKU", "Test Product", "Body content", 29.99, "USD", "cat_test")
		require.NoError(t, err)

		assert.NotEmpty(t, product.ID)
		assert.Equal(t, "TEST-SKU", product.SKU)
		assert.Equal(t, "Test Product", product.Title)
		assert.Equal(t, "Body content", product.Body)
		assert.Equal(t, 29.99, product.Price)
		assert.Equal(t, "USD", product.Currency)
		assert.Equal(t, "IN_STOCK", product.InventoryStatus)
		assert.Equal(t, "cat_test", product.CategoryID)
		assert.NotZero(t, product.CreatedAt)
		assert.NotZero(t, product.UpdatedAt)
		assert.Empty(t, product.CollectionIDs)
		assert.Empty(t, product.Images)
	})

	t.Run("should use default currency", func(t *testing.T) {
		product, err := NewProduct("TEST-SKU", "Test Product", "", 10.00, "", "cat_test")
		require.NoError(t, err)

		assert.Equal(t, "USD", product.Currency)
	})

	t.Run("should set timestamps", func(t *testing.T) {
		product, err := NewProduct("TEST-SKU", "Test Product", "", 10.00, "USD", "cat_test")
		require.NoError(t, err)

		assert.False(t, product.CreatedAt.IsZero())
		assert.False(t, product.UpdatedAt.IsZero())
		assert.Equal(t, product.CreatedAt, product.UpdatedAt)
	})
}

func TestValidateSKU(t *testing.T) {
	tests := []struct {
		name      string
		sku       string
		shouldErr bool
		errMsg    string
	}{
		{"valid SKU", "PRODUCT-123", false, ""},
		{"valid with underscore", "PRODUCT_123", false, ""},
		{"valid lowercase", "product-123", false, ""},
		{"empty SKU", "", true, "SKU is required"},
		{"too short", "AB", true, "at least 3 characters"},
		{"too long", strings.Repeat("A", 101), true, "at most 100 characters"},
		{"invalid characters", "SKU@123", true, "alphanumeric"},
		{"with spaces", "SKU 123", true, "alphanumeric"},
		{"with special chars", "SKU#123", true, "alphanumeric"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSKU(tt.sku)
			if tt.shouldErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateTitle(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		shouldErr bool
		errMsg    string
	}{
		{"valid title", "Product Title", false, ""},
		{"single char", "A", false, ""},
		{"max length", strings.Repeat("A", 200), false, ""},
		{"empty title", "", true, "title is required"},
		{"too long", strings.Repeat("A", 201), true, "at most 200 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTitle(tt.title)
			if tt.shouldErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePrice(t *testing.T) {
	tests := []struct {
		name      string
		price     float64
		shouldErr bool
		errMsg    string
	}{
		{"valid price", 29.99, false, ""},
		{"zero price", 0.00, false, ""},
		{"max price", 999999.99, false, ""},
		{"negative price", -10.00, true, "cannot be negative"},
		{"too large", 1000000.00, true, "too large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrice(tt.price)
			if tt.shouldErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateInventoryStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		shouldErr bool
	}{
		{"in stock", "IN_STOCK", false},
		{"out of stock", "OUT_OF_STOCK", false},
		{"preorder", "PREORDER", false},
		{"discontinued", "DISCONTINUED", false},
		{"lowercase", "in_stock", false}, // Should work - normalized to uppercase
		{"invalid status", "AVAILABLE", true},
		{"empty status", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInventoryStatus(tt.status)
			if tt.shouldErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestProductValidate(t *testing.T) {
	t.Run("should pass for valid product", func(t *testing.T) {
		product := &Product{
			ID:              "prod_abc123",
			SKU:             "VALID-SKU",
			Title:           "Valid Product",
			Price:           29.99,
			Currency:        "USD",
			InventoryStatus: "IN_STOCK",
			CategoryID:      "cat_test",
		}

		err := product.Validate()
		assert.NoError(t, err)
	})

	t.Run("should fail for invalid SKU", func(t *testing.T) {
		product := &Product{
			SKU:             "",
			Title:           "Product",
			Price:           10.00,
			Currency:        "USD",
			InventoryStatus: "IN_STOCK",
			CategoryID:      "cat_test",
		}

		err := product.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SKU")
	})

	t.Run("should fail for missing currency", func(t *testing.T) {
		product := &Product{
			SKU:             "VALID-SKU",
			Title:           "Product",
			Price:           10.00,
			Currency:        "",
			InventoryStatus: "IN_STOCK",
			CategoryID:      "cat_test",
		}

		err := product.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "currency")
	})

	t.Run("should fail for missing category", func(t *testing.T) {
		product := &Product{
			SKU:             "VALID-SKU",
			Title:           "Product",
			Price:           10.00,
			Currency:        "USD",
			InventoryStatus: "IN_STOCK",
			CategoryID:      "",
		}

		err := product.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "categoryID")
	})
}

func TestGetCategorySlug(t *testing.T) {
	tests := []struct {
		name       string
		categoryID string
		expected   string
	}{
		{"with prefix", "cat_electronics", "electronics"},
		{"without prefix", "electronics", "electronics"},
		{"empty string", "cat_", ""},
		{"nested", "cat_electronics_laptops", "electronics_laptops"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetCategorySlug(tt.categoryID)
			assert.Equal(t, tt.expected, result)
		})
	}
}
