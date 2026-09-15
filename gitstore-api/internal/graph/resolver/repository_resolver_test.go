// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestProvisionRepositoryStorage_onlyProvisionsAnActiveAdmittedNonBootstrapRepository(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "admitted", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))
	status, err := json.Marshal(catalog.RepositoryStatus{Conditions: []catalog.Condition{{
		Type: catalog.ConditionAdmissionAccepted, Status: catalog.ConditionTrue,
	}}})
	require.NoError(t, err)
	repository := &datastore.Repository{
		UID: "01960000-0000-7000-8000-000000000012", ID: "01960000-0000-7000-8000-000000000012",
		RepositoryID: "01960000-0000-7000-8000-000000000012", Namespace: "admitted", NamespaceID: "admitted",
		Name: "catalog", StorageClass: "premium", Status: status,
	}
	require.NoError(t, svcStore(t, svc).CreateRepositoryInActiveNamespace(ctx, repository))
	require.NoError(t, svcStore(t, svc).CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace: "admitted", Name: "catalog", RepositoryID: repository.UID,
	}))

	provisioned, err := svc.ProvisionRepositoryStorage(ctx, "admitted", "catalog")
	require.NoError(t, err)
	assert.Equal(t, repository.UID, provisioned.UID)
	_, err = svc.ProvisionRepositoryStorage(ctx, "admitted", "catalog")
	require.NoError(t, err, "retries must delegate to the idempotent git-service operation")

	writer.mu.Lock()
	assert.Equal(t, []string{repository.UID, repository.UID}, writer.createRepoCalls)
	writer.mu.Unlock()

	bootstrap := *repository
	bootstrap.Name = resolver.SystemRepositoryName
	bootstrap.UID = "01960000-0000-7000-8000-000000000014"
	bootstrap.ID = bootstrap.UID
	bootstrap.RepositoryID = bootstrap.UID
	require.NoError(t, svcStore(t, svc).CreateRepositoryInActiveNamespace(ctx, &bootstrap))
	require.NoError(t, svcStore(t, svc).CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace: "admitted", Name: resolver.SystemRepositoryName, RepositoryID: bootstrap.UID,
	}))
	_, err = svc.ProvisionRepositoryStorage(ctx, "admitted", resolver.SystemRepositoryName)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system-managed")

	terminating := *repository
	terminating.Name = "terminating"
	terminating.UID = "01960000-0000-7000-8000-000000000013"
	terminating.ID = terminating.UID
	terminating.RepositoryID = terminating.UID
	now := time.Now().UTC()
	terminating.DeletionTimestamp = &now
	require.NoError(t, svcStore(t, svc).CreateRepositoryInActiveNamespace(ctx, &terminating))
	require.NoError(t, svcStore(t, svc).CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace: "admitted", Name: "terminating", RepositoryID: terminating.UID,
	}))
	_, err = svc.ProvisionRepositoryStorage(ctx, "admitted", "terminating")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminating")

	notAdmitted := *repository
	notAdmitted.Name = "not-admitted"
	notAdmitted.UID = "01960000-0000-7000-8000-000000000015"
	notAdmitted.ID = notAdmitted.UID
	notAdmitted.RepositoryID = notAdmitted.UID
	notAdmitted.Status = []byte(`{"observedGeneration":0,"conditions":[]}`)
	require.NoError(t, svcStore(t, svc).CreateRepositoryInActiveNamespace(ctx, &notAdmitted))
	require.NoError(t, svcStore(t, svc).CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace: "admitted", Name: "not-admitted", RepositoryID: notAdmitted.UID,
	}))
	_, err = svc.ProvisionRepositoryStorage(ctx, "admitted", "not-admitted")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has not been admitted")
}

func TestProvisionRepositoryStorageTreatsAlreadyExistsAsIdempotentSuccess(t *testing.T) {
	writer := &mockGitWriter{createRepoErr: status.Error(codes.AlreadyExists, "repository already exists")}
	svc := newTestSvc(t, writer)
	ctx := context.Background()
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{ID: testNsID1, Name: "admitted", Tier: datastore.NamespaceTierUser}))
	statusJSON, err := json.Marshal(catalog.RepositoryStatus{Conditions: []catalog.Condition{{Type: catalog.ConditionAdmissionAccepted, Status: catalog.ConditionTrue}}})
	require.NoError(t, err)
	repository := &datastore.Repository{UID: "01960000-0000-7000-8000-000000000016", ID: "01960000-0000-7000-8000-000000000016", RepositoryID: "01960000-0000-7000-8000-000000000016", Namespace: "admitted", NamespaceID: "admitted", Name: "catalog", Status: statusJSON}
	require.NoError(t, svcStore(t, svc).CreateRepositoryInActiveNamespace(ctx, repository))
	require.NoError(t, svcStore(t, svc).CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "admitted", Name: "catalog", RepositoryID: repository.UID}))

	provisioned, err := svc.ProvisionRepositoryStorage(ctx, "admitted", "catalog")
	require.NoError(t, err)
	assert.Equal(t, repository.UID, provisioned.UID)
}

