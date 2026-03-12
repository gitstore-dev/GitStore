// Type converters between catalog and GraphQL models

package graph

import (
	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"github.com/commerce-projects/gitstore/api/internal/graph/model"
	"github.com/commerce-projects/gitstore/api/internal/graph/scalar"
)

// CatalogProductToGraphQL converts a catalog product to a GraphQL product
func CatalogProductToGraphQL(p *catalog.Product) *model.Product {
	if p == nil {
		return nil
	}

	return &model.Product{
		ID:                p.ID,
		Title:             p.Title,
		Sku:               &p.SKU,
		Price:             scalar.Decimal(p.Price),
		Currency:          &p.Currency,
		Body:              &p.Body,
		InventoryStatus:   model.InventoryStatus(p.InventoryStatus),
		InventoryQuantity: p.InventoryQuantity,
		Category:          nil, // TODO: lookup category if needed
		Collections:       []*model.Collection{}, // TODO: lookup collections if needed
		Images:            p.Images,
		Metadata:          p.Metadata,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
}

// CatalogCategoryToGraphQL converts a catalog category to a GraphQL category
func CatalogCategoryToGraphQL(c *catalog.Category) *model.Category {
	if c == nil {
		return nil
	}

	return &model.Category{
		ID:          c.ID,
		Name:        c.Name,
		Slug:        c.Slug,
		Body:        &c.Body,
		Parent:      nil, // TODO: lookup parent if needed
		Children:    []*model.Category{}, // TODO: lookup children if needed
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

// CatalogCollectionToGraphQL converts a catalog collection to a GraphQL collection
func CatalogCollectionToGraphQL(c *catalog.Collection) *model.Collection {
	if c == nil {
		return nil
	}

	return &model.Collection{
		ID:         c.ID,
		Name:       c.Name,
		Slug:       c.Slug,
		Body:       &c.Body,
		ProductIds: c.ProductIDs,
		Products:   []*model.Product{}, // TODO: lookup products if needed
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
}
