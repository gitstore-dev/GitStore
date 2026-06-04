// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph

import (
	"context"
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
	globalIDTestCategoryID   = "00000000-0000-0000-0000-000000000001"
	globalIDTestCollectionID = "00000000-0000-0000-0000-000000000002"
	globalIDTestProductUID   = "00000000-0000-0000-0000-000000000003"
	globalIDTestNamespaceID  = "00000000-0000-0000-0000-000000000004"
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
}

func TestQueryNodesPreservesOrderAndSkipsInvalidIDs(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	nodes, err := query.Nodes(ctx, []string{
		mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID),
		"not-base64!",
		mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryID),
		mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID),
		mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID),
		mustEncodeNodeID(nodeKindProduct, "missing"),
	})
	require.NoError(t, err)
	require.Len(t, nodes, 6)
	assert.IsType(t, &model.Namespace{}, nodes[0])
	assert.Nil(t, nodes[1])
	assert.IsType(t, &model.Category{}, nodes[2])
	assert.IsType(t, &model.Collection{}, nodes[3])
	assert.IsType(t, &model.Product{}, nodes[4])
	assert.Nil(t, nodes[5])
}

func TestLookupQueriesAcceptGlobalIDs(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(ctx, "test-store", "product-1")
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)

	categoryID := mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryID)
	category, err := query.Category(ctx, model.CategoryBy{ID: &categoryID})
	require.NoError(t, err)
	require.NotNil(t, category)
	assert.Equal(t, mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryID), category.ID)

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
}

func TestLookupProductByName(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(ctx, "test-store", "product-1")
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)
	assert.Equal(t, "catalog.gitstore.dev/v1beta1", product.APIVersion)
}

func TestLookupProduct_NotFound(t *testing.T) {
	_, resolver := newGlobalIDTestResolver(t)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(context.Background(), "test-store", "no-such-product")
	assert.Nil(t, product)
	assert.NoError(t, err) // not found returns nil, nil per resolver convention
}

func newGlobalIDTestResolver(t *testing.T) (datastore.Datastore, *Resolver) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	return store, NewResolver(store, nil, zap.NewNop())
}

func seedGlobalIDTestData(t *testing.T, ctx context.Context, store datastore.Datastore) {
	t.Helper()
	now := time.Now()
	require.NoError(t, store.CreateCategory(ctx, &datastore.Category{
		ID:        globalIDTestCategoryID,
		Name:      "Category 1",
		Slug:      "category-1",
		CreatedAt: now,
		UpdatedAt: now,
	}))
	require.NoError(t, store.CreateCollection(ctx, &datastore.Collection{
		ID:        globalIDTestCollectionID,
		Name:      "Collection 1",
		Slug:      "collection-1",
		CreatedAt: now,
		UpdatedAt: now,
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
		ID:         globalIDTestNamespaceID,
		Identifier: "namespace-1",
		Tier:       datastore.NamespaceTierUser,
		CreatedAt:  now,
		CreatedBy:  "tester",
		UpdatedAt:  now,
		UpdatedBy:  "tester",
	}))
}
