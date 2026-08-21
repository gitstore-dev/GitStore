// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnerReferenceProjection_BlocksChildrenAndPagesProducts(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	owners, ok := store.(datastore.OwnerReferenceStore)
	require.True(t, ok)

	ctx := context.Background()
	scope := datastore.OwnerReferenceScope{Namespace: "acme", RepositoryID: "repo-1"}
	parentUID := "00000000-0000-0000-0000-000000000001"
	blocking, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Name: "parent", UID: parentUID, BlockOwnerDeletion: true,
	}})
	require.NoError(t, err)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000002", Namespace: scope.Namespace, RepositoryID: scope.RepositoryID, Name: "child",
		ResourceVersion: "1", CreationTimestamp: time.Now(), OwnerReferences: blocking,
	}))

	hasBlocking, err := owners.HasBlockingOwnerDependents(ctx, scope, parentUID)
	require.NoError(t, err)
	assert.True(t, hasBlocking, "blocking lookups must stop after the first child")

	for _, uid := range []string{
		"00000000-0000-0000-0000-000000000004",
		"00000000-0000-0000-0000-000000000003",
	} {
		nonBlocking, marshalErr := json.Marshal([]catalog.OwnerReference{{
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Name: "parent", UID: parentUID,
		}})
		require.NoError(t, marshalErr)
		require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
			UID: uid, Namespace: scope.Namespace, RepositoryID: scope.RepositoryID, Name: uid,
			ResourceVersion: "1", CreationTimestamp: time.Now(), OwnerReferences: nonBlocking,
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

	child, err := store.GetCategoryTaxonomyByName(ctx, scope.Namespace, "child")
	require.NoError(t, err)
	child.OwnerReferences = nil
	child.ResourceVersion = "2"
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, child))
	hasBlocking, err = owners.HasBlockingOwnerDependents(ctx, scope, parentUID)
	require.NoError(t, err)
	assert.False(t, hasBlocking, "updates must replace the old projection atomically")
}

func TestCategoryTaxonomyDeletionLifecycleUsesResourceVersionPreconditions(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	lifecycle, ok := store.(datastore.CategoryTaxonomyDeletionStore)
	require.True(t, ok)

	ctx := context.Background()
	category := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000010", Namespace: "acme", Name: "parent",
		ResourceVersion: "1", CreationTimestamp: time.Now(),
	}

	require.NoError(t, store.CreateCategoryTaxonomy(ctx, category))

	_, err = lifecycle.MarkCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, "stale", time.Now())
	require.ErrorIs(t, err, datastore.ErrConflict)
	terminating, err := lifecycle.MarkCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, "1", time.Now())
	require.NoError(t, err)
	require.NotNil(t, terminating.DeletionTimestamp)
	assert.Contains(t, terminating.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer)

	_, err = lifecycle.CompleteCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, "1")
	require.ErrorIs(t, err, datastore.ErrConflict)
	_, err = lifecycle.CompleteCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, terminating.ResourceVersion)
	require.NoError(t, err)
	_, err = store.GetCategoryTaxonomyByName(ctx, category.Namespace, category.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestOwnerReferenceProjectionCapsProductPagesAndIgnoresLegacyRecords(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	owners := store.(datastore.OwnerReferenceStore)
	ctx := context.Background()
	scope := datastore.OwnerReferenceScope{Namespace: "acme", RepositoryID: "repo-1"}
	parentUID := "00000000-0000-0000-0000-000000000100"

	// A pre-ownerReferences record from a rolling upgrade remains readable and
	// does not create a false blocking dependent.
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000101", Namespace: scope.Namespace,
		RepositoryID: scope.RepositoryID, Name: "legacy", ResourceVersion: "1",
		CreationTimestamp: time.Now(),
	}))
	blocking, err := owners.HasBlockingOwnerDependents(ctx, scope, parentUID)
	require.NoError(t, err)
	assert.False(t, blocking)

	references, err := json.Marshal([]catalog.OwnerReference{{
		Kind: "CategoryTaxonomy", Name: "parent", UID: parentUID,
	}})
	require.NoError(t, err)
	for i := 0; i < datastore.MaxOwnerDependentPageSize+1; i++ {
		uid := fmt.Sprintf("00000000-0000-0000-0000-%012d", 200+i)
		require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
			UID: uid, Namespace: scope.Namespace, RepositoryID: scope.RepositoryID,
			Name: uid, ResourceVersion: "1", CreationTimestamp: time.Now(),
			OwnerReferences: references,
		}))
	}
	page, err := owners.ListNonBlockingProductOwnerDependents(ctx, scope, parentUID, "", datastore.MaxOwnerDependentPageSize+100)
	require.NoError(t, err)
	assert.Len(t, page.Items, datastore.MaxOwnerDependentPageSize)
	assert.NotEmpty(t, page.NextCursor)
}
