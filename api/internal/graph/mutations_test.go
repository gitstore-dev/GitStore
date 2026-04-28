package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/models"
	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestMutationRepo(t *testing.T) (string, func()) {
	tmpDir, err := os.MkdirTemp("", "gitstore-mutations-test-*")
	require.NoError(t, err)

	// Initialize git repository
	_, err = git.PlainInit(tmpDir, false)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return tmpDir, cleanup
}

func TestCreateProduct(t *testing.T) {
	t.Run("should create product with all required fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateProductInput{
			SKU:        "TEST-PRODUCT-001",
			Title:      "Test Product",
			Price:      29.99,
			CategoryID: "cat_electronics",
		}

		payload, err := service.CreateProduct(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)
		require.NotNil(t, payload.Product)

		product := payload.Product
		assert.Equal(t, "TEST-PRODUCT-001", product.SKU)
		assert.Equal(t, "Test Product", product.Title)
		assert.Equal(t, 29.99, product.Price)
		assert.Equal(t, "USD", product.Currency)
		assert.Equal(t, "IN_STOCK", product.InventoryStatus)
		assert.Equal(t, "cat_electronics", product.CategoryID)
		assert.NotEmpty(t, product.ID)
		assert.True(t, len(product.ID) > 5) // Should have prefix + base62
		assert.NotZero(t, product.CreatedAt)
		assert.NotZero(t, product.UpdatedAt)
	})

	t.Run("should create product with optional fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		currency := "EUR"
		inventoryStatus := "OUT_OF_STOCK"
		inventoryQuantity := 50
		body := "# Product Description\n\nThis is the product body."
		clientMutationID := "test-mutation-123"

		input := CreateProductInput{
			ClientMutationID:  &clientMutationID,
			SKU:               "TEST-002",
			Title:             "Product with Options",
			Body:              &body,
			Price:             99.99,
			Currency:          &currency,
			InventoryStatus:   &inventoryStatus,
			InventoryQuantity: &inventoryQuantity,
			CategoryID:        "cat_accessories",
			CollectionIDs:     []string{"coll_featured", "coll_bestsellers"},
			Images:            []string{"https://cdn.example.com/image.jpg"},
			Metadata: map[string]interface{}{
				"brand":  "TestBrand",
				"weight": 1.5,
			},
		}

		payload, err := service.CreateProduct(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)
		assert.Equal(t, "test-mutation-123", *payload.ClientMutationID)

		product := payload.Product
		assert.Equal(t, "EUR", product.Currency)
		assert.Equal(t, "OUT_OF_STOCK", product.InventoryStatus)
		assert.Equal(t, 50, *product.InventoryQuantity)
		assert.Contains(t, product.Body, "Product Description")
		assert.Len(t, product.CollectionIDs, 2)
		assert.Contains(t, product.CollectionIDs, "coll_featured")
		assert.Len(t, product.Images, 1)
		assert.Equal(t, "TestBrand", product.Metadata["brand"])
	})

	t.Run("should create markdown file in repository", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateProductInput{
			SKU:        "LAPTOP-001",
			Title:      "Premium Laptop",
			Price:      1299.99,
			CategoryID: "cat_electronics",
		}

		_, err := service.CreateProduct(ctx, input)
		require.NoError(t, err)

		// Verify file was created
		filePath := filepath.Join(repoPath, "products/electronics/LAPTOP-001.md")
		content, err := os.ReadFile(filePath)
		require.NoError(t, err)

		// Check file contains expected content
		contentStr := string(content)
		assert.Contains(t, contentStr, "---")
		assert.Contains(t, contentStr, "sku: LAPTOP-001")
		assert.Contains(t, contentStr, "title: Premium Laptop")
		assert.Contains(t, contentStr, "price: 1299.99")
		assert.Contains(t, contentStr, "# Premium Laptop")
	})

	t.Run("should commit file to git", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateProductInput{
			SKU:        "WIDGET-001",
			Title:      "Test Widget",
			Price:      19.99,
			CategoryID: "cat_widgets",
		}

		_, err := service.CreateProduct(ctx, input)
		require.NoError(t, err)

		// Verify git commit was created
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		ref, err := repo.Head()
		require.NoError(t, err)

		commit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)

		assert.Contains(t, commit.Message, "create")
		assert.Contains(t, commit.Message, "product")
		assert.Contains(t, commit.Message, "WIDGET-001")
	})

	t.Run("should validate required fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		tests := []struct {
			name        string
			input       CreateProductInput
			expectedErr string
		}{
			{
				name: "missing SKU",
				input: CreateProductInput{
					SKU:        "",
					Title:      "Test",
					Price:      10.00,
					CategoryID: "cat_test",
				},
				expectedErr: "SKU is required",
			},
			{
				name: "missing title",
				input: CreateProductInput{
					SKU:        "TEST-001",
					Title:      "",
					Price:      10.00,
					CategoryID: "cat_test",
				},
				expectedErr: "title is required",
			},
			{
				name: "negative price",
				input: CreateProductInput{
					SKU:        "TEST-001",
					Title:      "Test",
					Price:      -10.00,
					CategoryID: "cat_test",
				},
				expectedErr: "price cannot be negative",
			},
			{
				name: "missing category",
				input: CreateProductInput{
					SKU:        "TEST-001",
					Title:      "Test",
					Price:      10.00,
					CategoryID: "",
				},
				expectedErr: "categoryID is required",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := service.CreateProduct(ctx, tt.input)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
			})
		}
	})

	t.Run("should validate inventory status", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		invalidStatus := "INVALID_STATUS"
		input := CreateProductInput{
			SKU:             "TEST-001",
			Title:           "Test",
			Price:           10.00,
			CategoryID:      "cat_test",
			InventoryStatus: &invalidStatus,
		}

		_, err := service.CreateProduct(ctx, input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid inventory status")
	})

	t.Run("should validate SKU format", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		tests := []struct {
			sku         string
			shouldError bool
			errorMsg    string
		}{
			{"AB", true, "at least 3 characters"},
			{"VALID-SKU-123", false, ""},
			{"SKU_WITH_UNDERSCORE", false, ""},
			{"SKU@WITH@SPECIAL", true, "alphanumeric"},
			{"SKU WITH SPACES", true, "alphanumeric"},
		}

		for _, tt := range tests {
			input := CreateProductInput{
				SKU:        tt.sku,
				Title:      "Test Product",
				Price:      10.00,
				CategoryID: "cat_test",
			}

			_, err := service.CreateProduct(ctx, input)
			if tt.shouldError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		}
	})

	t.Run("should use default values", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateProductInput{
			SKU:        "TEST-DEFAULTS",
			Title:      "Test Defaults",
			Price:      10.00,
			CategoryID: "cat_test",
			// Currency, InventoryStatus not provided
		}

		payload, err := service.CreateProduct(ctx, input)
		require.NoError(t, err)

		product := payload.Product
		assert.Equal(t, "USD", product.Currency)
		assert.Equal(t, "IN_STOCK", product.InventoryStatus)
		assert.Empty(t, product.CollectionIDs)
		assert.Empty(t, product.Images)
		assert.Empty(t, product.Metadata)
	})

	t.Run("should handle uncategorized products", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateProductInput{
			SKU:        "UNCAT-001",
			Title:      "Uncategorized Product",
			Price:      5.00,
			CategoryID: "cat_",
		}

		_, err := service.CreateProduct(ctx, input)
		require.NoError(t, err)

		// Should create file in uncategorized folder
		filePath := filepath.Join(repoPath, "products/uncategorized/UNCAT-001.md")
		_, err = os.Stat(filePath)
		require.NoError(t, err)
	})
}

