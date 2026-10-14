// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
)

const (
	globalIDTestCategoryUID  = "00000000-0000-0000-0000-000000000001"
	globalIDTestCollectionID = "00000000-0000-0000-0000-000000000002"
	globalIDTestProductUID   = "00000000-0000-0000-0000-000000000003"
	globalIDTestNamespaceID  = "00000000-0000-0000-0000-000000000004"
	globalIDTestVariantUID   = "00000000-0000-0000-0000-000000000005"
	globalIDTestRepositoryID = "00000000-0000-0000-0000-000000000006"
	globalIDTestFileUID      = "00000000-0000-0000-0000-000000000007"
)

func TestCompletionResolversRejectInvalidNodeIDs(t *testing.T) {
	mutation := &mutationResolver{}
	for _, kind := range []string{nodeKindNamespace, nodeKindRepository, nodeKindProduct, nodeKindCategory} {
		t.Run(kind, func(t *testing.T) {
			for _, id := range []string{"", "not-a-node-id", globalIDTestProductUID, mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)} {
				var err error
				switch kind {
				case nodeKindNamespace:
					_, err = mutation.CompleteNamespaceDeletion(t.Context(), model.CompleteNamespaceDeletionInput{ID: id, Name: "acme", ResourceVersion: "1"})
				case nodeKindRepository:
					_, err = mutation.CompleteRepositoryDeletion(t.Context(), model.CompleteRepositoryDeletionInput{ID: id, Namespace: "acme", Name: "item", ResourceVersion: "1"})
				case nodeKindProduct:
					_, err = mutation.CompleteProductDeletion(t.Context(), model.CompleteProductDeletionInput{ID: id, Namespace: "acme", Name: "item", ResourceVersion: "1"})
				case nodeKindCategory:
					_, err = mutation.CompleteCategoryDeletion(t.Context(), model.CompleteCategoryDeletionInput{ID: id, Namespace: "acme", Name: "item", ResourceVersion: "1"})
				}
				require.ErrorContains(t, err, "invalid global ID", "completion must reject invalid or wrong-kind IDs before accessing storage")
			}
		})
	}
}

func TestQueryNodeResolvesByGlobalID(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	node, err := query.Node(ctx, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID))
	require.NoError(t, err)
	product, ok := node.(*model.Product)
	require.True(t, ok)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)

	variantNode, err := query.Node(ctx, mustEncodeNodeID(nodeKindProductVariant, globalIDTestVariantUID))
	require.NoError(t, err)
	variant, ok := variantNode.(*model.ProductVariant)
	require.True(t, ok)
	assert.Equal(t, mustEncodeNodeID(nodeKindProductVariant, globalIDTestVariantUID), variant.ID)

	fileNode, err := query.Node(ctx, mustEncodeNodeID(nodeKindFile, globalIDTestFileUID))
	require.NoError(t, err)
	file, ok := fileNode.(*model.File)
	require.True(t, ok)
	typedFile, err := query.File(ctx, "test-store", "file-1")
	require.NoError(t, err)
	require.Equal(t, typedFile, file)
}

func TestQueryNodesPreservesOrderAndSkipsInvalidIDs(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	nodes, err := query.Nodes(ctx, []string{
		mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID),
		"not-base64!",
		mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryUID),
		mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID),
		mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID),
		mustEncodeNodeID(nodeKindProductVariant, globalIDTestVariantUID),
		mustEncodeNodeID(nodeKindProduct, "missing"),
		mustEncodeNodeID(nodeKindFile, globalIDTestFileUID),
		mustEncodeNodeID(nodeKindFile, "00000000-0000-0000-0000-000000000008"),
	})
	require.NoError(t, err)
	require.Len(t, nodes, 9)
	assert.IsType(t, &model.Namespace{}, nodes[0])
	assert.Nil(t, nodes[1])
	assert.IsType(t, &model.Category{}, nodes[2])
	assert.IsType(t, &model.Collection{}, nodes[3])
	assert.IsType(t, &model.Product{}, nodes[4])
	assert.IsType(t, &model.ProductVariant{}, nodes[5])
	assert.Nil(t, nodes[6])
	assert.IsType(t, &model.File{}, nodes[7])
	assert.Nil(t, nodes[8])
}

