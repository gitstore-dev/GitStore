// Collection query resolvers

package graph

import (
	"context"
	"sort"

	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"github.com/commerce-projects/gitstore/api/internal/models"
	"go.uber.org/zap"
)

// CollectionById returns a collection by ID
func (r *queryResolver) CollectionById(ctx context.Context, id string) (*models.Collection, error) {
	r.logger.Debug("Fetching collection by ID", zap.String("id", id))

	// Get catalog from cache
	cat, err := r.cache.Get(ctx)
	if err != nil {
		r.logger.Error("Failed to load catalog", zap.Error(err))
		return nil, err
	}

	// Get collection by ID
	catalogColl, ok := cat.GetCollection(id)
	if !ok {
		r.logger.Debug("Collection not found", zap.String("id", id))
		return nil, nil
	}

	modelColl := catalogCollectionToModel(catalogColl)

	r.logger.Debug("Collection found",
		zap.String("id", id),
		zap.String("name", modelColl.Name),
	)

	return modelColl, nil
}

// Collection field resolvers

//lint:ignore U1000 Reserved for future GraphQL field resolver implementation
type collectionResolver struct{ *Resolver }

// ProductCount resolves the number of products in the collection
//lint:ignore U1000 Reserved for future GraphQL field resolver implementation
func (r *collectionResolver) ProductCount(ctx context.Context, obj *models.Collection) (int, error) {
	return obj.ProductCount(), nil
}

// Helper functions

func catalogCollectionToModel(coll *catalog.Collection) *models.Collection {
	return &models.Collection{
		ID:           coll.ID,
		Name:         coll.Name,
		Slug:         coll.Slug,
		DisplayOrder: coll.DisplayOrder,
		ProductIDs:   coll.ProductIDs,
		Body:         coll.Body,
		CreatedAt:    coll.CreatedAt,
		UpdatedAt:    coll.UpdatedAt,
	}
}

// GetProductsForCollection returns all products in a collection
func GetProductsForCollection(ctx context.Context, cat *catalog.Catalog, collectionID string) ([]*catalog.Product, error) {
	collection, ok := cat.GetCollection(collectionID)
	if !ok {
		return []*catalog.Product{}, nil
	}

	// Get products by IDs
	result := make([]*catalog.Product, 0, len(collection.ProductIDs))

	for _, productID := range collection.ProductIDs {
		product, ok := cat.GetProduct(productID)
		if ok {
			result = append(result, product)
		}
		// Skip products that don't exist (orphaned references)
	}

	// Sort by title
	sort.Slice(result, func(i, j int) bool {
		return result[i].Title < result[j].Title
	})

	return result, nil
}
