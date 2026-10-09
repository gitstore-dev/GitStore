// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func newCategoryResolverEnv(t *testing.T) (*queryResolver, datastore.Datastore) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	return &queryResolver{Resolver: r}, store
}

func newCategoryMutationResolverEnv(t *testing.T) (*mutationResolver, datastore.Datastore) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	return &mutationResolver{Resolver: r}, store
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
	assert.Equal(t, c.Namespace, got.Metadata.Namespace)
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

func TestCategoryProductsUsesOnlyItsMaterializedSubtreeMembership(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	ctx := context.Background()
	root := seedCategory(t, store, "shop", "root", time.Now().UTC())
	other := seedCategory(t, store, "shop", "other", time.Now().UTC())
	direct := &datastore.Product{UID: uuid.New().String(), Namespace: "shop", Name: "direct", CreationTimestamp: time.Now().UTC(), ResourceVersion: "1"}
	unrelated := &datastore.Product{UID: uuid.New().String(), Namespace: "shop", Name: "unrelated", CreationTimestamp: time.Now().UTC().Add(-time.Second), ResourceVersion: "1"}
	require.NoError(t, store.CreateProduct(ctx, direct))
	require.NoError(t, store.CreateProduct(ctx, unrelated))
	index := store.(datastore.CategoryProductIndex)
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, direct, []string{root.UID}))
	require.NoError(t, index.ReplaceCategoryProductMembership(ctx, unrelated, []string{other.UID}))
	resolver := &categoryResolver{qr.Resolver}
	connection, err := resolver.Products(ctx, DatastoreCategoryTaxonomyToGraphQL(root), nil, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, connection.Edges, 1)
	assert.Equal(t, "direct", connection.Edges[0].Node.Metadata.Name)
	assert.NotEmpty(t, connection.Edges[0].Cursor)
	_, err = resolver.Products(ctx, DatastoreCategoryTaxonomyToGraphQL(other), nil, &connection.Edges[0].Cursor, nil, nil)
	assert.Error(t, err, "a cursor from another category must not be reusable")
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

	page1, err := qr.Categories(context.Background(), ns, nil, &first, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, page1)
	assert.Len(t, page1.Edges, 2)
	assert.True(t, page1.PageInfo.HasNextPage)
	assert.False(t, page1.PageInfo.HasPreviousPage)
	require.NotNil(t, page1.PageInfo.EndCursor)

	// Page 2 using the end cursor from page 1.
	page2, err := qr.Categories(context.Background(), ns, nil, &first, page1.PageInfo.EndCursor, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, page2)
	assert.Len(t, page2.Edges, 2)
	assert.True(t, page2.PageInfo.HasNextPage)
	assert.True(t, page2.PageInfo.HasPreviousPage)
	require.NotNil(t, page2.PageInfo.EndCursor)

	// Page 3 — last item.
	page3, err := qr.Categories(context.Background(), ns, nil, &first, page2.PageInfo.EndCursor, nil, nil)
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

	result, err := qr.Categories(context.Background(), ns, nil, nil, nil, &last, nil)
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
	page1, err := qr.Categories(context.Background(), ns, nil, &first, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, page1.Edges, 3)
	require.NotNil(t, page1.PageInfo.EndCursor)

	// Walk backward from the 3rd item.
	last := int32(2)
	backward, err := qr.Categories(context.Background(), ns, nil, nil, nil, &last, page1.PageInfo.EndCursor)
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
	result, err := qr.Categories(context.Background(), ns, nil, &first, nil, nil, nil)
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
	result, err := qr.Categories(context.Background(), "test-ns", nil, &first, nil, nil, nil)
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
	result, err := qr.Categories(context.Background(), ns, nil, &first, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.Edges, 2)
}

func TestUpdateCategoryStatusDecouplesNonBlockingProducts(t *testing.T) {
	mutation, store := newCategoryMutationResolverEnv(t)
	ctx := context.Background()
	category := seedCategory(t, store, "test-ns", "parent", time.Now().UTC())
	category.RepositoryID = "repo-1"
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, category))
	lifecycle := store.(datastore.CategoryTaxonomyDeletionStore)
	terminating, err := lifecycle.MarkCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, category.ResourceVersion, time.Now().UTC())
	require.NoError(t, err)

	ownerReferences, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Name: category.Name, UID: category.UID,
	}})
	require.NoError(t, err)
	product := &datastore.Product{
		UID: "00000000-0000-0000-0000-000000000010", Namespace: category.Namespace, RepositoryID: category.RepositoryID, Name: "product",
		ResourceVersion: "1", CreationTimestamp: time.Now(), OwnerReferences: ownerReferences,
		Spec: []byte(`{"categoryRef":{"name":"parent"}}`),
	}
	require.NoError(t, store.CreateProduct(ctx, product))
	decouple := true
	payload, err := mutation.UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Namespace: category.Namespace, Name: category.Name, ResourceVersion: terminating.ResourceVersion, DecoupleProducts: &decouple,
	})
	require.NoError(t, err)
	assert.False(t, payload.HasMoreProductDependents)

	updated, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(updated.OwnerReferences))
	assert.JSONEq(t, `{"categoryRef":{"name":"parent"}}`, string(updated.Spec), "categoryRef remains Git-authored")
	assert.Contains(t, string(updated.Status), `"CategoryDeleted"`)
}

