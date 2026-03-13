// Service layer for GraphQL resolvers
// Handles CRUD operations with git persistence

package graph

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/commerce-projects/gitstore/api/internal/cache"
	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"github.com/commerce-projects/gitstore/api/internal/gitclient"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Service provides business logic for GraphQL operations
type Service struct {
	cacheManager *cache.Manager
	repoPath     string
	gitClient    *gitclient.HTTPGitClient
	logger       *zap.Logger
}

// NewService creates a new service instance
func NewService(cacheManager *cache.Manager, repoPath string, gitServerURL string, logger *zap.Logger) *Service {
	// Create HTTP git client
	gitClient := gitclient.NewHTTPGitClient(gitServerURL, "catalog.git", repoPath, logger)

	return &Service{
		cacheManager: cacheManager,
		repoPath:     repoPath,
		gitClient:    gitClient,
		logger:       logger,
	}
}

// GetCatalog retrieves the current catalog from cache
func (s *Service) GetCatalog(ctx context.Context) (*catalog.Catalog, error) {
	cat, err := s.cacheManager.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get catalog: %w", err)
	}
	return cat, nil
}

// GetProducts retrieves all products from catalog with optional filtering
func (s *Service) GetProducts(ctx context.Context, categoryID *string) ([]*catalog.Product, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	products := cat.AllProducts()

	// Filter by category if specified
	if categoryID != nil && *categoryID != "" {
		var filtered []*catalog.Product
		for _, p := range products {
			if p.CategoryID == *categoryID {
				filtered = append(filtered, p)
			}
		}
		products = filtered
	}

	return products, nil
}

// GetProductByID retrieves a product by ID
func (s *Service) GetProductByID(ctx context.Context, id string) (*catalog.Product, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	product, ok := cat.GetProduct(id)
	if !ok {
		return nil, fmt.Errorf("product not found: %s", id)
	}

	return product, nil
}

// GetProductBySKU retrieves a product by SKU
func (s *Service) GetProductBySKU(ctx context.Context, sku string) (*catalog.Product, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	product, ok := cat.GetProductBySKU(sku)
	if !ok {
		return nil, fmt.Errorf("product not found with SKU: %s", sku)
	}

	return product, nil
}

// GetCategories returns all categories
func (s *Service) GetCategories(ctx context.Context) ([]*catalog.Category, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	return cat.AllCategories(), nil
}

// GetCategoryByID returns a category by ID
func (s *Service) GetCategoryByID(ctx context.Context, id string) (*catalog.Category, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	category, ok := cat.GetCategory(id)
	if !ok {
		return nil, fmt.Errorf("category not found: %s", id)
	}

	return category, nil
}

// GetCategoryBySlug returns a category by slug
func (s *Service) GetCategoryBySlug(ctx context.Context, slug string) (*catalog.Category, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	category, ok := cat.GetCategoryBySlug(slug)
	if !ok {
		return nil, fmt.Errorf("category not found with slug: %s", slug)
	}

	return category, nil
}

// GetCollections returns all collections
func (s *Service) GetCollections(ctx context.Context) ([]*catalog.Collection, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	return cat.AllCollections(), nil
}

// GetCollectionByID returns a collection by ID
func (s *Service) GetCollectionByID(ctx context.Context, id string) (*catalog.Collection, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	collection, ok := cat.GetCollection(id)
	if !ok {
		return nil, fmt.Errorf("collection not found: %s", id)
	}

	return collection, nil
}

// GetCollectionBySlug returns a collection by slug
func (s *Service) GetCollectionBySlug(ctx context.Context, slug string) (*catalog.Collection, error) {
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	collection, ok := cat.GetCollectionBySlug(slug)
	if !ok {
		return nil, fmt.Errorf("collection not found with slug: %s", slug)
	}

	return collection, nil
}

