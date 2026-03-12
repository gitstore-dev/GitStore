package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/commerce-projects/gitstore/api/internal/gitclient"
	"github.com/commerce-projects/gitstore/api/internal/models"
)

// ProductMutationService handles product mutation operations
type ProductMutationService struct {
	repoPath              string
	remoteURL             string
	readProductFromGit    func(productID string) (*models.Product, string, error)
	readCategoryFromGit   func(categoryID string) (*models.CategoryMutation, string, error)
	readCollectionFromGit func(collectionID string) (*models.CollectionMutation, string, error)
}

// NewProductMutationService creates a new product mutation service
func NewProductMutationService(repoPath, remoteURL string) *ProductMutationService {
	s := &ProductMutationService{
		repoPath:  repoPath,
		remoteURL: remoteURL,
	}
	// Set default implementations
	s.readProductFromGit = s.defaultReadProductFromGit
	s.readCategoryFromGit = s.defaultReadCategoryFromGit
	s.readCollectionFromGit = s.defaultReadCollectionFromGit
	return s
}

// CreateProductInput represents the input for creating a product
type CreateProductInput struct {
	ClientMutationID  *string
	SKU               string
	Title             string
	Body              *string
	Price             float64
	Currency          *string
	InventoryStatus   *string
	InventoryQuantity *int
	CategoryID        string
	CollectionIDs     []string
	Images            []string
	Metadata          map[string]interface{}
}

// CreateProductPayload represents the payload returned from createProduct
type CreateProductPayload struct {
	ClientMutationID *string
	Product          *models.Product
}

// UpdateProductInput represents the input for updating a product
type UpdateProductInput struct {
	ClientMutationID  *string
	ID                string
	SKU               *string
	Title             *string
	Body              *string
	Price             *float64
	Currency          *string
	InventoryStatus   *string
	InventoryQuantity *int
	CategoryID        *string
	CollectionIDs     []string
	Images            []string
	Metadata          map[string]interface{}
	Version           string // For optimistic locking
}

// UpdateProductPayload represents the payload returned from updateProduct
type UpdateProductPayload struct {
	ClientMutationID *string
	Product          *models.Product
	Conflict         *OptimisticLockConflict
}

// OptimisticLockConflict contains information about a version conflict
type OptimisticLockConflict struct {
	Detected         bool
	CurrentVersion   string
	AttemptedVersion string
	CurrentProduct   *models.Product
	Diff             string
}

// DeleteProductInput represents the input for deleting a product
type DeleteProductInput struct {
	ClientMutationID *string
	ID               string
}

// DeleteProductPayload represents the payload returned from deleteProduct
type DeleteProductPayload struct {
	ClientMutationID *string
	DeletedProductID *string
}

// CreateCategoryInput represents the input for creating a category
type CreateCategoryInput struct {
	ClientMutationID *string
	Name             string
	Slug             string
	ParentID         *string
	DisplayOrder     *int
	Body             *string
}

// CreateCategoryPayload represents the payload returned from createCategory
type CreateCategoryPayload struct {
	ClientMutationID *string
	Category         *models.CategoryMutation
}

// UpdateCategoryInput represents the input for updating a category
type UpdateCategoryInput struct {
	ClientMutationID *string
	ID               string
	Name             *string
	Slug             *string
	ParentID         *string
	DisplayOrder     *int
	Body             *string
	Version          string // For optimistic locking
}

// UpdateCategoryPayload represents the payload returned from updateCategory
type UpdateCategoryPayload struct {
	ClientMutationID *string
	Category         *models.CategoryMutation
	Conflict         *OptimisticLockConflict
}

// DeleteCategoryInput represents the input for deleting a category
type DeleteCategoryInput struct {
	ClientMutationID *string
	ID               string
}

// DeleteCategoryPayload represents the payload returned from deleteCategory
type DeleteCategoryPayload struct {
	ClientMutationID  *string
	DeletedCategoryID *string
}

// CategoryOrderInput represents a category and its new display order
type CategoryOrderInput struct {
	ID           string
	DisplayOrder int
}

// ReorderCategoriesInput represents the input for reordering categories
type ReorderCategoriesInput struct {
	ClientMutationID *string
	Orders           []CategoryOrderInput
}

// ReorderCategoriesPayload represents the payload returned from reorderCategories
type ReorderCategoriesPayload struct {
	ClientMutationID *string
	Categories       []*models.CategoryMutation
}

// CreateCollectionInput represents the input for creating a collection
type CreateCollectionInput struct {
	ClientMutationID *string
	Name             string
	Slug             string
	DisplayOrder     *int
	Body             *string
}

// CreateCollectionPayload represents the payload returned from createCollection
type CreateCollectionPayload struct {
	ClientMutationID *string
	Collection       *models.CollectionMutation
}

// UpdateCollectionInput represents the input for updating a collection
type UpdateCollectionInput struct {
	ClientMutationID *string
	ID               string
	Name             *string
	Slug             *string
	DisplayOrder     *int
	Body             *string
	Version          string // For optimistic locking
}

// UpdateCollectionPayload represents the payload returned from updateCollection
type UpdateCollectionPayload struct {
	ClientMutationID *string
	Collection       *models.CollectionMutation
	Conflict         *OptimisticLockConflict
}

