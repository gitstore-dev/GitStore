// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type bootstrapGitProvisioner struct {
	repositoryIDs []string
	actors        []*auth.Principal
}

func (p *bootstrapGitProvisioner) CreateRepository(ctx context.Context, repositoryID, _ string) (string, error) {
	p.repositoryIDs = append(p.repositoryIDs, repositoryID)
	p.actors = append(p.actors, auth.PrincipalFromContext(ctx))
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
	for _, actor := range git.actors {
		require.NotNil(t, actor, "bootstrap must explicitly identify its internal backing actor")
		require.Equal(t, "system:api", actor.Subject)
		require.Equal(t, "internal", actor.AuthMethod)
	}

	for _, identifier := range []string{"gitstore-system", "default"} {
		namespace, getErr := store.GetNamespaceByName(context.Background(), identifier)
		require.NoError(t, getErr)
		assert.Equal(t, bootstrapActor, namespace.CreationActor)
		assert.Equal(t, datastore.NamespaceTierOrganization, namespace.Tier)
		assert.Equal(t, datastore.NamespaceInitialResourceVersion, namespace.ResourceVersion)
		mapping, lookupErr := store.LookupRepository(context.Background(), namespace.Name, bootstrapRepositoryName)
		require.NoError(t, lookupErr)
		repository, repoErr := store.GetRepository(context.Background(), mapping.RepositoryID)
		require.NoError(t, repoErr)
		assert.Equal(t, bootstrapRepositoryName, repository.Name)
		assert.Equal(t, namespace.Name, repository.Namespace)
		blocked, err := datastore.NamespaceDeletionBlocked(t.Context(), store, namespace)
		require.NoError(t, err)
		require.False(t, blocked, "an empty provisioned system repository is not an ordinary repository blocker")
	}
}

type recoverableBootstrapGit struct {
	mu       sync.Mutex
	ready    map[string]bool
	creates  map[string]int
	failNext bool
	loseAck  bool
}

func (g *recoverableBootstrapGit) CreateRepository(ctx context.Context, id, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		return "", errors.New("unbounded bootstrap")
	}
	g.creates[id]++
	if g.failNext {
		g.failNext = false
		return "", grpcstatus.Error(codes.Unavailable, "storage unavailable")
	}
	if g.ready[id] {
		return "", grpcstatus.Error(codes.AlreadyExists, "storage already provisioned")
	}
	g.ready[id] = true
	if g.loseAck {
		g.loseAck = false
		return "", grpcstatus.Error(codes.Unavailable, "lost storage acknowledgment")
	}
	return "/repos/" + id, nil
}

func (g *recoverableBootstrapGit) ResolveRef(_ context.Context, id, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.ready[id] {
		return "", grpcstatus.Error(codes.NotFound, "storage not provisioned")
	}
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

type interruptedBootstrapStore struct {
	datastore.Datastore
	failMapping atomic.Bool
}

func (s *interruptedBootstrapStore) CreateNamespaceMapping(ctx context.Context, mapping *datastore.NamespaceMapping) error {
	if s.failMapping.CompareAndSwap(true, false) {
		return errors.New("crash after repository row")
	}
	return s.Datastore.CreateNamespaceMapping(ctx, mapping)
}

func TestBootstrapProvisioningRecoversPartialMetadataAndUnknownGitOutcomes(t *testing.T) {
	for _, failure := range []string{"before-mapping", "before-storage", "lost-storage-ack", "legacy-self-identity"} {
		t.Run(failure, func(t *testing.T) {
			raw, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, raw.Close()) })
			store := &interruptedBootstrapStore{Datastore: raw}
			store.failMapping.Store(failure == "before-mapping")
			git := &recoverableBootstrapGit{ready: map[string]bool{}, creates: map[string]int{},
				failNext: failure == "before-storage" || failure == "legacy-self-identity", loseAck: failure == "lost-storage-ack"}
			clock, ids := apiruntime.SystemClock{}, apiruntime.UUIDGenerator{}
			require.Error(t, ensureBootstrapResources(t.Context(), store, git, clock, ids, zap.NewNop()))
			namespace, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
			require.NoError(t, err)
			rows, err := store.ListRepositoriesByNamespace(t.Context(), "gitstore-system", datastore.PageParams{})
			require.NoError(t, err)
			require.Len(t, rows.Items, 1, "an uncertain provisioning result must retain its authoritative row")
			originalID := rows.Items[0].UID
			if failure == "legacy-self-identity" {
				legacy := rows.Items[0]
				legacy.RepositoryID = ""
				require.NoError(t, store.UpdateRepository(t.Context(), legacy, legacy.ResourceVersion))
			}
			require.NoError(t, ensureBootstrapResources(t.Context(), store, git, clock, ids, zap.NewNop()))
			require.NoError(t, ensureBootstrapResources(t.Context(), store, git, clock, ids, zap.NewNop()))
			mapping, err := store.LookupRepository(t.Context(), namespace.Name, bootstrapRepositoryName)
			require.NoError(t, err)
			require.Equal(t, originalID, mapping.RepositoryID)
			rows, err = store.ListRepositoriesByNamespace(t.Context(), namespace.Name, datastore.PageParams{})
			require.NoError(t, err)
			require.Len(t, rows.Items, 1)
			blocked, err := datastore.NamespaceDeletionBlocked(t.Context(), store, namespace)
			require.NoError(t, err)
			require.False(t, blocked)
			require.True(t, git.ready[originalID])
			if failure == "lost-storage-ack" {
				require.Equal(t, 1, git.creates[originalID], "a proven existing repository is never reprovisioned")
			}
		})
	}
}

func TestConcurrentBootstrapKeepsOneRepositoryPerNamespace(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	git := &recoverableBootstrapGit{ready: map[string]bool{}, creates: map[string]int{}}
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			results <- ensureBootstrapResources(t.Context(), store, git, apiruntime.SystemClock{}, apiruntime.UUIDGenerator{}, zap.NewNop())
		}()
	}
	close(start)
	for range 8 {
		require.NoError(t, <-results)
	}
	namespaces, err := store.ListNamespaces(t.Context(), datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, namespaces.Items, 2)
	for _, namespace := range namespaces.Items {
		repositories, err := store.ListRepositoriesByNamespace(t.Context(), namespace.Name, datastore.PageParams{})
		require.NoError(t, err)
		require.Len(t, repositories.Items, 1)
		mapping, err := store.LookupRepository(t.Context(), namespace.Name, bootstrapRepositoryName)
		require.NoError(t, err)
		require.Equal(t, repositories.Items[0].UID, mapping.RepositoryID)
		require.True(t, git.ready[mapping.RepositoryID])
	}
}