func TestUpdateCategoryStatusDecouplesProductsInBoundedPages(t *testing.T) {
	mutation, store := newCategoryMutationResolverEnv(t)
	ctx := context.Background()
	category := seedCategory(t, store, "test-ns", "parent", time.Now().UTC())
	category.RepositoryID = "repo-1"
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, category))
	lifecycle := store.(datastore.CategoryTaxonomyDeletionStore)
	terminating, err := lifecycle.MarkCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, category.ResourceVersion, time.Now().UTC())
	require.NoError(t, err)

	references, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Name: category.Name, UID: category.UID,
	}})
	require.NoError(t, err)
	for i := 0; i < datastore.MaxOwnerDependentPageSize+1; i++ {
		product := &datastore.Product{
			UID: uuid.New().String(), Namespace: category.Namespace, RepositoryID: category.RepositoryID,
			Name: fmt.Sprintf("product-%03d", i), ResourceVersion: "1", CreationTimestamp: time.Now(),
			OwnerReferences: references, Spec: []byte(`{"categoryRef":{"name":"parent"}}`),
		}
		require.NoError(t, store.CreateProduct(ctx, product))
	}

	decouple := true
	first, err := mutation.UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Namespace: category.Namespace, Name: category.Name, ResourceVersion: terminating.ResourceVersion, DecoupleProducts: &decouple,
	})
	require.NoError(t, err)
	assert.True(t, first.HasMoreProductDependents, "the first bounded page must request a continuation")

	second, err := mutation.UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Namespace: category.Namespace, Name: category.Name, ResourceVersion: terminating.ResourceVersion, DecoupleProducts: &decouple,
	})
	require.NoError(t, err)
	assert.False(t, second.HasMoreProductDependents)

	owners := store.(datastore.OwnerReferenceStore)
	page, err := owners.ListNonBlockingProductOwnerDependents(ctx, datastore.OwnerReferenceScope{
		Namespace: category.Namespace, RepositoryID: category.RepositoryID,
	}, category.UID, "", 1)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestUpdateCategoryStatusDecoupleRejectsStaleCategoryVersion(t *testing.T) {
	mutation, store := newCategoryMutationResolverEnv(t)
	ctx := context.Background()
	category := seedCategory(t, store, "test-ns", "parent", time.Now().UTC())
	lifecycle := store.(datastore.CategoryTaxonomyDeletionStore)
	terminating, err := lifecycle.MarkCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, category.ResourceVersion, time.Now().UTC())
	require.NoError(t, err)

	decouple := true
	_, err = mutation.UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Namespace: category.Namespace, Name: category.Name, ResourceVersion: "stale", DecoupleProducts: &decouple,
	})
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, "CONFLICT", graphErr.Extensions["code"])
	assert.Contains(t, fmt.Sprint(graphErr.Extensions["diagnostics"]), "current resourceVersion is "+terminating.ResourceVersion)
}

func TestCategoryResolver_HierarchyOnlyFromResolvedStatus(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	ctx := context.Background()
	c := &datastore.CategoryTaxonomy{
		UID: uuid.New().String(), Namespace: "test-ns", Name: "laptops",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		ParentName: "computers", AncestorPath: "electronics/computers/laptops",
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))
	by := model.CategoryBy{NamespacePath: &model.CategoryNamespacePath{Namespace: "test-ns", Name: "laptops"}}

	got, err := qr.Category(ctx, by)
	require.NoError(t, err)
	require.NotNil(t, got)
	if got.Status != nil {
		assert.Nil(t, got.Status.Resolved, "an unreconciled category has no resolved hierarchy, even with an admission-time ancestor path")
	}

	_, err = store.UpdateCategoryTaxonomyStatus(ctx, "test-ns", "laptops", datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion: "1",
		Resolved:        &catalog.ResolvedCategoryTaxonomy{Path: []string{"home", "laptops"}, Depth: 1},
	})
	require.NoError(t, err)
	got, err = qr.Category(ctx, by)
	require.NoError(t, err)
	require.NotNil(t, got.Status)
	require.NotNil(t, got.Status.Resolved)
	assert.Equal(t, []string{"home", "laptops"}, got.Status.Resolved.Path)
	assert.EqualValues(t, 1, got.Status.Resolved.Depth)
}

func TestCategoryResolversNeverReadAncestorPath(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		assert.NotContainsf(t, string(raw), "AncestorPath", "%s must not expose the admission-internal ancestor path", name)
	}
}
