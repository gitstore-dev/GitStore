// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
)

// Helper functions for GraphQL resolvers

func generateID() string {
	return uuid.New().String()
}

func stringOrDefault(s *string, def string) string {
	if s != nil {
		return *s
	}
	return def
}

func intOrDefault(i *int32, def int32) int32 {
	if i != nil {
		return *i
	}
	return def
}

// getCatalogStats returns product/category/collection counts from the datastore.
func (r *Resolver) getCatalogStats(ctx context.Context) *model.CatalogStats {
	products, _ := r.service.GetProducts(ctx, nil)
	categories, _ := r.service.GetCategories(ctx)
	collections, _ := r.service.GetCollections(ctx)
	return &model.CatalogStats{
		ProductCount:       int32(len(products)),
		CategoryCount:      int32(len(categories)),
		CollectionCount:    int32(len(collections)),
		OrphanedReferences: 0,
	}
}

// applyProductFilters filters a product slice by the fields set in ProductFilter.
func applyProductFilters(products []*catalog.Product, filter *model.ProductFilter) []*catalog.Product {
	if filter == nil {
		return products
	}

	filtered := make([]*catalog.Product, 0, len(products))
	for _, p := range products {
		// Filter by collection ID
		if filter.CollectionID != nil {
			found := false
			for _, collID := range p.CollectionIDs {
				if collID == *filter.CollectionID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Filter by inventory status
		if filter.InventoryStatus != nil {
			if string(*filter.InventoryStatus) != p.InventoryStatus {
				continue
			}
		}

		// Filter by price range
		if filter.PriceMin != nil {
			minPrice, _ := filter.PriceMin.Float64()
			if p.Price < minPrice {
				continue
			}
		}
		if filter.PriceMax != nil {
			maxPrice, _ := filter.PriceMax.Float64()
			if p.Price > maxPrice {
				continue
			}
		}

		filtered = append(filtered, p)
	}
	return filtered
}
