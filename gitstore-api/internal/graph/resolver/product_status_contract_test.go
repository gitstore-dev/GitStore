// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newProductStatusTestFixture(t *testing.T) (*mutationResolver, datastore.Datastore, *datastore.Product, *datastore.CategoryTaxonomy) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: uuid.NewString(), Name: "acme", ResourceVersion: "1"}))
	repositoryID := uuid.NewString()

	category := &datastore.CategoryTaxonomy{
		UID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Namespace: "acme", Name: "laptops", ResourceVersion: "1", RepositoryID: repositoryID,
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, category))

	product := &datastore.Product{
		UID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		Namespace: "acme", Name: "widget", ResourceVersion: "1", RepositoryID: repositoryID,
	}
	require.NoError(t, store.CreateProduct(ctx, product))

	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	return &mutationResolver{Resolver: r}, store, product, category
}

func TestUpdateProductStatus_ResolvedCategorySetMergesConditionsAndOwnerReference(t *testing.T) {
	mutation, store, product, category := newProductStatusTestFixture(t)
	ctx := context.Background()
	categoryUID := mustEncodeNodeID(nodeKindCategory, category.UID)

	gen := int32(3)
	revision := "main@sha1:abc123"
	_, err := mutation.UpdateProductStatus(ctx, model.UpdateProductStatusInput{
		Namespace:           product.Namespace,
		Name:                product.Name,
		ResourceVersion:     product.ResourceVersion,
		ObservedGeneration:  &gen,
		LastAppliedRevision: &revision,
		Conditions: []*model.ConditionInput{
			{Type: string(catalog.ConditionCategoryResolved), Status: model.ConditionStatusTrue, Reason: strPtr("CategoryFound")},
			{Type: string(catalog.ConditionReady), Status: model.ConditionStatusTrue, Reason: strPtr("ProductReady")},
		},
		Resolved: &model.ResolvedProductStatusInput{
			Category: &model.ResolvedCategoryRefInput{Name: category.Name, UID: categoryUID},
		},
	})
	require.NoError(t, err)

	persisted, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)

	var status catalog.ProductStatus
	require.NoError(t, json.Unmarshal(persisted.Status, &status))
	assert.Equal(t, int64(3), status.ObservedGeneration)
	assert.Equal(t, "main@sha1:abc123", status.LastAppliedRevision)
	require.NotNil(t, status.Resolved)
	require.NotNil(t, status.Resolved.Category)
	assert.Equal(t, "laptops", status.Resolved.Category.Name)
	// Stored verbatim: already Relay-encoded, never decoded/re-encoded here.
	assert.Equal(t, categoryUID, status.Resolved.Category.UID)

	var refs []catalog.OwnerReference
	require.NoError(t, json.Unmarshal(persisted.OwnerReferences, &refs))
	require.Len(t, refs, 1)
	assert.Equal(t, "CategoryTaxonomy", refs[0].Kind)
	assert.Equal(t, category.UID, refs[0].UID)
	assert.False(t, refs[0].BlockOwnerDeletion)
}

func TestUpdateProductStatus_ResolvedCategoryNilClearsResolvedAndOwnerReference(t *testing.T) {
	mutation, store, product, category := newProductStatusTestFixture(t)
	ctx := context.Background()
	categoryUID := mustEncodeNodeID(nodeKindCategory, category.UID)

	_, err := mutation.UpdateProductStatus(ctx, model.UpdateProductStatusInput{
		Namespace: product.Namespace, Name: product.Name, ResourceVersion: product.ResourceVersion,
		Resolved: &model.ResolvedProductStatusInput{Category: &model.ResolvedCategoryRefInput{Name: category.Name, UID: categoryUID}},
	})
	require.NoError(t, err)
	afterResolve, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)

	_, err = mutation.UpdateProductStatus(ctx, model.UpdateProductStatusInput{
		Namespace: product.Namespace, Name: product.Name, ResourceVersion: afterResolve.ResourceVersion,
		Resolved: &model.ResolvedProductStatusInput{Category: nil},
	})
	require.NoError(t, err)

	persisted, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	var status catalog.ProductStatus
	require.NoError(t, json.Unmarshal(persisted.Status, &status))
	if status.Resolved != nil {
		assert.Nil(t, status.Resolved.Category)
	}

	var refs []catalog.OwnerReference
	require.NoError(t, json.Unmarshal(persisted.OwnerReferences, &refs))
	assert.Empty(t, refs)
}

func TestUpdateProductStatus_StaleResourceVersionRejected(t *testing.T) {
	mutation, _, product, _ := newProductStatusTestFixture(t)
	ctx := context.Background()

	_, err := mutation.UpdateProductStatus(ctx, model.UpdateProductStatusInput{
		Namespace: product.Namespace, Name: product.Name, ResourceVersion: "stale",
	})
	require.Error(t, err)
}

// TestUpdateProductStatus_ThenDecoupleCategoryProducts_ConvergesCategoryDeleted
// is T031: a regression test proving R7's "no new controller code" claim —
// the owner reference T020's declarative resolved.category sync establishes
// is exactly what the pre-existing spec-055 DecoupleCategoryProducts flow
// (category.resolvers.go's UpdateCategoryStatus, decoupleProducts: true)
// needs to locate this Product. This exercises the real sync path, not a
// hand-constructed OwnerReferences fixture like
// TestUpdateCategoryStatusDecouplesNonBlockingProducts already covers.
func TestUpdateProductStatus_ThenDecoupleCategoryProducts_ConvergesCategoryDeleted(t *testing.T) {
	mutation, store, product, category := newProductStatusTestFixture(t)
	ctx := context.Background()
	categoryUID := mustEncodeNodeID(nodeKindCategory, category.UID)

	// Establish the owner reference the same way T017's reconciler would, via
	// updateProductStatus's declarative resolved.category sync (T020).
	_, err := mutation.UpdateProductStatus(ctx, model.UpdateProductStatusInput{
		Namespace: product.Namespace, Name: product.Name, ResourceVersion: product.ResourceVersion,
		Resolved: &model.ResolvedProductStatusInput{Category: &model.ResolvedCategoryRefInput{Name: category.Name, UID: categoryUID}},
	})
	require.NoError(t, err)
	resolved, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	var refs []catalog.OwnerReference
	require.NoError(t, json.Unmarshal(resolved.OwnerReferences, &refs))
	require.Len(t, refs, 1, "resolved.category sync must have established the owner reference")

	lifecycle := store.(datastore.CategoryTaxonomyDeletionStore)
	terminating, err := lifecycle.MarkCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, category.ResourceVersion, time.Now().UTC())
	require.NoError(t, err)

	decouple := true
	payload, err := mutation.UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Namespace: category.Namespace, Name: category.Name, ResourceVersion: terminating.ResourceVersion, DecoupleProducts: &decouple,
	})
	require.NoError(t, err)
	assert.False(t, payload.HasMoreProductDependents, "DecoupleCategoryProducts must have found the Product via the owner reference T020 established")

	decoupled, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(decoupled.OwnerReferences))
	var status catalog.ProductStatus
	require.NoError(t, json.Unmarshal(decoupled.Status, &status))
	found := false
	for _, c := range status.Conditions {
		if c.Type == catalog.ConditionCategoryResolved && c.Status == catalog.ConditionFalse && c.Reason == "CategoryDeleted" {
			found = true
		}
	}
	assert.True(t, found, "expected transient CategoryResolved=False/CategoryDeleted, got %+v", status.Conditions)
}

func strPtr(s string) *string { return &s }
