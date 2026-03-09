// DataLoader context management - stores loaders in request context

package loader

import (
	"context"

	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

// Loaders holds all data loaders for a request
type Loaders struct {
	Product    *ProductLoader
	Category   *CategoryLoader
	Collection *CollectionLoader
}

type contextKey string

const loadersKey contextKey = "dataloaders"

// NewLoaders creates a new set of data loaders for a catalog
func NewLoaders(cat *catalog.Catalog, logger *zap.Logger) *Loaders {
	return &Loaders{
		Product:    NewProductLoader(cat, logger),
		Category:   NewCategoryLoader(cat, logger),
		Collection: NewCollectionLoader(cat, logger),
	}
}

// Middleware creates a middleware that adds loaders to the context
func Middleware(cat *catalog.Catalog, logger *zap.Logger) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		loaders := NewLoaders(cat, logger)
		return context.WithValue(ctx, loadersKey, loaders)
	}
}

// FromContext retrieves loaders from the context
func FromContext(ctx context.Context) *Loaders {
	loaders, ok := ctx.Value(loadersKey).(*Loaders)
	if !ok {
		// Return nil loaders - callers should handle this gracefully
		return nil
	}
	return loaders
}

// Clear clears all loader state (should be called on catalog reload)
func (l *Loaders) Clear() {
	if l.Product != nil {
		l.Product.Clear()
	}
	if l.Category != nil {
		l.Category.Clear()
	}
	if l.Collection != nil {
		l.Collection.Clear()
	}
}
