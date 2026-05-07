// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Unit tests for the Service write mutations (T032).
// Verifies that mutations call gRPC client methods and propagate errors correctly.

package graph_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/cache"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockGitWriter implements graph.GitWriter for testing.
type mockGitWriter struct {
	mu             sync.Mutex
	commitCalls    []gitclient.CommitFileParams
	deleteCalls    []gitclient.DeleteFileParams
	createTagCalls []gitclient.CreateTagParams

	commitErr error
	deleteErr error
}

func (m *mockGitWriter) CommitFile(_ context.Context, p gitclient.CommitFileParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commitCalls = append(m.commitCalls, p)
	if m.commitErr != nil {
		return "", m.commitErr
	}
	return "deadbeef", nil
}

func (m *mockGitWriter) DeleteFile(_ context.Context, p gitclient.DeleteFileParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls = append(m.deleteCalls, p)
	if m.deleteErr != nil {
		return "", m.deleteErr
	}
	return "cafe1234", nil
}

func (m *mockGitWriter) CreateTag(_ context.Context, p gitclient.CreateTagParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createTagCalls = append(m.createTagCalls, p)
	return "tag123", nil
}

// staticCatalogLoader implements catalog.CatalogLoader with a fixed catalog.
type staticCatalogLoader struct{ cat *catalog.Catalog }

func (l *staticCatalogLoader) LoadFromTag(_ context.Context, _ string) (*catalog.Catalog, error) {
	return l.cat, nil
}

func (l *staticCatalogLoader) LoadFromLatestTag(_ context.Context) (*catalog.Catalog, error) {
	return l.cat, nil
}

// newTestSvc builds a Service backed by a mock writer and a pre-loaded catalog.
func newTestSvc(t *testing.T, cat *catalog.Catalog, writer *mockGitWriter) *graph.Service {
	t.Helper()
	ldr := &staticCatalogLoader{cat: cat}
	mgr := cache.NewManager(ldr, zap.NewNop(), 10*time.Minute)
	return graph.NewServiceWithWriter(mgr, writer, zap.NewNop())
}

func TestServiceCreateProductCallsCommitFile(t *testing.T) {
	cat := catalog.NewCatalog("sha1", "v1.0.0")
	writer := &mockGitWriter{}
	svc := newTestSvc(t, cat, writer)

	_, err := svc.CreateProduct(context.Background(), map[string]interface{}{
		"sku":   "SKU-001",
		"title": "Widget",
		"price": 9.99,
	})
	require.NoError(t, err)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.commitCalls, 1)
	assert.Contains(t, writer.commitCalls[0].Path, "products/")
	assert.Contains(t, string(writer.commitCalls[0].Content), "sku: SKU-001")
	assert.Contains(t, writer.commitCalls[0].CommitMessage, "Widget")
}

func TestServiceCreateProductPropagatesCommitError(t *testing.T) {
	cat := catalog.NewCatalog("sha1", "v1.0.0")
	writer := &mockGitWriter{commitErr: fmt.Errorf("git-service unavailable")}
	svc := newTestSvc(t, cat, writer)

	_, err := svc.CreateProduct(context.Background(), map[string]interface{}{
		"sku": "SKU-002", "title": "Gadget", "price": 5.0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git-service unavailable")
}

func TestServiceDeleteProductCallsDeleteFile(t *testing.T) {
	cat := catalog.NewCatalog("sha1", "v1.0.0")
	prod := &catalog.Product{ID: "prod-001", SKU: "SKU-001", Title: "Widget"}
	cat.AddProduct(prod)

	writer := &mockGitWriter{}
	svc := newTestSvc(t, cat, writer)

	err := svc.DeleteProduct(context.Background(), "prod-001")
	require.NoError(t, err)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.deleteCalls, 1)
	assert.Equal(t, "products/prod-001.md", writer.deleteCalls[0].Path)
	assert.Contains(t, writer.deleteCalls[0].CommitMessage, "Widget")
}

func TestServiceDeleteProductNotFound(t *testing.T) {
	cat := catalog.NewCatalog("sha1", "v1.0.0")
	writer := &mockGitWriter{}
	svc := newTestSvc(t, cat, writer)

	err := svc.DeleteProduct(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "product not found")

	writer.mu.Lock()
	defer writer.mu.Unlock()
	assert.Empty(t, writer.deleteCalls, "DeleteFile must not be called when product does not exist")
}

func TestServiceUpdateProductCallsCommitFile(t *testing.T) {
	cat := catalog.NewCatalog("sha1", "v1.0.0")
	prod := &catalog.Product{ID: "prod-001", SKU: "SKU-001", Title: "Widget", Price: 9.99}
	cat.AddProduct(prod)

	writer := &mockGitWriter{}
	svc := newTestSvc(t, cat, writer)

	_, err := svc.UpdateProduct(context.Background(), "prod-001", map[string]interface{}{
		"title": "Super Widget",
	})
	require.NoError(t, err)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.commitCalls, 1)
	assert.Contains(t, string(writer.commitCalls[0].Content), "Super Widget")
}