func TestProvisionSystemRepositoryCreatesOnlyTheSystemManagedBootstrapRepository(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "bootstrap-target", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	require.NoError(t, svc.ProvisionSystemRepository(ctx, "bootstrap-target", "controller-manager"))
	require.NoError(t, svc.ProvisionSystemRepository(ctx, "bootstrap-target", "controller-manager"))

	mapping, err := svc.LookupRepository(ctx, "bootstrap-target", resolver.SystemRepositoryName)
	require.NoError(t, err)
	repository, err := svc.GetRepository(ctx, mapping.RepositoryID)
	require.NoError(t, err)
	assert.Equal(t, resolver.SystemRepositoryName, repository.Name)
	assert.Equal(t, "bootstrap-target", repository.Namespace)

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Len(t, writer.createRepoCalls, 1, "retries must not provision storage again")
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
			if operation == "rename" || operation == "transfer" {
				assert.Contains(t, err.Error(), "unimplemented")
				return
			}
			assert.Equal(t, "input: repository not found", err.Error())
		})
	}
}

// ── renameRepository ──────────────────────────────────────────────────────────

func TestRenameRepository_IsDeferredToLifecyclePhase2(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "acme-rename", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))

	repo, err := svc.CreateRepository(ctx, testNsID1, "old-name", "main", "default", "test-user")
	require.NoError(t, err)
	originalID := repo.ID

	_, err = svc.RenameRepository(ctx, originalID, "new-name", "test-user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unimplemented")

	persisted, err := svc.GetRepository(ctx, originalID)
	require.NoError(t, err)
	assert.Equal(t, "old-name", persisted.Name)
}

// ── transferRepository ────────────────────────────────────────────────────────

func TestTransferRepository_IsDeferredToLifecyclePhase2(t *testing.T) {
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

	_, err = svc.TransferRepository(ctx, originalID, testNsID2, "test-user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unimplemented")

	persisted, err := svc.GetRepository(ctx, originalID)
	require.NoError(t, err)
	assert.Equal(t, "ns-from", persisted.Namespace)
}

// ── deleteRepository ──────────────────────────────────────────────────────────

func TestDeleteRepository_marksTerminatingWithoutRemovingStorageOrMapping(t *testing.T) {
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
	assert.Empty(t, writer.deleteRepoCalls)

	persisted, err := svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.DeletionTimestamp)
	assert.Contains(t, persisted.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
	assert.Equal(t, "2", persisted.ResourceVersion)
	_, err = svcStore(t, svc).LookupRepository(ctx, "ns-del", "to-delete")
	require.NoError(t, err)

	// Repeated user requests are idempotent and do not advance the version.
	require.NoError(t, svc.DeleteRepository(ctx, repo.ID, "test-user"))
	again, err := svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)
	assert.Equal(t, persisted.ResourceVersion, again.ResourceVersion)
}

func TestCompleteRepositoryDeletion_removesStorageThenMetadataAfterDrain(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()

	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-complete-delete", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))
	repo, err := svc.CreateRepository(ctx, testNsID1, "to-complete", "main", "default", "test-user")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteRepository(ctx, repo.ID, "test-user"))
	terminating, err := svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)

	_, err = svc.CompleteRepositoryDeletion(ctx, terminating.Namespace, terminating.Name, terminating.ResourceVersion)
	require.NoError(t, err)
	writer.mu.Lock()
	require.Equal(t, []string{repo.ID}, writer.deleteRepoCalls)
	writer.mu.Unlock()
	_, err = svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = svcStore(t, svc).LookupRepository(ctx, "ns-complete-delete", "to-complete")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestCompleteRepositoryDeletion_keepsTerminatingRecordWhenCatalogResourcesReappear(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-complete-blocked", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))
	repo, err := svc.CreateRepository(ctx, testNsID1, "still-owned", "main", "default", "test-user")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteRepository(ctx, repo.ID, "test-user"))
	terminating, err := svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)
	require.NoError(t, svcStore(t, svc).CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID: "01960000-0000-7000-8000-000000000097", Namespace: terminating.Namespace, Name: "late-owner",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: time.Now(), RepositoryID: repo.ID,
	}))

	_, err = svc.CompleteRepositoryDeletion(ctx, terminating.Namespace, terminating.Name, terminating.ResourceVersion)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still contains catalog resources")
	persisted, err := svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)
	assert.NotNil(t, persisted.DeletionTimestamp)
	assert.Contains(t, persisted.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
	writer.mu.Lock()
	assert.Empty(t, writer.deleteRepoCalls)
	writer.mu.Unlock()
}

func TestCompleteRepositoryDeletion_preservesOtherFinalizers(t *testing.T) {
	writer := &mockGitWriter{}
	svc := newTestSvc(t, writer)
	ctx := context.Background()
	require.NoError(t, svcStore(t, svc).CreateNamespace(ctx, &datastore.Namespace{
		ID: testNsID1, Name: "ns-extra-finalizer", Tier: datastore.NamespaceTierUser, CreationActor: "test", UpdateActor: "test",
	}))
	repo, err := svc.CreateRepository(ctx, testNsID1, "extra-finalizer", "main", "default", "test-user")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteRepository(ctx, repo.ID, "test-user"))
	terminating, err := svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)
	terminating.Finalizers = append(terminating.Finalizers, "example.test/retain")
	expected := terminating.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(terminating)
	require.NoError(t, svcStore(t, svc).UpdateRepository(ctx, terminating, expected))

	_, err = svc.CompleteRepositoryDeletion(ctx, terminating.Namespace, terminating.Name, terminating.ResourceVersion)
	require.NoError(t, err)
	persisted, err := svcStore(t, svc).GetRepository(ctx, repo.ID)
	require.NoError(t, err)
	assert.NotContains(t, persisted.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
	assert.Contains(t, persisted.Finalizers, "example.test/retain")
	assert.NotNil(t, persisted.DeletionTimestamp)
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
