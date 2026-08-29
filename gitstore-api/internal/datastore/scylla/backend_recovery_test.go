// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogueRowsPreserveCanonicalEnvelope(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	deletedAt := now.Add(time.Hour)
	uid := uuid.New().String()
	repositoryID := uuid.New().String()
	owners := json.RawMessage(`[{"uid":"owner"}]`)
	spec := json.RawMessage(`{"field":"value"}`)
	status := json.RawMessage(`{"observedGeneration":4}`)

	product := &datastore.Product{
		UID: uid, Namespace: "shop", Name: "product", APIVersion: "catalog/v1", Kind: "Product",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: map[string]string{"a": "b"}, Annotations: map[string]string{"c": "d"},
		OwnerReferences: owners, Finalizers: []string{"finalizer"}, DeletionTimestamp: &deletedAt,
		RepositoryID: repositoryID, SourcePath: "products/product.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	productRow := toProductRow(product)
	assert.Equal(t, product.CreationActor, productRow.CreationActor)
	assert.Equal(t, string(owners), productRow.OwnerReferences)
	assert.Equal(t, product.Finalizers, productRow.Finalizers)
	assert.Equal(t, &deletedAt, productRow.DeletionTimestamp)
	require.NotNil(t, productRow.RepositoryID)
	assert.Equal(t, repositoryID, productRow.RepositoryID.String())
	assert.Equal(t, product, fromProductRow(productRow))

	category := &datastore.CategoryTaxonomy{
		UID: uid, Namespace: "shop", Name: "category", APIVersion: "catalog/v1", Kind: "CategoryTaxonomy",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, ParentName: "parent", AncestorPath: "parent/category",
		RepositoryID: repositoryID, SourcePath: "categories/category.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	categoryRow := toCategoryTaxonomyRow(category)
	assert.Equal(t, string(owners), categoryRow.OwnerReferences)
	assert.Equal(t, category, fromCategoryTaxonomyRow(categoryRow))

	collection := &datastore.Collection{
		UID: uid, Namespace: "shop", Name: "collection", APIVersion: "catalog/v1", Kind: "Collection",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, RepositoryID: repositoryID,
		SourcePath: "collections/collection.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	collectionRow := toCollectionRow(collection)
	assert.Equal(t, string(owners), collectionRow.OwnerReferences)
	assert.Equal(t, collection, fromCollectionRow(collectionRow))

	variant := &datastore.ProductVariant{
		UID: uid, Namespace: "shop", Name: "variant", APIVersion: "catalog/v1", Kind: "ProductVariant",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, SKU: "sku", ProductRefName: "product",
		RepositoryID: repositoryID, SourcePath: "variants/variant.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	variantRow := toProductVariantRow(variant)
	assert.Equal(t, string(owners), variantRow.OwnerReferences)
	assert.Equal(t, variant, fromProductVariantRow(variantRow))
}

func TestCatalogueCreateCompensatesEveryFailureBoundary(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid", "authoritative"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid", "authoritative"}},
		{kind: "Collection", projections: []string{"name", "uid", "authoritative"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "authoritative", "product-ref"}},
	}

	for _, test := range tests {
		for _, failedProjection := range test.projections {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				t.Run(test.kind+"/"+failedProjection+"/"+string(point), func(t *testing.T) {
					state := make(map[string]bool)
					injector := newTestFailureInjector()
					injector.fail("create-"+failedProjection, point)
					executor := newMutationExecutor(injector)

					err := executor.execute(context.Background(), createActions(test.kind, state, test.projections)...)

					require.Error(t, err)
					for _, projection := range test.projections {
						assert.False(t, state[projection], "projection %s was not compensated", projection)
					}
				})
			}
		}
	}
}

func TestCatalogueUpdateRollsForwardAfterProjectionFailure(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid"}},
		{kind: "Collection", projections: []string{"name", "uid"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "product-ref"}},
	}
	for _, test := range tests {
		for _, failedProjection := range test.projections {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				t.Run(test.kind+"/"+failedProjection+"/"+string(point), func(t *testing.T) {
					state := map[string]string{"authoritative": "old"}
					for _, projection := range test.projections {
						state[projection] = "old"
					}
					injector := newTestFailureInjector()
					injector.fail("converge-"+failedProjection, point)
					executor := newMutationExecutor(injector)

					err := executor.executeUpdate(
						context.Background(),
						"2",
						updateAuthoritativeAction(test.kind, state),
						updateProjectionActions(test.kind, state, test.projections)...,
					)

					require.NoError(t, err)
					for key, value := range state {
						assert.Equal(t, "new", value, "%s did not roll forward", key)
					}
				})
			}
		}
	}
}

