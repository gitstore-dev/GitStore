// Category DataLoader - batches category lookups to prevent N+1 queries

package loader

import (
	"context"
	"sync"

	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

// CategoryLoader batches category lookups
type CategoryLoader struct {
	catalog *catalog.Catalog
	logger  *zap.Logger

	// Batch state
	mu      sync.Mutex
	batch   []string
	waiting []chan []*categoryResult
}

type categoryResult struct {
	category *catalog.Category
	err      error
}

// NewCategoryLoader creates a new category data loader
func NewCategoryLoader(cat *catalog.Catalog, logger *zap.Logger) *CategoryLoader {
	return &CategoryLoader{
		catalog: cat,
		logger:  logger,
	}
}

// Load loads a single category by ID (batched)
func (l *CategoryLoader) Load(ctx context.Context, id string) (*catalog.Category, error) {
	l.mu.Lock()

	// Add this ID to the batch
	index := len(l.batch)
	l.batch = append(l.batch, id)

	// Create a channel to receive results
	resultChan := make(chan []*categoryResult, 1)
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
		return results[index].category, results[index].err
	}

	return nil, nil
}

// LoadMany loads multiple categories by IDs (batched)
func (l *CategoryLoader) LoadMany(ctx context.Context, ids []string) ([]*catalog.Category, []error) {
	categories := make([]*catalog.Category, len(ids))
	errs := make([]error, len(ids))

	for i, id := range ids {
		cat, err := l.Load(ctx, id)
		categories[i] = cat
		errs[i] = err
	}

	return categories, errs
}

// executeBatch executes the batched lookups
func (l *CategoryLoader) executeBatch() {
	l.mu.Lock()

	// Copy batch and waiting list
	ids := make([]string, len(l.batch))
	copy(ids, l.batch)
	waiting := make([]chan []*categoryResult, len(l.waiting))
	copy(waiting, l.waiting)

	// Reset for next batch
	l.batch = nil
	l.waiting = nil

	l.mu.Unlock()

	// Execute batch lookup
	results := make([]*categoryResult, len(ids))

	l.logger.Debug("Executing category batch lookup",
		zap.Int("count", len(ids)),
		zap.Strings("ids", ids),
	)

	for i, id := range ids {
		cat, ok := l.catalog.GetCategory(id)
		if ok {
			results[i] = &categoryResult{category: cat, err: nil}
		} else {
			results[i] = &categoryResult{category: nil, err: nil}
		}
	}

	// Send results to all waiting channels
	for _, ch := range waiting {
		ch <- results
		close(ch)
	}
}

// Prime preloads a category into the cache
func (l *CategoryLoader) Prime(id string, category *catalog.Category) {
	// For in-memory catalog, priming isn't necessary
	// This method is included for DataLoader interface compatibility
}

// Clear clears the loader state (called on catalog reload)
func (l *CategoryLoader) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.batch = nil
	l.waiting = nil
}
