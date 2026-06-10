// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testNsID1 = "01960000-0000-7000-8000-000000000010"
	testNsID2 = "01960000-0000-7000-8000-000000000011"
)

func svcStore(t *testing.T, svc *resolver.Service) datastore.Datastore {
	t.Helper()
	return svc.Store()
}

// ── createRepository ──────────────────────────────────────────────────────────

func TestCreateRepository_assignsUUIDv7AndCallsGRPC(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	// Pre-create the namespace in the datastore so lookups work
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID:         testNsID1,
		Identifier: "acme",
		Tier:       datastore.NamespaceTierUser,
		CreatedBy:  "test",
		UpdatedBy:  "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "my-catalog", "main", "default", "test-user")
	require.NoError(t, err)
	require.NotNil(t, repo)

	assert.NotEmpty(t, repo.ID)
	assert.Equal(t, "my-catalog", repo.Name)
	assert.Equal(t, testNsID1, repo.NamespaceID)
	assert.Equal(t, "main", repo.DefaultBranch)
	assert.Equal(t, "default", repo.StorageClass)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.createRepoCalls, 1, "gRPC CreateRepository must be called once")
	assert.Equal(t, repo.ID, writer.createRepoCalls[0], "gRPC must receive the repo_id UUID")
}

// ── renameRepository ──────────────────────────────────────────────────────────

func TestRenameRepository_oldNameNotFoundNewNameReturnsSameRepoID(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Identifier: "acme-rename", Tier: datastore.NamespaceTierUser, CreatedBy: "test", UpdatedBy: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "old-name", "main", "default", "test-user")
	require.NoError(t, err)
	originalID := repo.ID

	renamed, err := svc.RenameRepository(ctx, originalID, "new-name", "test-user")
	require.NoError(t, err)
	require.NotNil(t, renamed)
	assert.Equal(t, originalID, renamed.ID, "repo_id must be unchanged after rename")
	assert.Equal(t, "new-name", renamed.Name)

	_, err = svcStore(t, svc).LookupRepository(ctx, testNsID1, "old-name")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	m, err := svcStore(t, svc).LookupRepository(ctx, testNsID1, "new-name")
	require.NoError(t, err)
	assert.Equal(t, originalID, m.RepoID)
}

// ── transferRepository ────────────────────────────────────────────────────────

func TestTransferRepository_oldNSInvalidatedNewNSReturnsSameRepoID(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Identifier: "ns-from", Tier: datastore.NamespaceTierUser, CreatedBy: "test", UpdatedBy: "test",
	}))
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID2, Identifier: "ns-to", Tier: datastore.NamespaceTierUser, CreatedBy: "test", UpdatedBy: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "app", "main", "default", "test-user")
	require.NoError(t, err)
	originalID := repo.ID

	transferred, err := svc.TransferRepository(ctx, originalID, testNsID2, "test-user")
	require.NoError(t, err)
	assert.Equal(t, originalID, transferred.ID)
	assert.Equal(t, testNsID2, transferred.NamespaceID)

	_, err = svcStore(t, svc).LookupRepository(ctx, testNsID1, "app")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	m, err := svcStore(t, svc).LookupRepository(ctx, testNsID2, "app")
	require.NoError(t, err)
	assert.Equal(t, originalID, m.RepoID)
}

// ── deleteRepository ──────────────────────────────────────────────────────────

func TestDeleteRepository_callsGRPCAndRemovesMapping(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Identifier: "ns-del", Tier: datastore.NamespaceTierUser, CreatedBy: "test", UpdatedBy: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "to-delete", "main", "default", "test-user")
	require.NoError(t, err)

	err = svc.DeleteRepository(ctx, repo.ID, "test-user")
	require.NoError(t, err)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.deleteRepoCalls, 1)
	assert.Equal(t, repo.ID, writer.deleteRepoCalls[0])

	_, err = svcStore(t, svc).LookupRepository(ctx, testNsID1, "to-delete")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── LookupNamespaceByRepoID ───────────────────────────────────────────────────

func TestLookupNamespaceByRepoID_returnsMapping(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Identifier: "ns-reverse", Tier: datastore.NamespaceTierUser, CreatedBy: "test", UpdatedBy: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "configs", "main", "default", "test-user")
	require.NoError(t, err)

	m, err := svc.LookupNamespaceByRepoID(ctx, repo.ID)
	require.NoError(t, err)
	assert.Equal(t, "configs", m.Name)
	assert.Equal(t, testNsID1, m.NamespaceID)
}
