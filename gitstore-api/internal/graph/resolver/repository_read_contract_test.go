// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	repositoryReadNamespaceID = "01960000-0000-7000-8000-000000000120"
	repositoryReadDataDir     = "/srv/gitstore/repos"
)

type repositoryReadCountingStore struct {
	datastore.Datastore

	mu                       sync.Mutex
	getNamespaceCalls        int
	getNamespaceByIdentCalls int
}

func (s *repositoryReadCountingStore) GetNamespace(ctx context.Context, id string) (*datastore.Namespace, error) {
	s.mu.Lock()
	s.getNamespaceCalls++
	s.mu.Unlock()
	return s.Datastore.GetNamespace(ctx, id)
}

func (s *repositoryReadCountingStore) GetNamespaceByIdentifier(ctx context.Context, identifier string) (*datastore.Namespace, error) {
	s.mu.Lock()
	s.getNamespaceByIdentCalls++
	s.mu.Unlock()
	return s.Datastore.GetNamespaceByIdentifier(ctx, identifier)
}

func (s *repositoryReadCountingStore) resetNamespaceCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getNamespaceCalls = 0
	s.getNamespaceByIdentCalls = 0
}

func (s *repositoryReadCountingStore) namespaceCalls() (byID, byIdentifier int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getNamespaceCalls, s.getNamespaceByIdentCalls
}

type repositoryReadHarness struct {
	root      *resolver.Resolver
	store     *repositoryReadCountingStore
	namespace *datastore.Namespace
	repos     []*datastore.Repository
}

func newRepositoryReadHarness(t *testing.T) *repositoryReadHarness {
	t.Helper()
	ctx := context.Background()
	baseStore, err := memdb.New()
	require.NoError(t, err)
	countingStore := &repositoryReadCountingStore{Datastore: baseStore}
	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:  countingStore,
		Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	root.WithStorageDataDir(repositoryReadDataDir)

	namespace := &datastore.Namespace{
		ID:          repositoryReadNamespaceID,
		Identifier:  "repository-read-contract",
		DisplayName: "Repository Read Contract",
		Tier:        datastore.NamespaceTierUser,
		CreatedAt:   time.Date(2026, time.August, 16, 18, 0, 0, 0, time.UTC),
		CreatedBy:   "fixture",
		UpdatedAt:   time.Date(2026, time.August, 16, 18, 0, 0, 0, time.UTC),
		UpdatedBy:   "fixture",
	}
	require.NoError(t, baseStore.CreateNamespace(ctx, namespace))

	repos := []*datastore.Repository{
		{
			ID:               "01960000-0000-7000-8000-000000000121",
			NamespaceID:      namespace.ID,
			Name:             "catalog",
			DefaultBranch:    "main",
			StorageClass:     "fast",
			CreatedAt:        time.Date(2026, time.August, 16, 18, 1, 0, 0, time.UTC),
			CreatedBy:        "alice",
			UpdatedAt:        time.Date(2026, time.August, 16, 18, 2, 0, 0, time.UTC),
			UpdatedBy:        "bob",
			Generation:       3,
			ResourceVersion:  "7",
			Status:           json.RawMessage(`{"observedGeneration":3,"lastAppliedRevision":"main@sha1:abc123","conditions":[]}`),
			MaxPackSizeBytes: 1048576,
			MaxFileSizeBytes: 262144,
		},
		{
			ID:               "01960000-0000-7000-8000-000000000122",
			NamespaceID:      namespace.ID,
			Name:             "assets",
			DefaultBranch:    "trunk",
			StorageClass:     "archive",
			CreatedAt:        time.Date(2026, time.August, 16, 18, 3, 0, 0, time.UTC),
			CreatedBy:        "carol",
			UpdatedAt:        time.Date(2026, time.August, 16, 18, 4, 0, 0, time.UTC),
			UpdatedBy:        "dave",
			Generation:       2,
			ResourceVersion:  "4",
			Status:           json.RawMessage(`{"observedGeneration":1,"conditions":[]}`),
			MaxPackSizeBytes: 0,
			MaxFileSizeBytes: 0,
		},
	}
	for _, repo := range repos {
		require.NoError(t, baseStore.CreateRepository(ctx, repo))
		require.NoError(t, baseStore.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
			NamespaceID: namespace.ID,
			Name:        repo.Name,
			RepoID:      repo.ID,
		}))
	}
	countingStore.resetNamespaceCalls()

	return &repositoryReadHarness{
		root:      root,
		store:     countingStore,
		namespace: namespace,
		repos:     repos,
	}
}

