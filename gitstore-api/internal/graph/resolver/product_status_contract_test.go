// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
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

// failingCategoryLookupStore wraps a real Datastore, overriding only
// GetCategoryTaxonomyByName to fail — used to prove a transient lookup
// failure is propagated rather than silently committing an empty owner
// reference (P1 finding on PR #426).
type failingCategoryLookupStore struct {
	datastore.Datastore
	err error
}

func (s failingCategoryLookupStore) GetCategoryTaxonomyByName(context.Context, string, string) (*datastore.CategoryTaxonomy, error) {
	return nil, s.err
}

func newProductStatusTestFixture(t *testing.T) (*mutationResolver, datastore.Datastore, *datastore.Product, *datastore.CategoryTaxonomy) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: uuid.NewString(), Name: "acme", ResourceVersion: "1"}))
	repositoryID := uuid.NewString()
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: repositoryID, Namespace: "acme", Name: "catalog"}))

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

// TestUpdateProductStatus_TransientOwnerReferenceLookupFailurePropagates is a
// regression test for a P1 finding on PR #426: if the category lookup for
// owner-reference synchronization hits a transient datastore error,
// UpdateProductStatus must fail the whole write rather than silently commit
// CategoryResolved=True with an empty owner-reference projection —
// otherwise DecoupleCategoryProducts can never find this Product later if
// its category is deleted.
func TestUpdateProductStatus_TransientOwnerReferenceLookupFailurePropagates(t *testing.T) {
	_, store, product, category := newProductStatusTestFixture(t)
	ctx := context.Background()
	categoryUID := mustEncodeNodeID(nodeKindCategory, category.UID)

	failingStore := failingCategoryLookupStore{Datastore: store, err: errors.New("transient datastore error")}
	r, err := NewResolver(ResolverDeps{Store: failingStore, Logger: zap.NewNop()})
	require.NoError(t, err)
	mutation := &mutationResolver{Resolver: r}

	_, err = mutation.UpdateProductStatus(ctx, model.UpdateProductStatusInput{
		Namespace: product.Namespace, Name: product.Name, ResourceVersion: product.ResourceVersion,
		Resolved: &model.ResolvedProductStatusInput{Category: &model.ResolvedCategoryRefInput{Name: category.Name, UID: categoryUID}},
	})
	require.Error(t, err, "a transient owner-reference lookup failure must fail the write, not commit an empty projection")

	persisted, getErr := store.GetProduct(ctx, product.UID)
	require.NoError(t, getErr)
	assert.Empty(t, persisted.Status, "status must be unchanged after the failed write")
	assert.Empty(t, persisted.OwnerReferences, "owner references must be unchanged after the failed write")
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

type productUpdateAfterStatusReadStore struct {
	datastore.Datastore
	afterRead func(context.Context) error
}

func (s *productUpdateAfterStatusReadStore) GetProductByName(ctx context.Context, namespace, name string) (*datastore.Product, error) {
	product, err := s.Datastore.GetProductByName(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if s.afterRead != nil {
		afterRead := s.afterRead
		s.afterRead = nil
		if err := afterRead(ctx); err != nil {
			return nil, err
		}
	}
	return product, nil
}

func TestUpdateProductStatus_ConcurrentSpecUpdateIsNotOverwritten(t *testing.T) {
	_, store, product, _ := newProductStatusTestFixture(t)
	ctx := context.Background()
	product.Spec = json.RawMessage(`{"title":"Original Title"}`)
	require.NoError(t, store.UpdateProduct(ctx, product))

	interleaved := &productUpdateAfterStatusReadStore{Datastore: store}
	interleaved.afterRead = func(ctx context.Context) error {
		current, err := store.GetProduct(ctx, product.UID)
		if err != nil {
			return err
		}
		current.Spec = json.RawMessage(`{"title":"Updated Title"}`)
		current.Generation++
		current.ResourceVersion = "2"
		return store.UpdateProduct(ctx, current)
	}
	r, err := NewResolver(ResolverDeps{Store: interleaved, Logger: zap.NewNop()})
	require.NoError(t, err)
	_, updateErr := (&mutationResolver{Resolver: r}).UpdateProductStatus(ctx, model.UpdateProductStatusInput{
		Namespace: product.Namespace, Name: product.Name, ResourceVersion: "1",
		Conditions: []*model.ConditionInput{{Type: string(catalog.ConditionReady), Status: model.ConditionStatusTrue}},
	})
	persisted, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"title":"Updated Title"}`, string(persisted.Spec),
		"a controller status snapshot must not overwrite an intervening Git-authored spec")
	require.Error(t, updateErr, "the stale status resourceVersion must conflict")
}

type productUpdateAfterDependentReadStore struct {
	datastore.Datastore
	datastore.OwnerReferenceStore
	afterRead func(context.Context) error
}

func (s *productUpdateAfterDependentReadStore) GetProduct(ctx context.Context, uid string) (*datastore.Product, error) {
	product, err := s.Datastore.GetProduct(ctx, uid)
	if err != nil {
		return nil, err
	}
	if s.afterRead != nil {
		afterRead := s.afterRead
		s.afterRead = nil
		if err := afterRead(ctx); err != nil {
			return nil, err
		}
	}
	return product, nil
}

func TestDecoupleCategoryProducts_ConcurrentSpecUpdateIsNotOverwritten(t *testing.T) {
	_, store, product, category := newProductStatusTestFixture(t)
	ctx := context.Background()
	product.Spec = json.RawMessage(`{"title":"Original Title","categoryRef":{"name":"laptops"}}`)
	product.Generation = 1
	product.Finalizers = []string{"gitstore.dev/product-protection"}
	product.OwnerReferences, _ = json.Marshal([]catalog.OwnerReference{{
		APIVersion: category.APIVersion, Kind: category.Kind, Name: category.Name, UID: category.UID,
	}})
	require.NoError(t, store.UpdateProduct(ctx, product))
	terminating, err := store.(datastore.CategoryTaxonomyDeletionStore).MarkCategoryTaxonomyDeletion(
		ctx, category.Namespace, category.Name, category.ResourceVersion, time.Now().UTC())
	require.NoError(t, err)

	interleaved := &productUpdateAfterDependentReadStore{
		Datastore: store, OwnerReferenceStore: store.(datastore.OwnerReferenceStore),
	}
	interleaved.afterRead = func(ctx context.Context) error {
		current, err := store.GetProduct(ctx, product.UID)
		if err != nil {
			return err
		}
		current.Spec = json.RawMessage(`{"title":"Updated Title","categoryRef":{"name":"laptops"}}`)
		current.Generation, current.ResourceVersion = 2, "2"
		return store.UpdateProduct(ctx, current)
	}
	r, err := NewResolver(ResolverDeps{Store: interleaved, Logger: zap.NewNop()})
	require.NoError(t, err)
	mutation := &mutationResolver{Resolver: r}
	decouple := true
	input := model.UpdateCategoryStatusInput{
		Namespace: category.Namespace, Name: category.Name, ResourceVersion: terminating.ResourceVersion, DecoupleProducts: &decouple,
	}
	payload, err := mutation.UpdateCategoryStatus(ctx, input)
	require.NoError(t, err)
	persisted, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"title":"Updated Title","categoryRef":{"name":"laptops"}}`, string(persisted.Spec))
	assert.Equal(t, product.Finalizers, persisted.Finalizers)
	assert.Equal(t, int64(2), persisted.Generation)
	assert.Equal(t, "2", persisted.ResourceVersion)
	assert.JSONEq(t, string(product.OwnerReferences), string(persisted.OwnerReferences))
	require.True(t, payload.HasMoreProductDependents, "a conflicted cleanup must remain in the bounded continuation")

	payload, err = mutation.UpdateCategoryStatus(ctx, input)
	require.NoError(t, err)
	require.False(t, payload.HasMoreProductDependents)
	cleaned, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	assert.Equal(t, persisted.Spec, cleaned.Spec)
	assert.Equal(t, persisted.Generation, cleaned.Generation)
	assert.Equal(t, persisted.Finalizers, cleaned.Finalizers)
	assert.Equal(t, "3", cleaned.ResourceVersion)
	assert.JSONEq(t, `[]`, string(cleaned.OwnerReferences))
	var status catalog.ProductStatus
	require.NoError(t, json.Unmarshal(cleaned.Status, &status))
	require.Len(t, status.Conditions, 1)
	assert.Equal(t, catalog.ConditionCategoryResolved, status.Conditions[0].Type)
	assert.Equal(t, "CategoryDeleted", status.Conditions[0].Reason)
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