func TestUpdateProduct(t *testing.T) {
	t.Run("should update product fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create initial product
		createInput := CreateProductInput{
			SKU:        "TEST-UPDATE-001",
			Title:      "Original Title",
			Price:      29.99,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)
		originalProduct := createPayload.Product

		// Read the created file to get content for version
		filePath := filepath.Join(repoPath, "products/test/TEST-UPDATE-001.md")
		originalContent, err := os.ReadFile(filePath)
		require.NoError(t, err)

		// Calculate version
		versionChecker := NewVersionChecker()
		version := versionChecker.CalculateVersion(string(originalContent))

		// Update the product
		newTitle := "Updated Title"
		newPrice := 39.99
		updateInput := UpdateProductInput{
			ID:      originalProduct.ID,
			Title:   &newTitle,
			Price:   &newPrice,
			Version: version,
		}

		// Mock readProductFromGit by directly reading and parsing
		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return originalProduct, string(originalContent), nil
		}

		updatePayload, err := service.UpdateProduct(ctx, updateInput)
		require.NoError(t, err)
		require.NotNil(t, updatePayload)
		require.Nil(t, updatePayload.Conflict)

		updated := updatePayload.Product
		assert.Equal(t, "Updated Title", updated.Title)
		assert.Equal(t, 39.99, updated.Price)
		assert.Equal(t, originalProduct.SKU, updated.SKU) // Unchanged
		assert.True(t, updated.UpdatedAt.After(originalProduct.CreatedAt))
	})

	t.Run("should detect version conflict", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create initial product
		createInput := CreateProductInput{
			SKU:        "TEST-CONFLICT-001",
			Title:      "Original Title",
			Price:      29.99,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)
		originalProduct := createPayload.Product

		// Read original content
		filePath := filepath.Join(repoPath, "products/test/TEST-CONFLICT-001.md")
		originalContent, err := os.ReadFile(filePath)
		require.NoError(t, err)

		// Simulate concurrent update - modify the file
		modifiedProduct := *originalProduct
		modifiedProduct.Price = 49.99
		modifiedProduct.UpdatedAt = time.Now().UTC()

		frontMatter := gitclient.ProductFrontMatter{
			ID:              modifiedProduct.ID,
			SKU:             modifiedProduct.SKU,
			Title:           modifiedProduct.Title,
			Price:           modifiedProduct.Price,
			Currency:        modifiedProduct.Currency,
			InventoryStatus: modifiedProduct.InventoryStatus,
			CategoryID:      modifiedProduct.CategoryID,
			CollectionIDs:   modifiedProduct.CollectionIDs,
			Images:          modifiedProduct.Images,
			Metadata:        modifiedProduct.Metadata,
			CreatedAt:       modifiedProduct.CreatedAt.Format(time.RFC3339),
			UpdatedAt:       modifiedProduct.UpdatedAt.Format(time.RFC3339),
		}
		modifiedContent, _ := gitclient.GenerateProductMarkdown(frontMatter, modifiedProduct.Body)

		// Try to update with stale version
		versionChecker := NewVersionChecker()
		staleVersion := versionChecker.CalculateVersion(string(originalContent))

		newTitle := "My Update"
		updateInput := UpdateProductInput{
			ID:      originalProduct.ID,
			Title:   &newTitle,
			Version: staleVersion,
		}

		// Mock to return modified content
		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return &modifiedProduct, modifiedContent, nil
		}

		updatePayload, err := service.UpdateProduct(ctx, updateInput)
		require.NoError(t, err)
		require.NotNil(t, updatePayload.Conflict)

		conflict := updatePayload.Conflict
		assert.True(t, conflict.Detected)
		assert.NotEmpty(t, conflict.CurrentVersion)
		assert.NotEmpty(t, conflict.AttemptedVersion)
		assert.NotEmpty(t, conflict.Diff)
		assert.NotNil(t, conflict.CurrentProduct)
	})

	t.Run("should handle SKU change", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create initial product
		createInput := CreateProductInput{
			SKU:        "OLD-SKU-001",
			Title:      "Product",
			Price:      10.00,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)
		originalProduct := createPayload.Product

		filePath := filepath.Join(repoPath, "products/test/OLD-SKU-001.md")
		originalContent, err := os.ReadFile(filePath)
		require.NoError(t, err)

		versionChecker := NewVersionChecker()
		version := versionChecker.CalculateVersion(string(originalContent))

		// Update SKU
		newSKU := "NEW-SKU-001"
		updateInput := UpdateProductInput{
			ID:      originalProduct.ID,
			SKU:     &newSKU,
			Version: version,
		}

		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return originalProduct, string(originalContent), nil
		}

		_, err = service.UpdateProduct(ctx, updateInput)
		require.NoError(t, err)

		// Old file should be deleted
		_, err = os.Stat(filePath)
		assert.True(t, os.IsNotExist(err))

		// New file should exist
		newFilePath := filepath.Join(repoPath, "products/test/NEW-SKU-001.md")
		_, err = os.Stat(newFilePath)
		assert.NoError(t, err)
	})

	t.Run("should handle category change", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create product in one category
		createInput := CreateProductInput{
			SKU:        "MOVE-SKU-001",
			Title:      "Product",
			Price:      10.00,
			CategoryID: "cat_electronics",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)
		originalProduct := createPayload.Product

		oldFilePath := filepath.Join(repoPath, "products/electronics/MOVE-SKU-001.md")
		originalContent, err := os.ReadFile(oldFilePath)
		require.NoError(t, err)

		versionChecker := NewVersionChecker()
		version := versionChecker.CalculateVersion(string(originalContent))

		// Move to different category
		newCategory := "cat_accessories"
		updateInput := UpdateProductInput{
			ID:         originalProduct.ID,
			CategoryID: &newCategory,
			Version:    version,
		}

		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return originalProduct, string(originalContent), nil
		}

		_, err = service.UpdateProduct(ctx, updateInput)
		require.NoError(t, err)

		// Old file should be deleted
		_, err = os.Stat(oldFilePath)
		assert.True(t, os.IsNotExist(err))

		// New file should exist in new category folder
		newFilePath := filepath.Join(repoPath, "products/accessories/MOVE-SKU-001.md")
		_, err = os.Stat(newFilePath)
		assert.NoError(t, err)
	})

	t.Run("should validate updated fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create product
		createInput := CreateProductInput{
			SKU:        "VALIDATE-001",
			Title:      "Product",
			Price:      10.00,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)
		originalProduct := createPayload.Product

		filePath := filepath.Join(repoPath, "products/test/VALIDATE-001.md")
		originalContent, err := os.ReadFile(filePath)
		require.NoError(t, err)

		versionChecker := NewVersionChecker()
		version := versionChecker.CalculateVersion(string(originalContent))

		// Try to update with invalid price
		invalidPrice := -10.00
		updateInput := UpdateProductInput{
			ID:      originalProduct.ID,
			Price:   &invalidPrice,
			Version: version,
		}

		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return originalProduct, string(originalContent), nil
		}

		_, err = service.UpdateProduct(ctx, updateInput)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validation failed")
		assert.Contains(t, err.Error(), "negative")
	})

	t.Run("should only update provided fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create product with all fields
		quantity := 100
		createInput := CreateProductInput{
			SKU:               "PARTIAL-001",
			Title:             "Original Title",
			Price:             29.99,
			CategoryID:        "cat_test",
			InventoryQuantity: &quantity,
			CollectionIDs:     []string{"coll_1", "coll_2"},
			Images:            []string{"image1.jpg"},
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)
		originalProduct := createPayload.Product

		filePath := filepath.Join(repoPath, "products/test/PARTIAL-001.md")
		originalContent, err := os.ReadFile(filePath)
		require.NoError(t, err)

		versionChecker := NewVersionChecker()
		version := versionChecker.CalculateVersion(string(originalContent))

		// Update only price
		newPrice := 39.99
		updateInput := UpdateProductInput{
			ID:      originalProduct.ID,
			Price:   &newPrice,
			Version: version,
		}

		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return originalProduct, string(originalContent), nil
		}

		updatePayload, err := service.UpdateProduct(ctx, updateInput)
		require.NoError(t, err)

		updated := updatePayload.Product
		assert.Equal(t, 39.99, updated.Price)                                // Updated
		assert.Equal(t, "Original Title", updated.Title)                     // Unchanged
		assert.Equal(t, 100, *updated.InventoryQuantity)                     // Unchanged
		assert.Equal(t, []string{"coll_1", "coll_2"}, updated.CollectionIDs) // Unchanged
	})
}

