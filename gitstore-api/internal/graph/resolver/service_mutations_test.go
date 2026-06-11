// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockGitWriter implements resolver.GitWriter for testing.
type mockGitWriter struct {
	mu             sync.Mutex
	commitCalls    []gitclient.CommitFileParams
	deleteCalls    []gitclient.DeleteFileParams
	createTagCalls []gitclient.CreateTagParams

	createRepoCalls []string
	deleteRepoCalls []string

	commitErr     error
	deleteErr     error
	createRepoErr error
	deleteRepoErr error
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
func newTestSvc(t *testing.T, writer *mockGitWriter) *resolver.Service {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:     store,
		GitWriter: writer,
		Logger:    zap.NewNop(),
	})
	require.NoError(t, err)
	return svc
}

func TestServiceDeleteProductRemovesFromDatastore(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:     store,
		GitWriter: &mockGitWriter{},
		Logger:    zap.NewNop(),
	})
	require.NoError(t, err)

	uid := "a0000000-0000-0000-0000-000000000001"
	require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
		UID:       uid,
		Namespace: "test-store",
		Name:      "to-delete",
	}))

	require.NoError(t, svc.DeleteProduct(ctx, uid))

	_, err = svc.GetProductByUID(ctx, uid)
	require.Error(t, err)
}

func TestServiceDeleteProductNotFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	err := svc.DeleteProduct(context.Background(), "a0000000-0000-0000-0000-000000000099")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