// DeleteCollectionInput represents the input for deleting a collection
type DeleteCollectionInput struct {
	ClientMutationID *string
	ID               string
}

// DeleteCollectionPayload represents the payload returned from deleteCollection
type DeleteCollectionPayload struct {
	ClientMutationID    *string
	DeletedCollectionID *string
}

// CollectionOrderInput represents a collection and its new display order
type CollectionOrderInput struct {
	ID           string
	DisplayOrder int
}

// ReorderCollectionsInput represents the input for reordering collections
type ReorderCollectionsInput struct {
	ClientMutationID *string
	Orders           []CollectionOrderInput
}

// ReorderCollectionsPayload represents the payload returned from reorderCollections
type ReorderCollectionsPayload struct {
	ClientMutationID *string
	Collections      []*models.CollectionMutation
}

// PublishCatalogInput represents the input for publishing the catalog
type PublishCatalogInput struct {
	ClientMutationID *string
	TagName          *string // Optional: custom tag name (defaults to auto-generated semver)
	Message          *string // Optional: custom tag message
}

// PublishCatalogPayload represents the payload returned from publishCatalog
type PublishCatalogPayload struct {
	ClientMutationID *string
	TagName          string
	CommitHash       string
	Success          bool
}

// CreateProduct creates a new product and commits it to git
func (s *ProductMutationService) CreateProduct(ctx context.Context, input CreateProductInput) (*CreateProductPayload, error) {
	// Set defaults
	currency := "USD"
	if input.Currency != nil {
		currency = *input.Currency
	}

	inventoryStatus := "IN_STOCK"
	if input.InventoryStatus != nil {
		inventoryStatus = *input.InventoryStatus
	}

	body := ""
	if input.Body != nil {
		body = *input.Body
	}

	// Create product model
	product, err := models.NewProduct(
		input.SKU,
		input.Title,
		body,
		input.Price,
		currency,
		input.CategoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create product: %w", err)
	}

	// Set optional fields
	product.InventoryStatus = inventoryStatus
	product.InventoryQuantity = input.InventoryQuantity

	if input.CollectionIDs != nil {
		product.CollectionIDs = input.CollectionIDs
	}

	if input.Images != nil {
		product.Images = input.Images
	}

	if input.Metadata != nil {
		product.Metadata = input.Metadata
	}

	// Validate product
	if err := product.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Generate markdown content
	frontMatter := gitclient.ProductFrontMatter{
		ID:                product.ID,
		SKU:               product.SKU,
		Title:             product.Title,
		Price:             product.Price,
		Currency:          product.Currency,
		InventoryStatus:   product.InventoryStatus,
		InventoryQuantity: product.InventoryQuantity,
		CategoryID:        product.CategoryID,
		CollectionIDs:     product.CollectionIDs,
		Images:            product.Images,
		Metadata:          product.Metadata,
		CreatedAt:         product.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         product.UpdatedAt.Format(time.RFC3339),
	}

	markdown, err := gitclient.GenerateProductMarkdown(frontMatter, product.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to generate markdown: %w", err)
	}

	// Determine file path
	categorySlug := models.GetCategorySlug(product.CategoryID)
	filePath := gitclient.GetProductFilePath(product.SKU, categorySlug)

	// Commit the file
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("create", "product", product.SKU, product.Title)
	commitHash, err := commitBuilder.CommitChange(filePath, markdown, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Created product %s (commit: %s)\n", product.SKU, commitHash[:8])

	return &CreateProductPayload{
		ClientMutationID: input.ClientMutationID,
		Product:          product,
	}, nil
}

// ensureRepoExists creates the repository directory if it doesn't exist
func ensureRepoExists(repoPath string) error {
	// Check if directory exists
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		// Create directory
		if err := os.MkdirAll(repoPath, 0755); err != nil {
			return fmt.Errorf("failed to create repo directory: %w", err)
		}

		// Initialize git repository
		// For now, we'll just create the directory
		// The CommitBuilder will handle git init if needed
	} else if err != nil {
		return fmt.Errorf("failed to check repo directory: %w", err)
	}

	// Ensure .git directory exists (basic check)
	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		// Need to initialize git repository
		// CommitBuilder expects an existing repo, so we should initialize it here
		return fmt.Errorf("git repository not initialized at %s (run 'git init' first)", repoPath)
	}

	return nil
}