func TestRepositoryReadContract_SingleAndNodeUseCompleteSharedProjection(t *testing.T) {
	h := newRepositoryReadHarness(t)
	ctx := context.Background()
	query := h.root.Query()
	expected := h.repos[0]

	byPath, err := query.Repository(ctx, model.RepositoryBy{
		NamespacePath: &model.RepositoryNamespacePath{
			Namespace: h.namespace.Identifier,
			Name:      expected.Name,
		},
	})
	require.NoError(t, err)
	assertRepositoryReadContract(t, byPath, expected, h.namespace)
	byIDCalls, byIdentifierCalls := h.store.namespaceCalls()
	assert.Equal(t, 0, byIDCalls)
	assert.Equal(t, 1, byIdentifierCalls)

	h.store.resetNamespaceCalls()
	byID, err := query.Repository(ctx, model.RepositoryBy{ID: &byPath.ID})
	require.NoError(t, err)
	assertRepositoryReadContract(t, byID, expected, h.namespace)
	assert.Equal(t, byPath, byID)
	byIDCalls, byIdentifierCalls = h.store.namespaceCalls()
	assert.Equal(t, 1, byIDCalls)
	assert.Equal(t, 0, byIdentifierCalls)

	h.store.resetNamespaceCalls()
	node, err := query.Node(ctx, byPath.ID)
	require.NoError(t, err)
	nodeRepository, ok := node.(*model.Repository)
	require.True(t, ok, "global Node result must be a Repository")
	assertRepositoryReadContract(t, nodeRepository, expected, h.namespace)
	assert.Equal(t, byPath, nodeRepository)
	byIDCalls, byIdentifierCalls = h.store.namespaceCalls()
	assert.Equal(t, 1, byIDCalls)
	assert.Equal(t, 0, byIdentifierCalls)
}

func TestRepositoryReadContract_ListUsesResolvedNamespaceWithoutPerRowLookups(t *testing.T) {
	h := newRepositoryReadHarness(t)
	first := int32(10)

	connection, err := h.root.Query().Repositories(
		context.Background(),
		h.namespace.Identifier,
		&first,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, connection)
	require.Len(t, connection.Edges, len(h.repos))

	expectedByName := make(map[string]*datastore.Repository, len(h.repos))
	for _, repo := range h.repos {
		expectedByName[repo.Name] = repo
	}
	for _, edge := range connection.Edges {
		require.NotNil(t, edge)
		require.NotNil(t, edge.Node)
		expected := expectedByName[edge.Node.Name]
		require.NotNil(t, expected, "unexpected repository %q", edge.Node.Name)
		assertRepositoryReadContract(t, edge.Node, expected, h.namespace)
	}

	byIDCalls, byIdentifierCalls := h.store.namespaceCalls()
	assert.Equal(t, 0, byIDCalls, "list conversion must not look up the namespace once per row")
	assert.Equal(t, 1, byIdentifierCalls, "list resolver should resolve the namespace exactly once")
}

func assertRepositoryReadContract(
	t *testing.T,
	got *model.Repository,
	expected *datastore.Repository,
	namespace *datastore.Namespace,
) {
	t.Helper()
	require.NotNil(t, got)
	require.NotNil(t, got.Metadata)
	require.NotNil(t, got.Spec)
	require.NotNil(t, got.Spec.PushPolicy)
	require.NotNil(t, got.Status)
	require.NotNil(t, got.Status.Resolved)

	assert.Equal(t, "gitstore.dev/v1beta1", got.APIVersion)
	assert.Equal(t, "Repository", got.Kind)
	assert.Equal(t, expected.Name, got.Metadata.Name)
	assert.Equal(t, namespace.Identifier, got.Metadata.Namespace)
	assert.Equal(t, got.ID, got.Metadata.UID)
	assert.Equal(t, expected.ResourceVersion, got.Metadata.ResourceVersion)
	assert.Equal(t, int32(expected.Generation), got.Metadata.Generation)
	assert.Equal(t, expected.CreatedAt, got.Metadata.CreationTimestamp)
	assert.Empty(t, got.Metadata.Labels)
	assert.Empty(t, got.Metadata.Annotations)
	assert.Empty(t, got.Metadata.OwnerReferences)

	assert.Equal(t, expected.DefaultBranch, got.Spec.DefaultBranch)
	assert.Equal(t, model.RepositoryVisibilityPrivate, got.Spec.Visibility)
	assert.Equal(t, expected.MaxPackSizeBytes, got.Spec.PushPolicy.MaxPackSizeBytes)
	assert.Equal(t, expected.MaxFileSizeBytes, got.Spec.PushPolicy.MaxFileSizeBytes)
	assert.Nil(t, got.Spec.PushPolicy.ReceivePackHooks)
	assert.Nil(t, got.Spec.PushPolicy.SchemaValidation)
	assert.Nil(t, got.Spec.PushPolicy.AdmissionControl)

	assert.NotNil(t, got.Status.Conditions)
	assert.Equal(t, repositoryReadStoragePath(repositoryReadDataDir, expected.ID), got.Status.Resolved.StoragePath)
	assert.Equal(t, expected.StorageClass, got.Status.Resolved.StorageClass)

	assert.Equal(t, expected.Name, got.Name)
	require.NotNil(t, got.Namespace)
	assert.Equal(t, namespace.Identifier, got.Namespace.Identifier)
	assert.Equal(t, expected.DefaultBranch, got.DefaultBranch)
	assert.Equal(t, expected.StorageClass, got.StorageClass)
	assert.Equal(t, got.Status.Resolved.StoragePath, got.StoragePath)
	assert.Equal(t, expected.CreatedAt, got.CreatedAt)
	assert.Equal(t, expected.CreatedBy, got.CreatedBy)
	assert.Equal(t, expected.UpdatedAt, got.UpdatedAt)
	assert.Equal(t, expected.UpdatedBy, got.UpdatedBy)
}

func repositoryReadStoragePath(dataDir, repositoryID string) string {
	hexID := strings.ReplaceAll(repositoryID, "-", "")
	return fmt.Sprintf("%s/%s/%s/%s.git", dataDir, hexID[:2], hexID[2:4], repositoryID)
}
