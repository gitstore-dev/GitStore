// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Category DataLoader - batches category lookups to prevent N+1 queries

package loader

import (
	"context"
	"sync"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

// CategoryLoader batches category lookups
type CategoryLoader struct {
	store  datastore.Datastore
	logger *zap.Logger

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
func NewCategoryLoader(store datastore.Datastore, logger *zap.Logger) *CategoryLoader {
	return &CategoryLoader{
		store:  store,
		logger: logger,
	}
}

// Load loads a single category by ID (batched)
func (l *CategoryLoader) Load(ctx context.Context, id string) (*catalog.Category, error) {
	l.mu.Lock()

	index := len(l.batch)
	l.batch = append(l.batch, id)

	resultChan := make(chan []*categoryResult, 1)
	l.waiting = append(l.waiting, resultChan)

	if len(l.batch) == 1 {
		go l.executeBatch(ctx)
	}

	l.mu.Unlock()

	results := <-resultChan

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

func (l *CategoryLoader) executeBatch(ctx context.Context) {
	l.mu.Lock()

	ids := make([]string, len(l.batch))
	copy(ids, l.batch)
	waiting := make([]chan []*categoryResult, len(l.waiting))
	copy(waiting, l.waiting)

	l.batch = nil
	l.waiting = nil

	l.mu.Unlock()

	results := make([]*categoryResult, len(ids))

	l.logger.Debug("Executing category batch lookup",
		zap.Int("count", len(ids)),
		zap.Strings("ids", ids),
	)

	for i, id := range ids {
		c, err := l.store.GetCategory(ctx, id)
		if err != nil {
			results[i] = &categoryResult{category: nil, err: nil}
			continue
		}
		results[i] = &categoryResult{
			category: datastoreCategoryToCatalog(c),
		}
	}

	for _, ch := range waiting {
		ch <- results
		close(ch)
	}
}

// Prime is included for DataLoader interface compatibility.
func (l *CategoryLoader) Prime(_ string, _ *catalog.Category) {}

// Clear resets the loader state.
func (l *CategoryLoader) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.batch = nil
	l.waiting = nil
}

// datastoreCategoryToCatalog converts a datastore.Category to a catalog.Category.
func datastoreCategoryToCatalog(c *datastore.Category) *catalog.Category {
	return &catalog.Category{
		ID:           c.ID,
		Name:         c.Name,
		Slug:         c.Slug,
		ParentID:     c.ParentID,
		DisplayOrder: c.DisplayOrder,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
		Body:         c.Body,
	}
}
