// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func (globalIDFileFailureStore) GetFile(context.Context, string) (*datastore.File, error) {
	return nil, errors.New("file storage unavailable")
}

func TestFileNodeStorageErrorsAreNotReportedAsMissing(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	root.store = globalIDFileFailureStore{store}
	query := &queryResolver{Resolver: root}
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	_, err := query.Node(t.Context(), id)
	require.ErrorContains(t, err, "retrieve File: file storage unavailable")
	_, err = query.Nodes(t.Context(), []string{id})
	require.ErrorContains(t, err, "retrieve File: file storage unavailable")
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