func TestFileNodesObserveSharedDatastoreUpdatesAcrossResolvers(t *testing.T) {
	store, first := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, t.Context(), store)
	second, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	file, err := store.GetFile(t.Context(), globalIDTestFileUID)
	require.NoError(t, err)
	version := file.ResourceVersion
	file.Annotations = map[string]string{"sequence": "1"}
	datastore.AdvanceFileSystemVersion(file)
	require.NoError(t, store.UpdateFile(t.Context(), file, version))
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	for _, root := range []*Resolver{first, second} {
		nodes, err := (&queryResolver{Resolver: root}).Nodes(t.Context(), []string{id, id})
		require.NoError(t, err)
		require.Len(t, nodes, 2)
		for _, node := range nodes {
			file, ok := node.(*model.File)
			require.True(t, ok)
			require.Equal(t, id, file.ID)
			require.Equal(t, "1", file.Metadata.Annotations["sequence"])
		}
	}
}

type globalIDFileFailureStore struct{ datastore.Datastore }

type globalIDCountedFileStore struct {
	datastore.Datastore
	reads atomic.Int64
}

func (s *globalIDCountedFileStore) GetFile(ctx context.Context, uid string) (*datastore.File, error) {
	s.reads.Add(1)
	return s.Datastore.GetFile(ctx, uid)
}

func (s *globalIDCountedFileStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func TestFileNodeResolverReturnsExactAuthorizedFieldSnapshot(t *testing.T) {
	for _, field := range []string{"node", "nodes"} {
		t.Run(field, func(t *testing.T) {
			store, root := newGlobalIDTestResolver(t)
			seedGlobalIDTestData(t, t.Context(), store)
			counted := &globalIDCountedFileStore{Datastore: store}
			root.store = counted
			query := &queryResolver{Resolver: root}
			authz := testutil.NewAllowAllAuthZ()
			mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), counted, zap.NewNop())
			id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
			ctx := graphql.WithFieldContext(t.Context(), &graphql.FieldContext{
				Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}},
				Args: map[string]any{"id": id, "ids": []string{id, id}},
			})
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(authorizedCtx context.Context) (any, error) {
				current, err := store.GetFile(t.Context(), globalIDTestFileUID)
				require.NoError(t, err)
				expected := current.ResourceVersion
				current.Annotations = map[string]string{"sequence": "changed-after-authorization"}
				current.CreationActor = "different-owner"
				datastore.AdvanceFileSystemVersion(current)
				require.NoError(t, store.UpdateFile(t.Context(), current, expected))
				var nodes []model.Node
				if field == "node" {
					node, err := query.Node(authorizedCtx, id)
					require.NoError(t, err)
					nodes = []model.Node{node}
				} else {
					nodes, err = query.Nodes(authorizedCtx, []string{id, id})
					require.NoError(t, err)
					require.Len(t, nodes, 2)
				}
				for _, node := range nodes {
					file, ok := node.(*model.File)
					require.True(t, ok)
					require.NotEqual(t, "changed-after-authorization", file.Metadata.Annotations["sequence"])
				}
				return nil, nil
			})
			require.NoError(t, err)
			wantReads := int64(1)
			if field == "nodes" {
				wantReads = 2
			}
			require.Equal(t, wantReads, counted.reads.Load(), "hydration must not refetch an authorized File")
			_, err = mw.GraphQLFieldAuthorizer(ctx, func(freshCtx context.Context) (any, error) {
				node, err := query.Node(freshCtx, id)
				require.NoError(t, err)
				require.Equal(t, "changed-after-authorization", node.(*model.File).Metadata.Annotations["sequence"])
				return nil, nil
			})
			require.NoError(t, err)
			require.Equal(t, 2*wantReads, counted.reads.Load(), "another field must perform fresh authorization reads")
			require.Equal(t, "different-owner", authz.Resource.OwnerSub)
		})
	}
}

