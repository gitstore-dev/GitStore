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