func TestApplyUpdates(t *testing.T) {
	service := NewProductMutationService("", "")

	existing := &models.Product{
		ID:              "prod_123",
		SKU:             "ORIGINAL-SKU",
		Title:           "Original Title",
		Price:           29.99,
		Currency:        "USD",
		InventoryStatus: "IN_STOCK",
		CategoryID:      "cat_original",
		CollectionIDs:   []string{"coll_1"},
		Images:          []string{"img1.jpg"},
	}

	t.Run("should apply all provided updates", func(t *testing.T) {
		newSKU := "NEW-SKU"
		newTitle := "New Title"
		newPrice := 39.99
		newCategory := "cat_new"

		input := UpdateProductInput{
			SKU:        &newSKU,
			Title:      &newTitle,
			Price:      &newPrice,
			CategoryID: &newCategory,
		}

		updated := service.applyUpdates(existing, input)

		assert.Equal(t, "NEW-SKU", updated.SKU)
		assert.Equal(t, "New Title", updated.Title)
		assert.Equal(t, 39.99, updated.Price)
		assert.Equal(t, "cat_new", updated.CategoryID)
		assert.Equal(t, "prod_123", updated.ID) // Unchanged
	})

	t.Run("should preserve unspecified fields", func(t *testing.T) {
		newTitle := "Updated Title Only"

		input := UpdateProductInput{
			Title: &newTitle,
		}

		updated := service.applyUpdates(existing, input)

		assert.Equal(t, "Updated Title Only", updated.Title)
		assert.Equal(t, "ORIGINAL-SKU", updated.SKU)        // Unchanged
		assert.Equal(t, 29.99, updated.Price)               // Unchanged
		assert.Equal(t, "cat_original", updated.CategoryID) // Unchanged
	})
}

