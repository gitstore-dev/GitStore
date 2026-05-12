// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Collection DataLoader - batches collection lookups to prevent N+1 queries

package loader

import (
	"context"
	"sync"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

// CollectionLoader batches collection lookups
type CollectionLoader struct {
	store  datastore.Datastore
	logger *zap.Logger

	// Batch state
	mu      sync.Mutex
	batch   []string
	waiting []chan []*collectionResult
}

type collectionResult struct {
	collection *catalog.Collection
	err        error
}

// NewCollectionLoader creates a new collection data loader
func NewCollectionLoader(store datastore.Datastore, logger *zap.Logger) *CollectionLoader {
	return &CollectionLoader{
		store:  store,
		logger: logger,
	}
}

// Load loads a single collection by ID (batched)
func (l *CollectionLoader) Load(ctx context.Context, id string) (*catalog.Collection, error) {
	l.mu.Lock()

	index := len(l.batch)
	l.batch = append(l.batch, id)

	resultChan := make(chan []*collectionResult, 1)
	l.waiting = append(l.waiting, resultChan)

	if len(l.batch) == 1 {
		go l.executeBatch(ctx)
	}

	l.mu.Unlock()

	results := <-resultChan

	if index < len(results) {
		return results[index].collection, results[index].err
	}

	return nil, nil
}

// LoadMany loads multiple collections by IDs (batched)
func (l *CollectionLoader) LoadMany(ctx context.Context, ids []string) ([]*catalog.Collection, []error) {
	collections := make([]*catalog.Collection, len(ids))
	errs := make([]error, len(ids))

	for i, id := range ids {
		coll, err := l.Load(ctx, id)
		collections[i] = coll
		errs[i] = err
	}

	return collections, errs
}

func (l *CollectionLoader) executeBatch(ctx context.Context) {
	l.mu.Lock()

	ids := make([]string, len(l.batch))
	copy(ids, l.batch)
	waiting := make([]chan []*collectionResult, len(l.waiting))
	copy(waiting, l.waiting)

	l.batch = nil
	l.waiting = nil

	l.mu.Unlock()

	results := make([]*collectionResult, len(ids))

	l.logger.Debug("Executing collection batch lookup",
		zap.Int("count", len(ids)),
		zap.Strings("ids", ids),
	)

	for i, id := range ids {
		c, err := l.store.GetCollection(ctx, id)
		if err != nil {
			results[i] = &collectionResult{collection: nil, err: nil}
			continue
		}
		results[i] = &collectionResult{
			collection: datastoreCollectionToCatalog(c),
		}
	}

	for _, ch := range waiting {
		ch <- results
		close(ch)
	}
}

// Prime is included for DataLoader interface compatibility.
func (l *CollectionLoader) Prime(_ string, _ *catalog.Collection) {}

// Clear resets the loader state.
func (l *CollectionLoader) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.batch = nil
	l.waiting = nil
}

// datastoreCollectionToCatalog converts a datastore.Collection to a catalog.Collection.
func datastoreCollectionToCatalog(c *datastore.Collection) *catalog.Collection {
	return &catalog.Collection{
		ID:           c.ID,
		Name:         c.Name,
		Slug:         c.Slug,
		DisplayOrder: c.DisplayOrder,
		ProductIDs:   c.ProductIDs,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
		Body:         c.Body,
	}
}
