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
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newProductResolverEnv(t *testing.T) (*queryResolver, datastore.Datastore) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	r := NewResolver(store, nil, zap.NewNop())
	return &queryResolver{Resolver: r}, store
}

func seedProduct(t *testing.T, store datastore.Datastore, ns, name string) *datastore.Product {
	t.Helper()
	p := &datastore.Product{
		UID:               uuid.New().String(),
		Namespace:         ns,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, store.CreateProduct(context.Background(), p))
	return p
}

// ── ProductBy.namespacePath arm ───────────────────────────────────────────────

func TestProductResolver_NamespacePath_ReturnsProduct(t *testing.T) {
	qr, store := newProductResolverEnv(t)
	p := seedProduct(t, store, "my-store", "widget")

	by := model.ProductBy{NamespacePath: &model.ProductNamespacePath{
		Namespace: "my-store",
		Name:      "widget",
	}}
	got, err := qr.Product(context.Background(), by)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, p.UID), got.ID)
}

func TestProductResolver_NamespacePath_NotFound_ReturnsNil(t *testing.T) {
	qr, _ := newProductResolverEnv(t)

	by := model.ProductBy{NamespacePath: &model.ProductNamespacePath{
		Namespace: "my-store",
		Name:      "no-such-product",
	}}
	got, err := qr.Product(context.Background(), by)
	assert.Nil(t, got)
	assert.NoError(t, err)
}

// ── ProductBy.id arm ─────────────────────────────────────────────────────────

func TestProductResolver_ID_ReturnsProduct(t *testing.T) {
	qr, store := newProductResolverEnv(t)
	p := seedProduct(t, store, "my-store", "gadget")

	encodedID := mustEncodeNodeID(nodeKindProduct, p.UID)
	by := model.ProductBy{ID: &encodedID}

	got, err := qr.Product(context.Background(), by)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, encodedID, got.ID)
}

func TestProductResolver_ID_NotFound_ReturnsNil(t *testing.T) {
	qr, _ := newProductResolverEnv(t)

	nonExistent := mustEncodeNodeID(nodeKindProduct, uuid.New().String())
	by := model.ProductBy{ID: &nonExistent}

	got, err := qr.Product(context.Background(), by)
	assert.Nil(t, got)
	assert.NoError(t, err)
}

func TestProductResolver_ID_InvalidEncoding_ReturnsNil(t *testing.T) {
	qr, _ := newProductResolverEnv(t)

	invalid := "not-a-valid-global-id"
	by := model.ProductBy{ID: &invalid}

	got, err := qr.Product(context.Background(), by)
	assert.Nil(t, got)
	assert.NoError(t, err)
}

// ── Default arm (no selector) ────────────────────────────────────────────────

func TestProductResolver_NoSelector_ReturnsError(t *testing.T) {
	qr, _ := newProductResolverEnv(t)

	got, err := qr.Product(context.Background(), model.ProductBy{})
	assert.Nil(t, got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "selector")
}

// ── Converter integration via resolver ───────────────────────────────────────

func TestProductResolver_SpecHydratedViaResolver(t *testing.T) {
	qr, store := newProductResolverEnv(t)
	p := seedProduct(t, store, "my-store", "spec-product")
	p.Spec = []byte(`{"title":"My Widget","tags":["featured"]}`)
	require.NoError(t, store.UpdateProduct(context.Background(), p))

	encodedID := mustEncodeNodeID(nodeKindProduct, p.UID)
	by := model.ProductBy{ID: &encodedID}

	got, err := qr.Product(context.Background(), by)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec)
	require.NotNil(t, got.Spec.Title)
	assert.Equal(t, "My Widget", *got.Spec.Title)
	assert.Equal(t, []string{"featured"}, got.Spec.Tags)
}