func TestFileNodeMissingSnapshotDoesNotExposeLaterUnapprovedCreate(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	counted := &globalIDCountedFileStore{Datastore: store}
	root.store = counted
	query := &queryResolver{Resolver: root}
	authz := testutil.NewAllowAllAuthZ()
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), counted, zap.NewNop())
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	ctx := graphql.WithFieldContext(t.Context(), &graphql.FieldContext{
		Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "node"}},
		Args: map[string]any{"id": id},
	})
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(authorizedCtx context.Context) (any, error) {
		require.NoError(t, store.CreateFile(t.Context(), &datastore.File{
			UID: globalIDTestFileUID, Namespace: "private", Name: "late-file", CreationActor: "other-owner",
		}))
		node, err := query.Node(authorizedCtx, id)
		require.NoError(t, err)
		require.Nil(t, node)
		return nil, nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, counted.reads.Load())
	require.Empty(t, authz.Calls, "missing rows must not become authorized by a later lookup")
	_, err = mw.GraphQLFieldAuthorizer(ctx, func(freshCtx context.Context) (any, error) {
		node, err := query.Node(freshCtx, id)
		require.NoError(t, err)
		require.NotNil(t, node)
		return nil, nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, counted.reads.Load())
	require.Equal(t, "other-owner", authz.Resource.OwnerSub)
}

func (globalIDFileFailureStore) GetFile(context.Context, string) (*datastore.File, error) {
	return nil, errors.New("file storage unavailable")
}

func (s globalIDFileFailureStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func TestFileNodeStorageErrorsAreNotReportedAsMissing(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	root.store = globalIDFileFailureStore{store}
	query := &queryResolver{Resolver: root}
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	_, err := query.Node(t.Context(), id)
	require.ErrorContains(t, err, "retrieve File")
	require.NotContains(t, err.Error(), "file storage unavailable")
	_, err = query.Nodes(t.Context(), []string{id})
	require.ErrorContains(t, err, "retrieve File")
	require.NotContains(t, err.Error(), "file storage unavailable")
}

type globalIDPointFailureStore struct {
	datastore.Datastore
	err  error
	repo *datastore.Repository
}

func (s globalIDPointFailureStore) GetFile(context.Context, string) (*datastore.File, error) {
	return nil, s.err
}
func (s globalIDPointFailureStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}
func (s globalIDPointFailureStore) GetProduct(context.Context, string) (*datastore.Product, error) {
	return nil, s.err
}
func (s globalIDPointFailureStore) GetProductVariant(context.Context, string) (*datastore.ProductVariant, error) {
	return nil, s.err
}
func (s globalIDPointFailureStore) GetCategoryTaxonomy(context.Context, string) (*datastore.CategoryTaxonomy, error) {
	return nil, s.err
}
func (s globalIDPointFailureStore) GetCollection(context.Context, string) (*datastore.Collection, error) {
	return nil, s.err
}
func (s globalIDPointFailureStore) GetNamespace(context.Context, string) (*datastore.Namespace, error) {
	return nil, s.err
}
func (s globalIDPointFailureStore) GetRepository(context.Context, string) (*datastore.Repository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	return nil, s.err
}

func TestNodeReadsDistinguishAbsenceFromInfrastructureFailure(t *testing.T) {
	for kind := range supportedNodeKinds {
		for _, failure := range []error{
			datastore.ErrNotFound,
			fmt.Errorf("wrapped absence: %w", datastore.ErrNotFound),
			errors.New("private storage failure detail"),
			context.Canceled,
			context.DeadlineExceeded,
		} {
			t.Run(kind+"/"+failure.Error(), func(t *testing.T) {
				store, root := newGlobalIDTestResolver(t)
				failed := globalIDPointFailureStore{Datastore: store, err: failure}
				root.store, root.service.store = failed, failed
				query := &queryResolver{Resolver: root}
				id := mustEncodeNodeID(kind, globalIDTestProductUID)
				node, err := query.Node(t.Context(), id)
				require.Nil(t, node)
				if errors.Is(failure, datastore.ErrNotFound) {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, failure)
					require.NotContains(t, err.Error(), "private storage failure detail")
					encoded, marshalErr := json.Marshal(err)
					require.NoError(t, marshalErr)
					require.Contains(t, string(encoded), `"message":"retrieve `+kind+`"`)
					require.NotContains(t, string(encoded), "private storage failure detail")
				}
				nodes, err := query.Nodes(t.Context(), []string{id, id})
				if errors.Is(failure, datastore.ErrNotFound) {
					require.NoError(t, err)
					require.Equal(t, []model.Node{nil, nil}, nodes)
				} else {
					require.ErrorIs(t, err, failure)
					require.Nil(t, nodes, "an infrastructure failure returned a success-shaped partial result")
				}
			})
		}
	}
}

