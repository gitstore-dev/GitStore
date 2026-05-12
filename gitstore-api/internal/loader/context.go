// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// DataLoader context management - stores loaders in request context

package loader

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
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

// NewLoaders creates a new set of data loaders backed by the datastore.
func NewLoaders(store datastore.Datastore, logger *zap.Logger) *Loaders {
	return &Loaders{
		Product:    NewProductLoader(store, logger),
		Category:   NewCategoryLoader(store, logger),
		Collection: NewCollectionLoader(store, logger),
	}
}

// Middleware creates a middleware that adds loaders to the context.
func Middleware(store datastore.Datastore, logger *zap.Logger) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		loaders := NewLoaders(store, logger)
		return context.WithValue(ctx, loadersKey, loaders)
	}
}

// FromContext retrieves loaders from the context.
func FromContext(ctx context.Context) *Loaders {
	loaders, ok := ctx.Value(loadersKey).(*Loaders)
	if !ok {
		return nil
	}
	return loaders
}

// Clear clears all loader state.
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
