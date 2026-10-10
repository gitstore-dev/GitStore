// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnerReferenceProjection_ScyllaLimitOneAndKeysetRecovery(t *testing.T) {
	store := newTestStore(t)
	owners := store.(datastore.OwnerReferenceStore)
	ctx := context.Background()
	namespace, repositoryID := "owner-refs-"+newID()[:8], newID()
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: namespace, CreationTimestamp: now}))
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, &datastore.Repository{
		UID: repositoryID, Namespace: namespace, Name: "catalog", CreationTimestamp: now,
	}))
	parentUID := newID()
	scope := datastore.OwnerReferenceScope{Namespace: namespace, RepositoryID: repositoryID}
	blocking, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Name: "parent",
		UID: parentUID, BlockOwnerDeletion: true,
	}})
	require.NoError(t, err)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID: newID(), Namespace: namespace, RepositoryID: repositoryID, Name: "child",
		ResourceVersion: "1", CreationTimestamp: time.Now().UTC(), OwnerReferences: blocking,
	}))
	hasBlocking, err := owners.HasBlockingOwnerDependents(ctx, scope, parentUID)
	require.NoError(t, err)
	assert.True(t, hasBlocking)

	nonBlocking, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Name: "parent", UID: parentUID,
	}})
	require.NoError(t, err)
	for _, name := range []string{"product-a", "product-b"} {
		require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
			UID: newID(), Namespace: namespace, RepositoryID: repositoryID, Name: name,
			ResourceVersion: "1", CreationTimestamp: time.Now().UTC(), OwnerReferences: nonBlocking,
		}))
	}
	first, err := owners.ListNonBlockingProductOwnerDependents(ctx, scope, parentUID, "", 1)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	second, err := owners.ListNonBlockingProductOwnerDependents(ctx, scope, parentUID, first.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.Empty(t, second.NextCursor)
	assert.NotEqual(t, first.Items[0].DependentUID, second.Items[0].DependentUID)
}

func TestCategoryTaxonomyCompletion_ScyllaFencesSameNameReplacement(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	lifecycle := store.(datastore.CategoryTaxonomyDeletionStore)
	namespace, repositoryID := "category-completion-"+newID()[:8], newID()
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: newID(), Name: namespace, CreationTimestamp: now}))
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, &datastore.Repository{
		UID: repositoryID, Namespace: namespace, Name: "catalog", CreationTimestamp: now,
	}))
	old := &datastore.CategoryTaxonomy{
		UID: newID(), Namespace: namespace, RepositoryID: repositoryID, Name: "category",
		ResourceVersion: "7", CreationTimestamp: now, DeletionTimestamp: &now,
		Finalizers: []string{datastore.CategoryTaxonomyForegroundDeletionFinalizer},
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, old))
	_, err := lifecycle.CompleteCategoryTaxonomyDeletion(ctx, namespace, old.Name, old.ResourceVersion, old.UID)
	require.NoError(t, err)
	replacement := *old
	replacement.UID = newID()
	ownerUID := newID()
	replacement.OwnerReferences, err = json.Marshal([]catalog.OwnerReference{{
		Kind: "CategoryTaxonomy", Name: "parent", UID: ownerUID, BlockOwnerDeletion: true,
	}})
	require.NoError(t, err)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, &replacement))
	for _, uid := range []string{old.UID, ""} {
		_, err = lifecycle.CompleteCategoryTaxonomyDeletion(ctx, namespace, old.Name, old.ResourceVersion, uid)
		require.ErrorIs(t, err, datastore.ErrConflict)
	}
	current, err := store.GetCategoryTaxonomyByName(ctx, namespace, replacement.Name)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, current.UID)
	require.Equal(t, replacement.ResourceVersion, current.ResourceVersion)
	require.Equal(t, replacement.Finalizers, current.Finalizers)
	blocked, err := store.(datastore.OwnerReferenceStore).HasBlockingOwnerDependents(ctx,
		datastore.OwnerReferenceScope{Namespace: namespace, RepositoryID: repositoryID}, ownerUID)
	require.NoError(t, err)
	require.True(t, blocked)
	_, err = lifecycle.CompleteCategoryTaxonomyDeletion(ctx, namespace, current.Name, "stale", current.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	_, err = lifecycle.CompleteCategoryTaxonomyDeletion(ctx, namespace, current.Name, current.ResourceVersion, current.UID)
	require.NoError(t, err)
}
