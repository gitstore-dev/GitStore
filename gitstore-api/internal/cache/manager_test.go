// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Unit tests for cache.Manager — coalescing behaviour and gRPC-backed reload.

package cache_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/cache"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// slowLoader is a CatalogLoader that tracks call counts and can be delayed.
type slowLoader struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	cat   *catalog.Catalog
	err   error
}

func (l *slowLoader) LoadFromTag(_ context.Context, _ string) (*catalog.Catalog, error) {
	return l.load()
}

func (l *slowLoader) LoadFromLatestTag(_ context.Context) (*catalog.Catalog, error) {
	return l.load()
}

func (l *slowLoader) load() (*catalog.Catalog, error) {
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	return l.cat, nil
}

func (l *slowLoader) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// TestManagerGetReturnsCache verifies that repeated Get calls within TTL do not reload.
func TestManagerGetReturnsCache(t *testing.T) {
	ldr := &slowLoader{cat: catalog.NewCatalog("sha1", "v1.0.0")}
	mgr := cache.NewManager(ldr, zap.NewNop(), 10*time.Minute)

	ctx := context.Background()
	cat1, err := mgr.Get(ctx)
	require.NoError(t, err)

	cat2, err := mgr.Get(ctx)
	require.NoError(t, err)

	assert.Equal(t, cat1, cat2, "second Get should return cached catalog")
	assert.Equal(t, 1, ldr.callCount(), "loader should be called only once within TTL")
}

// TestManagerInvalidateForcesReload verifies Invalidate causes the next Get to reload.
func TestManagerInvalidateForcesReload(t *testing.T) {
	ldr := &slowLoader{cat: catalog.NewCatalog("sha1", "v1.0.0")}
	mgr := cache.NewManager(ldr, zap.NewNop(), 10*time.Minute)

	ctx := context.Background()
	_, err := mgr.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, ldr.callCount())

	mgr.Invalidate()

	_, err = mgr.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, ldr.callCount(), "loader should be called again after Invalidate")
}

// TestManagerCoalescesRapidFireReloads verifies that concurrent reload requests while
// a reload is already in-flight result in at most 2 total loader calls (one in-flight +
// one queued), not N.
func TestManagerCoalescesRapidFireReloads(t *testing.T) {
	const workers = 20

	ldr := &slowLoader{
		cat:   catalog.NewCatalog("sha1", "v1.0.0"),
		delay: 50 * time.Millisecond, // slow enough for workers to pile up
	}
	mgr := cache.NewManager(ldr, zap.NewNop(), 10*time.Minute)
	ctx := context.Background()

	// Invalidate so all workers will try to reload.
	mgr.Invalidate()

	var wg sync.WaitGroup
	var errCount atomic.Int32
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := mgr.Get(ctx); err != nil {
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Zero(t, errCount.Load(), "no errors expected")
	// With coalescing, the loader should be called at most twice:
	// once for the in-flight load and once for the queued one.
	calls := ldr.callCount()
	assert.LessOrEqual(t, calls, 2,
		"coalescing should prevent more than 2 loader calls; got %d", calls)
}

// TestManagerReloadCallerGetsLatestAfterCoalescing verifies that even a coalesced
// caller receives a valid (non-nil) catalog after the in-flight reload completes.
func TestManagerReloadCallerGetsLatestAfterCoalescing(t *testing.T) {
	ldr := &slowLoader{
		cat:   catalog.NewCatalog("sha1", "v1.0.0"),
		delay: 30 * time.Millisecond,
	}
	mgr := cache.NewManager(ldr, zap.NewNop(), 10*time.Minute)
	ctx := context.Background()

	mgr.Invalidate()

	results := make([]*catalog.Catalog, 5)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			cat, err := mgr.Get(ctx)
			require.NoError(t, err)
			results[idx] = cat
		}()
	}
	wg.Wait()

	for i, cat := range results {
		assert.NotNil(t, cat, "caller %d received nil catalog", i)
	}
}