func TestRepositoryNodeOwnerLookupDoesNotHideInfrastructureFailure(t *testing.T) {
	for _, failure := range []error{datastore.ErrNotFound, errors.New("private owner lookup failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			store, root := newGlobalIDTestResolver(t)
			failed := globalIDPointFailureStore{
				Datastore: store,
				err:       failure,
				repo:      &datastore.Repository{ID: globalIDTestRepositoryID, NamespaceID: globalIDTestNamespaceID},
			}
			root.store, root.service.store = failed, failed
			id := mustEncodeNodeID(nodeKindRepository, globalIDTestRepositoryID)
			query := &queryResolver{Resolver: root}
			node, err := query.Node(t.Context(), id)
			require.Nil(t, node)
			nodes, pluralErr := query.Nodes(t.Context(), []string{id, id})
			if errors.Is(failure, datastore.ErrNotFound) {
				require.NoError(t, err)
				require.NoError(t, pluralErr)
				require.Equal(t, []model.Node{nil, nil}, nodes)
			} else {
				require.ErrorIs(t, err, failure)
				require.ErrorIs(t, pluralErr, failure)
				require.NotContains(t, err.Error(), "private owner lookup failure")
				require.Nil(t, nodes)
			}
		})
	}
}

type globalIDBlockingFileStore struct {
	datastore.Datastore
	started chan string
	release <-chan struct{}
	active  atomic.Int64
}

func (s *globalIDBlockingFileStore) GetFile(ctx context.Context, uid string) (*datastore.File, error) {
	s.active.Add(1)
	defer s.active.Add(-1)
	s.started <- uid
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return &datastore.File{UID: uid, Namespace: "test-store", Name: uid}, nil
	}
}

func (s *globalIDBlockingFileStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func TestQueryNodesBoundsConcurrentHydrationAndPreservesPositions(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	release := make(chan struct{})
	blocked := &globalIDBlockingFileStore{Datastore: store, started: make(chan string, 100), release: release}
	root.store = blocked
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = mustEncodeNodeID(nodeKindFile, fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
	}
	type result struct {
		nodes []model.Node
		err   error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		nodes, err := (&queryResolver{Resolver: root}).Nodes(ctx, ids)
		done <- result{nodes, err}
	}()
	for range nodeFetchFanout {
		select {
		case <-blocked.started:
		case <-time.After(time.Second):
			t.Fatal("node hydration did not fill the bounded worker pool")
		}
	}
	require.EqualValues(t, nodeFetchFanout, blocked.active.Load())
	select {
	case <-blocked.started:
		t.Fatal("node hydration exceeded eight active reads")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Len(t, got.nodes, len(ids))
		for i, node := range got.nodes {
			require.Equal(t, ids[i], node.GetID())
		}
	case <-time.After(time.Second):
		t.Fatal("node hydration did not finish")
	}
	require.Zero(t, blocked.active.Load())
}

func TestQueryNodesCancellationDrainsReadsAndStopsScheduling(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	blocked := &globalIDBlockingFileStore{Datastore: store, started: make(chan string, 100)}
	root.store = blocked
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		nodes, err := (&queryResolver{Resolver: root}).Nodes(ctx, ids)
		if nodes != nil {
			done <- errors.New("canceled node query returned partial success")
			return
		}
		done <- err
	}()
	for range nodeFetchFanout {
		select {
		case <-blocked.started:
		case <-time.After(time.Second):
			t.Fatal("node hydration did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled node query retained active reads")
	}
	require.Zero(t, blocked.active.Load())
	require.Empty(t, blocked.started, "cancellation must not schedule the remaining IDs")
	_, err := (&queryResolver{Resolver: root}).Nodes(ctx, ids)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, blocked.started)
}