func TestCatalogueUpdateReturnsRepairRequiredWithoutRollingBackAuthoritative(t *testing.T) {
	state := map[string]string{"authoritative": "old", "old-sku": "present", "new-sku": "missing"}
	executor := newMutationExecutor(nil)
	newSKUConverged := false

	err := executor.executeUpdate(
		context.Background(),
		"2",
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "authoritative", Action: "update-authoritative"},
			Apply: func(context.Context) error {
				state["authoritative"] = "new"
				return nil
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "new-sku", Action: "converge-sku"},
			Apply: func(context.Context) error {
				return errors.New("persistent projection failure")
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "old-sku", Action: "delete-old-sku"},
			Apply: func(context.Context) error {
				if !newSKUConverged {
					return errors.New("new sku has not converged")
				}
				state["old-sku"] = "deleted"
				return nil
			},
		},
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.Equal(t, "new", state["authoritative"])
	assert.Equal(t, "present", state["old-sku"])
	assert.Equal(t, "missing", state["new-sku"])
}

func TestCatalogueUpdateConvergesAfterAuthoritativePostCommitFailure(t *testing.T) {
	state := map[string]string{"authoritative": "old", "name": "old"}
	injector := newTestFailureInjector()
	injector.fail("update-authoritative", failureAfter)
	executor := newMutationExecutor(injector)

	err := executor.executeUpdate(
		context.Background(),
		"2",
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "Product", Projection: "authoritative", Action: "update-authoritative"},
			Apply: func(context.Context) error {
				state["authoritative"] = "new"
				return nil
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "Product", Projection: "name", Action: "converge-name"},
			Apply: func(context.Context) error {
				state["name"] = "new"
				return nil
			},
		},
	)

	require.Error(t, err)
	assert.Equal(t, "new", state["authoritative"])
	assert.Equal(t, "new", state["name"])
}

func TestCatalogueUpdateAuthoritativeFailurePerformsNoProjectionWrites(t *testing.T) {
	for _, applyFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "apply"}[applyFailure], func(t *testing.T) {
			state := map[string]string{"authoritative": "old", "name": "old"}
			injector := newTestFailureInjector()
			if !applyFailure {
				injector.fail("update-authoritative", failureBefore)
			}
			executor := newMutationExecutor(injector)
			authoritative := updateAuthoritativeAction("Product", state)
			if applyFailure {
				authoritative.Apply = func(context.Context) error {
					return errors.New("compare-and-set failed")
				}
			}

			err := executor.executeUpdate(
				context.Background(),
				"2",
				authoritative,
				updateProjectionActions("Product", state, []string{"name"})...,
			)

			require.Error(t, err)
			assert.Equal(t, "old", state["authoritative"])
			assert.Equal(t, "old", state["name"])
		})
	}
}

func TestCatalogueStaleVersionPerformsNoWrites(t *testing.T) {
	writes := 0
	err := validateResourceVersionTransition("3", "2")
	if err == nil {
		writes++
	}

	require.ErrorIs(t, err, datastore.ErrConflict)
	assert.Zero(t, writes)
}

func TestCatalogueDeleteRestoresExactPriorStateAtEveryPrecommitBoundary(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid"}},
		{kind: "Collection", projections: []string{"name", "uid"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "product-ref"}},
	}
	for _, test := range tests {
		for _, failedStep := range append(append([]string(nil), test.projections...), "authoritative") {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				if failedStep == "authoritative" && point == failureAfter {
					continue
				}
				t.Run(test.kind+"/"+failedStep+"/"+string(point), func(t *testing.T) {
					state := map[string]bool{"authoritative": true}
					for _, projection := range test.projections {
						state[projection] = true
					}
					injector := newTestFailureInjector()
					injector.fail("delete-"+failedStep, point)
					executor := newMutationExecutor(injector)

					err := executor.executeDelete(
						context.Background(),
						deleteAuthoritativeAction(state, nil),
						deleteProjectionActions(state, test.projections)...,
					)

					require.Error(t, err)
					for key, present := range state {
						assert.True(t, present, "%s was not restored", key)
					}
				})
			}
		}
	}
}

