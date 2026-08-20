// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"testing"
	"time"

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
		ID:            testNsID1,
		Name:          "acme",
		Tier:          datastore.NamespaceTierUser,
		CreationActor: "test",
		UpdateActor:   "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "my-catalog", "main", "default", "test-user")
	require.NoError(t, err)
	require.NotNil(t, repo)

	assert.NotEmpty(t, repo.ID)
	assert.Equal(t, "my-catalog", repo.Name)
	assert.Equal(t, "acme", repo.Namespace)
	assert.Equal(t, "acme", repo.NamespaceID)
	assert.Equal(t, repo.UID, repo.RepositoryID)
	assert.Equal(t, "main", repo.DefaultBranch)
	assert.Equal(t, "default", repo.StorageClass)
	assert.Equal(t, int64(1), repo.Generation)
	assert.Equal(t, "1", repo.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(repo.Status))

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.createRepoCalls, 1, "gRPC CreateRepository must be called once")
	assert.Equal(t, repo.ID, writer.createRepoCalls[0], "gRPC must receive the repo_id UUID")
}

func TestRepositoryMutations_preserveExistingErrors(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "mutation-errors", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	_, err := svc.CreateRepository(ctx, testNsID1, "duplicate", "main", "default", "test-user")
	require.NoError(t, err)
	_, err = svc.CreateRepository(ctx, testNsID1, "duplicate", "main", "default", "test-user")
	require.Error(t, err)
	assert.Equal(t, "input: repository already exists", err.Error())

	for operation, call := range map[string]func() error{
		"rename": func() error {
			_, renameErr := svc.RenameRepository(ctx, "01960000-0000-7000-8000-000000000099", "new-name", "test-user")
			return renameErr
		},
		"transfer": func() error {
			_, transferErr := svc.TransferRepository(ctx, "01960000-0000-7000-8000-000000000099", testNsID1, "test-user")
			return transferErr
		},
		"delete": func() error {
			return svc.DeleteRepository(ctx, "01960000-0000-7000-8000-000000000099", "test-user")
		},
	} {
		t.Run(operation, func(t *testing.T) {
			err := call()
			require.Error(t, err)
			assert.Equal(t, "input: repository not found", err.Error())
		})
	}
}

// ── renameRepository ──────────────────────────────────────────────────────────

func TestRenameRepository_oldNameNotFoundNewNameReturnsSameRepoID(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "acme-rename", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "old-name", "main", "default", "test-user")
	require.NoError(t, err)
	originalID := repo.ID

	renamed, err := svc.RenameRepository(ctx, originalID, "new-name", "test-user")
	require.NoError(t, err)
	require.NotNil(t, renamed)
	assert.Equal(t, originalID, renamed.ID, "repo_id must be unchanged after rename")
	assert.Equal(t, "new-name", renamed.Name)
	assert.Equal(t, int64(2), renamed.Generation)
	assert.Equal(t, "2", renamed.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(renamed.Status))

	_, err = svcStore(t, svc).LookupRepository(ctx, "acme-rename", "old-name")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	m, err := svcStore(t, svc).LookupRepository(ctx, "acme-rename", "new-name")
	require.NoError(t, err)
	assert.Equal(t, originalID, m.RepoID)
}

// ── transferRepository ────────────────────────────────────────────────────────

func TestTransferRepository_oldNSInvalidatedNewNSReturnsSameRepoID(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-from", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID2, Name: "ns-to", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "app", "main", "default", "test-user")
	require.NoError(t, err)
	originalID := repo.ID

	transferred, err := svc.TransferRepository(ctx, originalID, testNsID2, "test-user")
	require.NoError(t, err)
	assert.Equal(t, originalID, transferred.ID)
	assert.Equal(t, "ns-to", transferred.Namespace)
	assert.Equal(t, "ns-to", transferred.NamespaceID)
	assert.Equal(t, int64(1), transferred.Generation)
	assert.Equal(t, "2", transferred.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(transferred.Status))

	_, err = svcStore(t, svc).LookupRepository(ctx, "ns-from", "app")
	require.ErrorIs(t, err, datastore.ErrNotFound)

	m, err := svcStore(t, svc).LookupRepository(ctx, "ns-to", "app")
	require.NoError(t, err)
	assert.Equal(t, originalID, m.RepoID)
}

// ── deleteRepository ──────────────────────────────────────────────────────────

func TestDeleteRepository_callsGRPCAndRemovesMapping(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-del", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "to-delete", "main", "default", "test-user")
	require.NoError(t, err)

	err = svc.DeleteRepository(ctx, repo.ID, "test-user")
	require.NoError(t, err)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.deleteRepoCalls, 1)
	assert.Equal(t, repo.ID, writer.deleteRepoCalls[0])

	_, err = svcStore(t, svc).LookupRepository(ctx, "ns-del", "to-delete")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestDeleteRepository_withCatalogResource_rejected(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-catalog-blocked", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "has-catalog", "main", "default", "test-user")
	require.NoError(t, err)

	require.NoError(t, svcStore(t, svc).CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID:               "01960000-0000-7000-8000-000000000099",
		Namespace:         "ns-catalog-blocked",
		Name:              "blocking-category",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: time.Now(),
		RepositoryID:      repo.ID,
	}))

	err = svc.DeleteRepository(ctx, repo.ID, "test-user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains catalog resources and cannot be deleted")

	_, err = svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)

	_, err = svcStore(t, svc).GetCategoryTaxonomy(ctx, "01960000-0000-7000-8000-000000000099")
	require.NoError(t, err)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	assert.Empty(t, writer.deleteRepoCalls)
}

func TestDeleteRepository_afterCatalogResourcesRemoved_succeeds(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-catalog-cleared", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "catalog-cleared", "main", "default", "test-user")
	require.NoError(t, err)

	require.NoError(t, svcStore(t, svc).CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID:               "01960000-0000-7000-8000-000000000098",
		Namespace:         "ns-catalog-cleared",
		Name:              "removable-category",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: time.Now(),
		RepositoryID:      repo.ID,
	}))
	require.NoError(t, svcStore(t, svc).DeleteCategoryTaxonomy(ctx, "01960000-0000-7000-8000-000000000098"))

	err = svc.DeleteRepository(ctx, repo.ID, "test-user")
	require.NoError(t, err)
}

// ── LookupNamespaceByRepoID ───────────────────────────────────────────────────

func TestLookupNamespaceByRepoID_returnsMapping(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-reverse", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "configs", "main", "default", "test-user")
	require.NoError(t, err)

	m, err := svc.LookupNamespaceByRepoID(ctx, repo.ID)
	require.NoError(t, err)
	assert.Equal(t, "configs", m.Name)
	assert.Equal(t, "ns-reverse", m.Namespace)
	assert.Equal(t, "ns-reverse", m.NamespaceID)
}

func TestListRepositoriesUsesOptionalGlobalLister(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		UID:           testNsID1,
		Name:          "global-list",
		Tier:          datastore.NamespaceTierUser,
		CreationActor: "test",
		UpdateActor:   "test",
	}))
	_, err := svc.CreateRepository(ctx, "global-list", "catalog", "main", "default", "test-user")
	require.NoError(t, err)

	result, err := svc.ListRepositories(ctx, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.LessOrEqual(t, result.TotalCount, int32(2))
}
