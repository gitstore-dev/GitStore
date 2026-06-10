// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

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

func newCategoryResolverEnv(t *testing.T) (*queryResolver, datastore.Datastore) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	r := NewResolver(store, nil, zap.NewNop())
	return &queryResolver{Resolver: r}, store
}

func seedCategory(t *testing.T, store datastore.Datastore, ns, name string, createdAt time.Time) *datastore.CategoryTaxonomy {
	t.Helper()
	c := &datastore.CategoryTaxonomy{
		UID:               uuid.New().String(),
		Namespace:         ns,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: createdAt,
	}
	require.NoError(t, store.CreateCategoryTaxonomy(context.Background(), c))
	return c
}

// ── Single category lookup ───────────────────────────────────────────────────

func TestCategoryResolver_CategoryByNamespacePath(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	ctx := context.Background()
	c := seedCategory(t, store, "test-ns", "electronics", time.Now().UTC())

	got, err := qr.Category(ctx, model.CategoryBy{
		NamespacePath: &model.CategoryNamespacePath{Namespace: c.Namespace, Name: c.Name},
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, mustEncodeNodeID(nodeKindCategory, c.UID), got.ID)
	assert.Equal(t, c.Name, got.Metadata.Name)
	require.NotNil(t, got.Metadata.Namespace)
	assert.Equal(t, c.Namespace, *got.Metadata.Namespace)
}

func TestCategoryResolver_CategoryByID(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	ctx := context.Background()
	c := seedCategory(t, store, "test-ns", "electronics", time.Now().UTC())
	categoryID := mustEncodeNodeID(nodeKindCategory, c.UID)

	got, err := qr.Category(ctx, model.CategoryBy{ID: &categoryID})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, categoryID, got.ID)
	assert.Equal(t, c.Name, got.Metadata.Name)
}

