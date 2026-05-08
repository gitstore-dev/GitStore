// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Base GraphQL resolver

package graph

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/cache"
	"github.com/gitstore-dev/gitstore/api/internal/loader"
	"go.uber.org/zap"
)

// Resolver is the root GraphQL resolver
type Resolver struct {
	logger  *zap.Logger
	cache   *cache.Manager
	service *Service
}

// NewResolver creates a new GraphQL resolver.
// writer is the GitWriter backed by the gRPC client; pass nil to disable writes.
func NewResolver(cacheManager *cache.Manager, writer GitWriter, logger *zap.Logger) *Resolver {
	svc := NewServiceWithWriter(cacheManager, writer, logger)
	return &Resolver{
		logger:  logger,
		cache:   cacheManager,
		service: svc,
	}
}

// getLoaders retrieves data loaders from context
func (r *Resolver) getLoaders(ctx context.Context) *loader.Loaders {
	return loader.FromContext(ctx)
}