// CreateProduct creates a new product and commits to git
func (s *Service) CreateProduct(ctx context.Context, input map[string]interface{}) (*catalog.Product, error) {
	// Generate ID
	id := uuid.New().String()

	// Create product struct
	now := time.Now()
	product := &catalog.Product{
		ID:        id,
		SKU:       getStringOrEmpty(input, "sku"),
		Title:     getStringOrEmpty(input, "title"),
		Price:     getFloatOrZero(input, "price"),
		Currency:  getStringOr(input, "currency", "USD"),
		Body:      getStringOrEmpty(input, "body"),
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Handle optional fields
	if status, ok := input["inventoryStatus"].(string); ok {
		product.InventoryStatus = status
	}
	if qty, ok := input["inventoryQuantity"].(int); ok {
		product.InventoryQuantity = &qty
	}
	if categoryID, ok := input["categoryId"].(string); ok {
		product.CategoryID = categoryID
	}
	if collectionIDs, ok := input["collectionIds"].([]string); ok {
		product.CollectionIDs = collectionIDs
	}
	if images, ok := input["images"].([]string); ok {
		product.Images = images
	}
	if metadata, ok := input["metadata"].(map[string]interface{}); ok {
		product.Metadata = metadata
	}

	// Write to git
	if err := s.writeProductToGit(ctx, product, "Create product: "+product.Title); err != nil {
		return nil, err
	}

	// Reload catalog
	if _, err := s.cacheManager.Reload(ctx); err != nil {
		s.logger.Error("Failed to reload catalog after create", zap.Error(err))
	}

	return product, nil
}

// UpdateProduct updates an existing product
func (s *Service) UpdateProduct(ctx context.Context, id string, input map[string]interface{}) (*catalog.Product, error) {
	// Get existing product
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}

	existing, ok := cat.GetProduct(id)
	if !ok {
		return nil, fmt.Errorf("product not found: %s", id)
	}

	// Create updated product
	product := *existing
	product.UpdatedAt = time.Now()

	// Update fields if provided
	if title, ok := input["title"].(string); ok {
		product.Title = title
	}
	if sku, ok := input["sku"].(string); ok {
		product.SKU = sku
	}
	if price, ok := input["price"].(float64); ok {
		product.Price = price
	}
	if currency, ok := input["currency"].(string); ok {
		product.Currency = currency
	}
	if body, ok := input["body"].(string); ok {
		product.Body = body
	}
	if status, ok := input["inventoryStatus"].(string); ok {
		product.InventoryStatus = status
	}
	if qty, ok := input["inventoryQuantity"].(int); ok {
		product.InventoryQuantity = &qty
	}
	if categoryID, ok := input["categoryId"].(string); ok {
		product.CategoryID = categoryID
	}
	if collectionIDs, ok := input["collectionIds"].([]string); ok {
		product.CollectionIDs = collectionIDs
	}
	if images, ok := input["images"].([]string); ok {
		product.Images = images
	}
	if metadata, ok := input["metadata"].(map[string]interface{}); ok {
		product.Metadata = metadata
	}

	// Write to git
	if err := s.writeProductToGit(ctx, &product, "Update product: "+product.Title); err != nil {
		return nil, err
	}

	// Reload catalog
	if _, err := s.cacheManager.Reload(ctx); err != nil {
		s.logger.Error("Failed to reload catalog after update", zap.Error(err))
	}

	return &product, nil
}

// DeleteProduct deletes a product
func (s *Service) DeleteProduct(ctx context.Context, id string) error {
	// Get existing product
	cat, err := s.GetCatalog(ctx)
	if err != nil {
		return err
	}

	product, ok := cat.GetProduct(id)
	if !ok {
		return fmt.Errorf("product not found: %s", id)
	}

	// Delete file via git-server HTTP
	filePath := filepath.Join("products", id+".md")
	if err := s.gitClient.PushDelete(ctx, filePath, "Delete product: "+product.Title); err != nil {
		return fmt.Errorf("failed to push deletion to git-server: %w", err)
	}

	// Reload catalog
	if _, err := s.cacheManager.Reload(ctx); err != nil {
		s.logger.Error("Failed to reload catalog after delete", zap.Error(err))
	}

	return nil
}

// writeProductToGit writes a product to git as markdown with YAML frontmatter
func (s *Service) writeProductToGit(ctx context.Context, product *catalog.Product, commitMessage string) error {
	// Create markdown content
	content := formatProductMarkdown(product)

	// Push to git-server via HTTP
	filePath := filepath.Join("products", product.ID+".md")
	if err := s.gitClient.PushChange(ctx, filePath, content, commitMessage); err != nil {
		return fmt.Errorf("failed to push to git-server: %w", err)
	}

	return nil
}

// formatProductMarkdown formats a product as markdown with YAML frontmatter
func formatProductMarkdown(p *catalog.Product) string {
	content := "---\n"
	content += fmt.Sprintf("id: %s\n", p.ID)
	content += fmt.Sprintf("sku: %s\n", p.SKU)
	content += fmt.Sprintf("title: %s\n", p.Title)
	content += fmt.Sprintf("price: %.2f\n", p.Price)
	content += fmt.Sprintf("currency: %s\n", p.Currency)

	if p.InventoryStatus != "" {
		content += fmt.Sprintf("inventory_status: %s\n", p.InventoryStatus)
	}
	if p.InventoryQuantity != nil {
		content += fmt.Sprintf("inventory_quantity: %d\n", *p.InventoryQuantity)
	}
	if p.CategoryID != "" {
		content += fmt.Sprintf("category_id: %s\n", p.CategoryID)
	}
	if len(p.CollectionIDs) > 0 {
		content += "collection_ids:\n"
		for _, id := range p.CollectionIDs {
			content += fmt.Sprintf("  - %s\n", id)
		}
	}
	if len(p.Images) > 0 {
		content += "images:\n"
		for _, img := range p.Images {
			content += fmt.Sprintf("  - %s\n", img)
		}
	}

	content += fmt.Sprintf("created_at: %s\n", p.CreatedAt.Format(time.RFC3339))
	content += fmt.Sprintf("updated_at: %s\n", p.UpdatedAt.Format(time.RFC3339))
	content += "---\n\n"

	if p.Body != "" {
		content += p.Body + "\n"
	}

	return content
}

// Helper functions
func getStringOrEmpty(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringOr(m map[string]interface{}, key, defaultVal string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return defaultVal
}

func getFloatOrZero(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0.0
}
