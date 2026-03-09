// Cache manager - manages catalog cache with TTL and websocket invalidation

package cache

import (
	"context"
	"sync"
	"time"

	"github.com/yourorg/gitstore/api/internal/catalog"
	"go.uber.org/zap"
)

// Manager manages the catalog cache
type Manager struct {
	mu       sync.RWMutex
	catalog  *catalog.Catalog
	loader   *catalog.Loader
	logger   *zap.Logger
	ttl      time.Duration
	loadedAt time.Time
}

// NewManager creates a new cache manager
func NewManager(loader *catalog.Loader, logger *zap.Logger, ttl time.Duration) *Manager {
	return &Manager{
		loader: loader,
		logger: logger,
		ttl:    ttl,
	}
}

// Get retrieves the current catalog, loading if necessary
func (m *Manager) Get(ctx context.Context) (*catalog.Catalog, error) {
	m.mu.RLock()

	// Check if cache is valid
	if m.catalog != nil && time.Since(m.loadedAt) < m.ttl {
		m.logger.Debug("Using cached catalog",
			zap.Time("loaded_at", m.loadedAt),
			zap.Duration("age", time.Since(m.loadedAt)),
		)
		catalog := m.catalog
		m.mu.RUnlock()
		return catalog, nil
	}

	m.mu.RUnlock()

	// Need to reload
	return m.Reload(ctx)
}

// Reload forces a reload of the catalog from git
func (m *Manager) Reload(ctx context.Context) (*catalog.Catalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("Reloading catalog from git")

	// Load catalog from latest tag
	newCatalog, err := m.loader.LoadFromLatestTag(ctx)
	if err != nil {
		return nil, err
	}

	m.catalog = newCatalog
	m.loadedAt = time.Now()

	m.logger.Info("Catalog reloaded successfully",
		zap.String("commit", newCatalog.Commit()),
		zap.Int("products", newCatalog.ProductCount()),
		zap.Int("categories", newCatalog.CategoryCount()),
		zap.Int("collections", newCatalog.CollectionCount()),
	)

	return m.catalog, nil
}

// Invalidate marks the cache as stale, forcing next Get to reload
func (m *Manager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("Cache invalidated")
	m.loadedAt = time.Time{} // Zero time forces reload
}

// LoadedAt returns when the catalog was last loaded
func (m *Manager) LoadedAt() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadedAt
}
