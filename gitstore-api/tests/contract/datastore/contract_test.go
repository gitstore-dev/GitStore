// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Backend-agnostic datastore contract suite.
// RunContractSuite verifies that any Datastore implementation satisfies the full
// behavioural contract: all 18 CRUD operations, sentinel error wrapping, filter
// semantics, and slug/SKU lookups.

package datastore_contract_test

import (
	"context"
	"encoding/json"
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
	name := "ns-" + newID()[:8]
	return &datastore.Namespace{
		UID:               id,
		Name:              name,
		Tier:              tier,
		CreationTimestamp: now,
		CreationActor:     "test-user",
		UpdateTimestamp:   now,
		UpdateActor:       "test-user",
	}
}

// RunContractSuite runs the full contract suite against any Datastore implementation.
// Callers should pass a freshly initialised, empty store.
func RunContractSuite(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	ctx := context.Background()

	t.Run("CanonicalEnvelope/AllResourcesRoundTrip", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		deletedAt := now.Add(time.Hour)
		ownerReferences := json.RawMessage(`[{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Collection","name":"owner","uid":"00000000-0000-0000-0000-000000000001"}]`)
		spec := json.RawMessage(`{"displayName":"canonical"}`)
		status := json.RawMessage(`{"observedGeneration":7,"conditions":[]}`)
		labels := map[string]string{"environment": "test"}
		annotations := map[string]string{"gitstore.dev/note": "round-trip"}
		finalizers := []string{"gitstore.dev/test"}

		namespace := &datastore.Namespace{
			APIVersion:        "catalog.gitstore.dev/v1beta1",
			Kind:              "Namespace",
			UID:               newID(),
			Name:              "envelope-ns-" + newID()[:8],
			Title:             "Envelope Namespace",
			Tier:              datastore.NamespaceTierOrganization,
			Generation:        7,
			ResourceVersion:   "9",
			Revision:          "main@sha1:namespace",
			CreationTimestamp: now,
			CreationActor:     "creator",
			UpdateTimestamp:   now.Add(time.Minute),
			UpdateActor:       "updater",
			Labels:            labels,
			Annotations:       annotations,
			OwnerReferences:   ownerReferences,
			Finalizers:        finalizers,
			DeletionTimestamp: &deletedAt,
			SourcePath:        "namespaces/envelope.md",
			GitCommitSHA:      "namespace-sha",
			GitRef:            "refs/heads/main",
			Spec:              spec,
			Body:              "# Namespace body\n",
			Status:            status,
		}
		require.NoError(t, ds.CreateNamespace(ctx, namespace))
		gotNamespace, err := ds.GetNamespace(ctx, namespace.UID)
		require.NoError(t, err)
		assert.Equal(t, namespace, gotNamespace)

		repository := &datastore.Repository{
			APIVersion:        "catalog.gitstore.dev/v1beta1",
			Kind:              "Repository",
			UID:               newID(),
			Namespace:         namespace.Name,
			Name:              "envelope-repo-" + newID()[:8],
			Generation:        7,
			ResourceVersion:   "9",
			Revision:          "main@sha1:repository",
			CreationTimestamp: now,
			CreationActor:     "creator",
			UpdateTimestamp:   now.Add(time.Minute),
			UpdateActor:       "updater",
			Labels:            labels,
			Annotations:       annotations,
			OwnerReferences:   ownerReferences,
			Finalizers:        finalizers,
			DeletionTimestamp: &deletedAt,
			RepositoryID:      newID(),
			SourcePath:        "repositories/envelope.md",
			GitCommitSHA:      "repository-sha",
			GitRef:            "refs/heads/main",
			Spec:              spec,
			Body:              "# Repository body\n",
			Status:            status,
			DefaultBranch:     "main",
			StorageClass:      "default",
		}
		require.NoError(t, ds.CreateRepository(ctx, repository))
		gotRepository, err := ds.GetRepository(ctx, repository.UID)
		require.NoError(t, err)
		assert.Equal(t, repository, gotRepository)

		product := newProduct()
		product.Generation, product.ResourceVersion, product.Revision = 7, "9", "main@sha1:product"
		product.CreationActor, product.UpdateTimestamp, product.UpdateActor = "creator", now.Add(time.Minute), "updater"
		product.CreationTimestamp = now
		product.Labels, product.Annotations = labels, annotations
		product.OwnerReferences, product.Finalizers, product.DeletionTimestamp = ownerReferences, finalizers, &deletedAt
		product.RepositoryID, product.SourcePath, product.GitCommitSHA, product.GitRef = repository.UID, "products/item.md", "product-sha", "refs/heads/main"
		product.Spec, product.Body, product.Status = spec, "# Product body\n", status
		require.NoError(t, ds.CreateProduct(ctx, product))
		gotProduct, err := ds.GetProduct(ctx, product.UID)
		require.NoError(t, err)
		assert.Equal(t, product, gotProduct)

		variant := &datastore.ProductVariant{
			UID: newID(), Namespace: namespace.Name, Name: "variant-" + newID()[:8],
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "ProductVariant",
			Generation: 7, ResourceVersion: "9", Revision: "main@sha1:variant",
			CreationTimestamp: now, CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
			Labels: labels, Annotations: annotations, OwnerReferences: ownerReferences,
			Finalizers: finalizers, DeletionTimestamp: &deletedAt,
			SKU: "sku-" + newID()[:8], ProductRefName: product.Name,
			RepositoryID: repository.UID, SourcePath: "variants/item.md", GitCommitSHA: "variant-sha", GitRef: "refs/heads/main",
			Spec: spec, Body: "# Variant body\n", Status: status,
		}
		require.NoError(t, ds.CreateProductVariant(ctx, variant))
		gotVariant, err := ds.GetProductVariant(ctx, variant.UID)
		require.NoError(t, err)
		assert.Equal(t, variant, gotVariant)

		collection := newCollection()
		collection.Namespace, collection.CreationTimestamp = namespace.Name, now
		collection.Generation, collection.ResourceVersion, collection.Revision = 7, "9", "main@sha1:collection"
		collection.CreationActor, collection.UpdateTimestamp, collection.UpdateActor = "creator", now.Add(time.Minute), "updater"
		collection.Labels, collection.Annotations, collection.OwnerReferences = labels, annotations, ownerReferences
		collection.Finalizers, collection.DeletionTimestamp = finalizers, &deletedAt
		collection.RepositoryID, collection.SourcePath, collection.GitCommitSHA, collection.GitRef = repository.UID, "collections/item.md", "collection-sha", "refs/heads/main"
		collection.Spec, collection.Body, collection.Status = spec, "# Collection body\n", status
		require.NoError(t, ds.CreateCollection(ctx, collection))
		gotCollection, err := ds.GetCollection(ctx, collection.UID)
		require.NoError(t, err)
		assert.Equal(t, collection, gotCollection)

		category := newCategoryTaxonomy()
		category.Namespace, category.CreationTimestamp = namespace.Name, now
		category.Generation, category.ResourceVersion, category.Revision = 7, "9", "main@sha1:category"
		category.CreationActor, category.UpdateTimestamp, category.UpdateActor = "creator", now.Add(time.Minute), "updater"
		category.Labels, category.Annotations, category.OwnerReferences = labels, annotations, ownerReferences
		category.Finalizers, category.DeletionTimestamp = finalizers, &deletedAt
		category.RepositoryID, category.SourcePath, category.GitCommitSHA, category.GitRef = repository.UID, "categories/item.md", "category-sha", "refs/heads/main"
		category.Spec, category.Body, category.Status = spec, "# Category body\n", status
		require.NoError(t, ds.CreateCategoryTaxonomy(ctx, category))
		gotCategory, err := ds.GetCategoryTaxonomy(ctx, category.UID)
		require.NoError(t, err)
		assert.Equal(t, category, gotCategory)
	})

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

	t.Run("ConditionalDelete/RejectsStaleResourceVersion", func(t *testing.T) {
		product := newProduct()
		product.ResourceVersion = "2"
		require.NoError(t, ds.CreateProduct(ctx, product))
		require.ErrorIs(t, ds.DeleteProductWithResourceVersion(ctx, product.UID, "1"), datastore.ErrConflict)
		_, err := ds.GetProduct(ctx, product.UID)
		require.NoError(t, err)
		require.NoError(t, ds.DeleteProductWithResourceVersion(ctx, product.UID, "2"))

		collection := newCollection()
		collection.ResourceVersion = "2"
		require.NoError(t, ds.CreateCollection(ctx, collection))
		require.ErrorIs(t, ds.DeleteCollectionWithResourceVersion(ctx, collection.UID, "1"), datastore.ErrConflict)
		_, err = ds.GetCollection(ctx, collection.UID)
		require.NoError(t, err)
		require.NoError(t, ds.DeleteCollectionWithResourceVersion(ctx, collection.UID, "2"))

		variant := &datastore.ProductVariant{
			UID: newID(), Namespace: "test-ns", Name: "variant-" + newID()[:8],
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "ProductVariant", ResourceVersion: "2",
			SKU: "sku-" + newID()[:8], ProductRefName: "product-" + newID()[:8],
		}
		require.NoError(t, ds.CreateProductVariant(ctx, variant))
		require.ErrorIs(t, ds.DeleteProductVariantWithResourceVersion(ctx, variant.UID, "1"), datastore.ErrConflict)
		_, err = ds.GetProductVariant(ctx, variant.UID)
		require.NoError(t, err)
		require.NoError(t, ds.DeleteProductVariantWithResourceVersion(ctx, variant.UID, "2"))

		file := &datastore.File{
			UID: newID(), Namespace: "test-ns", Name: "file-" + newID()[:8],
			APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", ResourceVersion: "2",
		}
		require.NoError(t, ds.CreateFile(ctx, file))
		require.ErrorIs(t, ds.DeleteFileWithResourceVersion(ctx, file.UID, "1"), datastore.ErrConflict)
		_, err = ds.GetFile(ctx, file.UID)
		require.NoError(t, err)
		require.NoError(t, ds.DeleteFileWithResourceVersion(ctx, file.UID, "2"))
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

	t.Run("ListProductsByLabelSelector/MatchExpressions_NotIn", func(t *testing.T) {
		ns := "sel-notin-" + newID()[:8]
		pApple := newProduct()
		pApple.Namespace = ns
		pApple.Name = "apple-" + newID()[:6]
		pApple.Labels = map[string]string{"brand": "apple"}
		require.NoError(t, ds.CreateProduct(ctx, pApple))

		pSamsung := newProduct()
		pSamsung.Namespace = ns
		pSamsung.Name = "samsung-" + newID()[:6]
		pSamsung.Labels = map[string]string{"brand": "samsung"}
		require.NoError(t, ds.CreateProduct(ctx, pSamsung))

		sel := catalog.LabelSelector{
			MatchExpressions: []catalog.LabelSelectorRequirement{
				{Key: "brand", Operator: "NotIn", Values: []string{"apple"}},
			},
		}
		result, err := ds.ListProductsByLabelSelector(ctx, ns, sel)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, pSamsung.UID, result[0].UID)
	})

	t.Run("ListProductsByLabelSelector/MatchExpressions_Exists", func(t *testing.T) {
		ns := "sel-exists-" + newID()[:8]
		pFeatured := newProduct()
		pFeatured.Namespace = ns
		pFeatured.Name = "featured-" + newID()[:6]
		pFeatured.Labels = map[string]string{"featured": "true"}
		require.NoError(t, ds.CreateProduct(ctx, pFeatured))

		pPlain := newProduct()
		pPlain.Namespace = ns
		pPlain.Name = "plain-" + newID()[:6]
		pPlain.Labels = map[string]string{"other": "val"}
		require.NoError(t, ds.CreateProduct(ctx, pPlain))

		sel := catalog.LabelSelector{
			MatchExpressions: []catalog.LabelSelectorRequirement{
				{Key: "featured", Operator: "Exists"},
			},
		}
		result, err := ds.ListProductsByLabelSelector(ctx, ns, sel)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, pFeatured.UID, result[0].UID)
	})

	t.Run("ListProductsByLabelSelector/MatchExpressions_DoesNotExist", func(t *testing.T) {
		ns := "sel-dne-" + newID()[:8]
		pSale := newProduct()
		pSale.Namespace = ns
		pSale.Name = "sale-" + newID()[:6]
		pSale.Labels = map[string]string{"sale": "true"}
		require.NoError(t, ds.CreateProduct(ctx, pSale))

		pNoSale := newProduct()
		pNoSale.Namespace = ns
		pNoSale.Name = "no-sale-" + newID()[:6]
		pNoSale.Labels = map[string]string{"other": "val"}
		require.NoError(t, ds.CreateProduct(ctx, pNoSale))

		sel := catalog.LabelSelector{
			MatchExpressions: []catalog.LabelSelectorRequirement{
				{Key: "sale", Operator: "DoesNotExist"},
			},
		}
		result, err := ds.ListProductsByLabelSelector(ctx, ns, sel)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, pNoSale.UID, result[0].UID)
	})

	t.Run("ListProductsByLabelSelector/MatchExpressions_In", func(t *testing.T) {
		ns := "sel-in-" + newID()[:8]
		pProd := newProduct()
		pProd.Namespace = ns
		pProd.Name = "env-prod-" + newID()[:6]
		pProd.Labels = map[string]string{"env": "prod"}
		require.NoError(t, ds.CreateProduct(ctx, pProd))

		pStaging := newProduct()
		pStaging.Namespace = ns
		pStaging.Name = "env-staging-" + newID()[:6]
		pStaging.Labels = map[string]string{"env": "staging"}
		require.NoError(t, ds.CreateProduct(ctx, pStaging))

		sel := catalog.LabelSelector{
			MatchExpressions: []catalog.LabelSelectorRequirement{
				{Key: "env", Operator: "In", Values: []string{"prod"}},
			},
		}
		result, err := ds.ListProductsByLabelSelector(ctx, ns, sel)
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, pProd.UID, result[0].UID)
	})

	// ── Namespace ─────────────────────────────────────────────────────────────

	t.Run("Namespace/TestCreateNamespace_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespace(ctx, ns.UID)
		require.NoError(t, err)
		assert.Equal(t, ns.UID, got.UID)
		assert.Equal(t, ns.Name, got.Name)
		assert.Equal(t, ns.Tier, got.Tier)
		assert.Equal(t, ns.CreationActor, got.CreationActor)
	})

	t.Run("Namespace/TestCreateNamespace_duplicateIdentifier", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganization)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		ns2 := newNamespace(datastore.NamespaceTierUser)
		ns2.Name = ns.Name // same name
		err := ds.CreateNamespace(ctx, ns2)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Namespace/TestCreateNamespace_duplicateUID", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganization)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		duplicateUID := newNamespace(datastore.NamespaceTierUser)
		duplicateUID.UID = ns.UID
		err := ds.CreateNamespace(ctx, duplicateUID)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Namespace/TestCreateNamespace_acrossTiers", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		// same name, different tier — must still conflict
		nsOrg := newNamespace(datastore.NamespaceTierOrganization)
		nsOrg.Name = ns.Name
		err := ds.CreateNamespace(ctx, nsOrg)
		assert.ErrorIs(t, err, datastore.ErrAlreadyExists)
	})

	t.Run("Namespace/TestGetNamespaceByName_notFound", func(t *testing.T) {
		_, err := ds.GetNamespaceByName(ctx, "does-not-exist-"+newID()[:8])
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
		ns2 := newNamespace(datastore.NamespaceTierOrganization)
		require.NoError(t, ds.CreateNamespace(ctx, ns1))
		require.NoError(t, ds.CreateNamespace(ctx, ns2))

		after, err := ds.ListNamespaces(ctx, datastore.PageParams{})
		require.NoError(t, err)
		assert.Equal(t, len(before.Items)+2, len(after.Items))
	})

	t.Run("Namespace/TestGetNamespace_byID_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespace(ctx, ns.UID)
		require.NoError(t, err)
		assert.Equal(t, ns.UID, got.UID)
	})

	t.Run("Namespace/TestGetNamespaceByName_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespaceByName(ctx, ns.Name)
		require.NoError(t, err)
		assert.Equal(t, ns.UID, got.UID)
		assert.Equal(t, ns.Name, got.Name)
	})

	t.Run("Namespace/TestResourceContractRoundTrip", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganization)
		deletionTimestamp := time.Now().UTC().Truncate(time.Millisecond)
		ns.Generation = 7
		ns.ResourceVersion = "11"
		ns.Status = []byte(`{"observedGeneration":7,"conditions":[{"type":"Ready","status":"True"}]}`)
		ns.DeletionTimestamp = &deletionTimestamp
		ns.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		got, err := ds.GetNamespace(ctx, ns.UID)
		require.NoError(t, err)
		assert.Equal(t, ns.Generation, got.Generation)
		assert.Equal(t, ns.ResourceVersion, got.ResourceVersion)
		assert.JSONEq(t, string(ns.Status), string(got.Status))
		assert.Equal(t, ns.DeletionTimestamp, got.DeletionTimestamp)
		assert.Equal(t, ns.Finalizers, got.Finalizers)
	})

	t.Run("Namespace/TestOptimisticUpdate", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		stale := *ns
		expectedVersion := ns.ResourceVersion

		ns.Title = "updated"
		datastore.AdvanceNamespaceSystemVersion(ns)
		require.NoError(t, ds.UpdateNamespace(ctx, ns, expectedVersion))

		stale.Title = "stale"
		datastore.AdvanceNamespaceSystemVersion(&stale)
		assert.ErrorIs(t, ds.UpdateNamespace(ctx, &stale, expectedVersion), datastore.ErrConflict)

		got, err := ds.GetNamespace(ctx, ns.UID)
		require.NoError(t, err)
		assert.Equal(t, "updated", got.Title)
		assert.Equal(t, ns.ResourceVersion, got.ResourceVersion)
	})

	t.Run("Namespace/TestDeleteWithResourceVersion", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		assert.ErrorIs(t, ds.DeleteNamespaceWithResourceVersion(ctx, ns.UID, "stale"), datastore.ErrConflict)
		require.NoError(t, ds.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion))

		_, err := ds.GetNamespace(ctx, ns.UID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
		_, err = ds.GetNamespaceByName(ctx, ns.Name)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestDeleteNamespace_success", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		require.NoError(t, ds.DeleteNamespace(ctx, ns.UID))

		_, err := ds.GetNamespace(ctx, ns.UID)
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestDeleteNamespace_notFound", func(t *testing.T) {
		err := ds.DeleteNamespace(ctx, newID())
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestDeleteNamespace_thenGetReturnsNotFound", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierOrganization)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		require.NoError(t, ds.DeleteNamespace(ctx, ns.UID))

		_, errID := ds.GetNamespace(ctx, ns.UID)
		assert.ErrorIs(t, errID, datastore.ErrNotFound)

		_, errIdent := ds.GetNamespaceByName(ctx, ns.Name)
		assert.ErrorIs(t, errIdent, datastore.ErrNotFound)
	})

	t.Run("Namespace/TestHasRepositories", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))

		has, err := ds.HasRepositories(ctx, ns.Name)
		require.NoError(t, err)
		assert.False(t, has)

		repo := newRepository(ns.Name)
		require.NoError(t, ds.CreateRepository(ctx, repo))

		has, err = ds.HasRepositories(ctx, ns.Name)
		require.NoError(t, err)
		assert.True(t, has)

		require.NoError(t, ds.DeleteRepository(ctx, repo.UID))

		has, err = ds.HasRepositories(ctx, ns.Name)
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("Namespace/TestRepositoryLifecycleCoordination", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		repository := newRepository(ns.Name)

		require.NoError(t, ds.CreateRepositoryInActiveNamespace(ctx, repository))
		current, err := ds.GetNamespace(ctx, ns.UID)
		require.NoError(t, err)
		deletedAt := time.Now().UTC().Truncate(time.Millisecond)
		expectedResourceVersion := current.ResourceVersion
		current.DeletionTimestamp = &deletedAt
		datastore.AdvanceNamespaceSystemVersion(current)
		require.ErrorIs(t, ds.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrNamespaceNotEmpty)

		require.NoError(t, ds.DeleteRepository(ctx, repository.UID))
		current, err = ds.GetNamespace(ctx, ns.UID)
		require.NoError(t, err)
		expectedResourceVersion = current.ResourceVersion
		current.DeletionTimestamp = &deletedAt
		datastore.AdvanceNamespaceSystemVersion(current)
		require.NoError(t, ds.MarkNamespaceDeletion(ctx, current, expectedResourceVersion))

		late := newRepository(ns.Name)
		require.ErrorIs(t, ds.CreateRepositoryInActiveNamespace(ctx, late), datastore.ErrNamespaceNotActive)
	})

	t.Run("Repository/TestTransferRequiresActiveTargetAndBlocksDeletion", func(t *testing.T) {
		source := newNamespace(datastore.NamespaceTierUser)
		target := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, source))
		require.NoError(t, ds.CreateNamespace(ctx, target))

		repository := newRepository(source.Name)
		repository.Name = "catalog"
		require.NoError(t, ds.CreateRepositoryInActiveNamespace(ctx, repository))
		require.NoError(t, ds.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
			Namespace:    source.Name,
			Name:         repository.Name,
			RepositoryID: repository.UID,
		}))

		require.NoError(t, ds.TransferRepository(ctx, repository.UID, source.Name, target.Name))
		transferred, err := ds.GetRepository(ctx, repository.UID)
		require.NoError(t, err)
		assert.Equal(t, target.Name, transferred.Namespace)

		currentTarget, err := ds.GetNamespace(ctx, target.UID)
		require.NoError(t, err)
		expectedResourceVersion := currentTarget.ResourceVersion
		deletedAt := time.Now().UTC().Truncate(time.Millisecond)
		currentTarget.DeletionTimestamp = &deletedAt
		datastore.AdvanceNamespaceSystemVersion(currentTarget)
		require.ErrorIs(t, ds.MarkNamespaceDeletion(ctx, currentTarget, expectedResourceVersion), datastore.ErrNamespaceNotEmpty)

		terminatingTarget := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, terminatingTarget))
		expectedResourceVersion = terminatingTarget.ResourceVersion
		terminatingTarget.DeletionTimestamp = &deletedAt
		datastore.AdvanceNamespaceSystemVersion(terminatingTarget)
		require.NoError(t, ds.MarkNamespaceDeletion(ctx, terminatingTarget, expectedResourceVersion))

		err = ds.TransferRepository(ctx, repository.UID, target.Name, terminatingTarget.Name)
		require.ErrorIs(t, err, datastore.ErrNamespaceNotActive)
		stillTransferred, getErr := ds.GetRepository(ctx, repository.UID)
		require.NoError(t, getErr)
		assert.Equal(t, target.Name, stillTransferred.Namespace)
	})

	t.Run("Repository/TestDuplicateUIDAndScopedName", func(t *testing.T) {
		namespace := "repo-uniqueness-" + newID()[:8]
		repository := newRepository(namespace)
		repository.Name = "shared-name"
		require.NoError(t, ds.CreateRepository(ctx, repository))

		duplicateUID := newRepository(namespace)
		duplicateUID.UID = repository.UID
		require.ErrorIs(t, ds.CreateRepository(ctx, duplicateUID), datastore.ErrAlreadyExists)

		duplicateName := newRepository(namespace)
		duplicateName.Name = repository.Name
		require.ErrorIs(t, ds.CreateRepository(ctx, duplicateName), datastore.ErrAlreadyExists)

		otherNamespace := newRepository("other-" + namespace)
		otherNamespace.Name = repository.Name
		require.NoError(t, ds.CreateRepository(ctx, otherNamespace))
	})

	t.Run("Repository/TestGlobalListing", func(t *testing.T) {
		lister, ok := ds.(datastore.GlobalRepositoryLister)
		require.True(t, ok)

		first := newRepository("global-a-" + newID()[:8])
		second := newRepository("global-b-" + newID()[:8])
		first.CreationTimestamp = time.Now().UTC().Add(-time.Second)
		second.CreationTimestamp = time.Now().UTC()
		require.NoError(t, ds.CreateRepository(ctx, first))
		require.NoError(t, ds.CreateRepository(ctx, second))

		result, err := lister.ListRepositories(ctx, datastore.PageParams{First: 1})
		require.NoError(t, err)
		require.Len(t, result.Items, 1)
		assert.True(t, result.HasNext)
		assert.True(t, result.TotalCount == -1 || result.TotalCount >= 2)
	})

	t.Run("Repository/TestMappingRenameAndTransfer", func(t *testing.T) {
		repositoryID := newID()
		fromNamespace := "mapping-from-" + newID()[:8]
		toNamespace := "mapping-to-" + newID()[:8]
		from := newNamespace(datastore.NamespaceTierUser)
		from.Name = fromNamespace
		to := newNamespace(datastore.NamespaceTierUser)
		to.Name = toNamespace
		require.NoError(t, ds.CreateNamespace(ctx, from))
		require.NoError(t, ds.CreateNamespace(ctx, to))
		mapping := &datastore.NamespaceMapping{
			Namespace:    fromNamespace,
			Name:         "old-name",
			RepositoryID: repositoryID,
		}
		require.NoError(t, ds.CreateNamespaceMapping(ctx, mapping))
		require.NoError(t, ds.CreateNamespaceMapping(ctx, mapping))

		require.NoError(t, ds.RenameRepository(ctx, fromNamespace, "old-name", "new-name"))
		_, err := ds.LookupRepository(ctx, fromNamespace, "old-name")
		require.ErrorIs(t, err, datastore.ErrNotFound)

		require.NoError(t, ds.TransferRepository(ctx, repositoryID, fromNamespace, toNamespace))
		got, err := ds.LookupRepository(ctx, toNamespace, "new-name")
		require.NoError(t, err)
		assert.Equal(t, repositoryID, got.RepositoryID)
		assert.Equal(t, toNamespace, got.Namespace)
	})

	// ── HasCatalogResources ───────────────────────────────────────────────────

	t.Run("Repository/TestHasCatalogResources", func(t *testing.T) {
		ns := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, ds.CreateNamespace(ctx, ns))
		repo := newRepository(ns.Name)
		require.NoError(t, ds.CreateRepository(ctx, repo))

		has, err := ds.HasCatalogResources(ctx, repo.UID)
		require.NoError(t, err)
		assert.False(t, has)

		p := newProduct()
		p.Namespace = ns.Name
		p.RepositoryID = repo.UID
		require.NoError(t, ds.CreateProduct(ctx, p))
		has, err = ds.HasCatalogResources(ctx, repo.UID)
		require.NoError(t, err)
		assert.True(t, has)
		require.NoError(t, ds.DeleteProduct(ctx, p.UID))

		v := &datastore.ProductVariant{
			UID:               newID(),
			Namespace:         ns.Name,
			Name:              "variant-" + newID()[:8],
			APIVersion:        "catalog.gitstore.dev/v1beta1",
			Kind:              "ProductVariant",
			CreationTimestamp: time.Now(),
			SKU:               "sku-" + newID()[:8],
			ProductRefName:    "product-" + newID()[:8],
			RepositoryID:      repo.UID,
		}
		require.NoError(t, ds.CreateProductVariant(ctx, v))
		has, err = ds.HasCatalogResources(ctx, repo.UID)
		require.NoError(t, err)
		assert.True(t, has)
		require.NoError(t, ds.DeleteProductVariant(ctx, v.UID))

		c := newCategoryTaxonomy()
		c.Namespace = ns.Name
		c.RepositoryID = repo.UID
		require.NoError(t, ds.CreateCategoryTaxonomy(ctx, c))
		has, err = ds.HasCatalogResources(ctx, repo.UID)
		require.NoError(t, err)
		assert.True(t, has)
		require.NoError(t, ds.DeleteCategoryTaxonomy(ctx, c.UID))

		coll := newCollection()
		coll.Namespace = ns.Name
		coll.RepositoryID = repo.UID
		require.NoError(t, ds.CreateCollection(ctx, coll))
		has, err = ds.HasCatalogResources(ctx, repo.UID)
		require.NoError(t, err)
		assert.True(t, has)
		require.NoError(t, ds.DeleteCollection(ctx, coll.UID))

		has, err = ds.HasCatalogResources(ctx, repo.UID)
		require.NoError(t, err)
		assert.False(t, has)
	})
}
