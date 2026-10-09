// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductLifecycleStoreTerminatesAndCompletesWithExpectedVersion(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	lifecycle, ok := any(store).(datastore.ProductLifecycleStore)
	require.True(t, ok)
	product := &datastore.Product{UID: uuid.NewString(), Namespace: "acme", Name: "widget", ResourceVersion: "7", Generation: 3}
	require.NoError(t, store.CreateProduct(t.Context(), product))

	terminating, err := lifecycle.MarkProductTerminating(t.Context(), product.UID, "7", "catalog.gitstore.dev/product-finalizer", time.Now())
	require.NoError(t, err)
	require.NotNil(t, terminating.DeletionTimestamp)
	assert.Equal(t, int64(3), terminating.Generation)
	assert.Equal(t, "8", terminating.ResourceVersion)
	assert.Equal(t, []string{"catalog.gitstore.dev/product-finalizer"}, terminating.Finalizers)

	_, err = lifecycle.MarkProductTerminating(t.Context(), product.UID, "7", "catalog.gitstore.dev/product-finalizer", time.Now())
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.NoError(t, lifecycle.CompleteProductDeletion(t.Context(), product.UID, "8"))
	_, err = store.GetProduct(context.Background(), product.UID)
	assert.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestProductLifecycleStoreRejectsStaleFinalRemoval(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	lifecycle := any(store).(datastore.ProductLifecycleStore)
	product := &datastore.Product{UID: uuid.NewString(), Namespace: "acme", Name: "gadget", ResourceVersion: "1"}
	require.NoError(t, store.CreateProduct(t.Context(), product))
	_, err = lifecycle.MarkProductTerminating(t.Context(), product.UID, "1", "finalizer", time.Now())
	require.NoError(t, err)
	err = lifecycle.CompleteProductDeletion(t.Context(), product.UID, "1")
	assert.True(t, errors.Is(err, datastore.ErrConflict))
}

func TestProductVariantBlockingOwnerReferenceIsIndexedByProductScope(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	owners, ok := any(store).(datastore.OwnerReferenceStore)
	require.True(t, ok)
	product := &datastore.Product{UID: uuid.NewString(), RepositoryID: uuid.NewString(), Namespace: "acme", Name: "widget", ResourceVersion: "1"}
	require.NoError(t, store.CreateProduct(t.Context(), product))
	references, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: product.Name,
		UID: product.UID, BlockOwnerDeletion: true,
	}})
	require.NoError(t, err)
	variant := &datastore.ProductVariant{UID: uuid.NewString(), RepositoryID: product.RepositoryID, Namespace: product.Namespace, Name: "widget-blue", SKU: "widget-blue", ProductRefName: product.Name, ResourceVersion: "1", OwnerReferences: references}
	require.NoError(t, store.CreateProductVariant(t.Context(), variant))

	blocked, err := owners.HasBlockingOwnerDependents(t.Context(), datastore.OwnerReferenceScope{Namespace: product.Namespace, RepositoryID: product.RepositoryID}, product.UID)
	require.NoError(t, err)
	assert.True(t, blocked)
}
