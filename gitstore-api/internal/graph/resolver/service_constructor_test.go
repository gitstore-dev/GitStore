// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"testing"
	"time"

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
	repositoryID := "22222222-2222-7222-8222-222222222222"
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	writer := &mockGitWriter{}
	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:       store,
		GitWriter:   writer,
		Logger:      zap.NewNop(),
		Clock:       apiruntime.NewFixedClock(now),
		IDGenerator: apiruntime.NewSequenceIDGenerator(namespaceID, repositoryID),
	})
	require.NoError(t, err)

	ns, err := svc.CreateNamespace(ctx, model.CreateNamespaceInput{
		Identifier: "acme",
		Tier:       model.NamespaceTierUser,
	}, "admin", true)
	require.NoError(t, err)
	assert.Equal(t, namespaceID, ns.ID)
	assert.Equal(t, now, ns.CreatedAt)
	assert.Equal(t, now, ns.UpdatedAt)

	repo, err := svc.CreateRepository(ctx, ns.ID, "catalog", "", "", "admin")
	require.NoError(t, err)
	assert.Equal(t, repositoryID, repo.ID)
	assert.Equal(t, now, repo.CreatedAt)
	assert.Equal(t, now, repo.UpdatedAt)
	assert.Equal(t, []string{repositoryID}, writer.createRepoCalls)
}
