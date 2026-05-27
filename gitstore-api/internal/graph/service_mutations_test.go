// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph_test

import (
	"context"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
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

	createRepoCalls []string
	deleteRepoCalls []string

	commitErr      error
	deleteErr      error
	createRepoErr  error
	deleteRepoErr  error
}

func (m *mockGitWriter) CreateRepository(_ context.Context, repositoryID, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createRepoCalls = append(m.createRepoCalls, repositoryID)
	if m.createRepoErr != nil {
		return "", m.createRepoErr
	}
	return "/data/xx/yy/" + repositoryID + ".git", nil
}

func (m *mockGitWriter) DeleteRepository(_ context.Context, repositoryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteRepoCalls = append(m.deleteRepoCalls, repositoryID)
	return m.deleteRepoErr
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

// newTestSvc builds a Service backed by an in-memory datastore.
func newTestSvc(t *testing.T, writer *mockGitWriter) *graph.Service {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	return graph.NewServiceWithWriter(store, writer, zap.NewNop())
}

func TestServiceCreateProductPersists(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	product, err := svc.CreateProduct(context.Background(), map[string]interface{}{
		"sku":   "SKU-001",
		"title": "Widget",
		"price": 9.99,
	})
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, "SKU-001", product.SKU)
	assert.Equal(t, "Widget", product.Title)
	assert.NotEmpty(t, product.ID)

	// Verify product is retrievable from the datastore
	fetched, err := svc.GetProductBySKU(context.Background(), "SKU-001")
	require.NoError(t, err)
	assert.Equal(t, product.ID, fetched.ID)
}

func TestServiceCreateProductDuplicateSKU(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	_, err := svc.CreateProduct(context.Background(), map[string]interface{}{
		"sku": "DUP-001", "title": "First", "price": 1.0,
	})
	require.NoError(t, err)

	_, err = svc.CreateProduct(context.Background(), map[string]interface{}{
		"sku": "DUP-001", "title": "Second", "price": 2.0,
	})
	require.Error(t, err)
}

func TestServiceDeleteProductRemovesFromDatastore(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	product, err := svc.CreateProduct(context.Background(), map[string]interface{}{
		"sku": "SKU-DEL", "title": "ToDelete", "price": 1.0,
	})
	require.NoError(t, err)

	require.NoError(t, svc.DeleteProduct(context.Background(), product.ID))

	_, err = svc.GetProductByID(context.Background(), product.ID)
	require.Error(t, err)
}

func TestServiceDeleteProductNotFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	err := svc.DeleteProduct(context.Background(), "nonexistent-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestServiceUpdateProductAppliesChanges(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	product, err := svc.CreateProduct(context.Background(), map[string]interface{}{
		"sku": "SKU-UPD", "title": "Original", "price": 9.99,
	})
	require.NoError(t, err)

	updated, err := svc.UpdateProduct(context.Background(), product.ID, map[string]interface{}{
		"title": "Super Widget",
	})
	require.NoError(t, err)
	assert.Equal(t, "Super Widget", updated.Title)
	assert.Equal(t, product.ID, updated.ID)
}