// UpdateProduct updates an existing product with optimistic locking
func (s *ProductMutationService) UpdateProduct(ctx context.Context, input UpdateProductInput) (*UpdateProductPayload, error) {
	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read existing product from git
	existingProduct, existingContent, err := s.readProductFromGit(input.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read product: %w", err)
	}

	// Check optimistic lock version
	versionChecker := NewVersionChecker()
	if err := versionChecker.CheckVersion(input.Version, existingContent, "product", input.ID); err != nil {
		// Version mismatch - return conflict information
		if vme, ok := err.(*VersionMismatchError); ok {
			// Generate diff
			diffGen := NewDiffGenerator()

			// Create updated product to show what user wanted
			updatedProduct := s.applyUpdates(existingProduct, input)
			updatedContent := s.generateProductContent(updatedProduct)

			diffResult := diffGen.GenerateDiff(existingContent, updatedContent)

			return &UpdateProductPayload{
				ClientMutationID: input.ClientMutationID,
				Product:          nil, // Not updated due to conflict
				Conflict: &OptimisticLockConflict{
					Detected:         true,
					CurrentVersion:   vme.ActualVersion,
					AttemptedVersion: vme.ExpectedVersion,
					CurrentProduct:   existingProduct,
					Diff:             diffResult.FormatDiffForDisplay(),
				},
			}, nil
		}
		return nil, err
	}

	// Apply updates to product
	updatedProduct := s.applyUpdates(existingProduct, input)

	// Update timestamp
	updatedProduct.UpdatedAt = time.Now().UTC()

	// Validate updated product
	if err := updatedProduct.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Generate markdown content
	frontMatter := gitclient.ProductFrontMatter{
		ID:                updatedProduct.ID,
		SKU:               updatedProduct.SKU,
		Title:             updatedProduct.Title,
		Price:             updatedProduct.Price,
		Currency:          updatedProduct.Currency,
		InventoryStatus:   updatedProduct.InventoryStatus,
		InventoryQuantity: updatedProduct.InventoryQuantity,
		CategoryID:        updatedProduct.CategoryID,
		CollectionIDs:     updatedProduct.CollectionIDs,
		Images:            updatedProduct.Images,
		Metadata:          updatedProduct.Metadata,
		CreatedAt:         updatedProduct.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         updatedProduct.UpdatedAt.Format(time.RFC3339),
	}

	markdown, err := gitclient.GenerateProductMarkdown(frontMatter, updatedProduct.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to generate markdown: %w", err)
	}

	// Determine file path (may have changed if category changed)
	categorySlug := models.GetCategorySlug(updatedProduct.CategoryID)
	filePath := gitclient.GetProductFilePath(updatedProduct.SKU, categorySlug)

	// Check if file path changed (category or SKU changed)
	oldCategorySlug := models.GetCategorySlug(existingProduct.CategoryID)
	oldFilePath := gitclient.GetProductFilePath(existingProduct.SKU, oldCategorySlug)

	// Commit the changes
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	var commitHash string
	if oldFilePath != filePath {
		// File moved - delete old and write new
		changes := map[string]string{
			filePath: markdown,
		}

		// Delete old file
		if err := commitBuilder.DeleteFile(oldFilePath); err != nil {
			return nil, fmt.Errorf("failed to delete old file: %w", err)
		}

		commitMsg := gitclient.GenerateCommitMessage("update", "product", updatedProduct.SKU,
			fmt.Sprintf("%s (moved from %s)", updatedProduct.Title, existingProduct.SKU))
		commitHash, err = commitBuilder.CommitMultiple(changes, commitMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to commit: %w", err)
		}
	} else {
		// Simple update
		commitMsg := gitclient.GenerateCommitMessage("update", "product", updatedProduct.SKU, updatedProduct.Title)
		commitHash, err = commitBuilder.CommitChange(filePath, markdown, commitMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to commit: %w", err)
		}
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Updated product %s (commit: %s)\n", updatedProduct.SKU, commitHash[:8])

	return &UpdateProductPayload{
		ClientMutationID: input.ClientMutationID,
		Product:          updatedProduct,
		Conflict:         nil,
	}, nil
}

// defaultReadProductFromGit is the default implementation for reading products
func (s *ProductMutationService) defaultReadProductFromGit(productID string) (*models.Product, string, error) {
	// For now, we need to search for the product file
	// In a real implementation, we'd have an index or cache
	// For testing, we'll use a simplified approach

	// This is a placeholder - in reality, you'd need to:
	// 1. Have a cache/index of products
	// 2. Know the file path from the index
	// 3. Read the file and parse it

	return nil, "", fmt.Errorf("product not found: %s (cache/index not yet implemented)", productID)
}

// applyUpdates applies the update input to an existing product
func (s *ProductMutationService) applyUpdates(existing *models.Product, input UpdateProductInput) *models.Product {
	updated := &models.Product{
		ID:                existing.ID,
		SKU:               existing.SKU,
		Title:             existing.Title,
		Body:              existing.Body,
		Price:             existing.Price,
		Currency:          existing.Currency,
		InventoryStatus:   existing.InventoryStatus,
		InventoryQuantity: existing.InventoryQuantity,
		CategoryID:        existing.CategoryID,
		CollectionIDs:     existing.CollectionIDs,
		Images:            existing.Images,
		Metadata:          existing.Metadata,
		CreatedAt:         existing.CreatedAt,
		UpdatedAt:         existing.UpdatedAt,
	}

	// Apply updates only for provided fields
	if input.SKU != nil {
		updated.SKU = *input.SKU
	}
	if input.Title != nil {
		updated.Title = *input.Title
	}
	if input.Body != nil {
		updated.Body = *input.Body
	}
	if input.Price != nil {
		updated.Price = *input.Price
	}
	if input.Currency != nil {
		updated.Currency = *input.Currency
	}
	if input.InventoryStatus != nil {
		updated.InventoryStatus = *input.InventoryStatus
	}
	if input.InventoryQuantity != nil {
		updated.InventoryQuantity = input.InventoryQuantity
	}
	if input.CategoryID != nil {
		updated.CategoryID = *input.CategoryID
	}
	if input.CollectionIDs != nil {
		updated.CollectionIDs = input.CollectionIDs
	}
	if input.Images != nil {
		updated.Images = input.Images
	}
	if input.Metadata != nil {
		updated.Metadata = input.Metadata
	}

	return updated
}

// generateProductContent generates the full markdown content for a product
func (s *ProductMutationService) generateProductContent(product *models.Product) string {
	frontMatter := gitclient.ProductFrontMatter{
		ID:                product.ID,
		SKU:               product.SKU,
		Title:             product.Title,
		Price:             product.Price,
		Currency:          product.Currency,
		InventoryStatus:   product.InventoryStatus,
		InventoryQuantity: product.InventoryQuantity,
		CategoryID:        product.CategoryID,
		CollectionIDs:     product.CollectionIDs,
		Images:            product.Images,
		Metadata:          product.Metadata,
		CreatedAt:         product.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         product.UpdatedAt.Format(time.RFC3339),
	}

	markdown, _ := gitclient.GenerateProductMarkdown(frontMatter, product.Body)
	return markdown
}

// DeleteProduct deletes an existing product
func (s *ProductMutationService) DeleteProduct(ctx context.Context, input DeleteProductInput) (*DeleteProductPayload, error) {
	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read existing product from git
	existingProduct, _, err := s.readProductFromGit(input.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read product: %w", err)
	}

	// Determine file path
	categorySlug := models.GetCategorySlug(existingProduct.CategoryID)
	filePath := gitclient.GetProductFilePath(existingProduct.SKU, categorySlug)

	// Commit the deletion
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("delete", "product", existingProduct.SKU, existingProduct.Title)
	commitHash, err := commitBuilder.CommitDelete(filePath, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit deletion: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Deleted product %s (commit: %s)\n", existingProduct.SKU, commitHash[:8])

	return &DeleteProductPayload{
		ClientMutationID: input.ClientMutationID,
		DeletedProductID: &input.ID,
	}, nil
}

// CreateCategory creates a new category and commits it to git
func (s *ProductMutationService) CreateCategory(ctx context.Context, input CreateCategoryInput) (*CreateCategoryPayload, error) {
	// Set defaults
	displayOrder := 0
	if input.DisplayOrder != nil {
		displayOrder = *input.DisplayOrder
	}

	body := ""
	if input.Body != nil {
		body = *input.Body
	}

	// Create category model
	category, err := models.NewCategory(input.Name, input.Slug, input.ParentID, displayOrder)
	if err != nil {
		return nil, fmt.Errorf("failed to create category: %w", err)
	}

	category.Body = body

	// Validate category
	if err := category.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Generate markdown content
	frontMatter := gitclient.CategoryFrontMatter{
		ID:           category.ID,
		Name:         category.Name,
		Slug:         category.Slug,
		ParentID:     category.ParentID,
		DisplayOrder: category.DisplayOrder,
		CreatedAt:    category.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    category.UpdatedAt.Format(time.RFC3339),
	}

	// Add description if body is provided
	var description *string
	if body != "" {
		description = &body
		frontMatter.Description = description
	}

	markdown, err := gitclient.GenerateCategoryMarkdown(frontMatter, body)
	if err != nil {
		return nil, fmt.Errorf("failed to generate markdown: %w", err)
	}

	// Determine file path
	filePath := gitclient.GetCategoryFilePath(category.Slug)

	// Commit the file
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("create", "category", category.Slug, category.Name)
	commitHash, err := commitBuilder.CommitChange(filePath, markdown, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Created category %s (commit: %s)\n", category.Slug, commitHash[:8])

	return &CreateCategoryPayload{
		ClientMutationID: input.ClientMutationID,
		Category:         category,
	}, nil
}

// defaultReadCategoryFromGit is the default implementation for reading categories
func (s *ProductMutationService) defaultReadCategoryFromGit(categoryID string) (*models.CategoryMutation, string, error) {
	// For now, we need to search for the category file
	// In a real implementation, we'd have an index or cache
	// For testing, we'll use a simplified approach

	// This is a placeholder - in reality, you'd need to:
	// 1. Have a cache/index of categories
	// 2. Know the file path from the index
	// 3. Read the file and parse it

	return nil, "", fmt.Errorf("category not found: %s (cache/index not yet implemented)", categoryID)
}

// UpdateCategory updates an existing category with optimistic locking
func (s *ProductMutationService) UpdateCategory(ctx context.Context, input UpdateCategoryInput) (*UpdateCategoryPayload, error) {
	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read existing category from git
	existingCategory, existingContent, err := s.readCategoryFromGit(input.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read category: %w", err)
	}

	// Check optimistic lock version
	versionChecker := NewVersionChecker()
	if err := versionChecker.CheckVersion(input.Version, existingContent, "category", input.ID); err != nil {
		// Version mismatch - return conflict information
		if vme, ok := err.(*VersionMismatchError); ok {
			// Generate diff
			diffGen := NewDiffGenerator()

			// Create updated category to show what user wanted
			updatedCategory := s.applyCategoryUpdates(existingCategory, input)
			updatedContent := s.generateCategoryContent(updatedCategory)

			diffResult := diffGen.GenerateDiff(existingContent, updatedContent)

			return &UpdateCategoryPayload{
				ClientMutationID: input.ClientMutationID,
				Category:         nil, // Not updated due to conflict
				Conflict: &OptimisticLockConflict{
					Detected:         true,
					CurrentVersion:   vme.ActualVersion,
					AttemptedVersion: vme.ExpectedVersion,
					CurrentProduct:   nil,
					Diff:             diffResult.FormatDiffForDisplay(),
				},
			}, nil
		}
		return nil, err
	}

	// Apply updates to category
	updatedCategory := s.applyCategoryUpdates(existingCategory, input)

	// Update timestamp
	updatedCategory.UpdatedAt = time.Now().UTC()

	// Validate updated category
	if err := updatedCategory.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Generate markdown content
	frontMatter := gitclient.CategoryFrontMatter{
		ID:           updatedCategory.ID,
		Name:         updatedCategory.Name,
		Slug:         updatedCategory.Slug,
		ParentID:     updatedCategory.ParentID,
		DisplayOrder: updatedCategory.DisplayOrder,
		CreatedAt:    updatedCategory.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    updatedCategory.UpdatedAt.Format(time.RFC3339),
	}

	// Add description if body is provided
	if updatedCategory.Body != "" {
		description := updatedCategory.Body
		frontMatter.Description = &description
	}

	markdown, err := gitclient.GenerateCategoryMarkdown(frontMatter, updatedCategory.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to generate markdown: %w", err)
	}

	// Determine file path (may have changed if slug changed)
	filePath := gitclient.GetCategoryFilePath(updatedCategory.Slug)

	// Check if file path changed (slug changed)
	oldFilePath := gitclient.GetCategoryFilePath(existingCategory.Slug)

	// Commit the changes
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	var commitHash string
	if oldFilePath != filePath {
		// File moved - delete old and write new
		changes := map[string]string{
			filePath: markdown,
		}

		// Delete old file
		if err := commitBuilder.DeleteFile(oldFilePath); err != nil {
			return nil, fmt.Errorf("failed to delete old file: %w", err)
		}

		commitMsg := gitclient.GenerateCommitMessage("update", "category", updatedCategory.Slug,
			fmt.Sprintf("%s (moved from %s)", updatedCategory.Name, existingCategory.Slug))
		commitHash, err = commitBuilder.CommitMultiple(changes, commitMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to commit: %w", err)
		}
	} else {
		// Simple update
		commitMsg := gitclient.GenerateCommitMessage("update", "category", updatedCategory.Slug, updatedCategory.Name)
		commitHash, err = commitBuilder.CommitChange(filePath, markdown, commitMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to commit: %w", err)
		}
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Updated category %s (commit: %s)\n", updatedCategory.Slug, commitHash[:8])

	return &UpdateCategoryPayload{
		ClientMutationID: input.ClientMutationID,
		Category:         updatedCategory,
		Conflict:         nil,
	}, nil
}

// applyCategoryUpdates applies the update input to an existing category
func (s *ProductMutationService) applyCategoryUpdates(existing *models.CategoryMutation, input UpdateCategoryInput) *models.CategoryMutation {
	updated := &models.CategoryMutation{
		ID:           existing.ID,
		Name:         existing.Name,
		Slug:         existing.Slug,
		ParentID:     existing.ParentID,
		DisplayOrder: existing.DisplayOrder,
		Body:         existing.Body,
		CreatedAt:    existing.CreatedAt,
		UpdatedAt:    existing.UpdatedAt,
	}

	// Apply updates only for provided fields
	if input.Name != nil {
		updated.Name = *input.Name
	}
	if input.Slug != nil {
		updated.Slug = *input.Slug
	}
	if input.ParentID != nil {
		updated.ParentID = input.ParentID
	}
	if input.DisplayOrder != nil {
		updated.DisplayOrder = *input.DisplayOrder
	}
	if input.Body != nil {
		updated.Body = *input.Body
	}

	return updated
}

// generateCategoryContent generates the full markdown content for a category
func (s *ProductMutationService) generateCategoryContent(category *models.CategoryMutation) string {
	frontMatter := gitclient.CategoryFrontMatter{
		ID:           category.ID,
		Name:         category.Name,
		Slug:         category.Slug,
		ParentID:     category.ParentID,
		DisplayOrder: category.DisplayOrder,
		CreatedAt:    category.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    category.UpdatedAt.Format(time.RFC3339),
	}

	if category.Body != "" {
		description := category.Body
		frontMatter.Description = &description
	}

	markdown, _ := gitclient.GenerateCategoryMarkdown(frontMatter, category.Body)
	return markdown
}

// DeleteCategory deletes an existing category
func (s *ProductMutationService) DeleteCategory(ctx context.Context, input DeleteCategoryInput) (*DeleteCategoryPayload, error) {
	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read existing category from git
	existingCategory, _, err := s.readCategoryFromGit(input.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read category: %w", err)
	}

	// Determine file path
	filePath := gitclient.GetCategoryFilePath(existingCategory.Slug)

	// Commit the deletion
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("delete", "category", existingCategory.Slug, existingCategory.Name)
	commitHash, err := commitBuilder.CommitDelete(filePath, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit deletion: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Deleted category %s (commit: %s)\n", existingCategory.Slug, commitHash[:8])

	return &DeleteCategoryPayload{
		ClientMutationID:  input.ClientMutationID,
		DeletedCategoryID: &input.ID,
	}, nil
}

// ReorderCategories updates the display order of multiple categories in a single transaction
func (s *ProductMutationService) ReorderCategories(ctx context.Context, input ReorderCategoriesInput) (*ReorderCategoriesPayload, error) {
	if len(input.Orders) == 0 {
		return nil, fmt.Errorf("at least one category order must be specified")
	}

	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read all categories and update their display orders
	var updatedCategories []*models.CategoryMutation
	filesToUpdate := make(map[string]string)

	for _, order := range input.Orders {
		// Read existing category
		category, _, err := s.readCategoryFromGit(order.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to read category %s: %w", order.ID, err)
		}

		// Validate display order
		if err := models.ValidateDisplayOrder(order.DisplayOrder); err != nil {
			return nil, fmt.Errorf("invalid display order for category %s: %w", order.ID, err)
		}

		// Update display order and timestamp
		category.DisplayOrder = order.DisplayOrder
		category.UpdatedAt = time.Now().UTC()

		// Generate markdown content
		markdown := s.generateCategoryContent(category)
		filePath := gitclient.GetCategoryFilePath(category.Slug)
		filesToUpdate[filePath] = markdown

		updatedCategories = append(updatedCategories, category)
	}

	// Commit all changes in a single transaction
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("reorder", "categories", "",
		fmt.Sprintf("%d categories", len(input.Orders)))
	commitHash, err := commitBuilder.CommitMultiple(filesToUpdate, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Reordered %d categories (commit: %s)\n", len(input.Orders), commitHash[:8])

	return &ReorderCategoriesPayload{
		ClientMutationID: input.ClientMutationID,
		Categories:       updatedCategories,
	}, nil
}

// CreateCollection creates a new collection and commits it to git
func (s *ProductMutationService) CreateCollection(ctx context.Context, input CreateCollectionInput) (*CreateCollectionPayload, error) {
	// Set defaults
	displayOrder := 0
	if input.DisplayOrder != nil {
		displayOrder = *input.DisplayOrder
	}

	body := ""
	if input.Body != nil {
		body = *input.Body
	}

	// Create collection model
	collection, err := models.NewCollection(input.Name, input.Slug, displayOrder)
	if err != nil {
		return nil, fmt.Errorf("failed to create collection: %w", err)
	}

	collection.Body = body

	// Validate collection
	if err := collection.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Generate markdown content
	frontMatter := gitclient.CollectionFrontMatter{
		ID:           collection.ID,
		Name:         collection.Name,
		Slug:         collection.Slug,
		DisplayOrder: collection.DisplayOrder,
		CreatedAt:    collection.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    collection.UpdatedAt.Format(time.RFC3339),
	}

	// Add description if body is provided
	var description *string
	if body != "" {
		description = &body
		frontMatter.Description = description
	}

	markdown, err := gitclient.GenerateCollectionMarkdown(frontMatter, body)
	if err != nil {
		return nil, fmt.Errorf("failed to generate markdown: %w", err)
	}

	// Determine file path
	filePath := gitclient.GetCollectionFilePath(collection.Slug)

	// Commit the file
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("create", "collection", collection.Slug, collection.Name)
	commitHash, err := commitBuilder.CommitChange(filePath, markdown, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Created collection %s (commit: %s)\n", collection.Slug, commitHash[:8])

	return &CreateCollectionPayload{
		ClientMutationID: input.ClientMutationID,
		Collection:       collection,
	}, nil
}

// defaultReadCollectionFromGit is the default implementation for reading collections
func (s *ProductMutationService) defaultReadCollectionFromGit(collectionID string) (*models.CollectionMutation, string, error) {
	// For now, we need to search for the collection file
	// In a real implementation, we'd have an index or cache
	// For testing, we'll use a simplified approach

	// This is a placeholder - in reality, you'd need to:
	// 1. Have a cache/index of collections
	// 2. Know the file path from the index
	// 3. Read the file and parse it

	return nil, "", fmt.Errorf("collection not found: %s (cache/index not yet implemented)", collectionID)
}

// UpdateCollection updates an existing collection with optimistic locking
func (s *ProductMutationService) UpdateCollection(ctx context.Context, input UpdateCollectionInput) (*UpdateCollectionPayload, error) {
	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read existing collection from git
	existingCollection, existingContent, err := s.readCollectionFromGit(input.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read collection: %w", err)
	}

	// Check optimistic lock version
	versionChecker := NewVersionChecker()
	if err := versionChecker.CheckVersion(input.Version, existingContent, "collection", input.ID); err != nil {
		// Version mismatch - return conflict information
		if vme, ok := err.(*VersionMismatchError); ok {
			// Generate diff
			diffGen := NewDiffGenerator()

			// Create updated collection to show what user wanted
			updatedCollection := s.applyCollectionUpdates(existingCollection, input)
			updatedContent := s.generateCollectionContent(updatedCollection)

			diffResult := diffGen.GenerateDiff(existingContent, updatedContent)

			return &UpdateCollectionPayload{
				ClientMutationID: input.ClientMutationID,
				Collection:       nil, // Not updated due to conflict
				Conflict: &OptimisticLockConflict{
					Detected:         true,
					CurrentVersion:   vme.ActualVersion,
					AttemptedVersion: vme.ExpectedVersion,
					CurrentProduct:   nil,
					Diff:             diffResult.FormatDiffForDisplay(),
				},
			}, nil
		}
		return nil, err
	}

	// Apply updates to collection
	updatedCollection := s.applyCollectionUpdates(existingCollection, input)

	// Update timestamp
	updatedCollection.UpdatedAt = time.Now().UTC()

	// Validate updated collection
	if err := updatedCollection.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Generate markdown content
	frontMatter := gitclient.CollectionFrontMatter{
		ID:           updatedCollection.ID,
		Name:         updatedCollection.Name,
		Slug:         updatedCollection.Slug,
		DisplayOrder: updatedCollection.DisplayOrder,
		CreatedAt:    updatedCollection.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    updatedCollection.UpdatedAt.Format(time.RFC3339),
	}

	// Add description if body is provided
	if updatedCollection.Body != "" {
		description := updatedCollection.Body
		frontMatter.Description = &description
	}

	markdown, err := gitclient.GenerateCollectionMarkdown(frontMatter, updatedCollection.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to generate markdown: %w", err)
	}

	// Determine file path (may have changed if slug changed)
	filePath := gitclient.GetCollectionFilePath(updatedCollection.Slug)

	// Check if file path changed (slug changed)
	oldFilePath := gitclient.GetCollectionFilePath(existingCollection.Slug)

	// Commit the changes
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	var commitHash string
	if oldFilePath != filePath {
		// File moved - delete old and write new
		changes := map[string]string{
			filePath: markdown,
		}

		// Delete old file
		if err := commitBuilder.DeleteFile(oldFilePath); err != nil {
			return nil, fmt.Errorf("failed to delete old file: %w", err)
		}

		commitMsg := gitclient.GenerateCommitMessage("update", "collection", updatedCollection.Slug,
			fmt.Sprintf("%s (moved from %s)", updatedCollection.Name, existingCollection.Slug))
		commitHash, err = commitBuilder.CommitMultiple(changes, commitMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to commit: %w", err)
		}
	} else {
		// Simple update
		commitMsg := gitclient.GenerateCommitMessage("update", "collection", updatedCollection.Slug, updatedCollection.Name)
		commitHash, err = commitBuilder.CommitChange(filePath, markdown, commitMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to commit: %w", err)
		}
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Updated collection %s (commit: %s)\n", updatedCollection.Slug, commitHash[:8])

	return &UpdateCollectionPayload{
		ClientMutationID: input.ClientMutationID,
		Collection:       updatedCollection,
		Conflict:         nil,
	}, nil
}

// applyCollectionUpdates applies the update input to an existing collection
func (s *ProductMutationService) applyCollectionUpdates(existing *models.CollectionMutation, input UpdateCollectionInput) *models.CollectionMutation {
	updated := &models.CollectionMutation{
		ID:           existing.ID,
		Name:         existing.Name,
		Slug:         existing.Slug,
		DisplayOrder: existing.DisplayOrder,
		Body:         existing.Body,
		CreatedAt:    existing.CreatedAt,
		UpdatedAt:    existing.UpdatedAt,
	}

	// Apply updates only for provided fields
	if input.Name != nil {
		updated.Name = *input.Name
	}
	if input.Slug != nil {
		updated.Slug = *input.Slug
	}
	if input.DisplayOrder != nil {
		updated.DisplayOrder = *input.DisplayOrder
	}
	if input.Body != nil {
		updated.Body = *input.Body
	}

	return updated
}

// generateCollectionContent generates the full markdown content for a collection
func (s *ProductMutationService) generateCollectionContent(collection *models.CollectionMutation) string {
	frontMatter := gitclient.CollectionFrontMatter{
		ID:           collection.ID,
		Name:         collection.Name,
		Slug:         collection.Slug,
		DisplayOrder: collection.DisplayOrder,
		CreatedAt:    collection.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    collection.UpdatedAt.Format(time.RFC3339),
	}

	if collection.Body != "" {
		description := collection.Body
		frontMatter.Description = &description
	}

	markdown, _ := gitclient.GenerateCollectionMarkdown(frontMatter, collection.Body)
	return markdown
}

// DeleteCollection deletes an existing collection
func (s *ProductMutationService) DeleteCollection(ctx context.Context, input DeleteCollectionInput) (*DeleteCollectionPayload, error) {
	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read existing collection from git
	existingCollection, _, err := s.readCollectionFromGit(input.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read collection: %w", err)
	}

	// Determine file path
	filePath := gitclient.GetCollectionFilePath(existingCollection.Slug)

	// Commit the deletion
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("delete", "collection", existingCollection.Slug, existingCollection.Name)
	commitHash, err := commitBuilder.CommitDelete(filePath, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit deletion: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Deleted collection %s (commit: %s)\n", existingCollection.Slug, commitHash[:8])

	return &DeleteCollectionPayload{
		ClientMutationID:    input.ClientMutationID,
		DeletedCollectionID: &input.ID,
	}, nil
}

// ReorderCollections updates the display order of multiple collections in a single transaction
func (s *ProductMutationService) ReorderCollections(ctx context.Context, input ReorderCollectionsInput) (*ReorderCollectionsPayload, error) {
	if len(input.Orders) == 0 {
		return nil, fmt.Errorf("at least one collection order must be specified")
	}

	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Read all collections and update their display orders
	var updatedCollections []*models.CollectionMutation
	filesToUpdate := make(map[string]string)

	for _, order := range input.Orders {
		// Read existing collection
		collection, _, err := s.readCollectionFromGit(order.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to read collection %s: %w", order.ID, err)
		}

		// Validate display order
		if err := models.ValidateDisplayOrder(order.DisplayOrder); err != nil {
			return nil, fmt.Errorf("invalid display order for collection %s: %w", order.ID, err)
		}

		// Update display order and timestamp
		collection.DisplayOrder = order.DisplayOrder
		collection.UpdatedAt = time.Now().UTC()

		// Generate markdown content
		markdown := s.generateCollectionContent(collection)
		filePath := gitclient.GetCollectionFilePath(collection.Slug)
		filesToUpdate[filePath] = markdown

		updatedCollections = append(updatedCollections, collection)
	}

	// Commit all changes in a single transaction
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	commitMsg := gitclient.GenerateCommitMessage("reorder", "collections", "",
		fmt.Sprintf("%d collections", len(input.Orders)))
	commitHash, err := commitBuilder.CommitMultiple(filesToUpdate, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Push to remote (if configured)
	if s.remoteURL != "" {
		pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize push client: %w", err)
		}

		if err := pushClient.PushBranch(); err != nil {
			return nil, fmt.Errorf("failed to push to remote: %w", err)
		}
	}

	// Log success
	fmt.Printf("Reordered %d collections (commit: %s)\n", len(input.Orders), commitHash[:8])

	return &ReorderCollectionsPayload{
		ClientMutationID: input.ClientMutationID,
		Collections:      updatedCollections,
	}, nil
}

// PublishCatalog commits all changes, pushes to remote, and creates a release tag
func (s *ProductMutationService) PublishCatalog(ctx context.Context, input PublishCatalogInput) (*PublishCatalogPayload, error) {
	// Ensure repository exists
	if err := ensureRepoExists(s.repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Check if remote is configured
	if s.remoteURL == "" {
		return nil, fmt.Errorf("remote URL not configured - cannot publish catalog")
	}

	// Initialize commit builder to check for uncommitted changes
	commitBuilder, err := gitclient.NewCommitBuilder(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git: %w", err)
	}

	// Check if there are uncommitted changes
	hasChanges, err := commitBuilder.HasChanges()
	if err != nil {
		return nil, fmt.Errorf("failed to check for changes: %w", err)
	}

	var commitHash string
	if hasChanges {
		// Commit all pending changes
		commitMsg := "chore: publish catalog with all pending changes"
		if input.Message != nil && *input.Message != "" {
			commitMsg = *input.Message
		}

		commitHash, err = commitBuilder.CommitAll(commitMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to commit changes: %w", err)
		}
		fmt.Printf("Committed pending changes (commit: %s)\n", commitHash[:8])
	} else {
		// Get current HEAD commit
		commitHash = commitBuilder.GetCurrentCommitHash()
		if commitHash == "" {
			return nil, fmt.Errorf("failed to get current commit hash")
		}
	}

	// Push to remote
	pushClient, err := gitclient.NewPushClient(s.repoPath, "origin", s.remoteURL)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize push client: %w", err)
	}

	if err := pushClient.PushBranch(); err != nil {
		return nil, fmt.Errorf("failed to push to remote: %w", err)
	}
	fmt.Printf("Pushed changes to remote\n")

	// Create tag
	tagClient, err := gitclient.NewTagClient(s.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tag client: %w", err)
	}

	// Determine tag name
	var tagName string
	if input.TagName != nil && *input.TagName != "" {
		tagName = *input.TagName
	} else {
		// Auto-generate semver tag
		tagName, err = tagClient.GenerateSemverTagName()
		if err != nil {
			return nil, fmt.Errorf("failed to generate tag name: %w", err)
		}
	}

	// Determine tag message
	tagMessage := "Release catalog version " + tagName
	if input.Message != nil && *input.Message != "" {
		tagMessage = *input.Message
	}

	// Create annotated tag
	tagOpts := gitclient.TagOptions{
		Name:    tagName,
		Message: tagMessage,
	}

	_, err = tagClient.CreateTag(tagOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create tag: %w", err)
	}
	fmt.Printf("Created tag %s\n", tagName)

	// Push tag to remote
	if err := tagClient.PushTag(tagName, "origin", s.remoteURL); err != nil {
		return nil, fmt.Errorf("failed to push tag: %w", err)
	}
	fmt.Printf("Pushed tag %s to remote\n", tagName)

	return &PublishCatalogPayload{
		ClientMutationID: input.ClientMutationID,
		TagName:          tagName,
		CommitHash:       commitHash,
		Success:          true,
	}, nil
}
