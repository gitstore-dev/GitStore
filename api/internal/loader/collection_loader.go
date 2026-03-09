// Collection DataLoader - batches collection lookups to prevent N+1 queries

package loader

import (
	"context"
	"sync"

	"github.com/yourorg/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

// CollectionLoader batches collection lookups
type CollectionLoader struct {
	catalog *catalog.Catalog
	logger  *zap.Logger

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
func NewCollectionLoader(cat *catalog.Catalog, logger *zap.Logger) *CollectionLoader {
	return &CollectionLoader{
		catalog: cat,
		logger:  logger,
	}
}

// Load loads a single collection by ID (batched)
func (l *CollectionLoader) Load(ctx context.Context, id string) (*catalog.Collection, error) {
	l.mu.Lock()

	// Add this ID to the batch
	index := len(l.batch)
	l.batch = append(l.batch, id)

	// Create a channel to receive results
	resultChan := make(chan []*collectionResult, 1)
	l.waiting = append(l.waiting, resultChan)

	// If this is the first request in the batch, schedule execution
	if len(l.batch) == 1 {
		go l.executeBatch()
	}

	l.mu.Unlock()

	// Wait for batch to complete
	results := <-resultChan

	// Get our result by index
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

// executeBatch executes the batched lookups
func (l *CollectionLoader) executeBatch() {
	l.mu.Lock()

	// Copy batch and waiting list
	ids := make([]string, len(l.batch))
	copy(ids, l.batch)
	waiting := make([]chan []*collectionResult, len(l.waiting))
	copy(waiting, l.waiting)

	// Reset for next batch
	l.batch = nil
	l.waiting = nil

	l.mu.Unlock()

	// Execute batch lookup
	results := make([]*collectionResult, len(ids))

	l.logger.Debug("Executing collection batch lookup",
		zap.Int("count", len(ids)),
		zap.Strings("ids", ids),
	)

	for i, id := range ids {
		coll, ok := l.catalog.GetCollection(id)
		if ok {
			results[i] = &collectionResult{collection: coll, err: nil}
		} else {
			results[i] = &collectionResult{collection: nil, err: nil}
		}
	}

	// Send results to all waiting channels
	for _, ch := range waiting {
		ch <- results
		close(ch)
	}
}

// Prime preloads a collection into the cache
func (l *CollectionLoader) Prime(id string, collection *catalog.Collection) {
	// For in-memory catalog, priming isn't necessary
	// This method is included for DataLoader interface compatibility
}

// Clear clears the loader state (called on catalog reload)
func (l *CollectionLoader) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.batch = nil
	l.waiting = nil
}
