// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunBackfillsOwnerReferencesAndIsResumable(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{
		UID: "00000000-0000-0000-0000-000000000001", Name: "acme", ResourceVersion: "1", CreationTimestamp: now,
	}))
	parent := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000002", Namespace: "acme", RepositoryID: "repo-1",
		Name: "parent", ResourceVersion: "1", CreationTimestamp: now,
	}
	child := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000003", Namespace: "acme", RepositoryID: "repo-1",
		Name: "child", ParentName: "parent", ResourceVersion: "1", CreationTimestamp: now.Add(time.Nanosecond),
	}
	product := &datastore.Product{
		UID: "00000000-0000-0000-0000-000000000004", Namespace: "acme", RepositoryID: "repo-1",
		Name: "product", ResourceVersion: "1", CreationTimestamp: now, Spec: []byte(`{"categoryRef":{"name":"parent"}}`),
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, parent))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, child))
	require.NoError(t, store.CreateProduct(ctx, product))

	first, err := run(ctx, store, "", false)
	require.NoError(t, err)
	assert.Equal(t, 1, first.CategoriesUpdated)
	assert.Equal(t, 1, first.ProductsUpdated)
	assert.NotEmpty(t, first.ResumeAfter)

	owners := store.(datastore.OwnerReferenceStore)
	blocked, err := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{
		Namespace: "acme", RepositoryID: "repo-1",
	}, parent.UID)
	require.NoError(t, err)
	assert.True(t, blocked)
	page, err := owners.ListNonBlockingProductOwnerDependents(ctx, datastore.OwnerReferenceScope{
		Namespace: "acme", RepositoryID: "repo-1",
	}, parent.UID, "", 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	second, err := run(ctx, store, "", false)
	require.NoError(t, err)
	assert.Zero(t, second.CategoriesUpdated)
	assert.Zero(t, second.ProductsUpdated)
}

func TestRunDryRunLeavesSourceRecordsUntouched(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{
		UID: "00000000-0000-0000-0000-000000000010", Name: "acme", ResourceVersion: "1", CreationTimestamp: now,
	}))
	parent := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000011", Namespace: "acme", RepositoryID: "repo-1",
		Name: "parent", ResourceVersion: "1", CreationTimestamp: now,
	}
	child := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000012", Namespace: "acme", RepositoryID: "repo-1",
		Name: "child", ParentName: "parent", ResourceVersion: "1", CreationTimestamp: now.Add(time.Nanosecond),
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, parent))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, child))

	result, err := run(ctx, store, "", true)
	require.NoError(t, err)
	assert.True(t, result.DryRun)
	assert.Equal(t, 1, result.CategoriesUpdated)
	current, err := store.GetCategoryTaxonomy(ctx, child.UID)
	require.NoError(t, err)
	assert.Empty(t, current.OwnerReferences)
}
