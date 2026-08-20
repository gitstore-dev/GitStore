// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewServiceRequiresDatastore(t *testing.T) {
	_, err := resolver.NewService(resolver.ServiceDeps{Logger: zap.NewNop()})
	require.ErrorContains(t, err, "datastore is required")
}

func TestNewServiceRequiresLogger(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()

	_, err = resolver.NewService(resolver.ServiceDeps{Store: store})
	require.ErrorContains(t, err, "logger is required")
}

func TestNewServiceDefaultsOptionalDependencies(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()

	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:  store,
		Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestServiceCreateNamespaceAndRepositoryUsesInjectedClockAndIDs(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	namespaceID := "11111111-1111-4111-8111-111111111111"
	systemRepositoryID := "22222222-2222-7222-8222-222222222222"
	repositoryID := "33333333-3333-7333-8333-333333333333"
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	systemNamespace := &datastore.Namespace{
		ID:                "44444444-4444-4444-8444-444444444444",
		Name:              "gitstore-system",
		Tier:              datastore.NamespaceTierOrganization,
		CreationTimestamp: now,
		CreationActor:     "system",
		UpdateTimestamp:   now,
		UpdateActor:       "system",
	}
	require.NoError(t, store.CreateNamespace(ctx, systemNamespace))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID:               systemRepositoryID,
		Namespace:         systemNamespace.Name,
		RepositoryID:      systemRepositoryID,
		Name:              resolver.SystemRepositoryName,
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     "system",
		UpdateTimestamp:   now,
		UpdateActor:       "system",
	}))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace:    systemNamespace.Name,
		Name:         resolver.SystemRepositoryName,
		RepositoryID: systemRepositoryID,
	}))
	writer := &mockGitWriter{}
	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:       store,
		GitWriter:   writer,
		Logger:      zap.NewNop(),
		Clock:       apiruntime.NewFixedClock(now),
		IDGenerator: apiruntime.NewSequenceIDGenerator(namespaceID, repositoryID),
	})
	require.NoError(t, err)

	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("acme", model.NamespaceTierUser), "admin")
	require.NoError(t, err)
	assert.Equal(t, namespaceID, ns.ID)
	assert.Equal(t, now, ns.CreationTimestamp)
	assert.Equal(t, now, ns.UpdateTimestamp)

	repo, err := svc.CreateRepository(ctx, ns.ID, "catalog", "", "", "admin")
	require.NoError(t, err)
	assert.Equal(t, repositoryID, repo.ID)
	assert.Equal(t, now, repo.CreationTimestamp)
	assert.Equal(t, now, repo.UpdateTimestamp)
	assert.Equal(t, []string{repositoryID}, writer.createRepoCalls)
}
