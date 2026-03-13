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
	logger  *zap.Logger
	cache   *cache.Manager
	service *Service
}

// NewResolver creates a new GraphQL resolver
func NewResolver(cacheManager *cache.Manager, repoPath string, gitServerURL string) *Resolver {
	return &Resolver{
		logger:  logger.Log,
		cache:   cacheManager,
		service: NewService(cacheManager, repoPath, gitServerURL, logger.Log),
	}
}

// getLoaders retrieves data loaders from context
//lint:ignore U1000 Reserved for future DataLoader implementation
func (r *Resolver) getLoaders(ctx context.Context) *loader.Loaders {
	return loader.FromContext(ctx)
}