func TestCatalogueDeleteAfterAuthoritativeFailureKeepsDesiredFinalState(t *testing.T) {
	state := map[string]bool{"authoritative": true, "name": true, "uid": true}
	injector := newTestFailureInjector()
	injector.fail("delete-authoritative", failureAfter)
	executor := newMutationExecutor(injector)

	err := executor.executeDelete(
		context.Background(),
		deleteAuthoritativeAction(state, nil),
		deleteProjectionActions(state, []string{"name", "uid"})...,
	)

	require.Error(t, err)
	assert.False(t, state["authoritative"])
	assert.False(t, state["name"])
	assert.False(t, state["uid"])
}

func TestCatalogueDeleteCompensationFailureRequiresRepair(t *testing.T) {
	state := map[string]bool{"authoritative": true, "name": true}
	executor := newMutationExecutor(nil)
	projection := mutationAction{
		Step: datastore.MutationStep{ResourceKind: "Collection", Projection: "name", Action: "delete-name"},
		Apply: func(context.Context) error {
			state["name"] = false
			return nil
		},
		Compensate: func(context.Context) error {
			return errors.New("restore failed")
		},
	}

	err := executor.executeDelete(
		context.Background(),
		deleteAuthoritativeAction(state, errors.New("delete failed")),
		projection,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.True(t, state["authoritative"])
	assert.False(t, state["name"])
}

func TestConditionalDeleteConflictDoesNotMutateProjections(t *testing.T) {
	projectionDeletes := 0
	executor := newMutationExecutor(nil)
	authoritative := mutationAction{
		Step:  datastore.MutationStep{Operation: "delete", ResourceKind: "ProductVariant", Projection: "authoritative", Action: "delete-authoritative"},
		Apply: func(context.Context) error { return datastore.ErrConflict },
	}
	projection := mutationAction{
		Step: datastore.MutationStep{Operation: "delete", ResourceKind: "ProductVariant", Projection: "sku", Action: "delete-sku"},
		Apply: func(context.Context) error {
			projectionDeletes++
			return nil
		},
	}

	err := executor.executeConditionalDelete(context.Background(), "1", authoritative, projection)

	require.ErrorIs(t, err, datastore.ErrConflict)
	assert.Zero(t, projectionDeletes)
}

func createActions(kind string, state map[string]bool, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{
				Operation: "create", ResourceKind: kind, Projection: projection, Action: "create-" + projection,
			},
			Apply: func(context.Context) error {
				state[projection] = true
				return nil
			},
			Compensate: func(context.Context) error {
				state[projection] = false
				return nil
			},
		})
	}
	return actions
}

func updateAuthoritativeAction(kind string, state map[string]string) mutationAction {
	return mutationAction{
		Step: datastore.MutationStep{
			Operation: "update", ResourceKind: kind, Projection: "authoritative", Action: "update-authoritative",
		},
		Apply: func(context.Context) error {
			state["authoritative"] = "new"
			return nil
		},
	}
}

func updateProjectionActions(kind string, state map[string]string, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{
				Operation: "update", ResourceKind: kind, Projection: projection, Action: "converge-" + projection,
			},
			Apply: func(context.Context) error {
				state[projection] = "new"
				return nil
			},
		})
	}
	return actions
}

func deleteProjectionActions(state map[string]bool, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{Operation: "delete", Projection: projection, Action: "delete-" + projection},
			Apply: func(context.Context) error {
				state[projection] = false
				return nil
			},
			Compensate: func(context.Context) error {
				state[projection] = true
				return nil
			},
		})
	}
	return actions
}

func deleteAuthoritativeAction(state map[string]bool, applyErr error) mutationAction {
	return mutationAction{
		Step: datastore.MutationStep{Operation: "delete", Projection: "authoritative", Action: "delete-authoritative"},
		Apply: func(context.Context) error {
			if applyErr != nil {
				return applyErr
			}
			state["authoritative"] = false
			return nil
		},
	}
}
