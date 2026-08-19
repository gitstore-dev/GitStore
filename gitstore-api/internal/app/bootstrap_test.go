// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type bootstrapGitProvisioner struct {
	repositoryIDs []string
}

func (p *bootstrapGitProvisioner) CreateRepository(_ context.Context, repositoryID, _ string) (string, error) {
	p.repositoryIDs = append(p.repositoryIDs, repositoryID)
	return "/repos/" + repositoryID, nil
}

func TestEnsureBootstrapResourcesCreatesExactlyTwoNamespacesAndRepositoriesIdempotently(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	git := &bootstrapGitProvisioner{}
	ids := apiruntime.NewSequenceIDGenerator(
		"00000000-0000-7000-8000-000000000001",
		"00000000-0000-7000-8000-000000000002",
		"00000000-0000-7000-8000-000000000003",
		"00000000-0000-7000-8000-000000000004",
	)
	clock := apiruntime.NewFixedClock(time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC))

	require.NoError(t, ensureBootstrapResources(context.Background(), store, git, clock, ids, zap.NewNop()))
	require.NoError(t, ensureBootstrapResources(context.Background(), store, git, clock, ids, zap.NewNop()))

	namespaces, err := store.ListNamespaces(context.Background(), datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, namespaces.Items, 2)
	assert.Len(t, git.repositoryIDs, 2)

	for _, identifier := range []string{"gitstore-system", "default"} {
		namespace, getErr := store.GetNamespaceByName(context.Background(), identifier)
		require.NoError(t, getErr)
		assert.Equal(t, bootstrapActor, namespace.CreationActor)
		assert.Equal(t, datastore.NamespaceInitialResourceVersion, namespace.ResourceVersion)

		mapping, lookupErr := store.LookupRepository(context.Background(), namespace.ID, bootstrapRepositoryName)
		require.NoError(t, lookupErr)
		repository, repoErr := store.GetRepository(context.Background(), mapping.RepoID)
		require.NoError(t, repoErr)
		assert.Equal(t, bootstrapRepositoryName, repository.Name)
		assert.Equal(t, namespace.ID, repository.NamespaceID)
	}
}
