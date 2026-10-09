// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// seedResolvedCategory creates a category and writes its controller-resolved
// path, which is what maintains the ancestor index.
func seedResolvedCategory(t *testing.T, store datastore.Datastore, ns string, path ...string) *datastore.CategoryTaxonomy {
	t.Helper()
	name := path[len(path)-1]
	c := seedCategory(t, store, ns, name, time.Now().UTC())
	if len(path) > 1 {
		c.ParentName = path[len(path)-2]
		require.NoError(t, store.UpdateCategoryTaxonomy(context.Background(), c))
		c, _ = store.GetCategoryTaxonomyByName(context.Background(), ns, name)
	}
	updated, err := store.UpdateCategoryTaxonomyStatus(context.Background(), ns, name, datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion: c.ResourceVersion,
		Resolved:        &catalog.ResolvedCategoryTaxonomy{Path: path, Depth: int8(len(path) - 1)},
	})
	require.NoError(t, err)
	return updated
}

func seedElectronicsTree(t *testing.T, store datastore.Datastore) {
	t.Helper()
	seedResolvedCategory(t, store, "shop", "electronics")
	seedResolvedCategory(t, store, "shop", "electronics", "computers")
	seedResolvedCategory(t, store, "shop", "electronics", "computers-refurb")
	seedResolvedCategory(t, store, "shop", "electronics", "computers", "laptops")
	seedResolvedCategory(t, store, "shop", "electronics", "computers", "desktops")
	seedCategory(t, store, "shop", "unreconciled", time.Now().UTC())
}

func categoryNames(conn *model.CategoryConnection) []string {
	names := make([]string, 0, len(conn.Edges))
	for _, edge := range conn.Edges {
		names = append(names, edge.Node.Metadata.Name)
	}
	return names
}

func int32Ptr(v int32) *int32 { return &v }

func TestCategoriesFilterReturnsSubtreeByDepthThenName(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	seedElectronicsTree(t, store)
	ctx := context.Background()

	conn, err := qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "computers"}, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"desktops", "laptops"}, categoryNames(conn), "segment match only: computers-refurb is a sibling")

	includeSelf := true
	conn, err = qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "computers", IncludeSelf: &includeSelf}, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"computers", "desktops", "laptops"}, categoryNames(conn))

	conn, err = qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "electronics", MaxDepth: int32Ptr(1)}, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"computers", "computers-refurb"}, categoryNames(conn))

	conn, err = qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "electronics"}, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"computers", "computers-refurb", "desktops", "laptops"}, categoryNames(conn), "unreconciled categories are not matched")
}

func TestCategoriesFilterPaginatesWithClosureCursors(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	seedElectronicsTree(t, store)
	ctx := context.Background()
	filter := &model.CategoryFilterInput{DescendantOf: "electronics"}

	page1, err := qr.Categories(ctx, "shop", filter, int32Ptr(3), nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"computers", "computers-refurb", "desktops"}, categoryNames(page1))
	require.True(t, page1.PageInfo.HasNextPage)
	page2, err := qr.Categories(ctx, "shop", filter, int32Ptr(3), page1.PageInfo.EndCursor, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"laptops"}, categoryNames(page2))
	assert.False(t, page2.PageInfo.HasNextPage)
}

func TestCategoriesFilterUnknownAncestorIsEmpty(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	seedElectronicsTree(t, store)
	conn, err := qr.Categories(context.Background(), "shop", &model.CategoryFilterInput{DescendantOf: "missing"}, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, conn.Edges)
	assert.False(t, conn.PageInfo.HasNextPage)
}

func requireBadUserInput(t *testing.T, err error) {
	t.Helper()
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	assert.Equal(t, "BAD_USER_INPUT", gqlErr.Extensions["code"])
	diagnostics, _ := gqlErr.Extensions["diagnostics"].([]map[string]any)
	require.NotEmpty(t, diagnostics)
	assert.Equal(t, "INVALID_ARGUMENT", diagnostics[0]["reason"])
}

func TestCategoriesFilterRejectsBadArguments(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	seedElectronicsTree(t, store)
	ctx := context.Background()
	_, err := qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "electronics", MaxDepth: int32Ptr(128)}, nil, nil, nil, nil)
	require.NoError(t, err, "the documented upper bound is accepted")
	for _, maxDepth := range []int32{0, -1, 129} {
		_, err := qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "electronics", MaxDepth: int32Ptr(maxDepth)}, nil, nil, nil, nil)
		requireBadUserInput(t, err)
	}

	unfiltered, err := qr.Categories(ctx, "shop", nil, int32Ptr(1), nil, nil, nil)
	require.NoError(t, err)
	_, err = qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "electronics"}, int32Ptr(1), unfiltered.PageInfo.EndCursor, nil, nil)
	requireBadUserInput(t, err)

	filtered, err := qr.Categories(ctx, "shop", &model.CategoryFilterInput{DescendantOf: "electronics"}, int32Ptr(1), nil, nil, nil)
	require.NoError(t, err)
	_, err = qr.Categories(ctx, "shop", nil, int32Ptr(1), filtered.PageInfo.EndCursor, nil, nil)
	requireBadUserInput(t, err)
}

