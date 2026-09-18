// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Typed and generic Product streams are projections of the same durable
// cursor. This contract intentionally bypasses the process-local event bus so
// replay and selector behavior cannot regress to replica-local state.
func TestProductDurableWatchContract_TypedGenericBootstrapReplayAndSelector(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(context.Background(), "product-contract", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = journal.Append(context.Background(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark, At: time.Now()}, time.Hour)
	require.NoError(t, err)
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store: store, Logger: zap.NewNop(), ResourceJournal: journal,
		NamespaceWatch: config.NamespaceWatchConfig{ReadersEnabled: true, ReadBatchSize: 32, MaxReplayEvents: 32, SubscriberBuffer: 8, SubscriberBackpressureMillis: 100, PollMinMillis: 1, PollMaxMillis: 5, MaxMaterializerLagSeconds: 60},
	})
	require.NoError(t, err)
	bootstrap := watchjournal.BootstrapCursor
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "catalog"}}
	typed, err := r.Subscription().WatchProducts(context.Background(), nil, selector, &bootstrap)
	require.NoError(t, err)
	generic, err := r.Subscription().WatchResources(context.Background(), "Product", nil, selector, &bootstrap)
	require.NoError(t, err)
	require.Equal(t, model.WatchEventTypeBookmark, receiveProductContractTyped(t, typed).Type)
	require.Equal(t, model.WatchEventTypeBookmark, receiveProductContractGeneric(t, generic).Type)

	product := &datastore.Product{UID: uuid.NewString(), Namespace: "gitstore", Name: "widget", APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Generation: 1, ResourceVersion: "1", Labels: map[string]string{"team": "catalog"}}
	payload, err := json.Marshal(product)
	require.NoError(t, err)
	_, err = journal.Append(context.Background(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "Product", Namespace: product.Namespace, Name: product.Name, Payload: payload, SelectorLabels: product.Labels, At: time.Now()}, time.Hour)
	require.NoError(t, err)
	gotTyped := receiveProductContractTyped(t, typed)
	gotGeneric := receiveProductContractGeneric(t, generic)
	require.Equal(t, model.WatchEventTypeAdded, gotTyped.Type)
	require.Equal(t, gotTyped.ResourceVersion, gotGeneric.ResourceVersion)
	require.NotNil(t, gotTyped.Product)
	require.Equal(t, product.Name, gotTyped.Product.Metadata.Name)
}

func receiveProductContractTyped(t *testing.T, events <-chan *model.ProductWatchEvent) *model.ProductWatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for typed Product event")
		return nil
	}
}

func receiveProductContractGeneric(t *testing.T, events <-chan *model.WatchEvent) *model.WatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generic Product event")
		return nil
	}
}

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
		UID:               uuid.New().String(),
		Name:              "gitstore",
		Title:             "GitStore Test",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
	}
	require.NoError(t, store.CreateNamespace(ctx, ns))

	const repoID = "00000000-0000-0000-0000-000000000001"
	repo := &datastore.Repository{
		UID:               repoID,
		Namespace:         ns.Name,
		Name:              "catalog",
		DefaultBranch:     "main",
		StorageClass:      "local",
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
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
		NewCommitSha: strings.Repeat("a", 40),
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
		require.Equal(t, "widget", ev.Product.Metadata.Name)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watchProducts event")
	}
}

// T019: deleting a product with a categoryRef first delivers a MODIFIED
// terminating ProductWatchEvent. The Product controller emits the final
// DELETED event only after foreground blockers are clear.
func TestWatchProducts_ProductDeletion_DeliversTerminatingEvent(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Now()
	ns := &datastore.Namespace{
		UID:               uuid.New().String(),
		Name:              "gitstore",
		Title:             "GitStore Test",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
	}
	require.NoError(t, store.CreateNamespace(ctx, ns))

	const repoID = "00000000-0000-0000-0000-000000000002"
	repo := &datastore.Repository{
		UID:               repoID,
		Namespace:         ns.Name,
		Name:              "catalog",
		DefaultBranch:     "main",
		StorageClass:      "local",
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
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
		OldCommitSha: a,
		NewCommitSha: b,
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	select {
	case ev := <-events:
		require.Equal(t, model.WatchEventTypeModified, ev.Type)
		require.Equal(t, "widget", ev.Name)
		require.NotNil(t, ev.Namespace)
		require.Equal(t, "gitstore", *ev.Namespace)
		require.NotNil(t, ev.Product)
		require.NotNil(t, ev.Product.Metadata.DeletionTimestamp)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminating watchProducts event")
	}
}
