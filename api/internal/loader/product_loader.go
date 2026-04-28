// Product DataLoader - batches product lookups to prevent N+1 queries

package loader

import (
	"context"
	"sync"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

// ProductLoader batches product lookups
type ProductLoader struct {
	catalog *catalog.Catalog
	logger  *zap.Logger

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
func NewProductLoader(cat *catalog.Catalog, logger *zap.Logger) *ProductLoader {
	return &ProductLoader{
		catalog: cat,
		logger:  logger,
	}
}

// Load loads a single product by ID (batched)
func (l *ProductLoader) Load(ctx context.Context, id string) (*catalog.Product, error) {
	l.mu.Lock()

	// Add this ID to the batch
	index := len(l.batch)
	l.batch = append(l.batch, id)

	// Create a channel to receive results
	resultChan := make(chan []*productResult, 1)
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

// executeBatch executes the batched lookups
func (l *ProductLoader) executeBatch() {
	l.mu.Lock()

	// Copy batch and waiting list
	ids := make([]string, len(l.batch))
	copy(ids, l.batch)
	waiting := make([]chan []*productResult, len(l.waiting))
	copy(waiting, l.waiting)

	// Reset for next batch
	l.batch = nil
	l.waiting = nil

	l.mu.Unlock()

	// Execute batch lookup
	results := make([]*productResult, len(ids))

	l.logger.Debug("Executing product batch lookup",
		zap.Int("count", len(ids)),
		zap.Strings("ids", ids),
	)

	for i, id := range ids {
		prod, ok := l.catalog.GetProduct(id)
		if ok {
			results[i] = &productResult{product: prod, err: nil}
		} else {
			results[i] = &productResult{product: nil, err: nil}
		}
	}

	// Send results to all waiting channels
	for _, ch := range waiting {
		ch <- results
		close(ch)
	}
}

// Prime preloads a product into the cache
func (l *ProductLoader) Prime(id string, product *catalog.Product) {
	// For in-memory catalog, priming isn't necessary
	// This method is included for DataLoader interface compatibility
}

// Clear clears the loader state (called on catalog reload)
func (l *ProductLoader) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.batch = nil
	l.waiting = nil
}