// staleIndexStore reports an index row whose record was recreated, as can
// happen before repair after a rolling upgrade.
type staleIndexStore struct {
	datastore.Datastore
}

func (s staleIndexStore) ListCategoryDescendants(ctx context.Context, q datastore.CategoryDescendantQuery) (*datastore.PageResult[datastore.CategoryDescendant], error) {
	page, err := s.Datastore.(datastore.CategoryAncestorIndex).ListCategoryDescendants(ctx, q)
	if err != nil {
		return nil, err
	}
	page.Items = append(page.Items,
		&datastore.CategoryDescendant{Name: "laptops", UID: "00000000-0000-0000-0000-0000000000ff", Depth: 9},
		&datastore.CategoryDescendant{Name: "gone", UID: "00000000-0000-0000-0000-0000000000fe", Depth: 9},
	)
	return page, nil
}

func TestCategoriesFilterDropsStaleIndexRows(t *testing.T) {
	_, base := newCategoryResolverEnv(t)
	seedElectronicsTree(t, base)
	r, err := NewResolver(ResolverDeps{Store: staleIndexStore{Datastore: base}, Logger: zap.NewNop()})
	require.NoError(t, err)
	qr := &queryResolver{Resolver: r}
	conn, err := qr.Categories(context.Background(), "shop", &model.CategoryFilterInput{DescendantOf: "computers"}, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"desktops", "laptops"}, categoryNames(conn))
}

func TestCategoriesWithoutFilterIsUnchanged(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	seedElectronicsTree(t, store)
	conn, err := qr.Categories(context.Background(), "shop", nil, int32Ptr(100), nil, nil, nil)
	require.NoError(t, err)
	assert.Len(t, conn.Edges, 6, "the unfiltered list includes unreconciled categories")
	_, _, err = datastore.DecodeClosureCursor(*conn.PageInfo.EndCursor)
	assert.Error(t, err, "unfiltered lists keep keyset cursors")
}

func TestCategoryParentAndChildren(t *testing.T) {
	qr, store := newCategoryResolverEnv(t)
	seedElectronicsTree(t, store)
	ctx := context.Background()
	cr := &categoryResolver{Resolver: qr.Resolver}

	setParentResolved := func(name string, resolved bool) {
		c, err := store.GetCategoryTaxonomyByName(ctx, "shop", name)
		require.NoError(t, err)
		status := catalog.ConditionFalse
		if resolved {
			status = catalog.ConditionTrue
		}
		_, err = store.UpdateCategoryTaxonomyStatus(ctx, "shop", name, datastore.CategoryTaxonomyStatusPatch{
			ResourceVersion: c.ResourceVersion,
			Conditions:      []catalog.Condition{{Type: catalog.ConditionParentResolved, Status: status, LastTransitionTime: time.Now()}},
		})
		require.NoError(t, err)
	}
	load := func(name string) *model.Category {
		got, err := qr.Category(ctx, model.CategoryBy{NamespacePath: &model.CategoryNamespacePath{Namespace: "shop", Name: name}})
		require.NoError(t, err)
		require.NotNil(t, got)
		return got
	}
	specWithParent := func(name, parent string) {
		c, err := store.GetCategoryTaxonomyByName(ctx, "shop", name)
		require.NoError(t, err)
		c.Spec = []byte(`{"title":"` + name + `","parentRef":{"name":"` + parent + `"}}`)
		require.NoError(t, store.UpdateCategoryTaxonomy(ctx, c))
	}

	specWithParent("computers", "electronics")
	setParentResolved("computers", true)
	parent, err := cr.Parent(ctx, load("computers"))
	require.NoError(t, err)
	require.NotNil(t, parent)
	assert.Equal(t, "electronics", parent.Metadata.Name)

	children, err := cr.Children(ctx, load("computers"))
	require.NoError(t, err)
	assert.Equal(t, []string{"desktops", "laptops"}, func() []string {
		names := []string{}
		for _, c := range children {
			names = append(names, c.Metadata.Name)
		}
		return names
	}(), "direct children only, ordered by name")

	root, err := cr.Parent(ctx, load("electronics"))
	require.NoError(t, err)
	assert.Nil(t, root, "roots have no parent")

	specWithParent("computers-refurb", "electronics")
	setParentResolved("computers-refurb", false)
	unresolved, err := cr.Parent(ctx, load("computers-refurb"))
	require.NoError(t, err)
	assert.Nil(t, unresolved, "an unresolved parent is null")

	specWithParent("unreconciled", "electronics")
	unreconciled, err := cr.Parent(ctx, load("unreconciled"))
	require.NoError(t, err)
	assert.Nil(t, unreconciled, "an unreconciled category has no parent")
	noChildren, err := cr.Children(ctx, load("unreconciled"))
	require.NoError(t, err)
	assert.Empty(t, noChildren)
}
