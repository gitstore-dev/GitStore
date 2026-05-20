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
	"github.com/gitstore-dev/gitstore/api/internal/graph/scalar"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	globalIDTestCategoryID   = "00000000-0000-0000-0000-000000000001"
	globalIDTestCollectionID = "00000000-0000-0000-0000-000000000002"
	globalIDTestProductID    = "00000000-0000-0000-0000-000000000003"
	globalIDTestNamespaceID  = "ns-1"
)

func TestQueryNodeResolvesByGlobalID(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	node, err := query.Node(ctx, mustEncodeNodeID(nodeKindProduct, globalIDTestProductID))
	require.NoError(t, err)
	product, ok := node.(*model.Product)
	require.True(t, ok)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductID), product.ID)
	assert.Equal(t, "SKU-1", product.Sku)
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
		mustEncodeNodeID(nodeKindProduct, globalIDTestProductID),
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

	productID := mustEncodeNodeID(nodeKindProduct, globalIDTestProductID)
	product, err := query.Product(ctx, model.ProductBy{ID: &productID})
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductID), product.ID)

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

func TestProductFilterIDsAreDecoded(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}
	categoryID := mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryID)
	collectionID := mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID)

	conn, err := query.Products(ctx, nil, nil, nil, nil, &model.ProductFilter{
		CategoryID:   &categoryID,
		CollectionID: &collectionID,
	})
	require.NoError(t, err)
	require.Len(t, conn.Edges, 1)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductID), conn.Edges[0].Node.ID)
}

func TestCreateProductDecodesNodeReferenceInputs(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	mutation := &mutationResolver{Resolver: resolver}

	payload, err := mutation.CreateProduct(ctx, model.CreateProductInput{
		Sku:           "SKU-2",
		Title:         "Second product",
		Price:         scalar.Decimal{Decimal: decimal.NewFromFloat(2.50)},
		CategoryID:    mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryID),
		CollectionIds: []string{mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID)},
	})
	require.NoError(t, err)
	require.NotNil(t, payload.Product)

	_, rawProductID, err := DecodeNodeID(payload.Product.ID)
	require.NoError(t, err)
	stored, err := store.GetProduct(ctx, rawProductID)
	require.NoError(t, err)
	assert.Equal(t, globalIDTestCategoryID, stored.CategoryID)
	assert.Equal(t, []string{globalIDTestCollectionID}, stored.CollectionIDs)
}

func TestMalformedGlobalIDReturnsError(t *testing.T) {
	_, resolver := newGlobalIDTestResolver(t)
	query := &queryResolver{Resolver: resolver}

	id := "not-base64!"
	product, err := query.Product(context.Background(), model.ProductBy{ID: &id})
	assert.Nil(t, product)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid global ID")
}

func TestBusinessIdentifiersAreNotDecoded(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	sku := "SKU-1"
	product, err := query.Product(ctx, model.ProductBy{Sku: &sku})
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductID), product.ID)

	categorySlug := "category-1"
	category, err := query.Category(ctx, model.CategoryBy{Slug: &categorySlug})
	require.NoError(t, err)
	require.NotNil(t, category)
	assert.Equal(t, mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryID), category.ID)

	collectionSlug := "collection-1"
	collection, err := query.Collection(ctx, model.CollectionBy{Slug: &collectionSlug})
	require.NoError(t, err)
	require.NotNil(t, collection)
	assert.Equal(t, mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID), collection.ID)

	namespaceIdentifier := "namespace-1"
	namespace, err := query.Namespace(ctx, model.NamespaceBy{Identifier: &namespaceIdentifier})
	require.NoError(t, err)
	require.NotNil(t, namespace)
	assert.Equal(t, mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID), namespace.ID)
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
		ID:            globalIDTestProductID,
		SKU:           "SKU-1",
		Title:         "Product 1",
		Price:         1.25,
		Currency:      "USD",
		CategoryID:    globalIDTestCategoryID,
		CollectionIDs: []string{globalIDTestCollectionID},
		CreatedAt:     now,
		UpdatedAt:     now,
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
