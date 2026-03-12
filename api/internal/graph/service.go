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
	logger       *zap.Logger
}

// NewService creates a new service instance
func NewService(cacheManager *cache.Manager, repoPath string, logger *zap.Logger) *Service {
	return &Service{
		cacheManager: cacheManager,
		repoPath:     repoPath,
		logger:       logger,
	}
}

// GetCatalog retrieves the current catalog from cache
func (s *Service) GetCatalog(ctx context.Context) (*catalog.Catalog, error) {
	cat, err := s.cacheManager.GetCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get catalog: %w", err)
	}
	return cat, nil
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
	if err := s.cacheManager.Reload(ctx); err != nil {
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
	if err := s.cacheManager.Reload(ctx); err != nil {
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

	// Delete file from git
	filePath := filepath.Join("products", id+".md")
	writer := gitclient.NewWriter(s.repoPath, s.logger)

	if err := writer.DeleteFile(ctx, filePath, "Delete product: "+product.Title); err != nil {
		return fmt.Errorf("failed to delete product: %w", err)
	}

	// Reload catalog
	if err := s.cacheManager.Reload(ctx); err != nil {
		s.logger.Error("Failed to reload catalog after delete", zap.Error(err))
	}

	return nil
}

// writeProductToGit writes a product to git as markdown with YAML frontmatter
func (s *Service) writeProductToGit(ctx context.Context, product *catalog.Product, commitMessage string) error {
	// Create markdown content
	content := formatProductMarkdown(product)

	// Write to git
	filePath := filepath.Join("products", product.ID+".md")
	writer := gitclient.NewWriter(s.repoPath, s.logger)

	if err := writer.WriteFile(ctx, filePath, []byte(content), commitMessage); err != nil {
		return fmt.Errorf("failed to write product: %w", err)
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
