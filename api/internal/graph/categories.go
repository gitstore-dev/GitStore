// Category query resolvers

package graph

import (
	"context"
	"sort"

	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"github.com/commerce-projects/gitstore/api/internal/models"
	"go.uber.org/zap"
)

// CategoryById returns a category by ID
func (r *queryResolver) CategoryById(ctx context.Context, id string) (*models.Category, error) {
	r.logger.Debug("Fetching category by ID", zap.String("id", id))

	// Get catalog from cache
	cat, err := r.cache.Get(ctx)
	if err != nil {
		r.logger.Error("Failed to load catalog", zap.Error(err))
		return nil, err
	}

	// Get category by ID
	catalogCat, ok := cat.GetCategory(id)
	if !ok {
		r.logger.Debug("Category not found", zap.String("id", id))
		return nil, nil
	}

	// Build full tree to get parent/children relationships
	tree := buildCategoryTree(cat.AllCategories())

	// Get the category with relationships from tree
	modelCat, ok := tree.GetCategory(catalogCat.ID)
	if !ok {
		// This shouldn't happen, but handle gracefully
		modelCat = catalogCategoryToModel(catalogCat)
	}

	r.logger.Debug("Category found",
		zap.String("id", id),
		zap.String("name", modelCat.Name),
	)

	return modelCat, nil
}

// Category field resolvers

type categoryResolver struct{ *Resolver }

// Parent resolves the parent category
func (r *categoryResolver) Parent(ctx context.Context, obj *models.Category) (*models.Category, error) {
	if obj.ParentID == nil {
		return nil, nil
	}

	// Parent should already be set by tree builder
	if obj.Parent != nil {
		return obj.Parent, nil
	}

	// Fallback: use DataLoader if available
	loaders := r.getLoaders(ctx)
	if loaders != nil && loaders.Category != nil {
		catalogCat, err := loaders.Category.Load(ctx, *obj.ParentID)
		if err != nil {
			return nil, err
		}
		if catalogCat != nil {
			return catalogCategoryToModel(catalogCat), nil
		}
		return nil, nil
	}

	// Final fallback: direct catalog lookup
	cat, err := r.cache.Get(ctx)
	if err != nil {
		return nil, err
	}

	catalogCat, ok := cat.GetCategory(*obj.ParentID)
	if !ok {
		r.logger.Warn("Parent category not found",
			zap.String("category_id", obj.ID),
			zap.String("parent_id", *obj.ParentID),
		)
		return nil, nil
	}

	return catalogCategoryToModel(catalogCat), nil
}

// Children resolves child categories
func (r *categoryResolver) Children(ctx context.Context, obj *models.Category) ([]*models.Category, error) {
	// Children should already be set by tree builder
	if obj.Children != nil {
		return obj.Children, nil
	}

	// Return empty list if no children
	return []*models.Category{}, nil
}

// Path resolves the path from root to current category
func (r *categoryResolver) Path(ctx context.Context, obj *models.Category) ([]string, error) {
	path := make([]string, 0, len(obj.Path)+1)

	for _, ancestor := range obj.Path {
		path = append(path, ancestor.Name)
	}
	path = append(path, obj.Name)

	return path, nil
}

// Depth resolves the depth in the tree
func (r *categoryResolver) Depth(ctx context.Context, obj *models.Category) (int, error) {
	return obj.Depth, nil
}

// Helper functions

func catalogCategoryToModel(cat *catalog.Category) *models.Category {
	return &models.Category{
		ID:           cat.ID,
		Name:         cat.Name,
		Slug:         cat.Slug,
		ParentID:     cat.ParentID,
		DisplayOrder: cat.DisplayOrder,
		Body:         cat.Body,
		CreatedAt:    cat.CreatedAt,
		UpdatedAt:    cat.UpdatedAt,
		Children:     []*models.Category{},
		Path:         []*models.Category{},
		Depth:        0,
	}
}

func buildCategoryTree(catalogCategories []*catalog.Category) *models.CategoryTree {
	tree := models.NewCategoryTree()

	for _, cat := range catalogCategories {
		modelCat := catalogCategoryToModel(cat)
		tree.AddCategory(modelCat)
	}

	tree.Build()
	return tree
}

// GetProductsForCategory returns all products in a category (including subcategories)
func GetProductsForCategory(ctx context.Context, cat *catalog.Catalog, categoryID string) ([]*catalog.Product, error) {
	// Build category tree to get descendants
	tree := buildCategoryTree(cat.AllCategories())

	category, ok := tree.GetCategory(categoryID)
	if !ok {
		return []*catalog.Product{}, nil
	}

	// Get all descendant IDs
	descendantIDs := category.GetDescendantIDs()
	targetIDs := append([]string{categoryID}, descendantIDs...)

	// Filter products by category
	allProducts := cat.AllProducts()
	result := make([]*catalog.Product, 0)

	for _, product := range allProducts {
		for _, targetID := range targetIDs {
			if product.CategoryID == targetID {
				result = append(result, product)
				break
			}
		}
	}

	// Sort by title
	sort.Slice(result, func(i, j int) bool {
		return result[i].Title < result[j].Title
	})

	return result, nil
}
