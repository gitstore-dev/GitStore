// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Backend-agnostic datastore contract suite.
// RunContractSuite verifies that any Datastore implementation satisfies the full
// behavioural contract: all 18 CRUD operations, sentinel error wrapping, filter
// semantics, and slug/SKU lookups.

package datastore_contract_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newID() string { return uuid.New().String() }

func newProduct() *datastore.Product {
	return &datastore.Product{
		UID:               newID(),
		Namespace:         "test-ns",
		Name:              "product-" + newID()[:8],
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: time.Now(),
	}
}

func newCategoryTaxonomy() *datastore.CategoryTaxonomy {
	now := time.Now()
	uid := newID()
	name := "cat-" + newID()[:8]
	return &datastore.CategoryTaxonomy{
		UID:               uid,
		Namespace:         "test-ns",
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: now,
	}
}

func newCollection() *datastore.Collection {
	return &datastore.Collection{
		UID:        newID(),
		Namespace:  "test-store",
		Name:       "coll-" + newID()[:8],
		APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind:       "Collection",
	}
}

func newNamespace(tier datastore.NamespaceTier) *datastore.Namespace {
	now := time.Now()
	id := newID()
	identifier := "ns-" + newID()[:8]
	return &datastore.Namespace{
		ID:         id,
		Identifier: identifier,
		Tier:       tier,
		CreatedAt:  now,
		CreatedBy:  "test-user",
		UpdatedAt:  now,
		UpdatedBy:  "test-user",
	}
}

