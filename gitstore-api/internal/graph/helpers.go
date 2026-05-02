package graph

import (
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

// getProductsInCategoryTree returns all products belonging to a category or any of its descendants.
func getProductsInCategoryTree(cat *catalog.Catalog, categoryID string) ([]*catalog.Product, error) {
	// Collect all category IDs in the subtree rooted at categoryID.
	subIDs := map[string]struct{}{categoryID: {}}
	for _, c := range cat.AllCategories() {
		if isDescendantOf(c, categoryID) {
			subIDs[c.ID] = struct{}{}
		}
	}

	var products []*catalog.Product
	for _, p := range cat.AllProducts() {
		if _, ok := subIDs[p.CategoryID]; ok {
			products = append(products, p)
		}
	}
	return products, nil
}

// isDescendantOf reports whether c is a descendant of the category with the given ID.
func isDescendantOf(c *catalog.Category, ancestorID string) bool {
	cur := c.Parent
	for cur != nil {
		if cur.ID == ancestorID {
			return true
		}
		cur = cur.Parent
	}
	return false
}

// getProductsInCollection returns all products that list the given collection ID.
func getProductsInCollection(cat *catalog.Catalog, collectionID string) []*catalog.Product {
	var products []*catalog.Product
	for _, p := range cat.AllProducts() {
		for _, cid := range p.CollectionIDs {
			if cid == collectionID {
				products = append(products, p)
				break
			}
		}
	}
	return products
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