func TestCategoryResolver_CategoryByNamespacePath_NotFound(t *testing.T) {
	qr, _ := newCategoryResolverEnv(t)

	got, err := qr.Category(context.Background(), model.CategoryBy{
		NamespacePath: &model.CategoryNamespacePath{Namespace: "test-ns", Name: "missing"},
	})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestCategoryResolver_CategoryByID_NotFound(t *testing.T) {
	qr, _ := newCategoryResolverEnv(t)
	categoryID := mustEncodeNodeID(nodeKindCategory, uuid.New().String())

	got, err := qr.Category(context.Background(), model.CategoryBy{ID: &categoryID})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestCategoryResolver_CategoryByID_Malformed(t *testing.T) {
	qr, _ := newCategoryResolverEnv(t)
	badID := "not-base64"

	got, err := qr.Category(context.Background(), model.CategoryBy{ID: &badID})
	assert.Error(t, err)
	assert.Nil(t, got)
}

// ── Categories forward pagination ────────────────────────────────────────────

func TestCategoryResolver_Categories_ForwardPagination(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	base := time.Now().UTC()
	ns := "test-ns"

	// Seed 5 categories with distinct timestamps so ordering is deterministic.
	for i := range 5 {
		seedCategory(t, store, ns, uuid.New().String()[:8], base.Add(time.Duration(i)*time.Second))
	}

	first := int32(2)

	page1, err := qr.Categories(context.Background(), &first, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, page1)
	assert.Len(t, page1.Edges, 2)
	assert.True(t, page1.PageInfo.HasNextPage)
	assert.False(t, page1.PageInfo.HasPreviousPage)
	require.NotNil(t, page1.PageInfo.EndCursor)

	// Page 2 using the end cursor from page 1.
	page2, err := qr.Categories(context.Background(), &first, page1.PageInfo.EndCursor, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, page2)
	assert.Len(t, page2.Edges, 2)
	assert.True(t, page2.PageInfo.HasNextPage)
	assert.True(t, page2.PageInfo.HasPreviousPage)
	require.NotNil(t, page2.PageInfo.EndCursor)

	// Page 3 — last item.
	page3, err := qr.Categories(context.Background(), &first, page2.PageInfo.EndCursor, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, page3)
	assert.Len(t, page3.Edges, 1)
	assert.False(t, page3.PageInfo.HasNextPage)
	assert.True(t, page3.PageInfo.HasPreviousPage)
}

// ── Categories backward pagination ───────────────────────────────────────────

func TestCategoryResolver_Categories_BackwardPagination(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	base := time.Now().UTC()
	ns := "test-ns"

	for i := range 4 {
		seedCategory(t, store, ns, uuid.New().String()[:8], base.Add(time.Duration(i)*time.Second))
	}

	last := int32(2)

	result, err := qr.Categories(context.Background(), nil, nil, &last, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.Edges, 2)
	assert.False(t, result.PageInfo.HasNextPage)
	assert.True(t, result.PageInfo.HasPreviousPage)
}

// ── Categories backward with before cursor ────────────────────────────────────

func TestCategoryResolver_Categories_BackwardWithBefore(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	base := time.Now().UTC()
	ns := "test-ns"

	for i := range 5 {
		seedCategory(t, store, ns, uuid.New().String()[:8], base.Add(time.Duration(i)*time.Second))
	}

	// Get the first 3 items (newest first) to establish a mid-point cursor.
	first := int32(3)
	page1, err := qr.Categories(context.Background(), &first, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, page1.Edges, 3)
	require.NotNil(t, page1.PageInfo.EndCursor)

	// Walk backward from the 3rd item.
	last := int32(2)
	backward, err := qr.Categories(context.Background(), nil, nil, &last, page1.PageInfo.EndCursor)
	require.NoError(t, err)
	require.NotNil(t, backward)
	assert.Len(t, backward.Edges, 2)
	assert.True(t, backward.PageInfo.HasNextPage)
}

// ── Cursor values are stable and non-empty ────────────────────────────────────

func TestCategoryResolver_Categories_CursorFields(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	ns := "test-ns"
	seedCategory(t, store, ns, "alpha", time.Now().UTC())
	seedCategory(t, store, ns, "beta", time.Now().UTC().Add(time.Second))

	first := int32(2)
	result, err := qr.Categories(context.Background(), &first, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, result.Edges, 2)

	for _, edge := range result.Edges {
		assert.NotEmpty(t, edge.Cursor, "every edge must carry a non-empty cursor")
	}
	require.NotNil(t, result.PageInfo.StartCursor)
	require.NotNil(t, result.PageInfo.EndCursor)
	assert.NotEmpty(t, *result.PageInfo.StartCursor)
	assert.NotEmpty(t, *result.PageInfo.EndCursor)
	assert.Equal(t, result.Edges[0].Cursor, *result.PageInfo.StartCursor)
	assert.Equal(t, result.Edges[len(result.Edges)-1].Cursor, *result.PageInfo.EndCursor)
}

// ── Empty namespace returns empty connection ──────────────────────────────────

func TestCategoryResolver_Categories_Empty(t *testing.T) {
	qr, _ := newCategoryResolverEnv(t)

	first := int32(10)
	result, err := qr.Categories(context.Background(), &first, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Edges)
	assert.False(t, result.PageInfo.HasNextPage)
	assert.False(t, result.PageInfo.HasPreviousPage)
}

// ── TotalCount reflects full dataset, not page size ───────────────────────────

func TestCategoryResolver_Categories_TotalCount(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	ns := "test-ns"
	base := time.Now().UTC()

	for i := range 5 {
		seedCategory(t, store, ns, uuid.New().String()[:8], base.Add(time.Duration(i)*time.Second))
	}

	first := int32(2)
	result, err := qr.Categories(context.Background(), &first, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.Edges, 2)
	// memdb returns exact total; assert it reflects the full 5-item set.
	if result.TotalCount >= 0 {
		assert.Equal(t, int32(5), result.TotalCount)
	}
}
