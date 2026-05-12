// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Product DataLoader - batches product lookups to prevent N+1 queries

package loader

import (
	"context"
	"sync"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

// ProductLoader batches product lookups
type ProductLoader struct {
	store  datastore.Datastore
	logger *zap.Logger

	// Batch state
	mu      sync.Mutex
	batch   []string
	waiting []chan []*productResult
}

type productResult struct {
	product *catalog.Product
	err     error
}

// NewProductLoader creates a new product data loader
func NewProductLoader(store datastore.Datastore, logger *zap.Logger) *ProductLoader {
	return &ProductLoader{
		store:  store,
		logger: logger,
	}
}

// Load loads a single product by ID (batched)
func (l *ProductLoader) Load(ctx context.Context, id string) (*catalog.Product, error) {
	l.mu.Lock()

	index := len(l.batch)
	l.batch = append(l.batch, id)

	resultChan := make(chan []*productResult, 1)
	l.waiting = append(l.waiting, resultChan)

	if len(l.batch) == 1 {
		go l.executeBatch(ctx)
	}

	l.mu.Unlock()

	results := <-resultChan

	if index < len(results) {
		return results[index].product, results[index].err
	}

	return nil, nil
}

// LoadMany loads multiple products by IDs (batched)
func (l *ProductLoader) LoadMany(ctx context.Context, ids []string) ([]*catalog.Product, []error) {
	products := make([]*catalog.Product, len(ids))
	errs := make([]error, len(ids))

	for i, id := range ids {
		prod, err := l.Load(ctx, id)
		products[i] = prod
		errs[i] = err
	}

	return products, errs
}

func (l *ProductLoader) executeBatch(ctx context.Context) {
	l.mu.Lock()

	ids := make([]string, len(l.batch))
	copy(ids, l.batch)
	waiting := make([]chan []*productResult, len(l.waiting))
	copy(waiting, l.waiting)

	l.batch = nil
	l.waiting = nil

	l.mu.Unlock()

	results := make([]*productResult, len(ids))

	l.logger.Debug("Executing product batch lookup",
		zap.Int("count", len(ids)),
		zap.Strings("ids", ids),
	)

	for i, id := range ids {
		p, err := l.store.GetProduct(ctx, id)
		if err != nil {
			results[i] = &productResult{product: nil, err: nil}
			continue
		}
		results[i] = &productResult{
			product: datastoreProductToCatalog(p),
		}
	}

	for _, ch := range waiting {
		ch <- results
		close(ch)
	}
}

// Prime is included for DataLoader interface compatibility.
func (l *ProductLoader) Prime(_ string, _ *catalog.Product) {}

// Clear resets the loader state.
func (l *ProductLoader) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.batch = nil
	l.waiting = nil
}

// datastoreProductToCatalog converts a datastore.Product to a catalog.Product.
func datastoreProductToCatalog(p *datastore.Product) *catalog.Product {
	return &catalog.Product{
		ID:                p.ID,
		SKU:               p.SKU,
		Title:             p.Title,
		Price:             p.Price,
		Currency:          p.Currency,
		InventoryStatus:   p.InventoryStatus,
		InventoryQuantity: p.InventoryQuantity,
		CategoryID:        p.CategoryID,
		CollectionIDs:     p.CollectionIDs,
		Images:            p.Images,
		Metadata:          p.Metadata,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
		Body:              p.Body,
	}
}