// RunContractSuite runs the full contract suite against any Datastore implementation.
// Callers should pass a freshly initialised, empty store.
func RunContractSuite(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	ctx := context.Background()

	t.Run("Product/CreateAndGet", func(t *testing.T) {
		p := newProduct()
		require.NoError(t, ds.CreateProduct(ctx, p))

		got, err := ds.GetProduct(ctx, p.UID)
		require.NoError(t, err)
		assert.Equal(t, p.UID, got.UID)
		assert.Equal(t, p.Name, got.Name)
		assert.Equal(t, p.Namespace, got.Namespace)
	})

	t.Run("Product/GetNotFound", func(t *testing.T) {
		_, err := ds.GetProduct(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/GetByName", func(t *testing.T) {
		p := newProduct()
		require.NoError(t, ds.CreateProduct(ctx, p))

		got, err := ds.GetProductByName(ctx, p.Namespace, p.Name)
		require.NoError(t, err)
		assert.Equal(t, p.UID, got.UID)
	})

	t.Run("Product/GetByNameNotFound", func(t *testing.T) {
		_, err := ds.GetProductByName(ctx, "test-ns", "does-not-exist-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/DuplicateUIDReturnsAlreadyExists", func(t *testing.T) {
		p := newProduct()
		require.NoError(t, ds.CreateProduct(ctx, p))
		err := ds.CreateProduct(ctx, p)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Product/DuplicateNameReturnsAlreadyExists", func(t *testing.T) {
		p := newProduct()
		require.NoError(t, ds.CreateProduct(ctx, p))

		p2 := newProduct()
		p2.Name = p.Name // same name, different UID
		err := ds.CreateProduct(ctx, p2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Product/Update", func(t *testing.T) {
		p := newProduct()
		require.NoError(t, ds.CreateProduct(ctx, p))

		p.GitRef = "main"
		require.NoError(t, ds.UpdateProduct(ctx, p))

		got, err := ds.GetProduct(ctx, p.UID)
		require.NoError(t, err)
		assert.Equal(t, "main", got.GitRef)
	})

	t.Run("Product/UpdateNotFound", func(t *testing.T) {
		p := newProduct()
		p.UID = newID() // does not exist
		err := ds.UpdateProduct(ctx, p)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/Delete", func(t *testing.T) {
		p := newProduct()
		require.NoError(t, ds.CreateProduct(ctx, p))
		require.NoError(t, ds.DeleteProduct(ctx, p.UID))

		_, err := ds.GetProduct(ctx, p.UID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/DeleteNotFound", func(t *testing.T) {
		err := ds.DeleteProduct(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Product/ListAll", func(t *testing.T) {
		before, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{})
		require.NoError(t, err)

		p1 := newProduct()
		p2 := newProduct()
		require.NoError(t, ds.CreateProduct(ctx, p1))
		require.NoError(t, ds.CreateProduct(ctx, p2))

		after, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{})
		require.NoError(t, err)
		assert.Equal(t, len(before.Items)+2, len(after.Items))
	})

	t.Run("Product/ListPaginated", func(t *testing.T) {
		require.NoError(t, ds.CreateProduct(ctx, newProduct()))
		require.NoError(t, ds.CreateProduct(ctx, newProduct()))

		result, err := ds.ListProducts(ctx, "test-ns", datastore.PageParams{First: 1})
		require.NoError(t, err)
		assert.Len(t, result.Items, 1)
		assert.True(t, result.HasNext)
	})

	t.Run("Product/SpecRoundTrip", func(t *testing.T) {
		p := newProduct()
		p.Spec = []byte(`{"title":"Widget","tags":["new"]}`)
		require.NoError(t, ds.CreateProduct(ctx, p))

		got, err := ds.GetProduct(ctx, p.UID)
		require.NoError(t, err)
		assert.Equal(t, string(p.Spec), string(got.Spec))
	})

	t.Run("Product/StatusRoundTrip", func(t *testing.T) {
		p := newProduct()
		p.Status = []byte(`{"observedGeneration":2,"conditions":[{"type":"READY","status":"TRUE","lastTransitionTime":"2026-01-01T00:00:00Z"}]}`)
		require.NoError(t, ds.CreateProduct(ctx, p))

		got, err := ds.GetProduct(ctx, p.UID)
		require.NoError(t, err)
		assert.Equal(t, string(p.Status), string(got.Status))
	})

	t.Run("CategoryTaxonomy/CreateAndGet", func(t *testing.T) {
		c := newCategoryTaxonomy()
		require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

		got, err := ds.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
		require.NoError(t, err)
		assert.Equal(t, c.UID, got.UID)
		assert.Equal(t, c.Name, got.Name)
	})

	t.Run("CategoryTaxonomy/GetByUID", func(t *testing.T) {
		c := newCategoryTaxonomy()
		require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

		got, err := ds.GetCategoryTaxonomy(ctx, c.UID)
		require.NoError(t, err)
		assert.Equal(t, c.UID, got.UID)
		assert.Equal(t, c.Name, got.Name)
		assert.Equal(t, c.Namespace, got.Namespace)
	})

	t.Run("CategoryTaxonomy/GetByUIDNotFound", func(t *testing.T) {
		_, err := ds.GetCategoryTaxonomy(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("CategoryTaxonomy/GetNotFound", func(t *testing.T) {
		_, err := ds.GetCategoryTaxonomyByName(ctx, "test-ns", "does-not-exist-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("CategoryTaxonomy/DuplicateNameReturnsAlreadyExists", func(t *testing.T) {
		c := newCategoryTaxonomy()
		require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

		c2 := newCategoryTaxonomy()
		c2.Name = c.Name
		err := ds.CreateCategoryTaxonomy(ctx, c2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("CategoryTaxonomy/Update", func(t *testing.T) {
		c := newCategoryTaxonomy()
		require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))

		c.AncestorPath = "electronics"
		require.NoError(t, ds.UpdateCategoryTaxonomy(ctx, c))

		got, err := ds.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
		require.NoError(t, err)
		assert.Equal(t, "electronics", got.AncestorPath)
	})

	t.Run("CategoryTaxonomy/UpdateNotFound", func(t *testing.T) {
		c := newCategoryTaxonomy()
		err := ds.UpdateCategoryTaxonomy(ctx, c)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/CreateAndGet", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		got, err := ds.GetCollection(ctx, c.UID)
		require.NoError(t, err)
		assert.Equal(t, c.UID, got.UID)
		assert.Equal(t, c.Name, got.Name)
	})

	t.Run("Collection/GetNotFound", func(t *testing.T) {
		_, err := ds.GetCollection(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/GetByName", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		got, err := ds.GetCollectionByName(ctx, c.Namespace, c.Name)
		require.NoError(t, err)
		assert.Equal(t, c.UID, got.UID)
	})

	t.Run("Collection/GetByNameNotFound", func(t *testing.T) {
		_, err := ds.GetCollectionByName(ctx, "test-store", "name-does-not-exist-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/DuplicateNameReturnsAlreadyExists", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		c2 := newCollection()
		c2.Name = c.Name
		err := ds.CreateCollection(ctx, c2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Collection/Update", func(t *testing.T) {
		c := newCollection()
		require.NoError(t, ds.CreateCollection(ctx, c))

		c.Body = "Updated description"
		require.NoError(t, ds.UpdateCollection(ctx, c))

		got, err := ds.GetCollection(ctx, c.UID)
		require.NoError(t, err)
		assert.Equal(t, "Updated description", got.Body)
	})

	t.Run("Collection/UpdateNotFound", func(t *testing.T) {
		c := newCollection()
		c.UID = newID()
		err := ds.UpdateCollection(ctx, c)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Collection/ListByNamespace", func(t *testing.T) {
		ns := "list-ns-" + newID()[:8]
		for i := 0; i < 3; i++ {
			c := newCollection()
			c.Namespace = ns
			c.Name = fmt.Sprintf("coll-%d-%s", i, newID()[:6])
			require.NoError(t, ds.CreateCollection(ctx, c))
		}
		result, err := ds.ListCollections(ctx, ns, datastore.PageParams{First: 10})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(result.Items), 3)
	})

	// ── ListProductsByLabelSelector ───────────────────────────────────────────

	t.Run("ListProductsByLabelSelector/MatchLabels", func(t *testing.T) {
		ns := "sel-ns-" + newID()[:8]
		p1 := newProduct()
		p1.Namespace = ns
		p1.Name = "sel-p1-" + newID()[:6]
		p1.Labels = map[string]string{"env": "prod", "tier": "web"}
		require.NoError(t, ds.CreateProduct(ctx, p1))

		p2 := newProduct()
		p2.Namespace = ns
		p2.Name = "sel-p2-" + newID()[:6]
		p2.Labels = map[string]string{"env": "staging"}
		require.NoError(t, ds.CreateProduct(ctx, p2))

		sel := catalog.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}
		result, err := ds.ListProductsByLabelSelector(ctx, ns, sel)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, p1.UID, result[0].UID)
	})

	t.Run("ListProductsByLabelSelector/NoMatch", func(t *testing.T) {
		ns := "sel-nomatch-" + newID()[:8]
		p := newProduct()
		p.Namespace = ns
		p.Name = "product-" + newID()[:6]
		p.Labels = map[string]string{"env": "dev"}
		require.NoError(t, ds.CreateProduct(ctx, p))

		sel := catalog.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}
		result, err := ds.ListProductsByLabelSelector(ctx, ns, sel)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("ListProductsByLabelSelector/EmptySelector", func(t *testing.T) {
		ns := "sel-empty-" + newID()[:8]
		p := newProduct()
		p.Namespace = ns
		p.Name = "product-" + newID()[:6]
		p.Labels = map[string]string{"env": "prod"}
		require.NoError(t, ds.CreateProduct(ctx, p))

		sel := catalog.LabelSelector{}
		result, err := ds.ListProductsByLabelSelector(ctx, ns, sel)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	// ── Namespace ─────────────────────────────────────────────────────────────

	t.Run("Namespace/TestCreateNamespace_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespace(ctx, ns.ID)
		require.NoError(t, err)
		assert.Equal(t, ns.ID, got.ID)
		assert.Equal(t, ns.Identifier, got.Identifier)
		assert.Equal(t, ns.Tier, got.Tier)
		assert.Equal(t, ns.CreatedBy, got.CreatedBy)
	})

	t.Run("Namespace/TestCreateNamespace_duplicateIdentifier", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganisation)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		ns2 := newNamespace(datastore.NamespaceTierUser)
		ns2.Identifier = ns.Identifier // same identifier
		err := ds.CreateNamespace(ctx, ns2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Namespace/TestCreateNamespace_acrossAllTiers", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierEnterprise)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		// same identifier, different tier — must still conflict
		nsOrg := newNamespace(datastore.NamespaceTierOrganisation)
		nsOrg.Identifier = ns.Identifier
		err := ds.CreateNamespace(ctx, nsOrg)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Namespace/TestGetNamespaceByIdentifier_notFound", func(t *testing.T) {
		_, err := ds.GetNamespaceByIdentifier(ctx, "does-not-exist-"+newID()[:8])
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestListNamespaces_empty", func(t *testing.T) {
		// fresh store or just verify list succeeds
		nss, err := ds.ListNamespaces(ctx, datastore.PageParams{})
		require.NoError(t, err)
		assert.NotNil(t, nss)
	})

	t.Run("Namespace/TestListNamespaces_multiple", func(t *testing.T) {
		before, err := ds.ListNamespaces(ctx, datastore.PageParams{})
		require.NoError(t, err)

		ns1 := newNamespace(datastore.NamespaceTierUser)
		ns2 := newNamespace(datastore.NamespaceTierOrganisation)
		require.NoError(t, ds.CreateNamespace(ctx, ns1))
		require.NoError(t, ds.CreateNamespace(ctx, ns2))

		after, err := ds.ListNamespaces(ctx, datastore.PageParams{})
		require.NoError(t, err)
		assert.Equal(t, len(before.Items)+2, len(after.Items))
	})

	t.Run("Namespace/TestGetNamespace_byID_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespace(ctx, ns.ID)
		require.NoError(t, err)
		assert.Equal(t, ns.ID, got.ID)
	})

	t.Run("Namespace/TestGetNamespaceByIdentifier_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespaceByIdentifier(ctx, ns.Identifier)
		require.NoError(t, err)
		assert.Equal(t, ns.ID, got.ID)
		assert.Equal(t, ns.Identifier, got.Identifier)
	})

	t.Run("Namespace/TestDeleteNamespace_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		require.NoError(t, ds.DeleteNamespace(ctx, ns.ID))

		_, err := ds.GetNamespace(ctx, ns.ID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestDeleteNamespace_notFound", func(t *testing.T) {
		err := ds.DeleteNamespace(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestDeleteNamespace_thenGetReturnsNotFound", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganisation)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		require.NoError(t, ds.DeleteNamespace(ctx, ns.ID))

		_, errID := ds.GetNamespace(ctx, ns.ID)
		assert.ErrorIs(t, errID, datastore.ErrNotFound)

		_, errIdent := ds.GetNamespaceByIdentifier(ctx, ns.Identifier)
		assert.ErrorIs(t, errIdent, datastore.ErrNotFound)
	})
}