func TestLookupQueriesAcceptGlobalIDs(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(ctx, model.ProductBy{NamespacePath: &model.ProductNamespacePath{Namespace: "test-store", Name: "product-1"}})
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)

	categoryID := mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryUID)
	category, err := query.Category(ctx, model.CategoryBy{ID: &categoryID})
	require.NoError(t, err)
	require.NotNil(t, category)
	assert.Equal(t, categoryID, category.ID)

	collectionID := mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID)
	collection, err := query.Collection(ctx, model.CollectionBy{ID: &collectionID})
	require.NoError(t, err)
	require.NotNil(t, collection)
	assert.Equal(t, mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID), collection.ID)

	namespaceID := mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID)
	namespace, err := query.Namespace(ctx, model.NamespaceBy{ID: &namespaceID})
	require.NoError(t, err)
	require.NotNil(t, namespace)
	assert.Equal(t, mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID), namespace.ID)
	require.NotNil(t, namespace.Metadata)
	assert.Equal(t, namespace.ID, namespace.Metadata.UID)
	assert.Equal(t, "1", namespace.Metadata.ResourceVersion)
	assert.Equal(t, int32(1), namespace.Metadata.Generation)
	assert.Equal(t, "namespace-1", namespace.Metadata.Name)
}

func TestLookupProductByName(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(ctx, model.ProductBy{NamespacePath: &model.ProductNamespacePath{Namespace: "test-store", Name: "product-1"}})
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)
	assert.Equal(t, "catalog.gitstore.dev/v1beta1", product.APIVersion)
}

func TestLookupProduct_NotFound(t *testing.T) {
	_, resolver := newGlobalIDTestResolver(t)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(context.Background(), model.ProductBy{NamespacePath: &model.ProductNamespacePath{Namespace: "test-store", Name: "no-such-product"}})
	assert.Nil(t, product)
	assert.NoError(t, err) // not found returns nil, nil per resolver convention
}

func TestRepositoryNodeIdentityUsesStableRelayIDAndMetadataUID(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	node, err := query.Node(ctx, mustEncodeNodeID(nodeKindRepository, globalIDTestRepositoryID))
	require.NoError(t, err)
	repository, ok := node.(*model.Repository)
	require.True(t, ok)
	assert.Equal(t, mustEncodeNodeID(nodeKindRepository, globalIDTestRepositoryID), repository.ID)
	require.NotNil(t, repository.Metadata)
	assert.Equal(t, repository.ID, repository.Metadata.UID)
	assert.Equal(t, "1", repository.Metadata.ResourceVersion)
	assert.Equal(t, int32(1), repository.Metadata.Generation)
}

func newGlobalIDTestResolver(t *testing.T) (datastore.Datastore, *Resolver) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	return store, r
}

func seedGlobalIDTestData(t *testing.T, ctx context.Context, store datastore.Datastore) {
	t.Helper()
	now := time.Now()
	require.NoError(t, store.CreateFile(ctx, &datastore.File{
		UID: globalIDTestFileUID, Namespace: "test-store", Name: "file-1",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: now,
		Spec: json.RawMessage(`{"contentType":"image/jpeg","type":"gitstore.dev/media"}`),
	}))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID:               globalIDTestCategoryUID,
		Namespace:         "test-store",
		Name:              "category-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: now,
	}))
	require.NoError(t, store.CreateCollection(ctx, &datastore.Collection{
		UID:               globalIDTestCollectionID,
		Namespace:         "test-store",
		Name:              "collection-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Collection",
		CreationTimestamp: now,
	}))
	require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
		UID:               globalIDTestProductUID,
		Namespace:         "test-store",
		Name:              "product-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: now,
	}))
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{
		ID:                globalIDTestNamespaceID,
		Name:              "namespace-1",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: now,
		CreationActor:     "tester",
		UpdateTimestamp:   now,
		UpdateActor:       "tester",
	}))
	require.NoError(t, store.CreateProductVariant(ctx, &datastore.ProductVariant{
		UID:               globalIDTestVariantUID,
		Namespace:         "test-store",
		Name:              "variant-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "ProductVariant",
		CreationTimestamp: now,
		SKU:               "sku-1",
		ProductRefName:    "product-1",
	}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID:               globalIDTestRepositoryID,
		Namespace:         "namespace-1",
		RepositoryID:      globalIDTestRepositoryID,
		Name:              "repository-1",
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     "tester",
		UpdateTimestamp:   now,
		UpdateActor:       "tester",
	}))
}