func TestDeleteProduct(t *testing.T) {
	t.Run("should delete product", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create product first
		createInput := CreateProductInput{
			SKU:        "DELETE-TEST-001",
			Title:      "Product to Delete",
			Price:      29.99,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)
		productID := createPayload.Product.ID

		// Verify file exists
		filePath := filepath.Join(repoPath, "products/test/DELETE-TEST-001.md")
		_, err = os.Stat(filePath)
		require.NoError(t, err)

		// Mock readProductFromGit
		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return createPayload.Product, "", nil
		}

		// Delete the product
		deleteInput := DeleteProductInput{
			ID: productID,
		}

		deletePayload, err := service.DeleteProduct(ctx, deleteInput)
		require.NoError(t, err)
		require.NotNil(t, deletePayload)
		require.NotNil(t, deletePayload.DeletedProductID)
		assert.Equal(t, productID, *deletePayload.DeletedProductID)

		// Verify file is deleted
		_, err = os.Stat(filePath)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("should delete product with client mutation ID", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create product
		createInput := CreateProductInput{
			SKU:        "DELETE-WITH-ID-001",
			Title:      "Product",
			Price:      10.00,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)

		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return createPayload.Product, "", nil
		}

		// Delete with client mutation ID
		clientMutationID := "test-delete-123"
		deleteInput := DeleteProductInput{
			ClientMutationID: &clientMutationID,
			ID:               createPayload.Product.ID,
		}

		deletePayload, err := service.DeleteProduct(ctx, deleteInput)
		require.NoError(t, err)
		require.NotNil(t, deletePayload.ClientMutationID)
		assert.Equal(t, "test-delete-123", *deletePayload.ClientMutationID)
	})

	t.Run("should commit deletion to git", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create product
		createInput := CreateProductInput{
			SKU:        "DELETE-COMMIT-001",
			Title:      "Product for Commit Test",
			Price:      15.00,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)

		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return createPayload.Product, "", nil
		}

		// Delete product
		deleteInput := DeleteProductInput{
			ID: createPayload.Product.ID,
		}

		_, err = service.DeleteProduct(ctx, deleteInput)
		require.NoError(t, err)

		// Verify git commit
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		ref, err := repo.Head()
		require.NoError(t, err)

		commit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)

		assert.Contains(t, commit.Message, "delete")
		assert.Contains(t, commit.Message, "product")
		assert.Contains(t, commit.Message, "DELETE-COMMIT-001")
	})

	t.Run("should fail for non-existent product", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Try to delete non-existent product
		deleteInput := DeleteProductInput{
			ID: "prod_nonexistent",
		}

		_, err := service.DeleteProduct(ctx, deleteInput)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read product")
	})

	t.Run("should delete product from different categories", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create products in different categories
		categories := []string{"cat_electronics", "cat_books", "cat_clothing"}

		for i, categoryID := range categories {
			sku := fmt.Sprintf("DELETE-CAT-%d", i)
			createInput := CreateProductInput{
				SKU:        sku,
				Title:      "Product",
				Price:      10.00,
				CategoryID: categoryID,
			}

			createPayload, err := service.CreateProduct(ctx, createInput)
			require.NoError(t, err)

			// Delete immediately
			service.readProductFromGit = func(id string) (*models.Product, string, error) {
				return createPayload.Product, "", nil
			}

			deleteInput := DeleteProductInput{
				ID: createPayload.Product.ID,
			}

			_, err = service.DeleteProduct(ctx, deleteInput)
			require.NoError(t, err)

			// Verify file deleted
			categorySlug := strings.TrimPrefix(categoryID, "cat_")
			filePath := filepath.Join(repoPath, "products", categorySlug, sku+".md")
			_, err = os.Stat(filePath)
			assert.True(t, os.IsNotExist(err), "File should be deleted: %s", filePath)
		}
	})

	t.Run("should handle deletion of product with special characters in SKU", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create product with hyphens and underscores
		createInput := CreateProductInput{
			SKU:        "SPECIAL-SKU_123",
			Title:      "Special Product",
			Price:      25.00,
			CategoryID: "cat_test",
		}

		createPayload, err := service.CreateProduct(ctx, createInput)
		require.NoError(t, err)

		service.readProductFromGit = func(id string) (*models.Product, string, error) {
			return createPayload.Product, "", nil
		}

		// Delete product
		deleteInput := DeleteProductInput{
			ID: createPayload.Product.ID,
		}

		_, err = service.DeleteProduct(ctx, deleteInput)
		require.NoError(t, err)

		// Verify file deleted
		filePath := filepath.Join(repoPath, "products/test/SPECIAL-SKU_123.md")
		_, err = os.Stat(filePath)
		assert.True(t, os.IsNotExist(err))
	})
}

func TestEnsureRepoExists(t *testing.T) {
	t.Run("should succeed for existing git repo", func(t *testing.T) {
		tmpDir, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		err := ensureRepoExists(tmpDir)
		assert.NoError(t, err)
	})

	t.Run("should fail for non-git directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "gitstore-not-git-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		err = ensureRepoExists(tmpDir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "git repository not initialized")
	})

	t.Run("should create directory if it doesn't exist", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "gitstore-parent-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		newRepoPath := filepath.Join(tmpDir, "new-repo")

		// Should create the directory
		err = ensureRepoExists(newRepoPath)
		// Will fail because .git doesn't exist, but directory should be created
		assert.Error(t, err) // Expected - no git init yet

		// Check directory was created
		_, err = os.Stat(newRepoPath)
		assert.NoError(t, err)
	})
}
