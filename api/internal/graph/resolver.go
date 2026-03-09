// Base GraphQL resolver

package graph

import (
	"context"

	"github.com/commerce-projects/gitstore/api/internal/cache"
	"github.com/commerce-projects/gitstore/api/internal/loader"
	"github.com/commerce-projects/gitstore/api/internal/logger"
	"go.uber.org/zap"
)

// Resolver is the root GraphQL resolver
type Resolver struct {
	logger *zap.Logger
	cache  *cache.Manager
}

// NewResolver creates a new GraphQL resolver
func NewResolver(cacheManager *cache.Manager) *Resolver {
	return &Resolver{
		logger: logger.Log,
		cache:  cacheManager,
	}
}

// getLoaders retrieves data loaders from context
func (r *Resolver) getLoaders(ctx context.Context) *loader.Loaders {
	return loader.FromContext(ctx)
}

// Query returns QueryResolver interface
func (r *Resolver) Query() QueryResolver {
	return &queryResolver{r}
}

// Mutation returns MutationResolver interface
func (r *Resolver) Mutation() MutationResolver {
	return &mutationResolver{r}
}

// Category returns CategoryResolver interface
func (r *Resolver) Category() CategoryResolver {
	return &categoryResolver{r}
}

// Collection returns CollectionResolver interface
func (r *Resolver) Collection() CollectionResolver {
	return &collectionResolver{r}
}

type queryResolver struct{ *Resolver }
type mutationResolver struct{ *Resolver }

// QueryResolver interface
type QueryResolver interface{}

// MutationResolver interface
type MutationResolver interface{}

// CategoryResolver interface
type CategoryResolver interface{}

// CollectionResolver interface
type CollectionResolver interface{}
