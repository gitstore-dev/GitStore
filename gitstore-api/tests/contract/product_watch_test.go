// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// stubGitReader is a minimal cataloggrpc.GitReader returning one fixed
// product file, for exercising AdmitResources end-to-end in this package.
type stubGitReader struct {
	path string
	blob []byte
}

func (g *stubGitReader) ListFiles(_ context.Context, _, _, _ string) ([]string, error) {
	return []string{g.path}, nil
}

func (g *stubGitReader) ReadFile(_ context.Context, _, _, _ string) ([]byte, error) {
	return g.blob, nil
}

func (g *stubGitReader) ResolveRef(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

// treeStubGitReader serves a different file tree per commit ref, for
// exercising an old→new commit delta (e.g. a file's removal) in
// AdmitResources.
type treeStubGitReader struct {
	current *string
	trees   map[string]map[string][]byte
}

func (g *treeStubGitReader) ListFiles(_ context.Context, _, _, ref string) ([]string, error) {
	tree := g.trees[ref]
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	return paths, nil
}

func (g *treeStubGitReader) ReadFile(_ context.Context, _, path, ref string) ([]byte, error) {
	return g.trees[ref][path], nil
}

func (g *treeStubGitReader) ResolveRef(_ context.Context, _, _ string) (string, error) {
	return *g.current, nil
}

// T007: admitting a product with a categoryRef via the gRPC admission path
// delivers a matching ProductWatchEvent to a watchProducts subscriber.
func TestWatchProducts_ProductAdmission_DeliversAddedEvent(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Now()
	ns := &datastore.Namespace{
		ID:          uuid.New().String(),
		Identifier:  "gitstore",
		DisplayName: "GitStore Test",
		Tier:        datastore.NamespaceTierUser,
		CreatedAt:   now,
		CreatedBy:   "test",
		UpdatedAt:   now,
		UpdatedBy:   "test",
	}
	require.NoError(t, store.CreateNamespace(ctx, ns))

	const repoID = "00000000-0000-0000-0000-000000000001"
	repo := &datastore.Repository{
		ID:            repoID,
		NamespaceID:   ns.ID,
		Name:          "catalog",
		DefaultBranch: "main",
		StorageClass:  "local",
		CreatedAt:     now,
		CreatedBy:     "test",
		UpdatedAt:     now,
		UpdatedBy:     "test",
	}
	require.NoError(t, store.CreateRepository(ctx, repo))

	bus := eventbus.New(100)

	srv, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store:    store,
		Logger:   zap.NewNop(),
		EventBus: bus,
		GitReader: &stubGitReader{
			path: "products/widget.md",
			blob: []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: widget\n  namespace: gitstore\nspec:\n  title: Widget\n  categoryRef:\n    name: electronics\n---\n"),
		},
	})
	require.NoError(t, err)

	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:    store,
		Logger:   zap.NewNop(),
		Clock:    apiruntime.SystemClock{},
		EventBus: bus,
	})
	require.NoError(t, err)

	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := r.Subscription().WatchProducts(watchCtx, nil, nil, nil)
	require.NoError(t, err)

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: repoID,
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	select {
	case ev := <-events:
		require.Equal(t, model.WatchEventTypeAdded, ev.Type)
		require.Equal(t, "widget", ev.Name)
		require.NotNil(t, ev.Namespace)
		require.Equal(t, "gitstore", *ev.Namespace)
		require.NotNil(t, ev.Product)
		require.NotNil(t, ev.Product.Spec.CategoryRef)
		require.Equal(t, "electronics", ev.Product.Spec.CategoryRef.Name)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watchProducts event")
	}
}

// T019: deleting a product with a categoryRef delivers a DELETED
// ProductWatchEvent with product: null and the correct name/namespace.
func TestWatchProducts_ProductDeletion_DeliversDeletedEvent(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Now()
	ns := &datastore.Namespace{
		ID:          uuid.New().String(),
		Identifier:  "gitstore",
		DisplayName: "GitStore Test",
		Tier:        datastore.NamespaceTierUser,
		CreatedAt:   now,
		CreatedBy:   "test",
		UpdatedAt:   now,
		UpdatedBy:   "test",
	}
	require.NoError(t, store.CreateNamespace(ctx, ns))

	const repoID = "00000000-0000-0000-0000-000000000002"
	repo := &datastore.Repository{
		ID:            repoID,
		NamespaceID:   ns.ID,
		Name:          "catalog",
		DefaultBranch: "main",
		StorageClass:  "local",
		CreatedAt:     now,
		CreatedBy:     "test",
		UpdatedAt:     now,
		UpdatedBy:     "test",
	}
	require.NoError(t, store.CreateRepository(ctx, repo))

	bus := eventbus.New(100)

	zero := strings.Repeat("0", 40)
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	current := a
	blob := []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: widget\n  namespace: gitstore\nspec:\n  title: Widget\n  categoryRef:\n    name: electronics\n---\n")
	git := &treeStubGitReader{
		current: &current,
		trees: map[string]map[string][]byte{
			a: {"products/widget.md": blob},
			b: {},
		},
	}

	srv, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store:     store,
		Logger:    zap.NewNop(),
		EventBus:  bus,
		GitReader: git,
	})
	require.NoError(t, err)

	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:    store,
		Logger:   zap.NewNop(),
		Clock:    apiruntime.SystemClock{},
		EventBus: bus,
	})
	require.NoError(t, err)

	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := r.Subscription().WatchProducts(watchCtx, nil, nil, nil)
	require.NoError(t, err)

	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: repoID,
		CommitSha:    a,
		OldCommitSha: zero,
		NewCommitSha: a,
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	// Drain the ADDED event from creation.
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ADDED event")
	}

	current = b
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
		RepositoryId: repoID,
		CommitSha:    b,
		OldCommitSha: a,
		NewCommitSha: b,
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	select {
	case ev := <-events:
		require.Equal(t, model.WatchEventTypeDeleted, ev.Type)
		require.Equal(t, "widget", ev.Name)
		require.NotNil(t, ev.Namespace)
		require.Equal(t, "gitstore", *ev.Namespace)
		require.Nil(t, ev.Product)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DELETED watchProducts event")
	}
}
