// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOwnerReferenceProjection_ScyllaLimitOneAndKeysetRecovery(t *testing.T) {
	store := newTestStore(t)
	markOwnerReferenceBackfillCompleteForTest(t, store)
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

func TestRepositoryDeletionRacesWithDependentCreateAndReparent(t *testing.T) {
	first, second := newTestStores(t)
	ctx := t.Context()
	markRepositoryForDeletion := func(store datastore.Datastore, repo *datastore.Repository) error {
		current, err := store.GetRepository(ctx, repo.UID)
		if err != nil {
			return err
		}
		at := time.Now().UTC()
		current.DeletionTimestamp = &at
		current.Finalizers = append(current.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
		expected := current.ResourceVersion
		datastore.AdvanceRepositorySystemVersion(current)
		return store.UpdateRepository(ctx, current, expected)
	}

	t.Run("dependent create", func(t *testing.T) {
		namespace := "owner-race-create-" + newID()[:8]
		now := time.Now().UTC().Truncate(time.Millisecond)
		require.NoError(t, first.CreateNamespace(ctx, &datastore.Namespace{
			UID: newID(), Name: namespace, CreationTimestamp: now,
		}))
		repositoryA := &datastore.Repository{UID: newID(), Namespace: namespace, Name: "catalog-a", ResourceVersion: "1", CreationTimestamp: now}
		require.NoError(t, first.CreateRepositoryInActiveNamespace(ctx, repositoryA))
		start := make(chan struct{})
		var wg sync.WaitGroup
		var deletionErr, createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			deletionErr = markRepositoryForDeletion(first, repositoryA)
		}()
		go func() {
			defer wg.Done()
			<-start
			references, err := json.Marshal([]catalog.OwnerReference{{
				APIVersion: "gitstore.dev/v1beta1", Kind: "Repository",
				Name: repositoryA.Name, UID: repositoryA.UID, BlockOwnerDeletion: true,
				RepositoryID: repositoryA.UID,
			}})
			if err != nil {
				createErr = err
				return
			}
			createErr = second.CreateProduct(ctx, &datastore.Product{
				UID: newID(), Namespace: namespace, Name: "created-product", RepositoryID: repositoryA.UID,
				ResourceVersion: "1", CreationTimestamp: time.Now().UTC(), OwnerReferences: references,
			})
		}()
		close(start)
		wg.Wait()

		if deletionErr == nil {
			require.ErrorIs(t, createErr, datastore.ErrConflict)
		} else {
			require.NoError(t, createErr, "a committed dependent must prevent owner deletion")
			require.ErrorIs(t, deletionErr, datastore.ErrConflict)
		}
		current, err := first.GetRepository(ctx, repositoryA.UID)
		require.NoError(t, err)
		product, productErr := first.GetProductByName(ctx, namespace, "created-product")
		if current.DeletionTimestamp != nil {
			require.ErrorIs(t, productErr, datastore.ErrNotFound)
		} else {
			require.NoError(t, productErr)
			require.Equal(t, repositoryA.UID, product.RepositoryID)
		}
	})

	t.Run("dependent reparent", func(t *testing.T) {
		namespace := "owner-race-reparent-" + newID()[:8]
		now := time.Now().UTC().Truncate(time.Millisecond)
		require.NoError(t, first.CreateNamespace(ctx, &datastore.Namespace{
			UID: newID(), Name: namespace, CreationTimestamp: now,
		}))
		repositoryA := &datastore.Repository{UID: newID(), Namespace: namespace, Name: "catalog-a", ResourceVersion: "1", CreationTimestamp: now}
		repositoryB := &datastore.Repository{UID: newID(), Namespace: namespace, Name: "catalog-b", ResourceVersion: "1", CreationTimestamp: now}
		require.NoError(t, first.CreateRepositoryInActiveNamespace(ctx, repositoryA))
		require.NoError(t, first.CreateRepositoryInActiveNamespace(ctx, repositoryB))
		product := &datastore.Product{
			UID: newID(), Namespace: namespace, Name: "reparented-product", RepositoryID: repositoryA.UID,
			ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		}
		require.NoError(t, first.CreateProduct(ctx, product))
		start := make(chan struct{})
		var wg sync.WaitGroup
		var deletionErr, reparentErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			deletionErr = markRepositoryForDeletion(first, repositoryA)
		}()
		go func() {
			defer wg.Done()
			<-start
			current, err := second.GetProduct(ctx, product.UID)
			if err != nil {
				reparentErr = err
				return
			}
			current.RepositoryID = repositoryB.UID
			expected := current.ResourceVersion
			datastore.AdvanceProductSystemVersion(current)
			reparentErr = second.UpdateProduct(ctx, current, expected)
		}()
		close(start)
		wg.Wait()

		currentRepo, err := first.GetRepository(ctx, repositoryA.UID)
		require.NoError(t, err)
		currentProduct, err := first.GetProduct(ctx, product.UID)
		require.NoError(t, err)
		if currentRepo.DeletionTimestamp != nil {
			require.NotEqual(t, repositoryA.UID, currentProduct.RepositoryID,
				"an owner cannot enter deletion while a dependent still points at it")
		}
		if deletionErr != nil {
			require.True(t, errors.Is(deletionErr, datastore.ErrConflict) || errors.Is(reparentErr, datastore.ErrConflict),
				"race must resolve through a deletion blocker or rejected reparent: delete=%v reparent=%v", deletionErr, reparentErr)
		}
	})
}

func TestOwnerReferenceBackfillProgress_SurvivesOperatorReplacement(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace = "gitstore_owner_backfill_" + newID()[:8]
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })

	first, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	second, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, first.Close())
		require.NoError(t, second.Close())
	}()
	ctx := t.Context()
	start := time.Now().UTC().Truncate(time.Millisecond)
	firstStore := first.(datastore.OwnerReferenceBackfillStore)
	secondStore := second.(datastore.OwnerReferenceBackfillStore)
	firstLease, acquired, err := firstStore.AcquireOwnerReferenceBackfillLease(ctx, "operator-a", start, 20*time.Millisecond)
	require.NoError(t, err)
	require.True(t, acquired)

	progress := datastore.OwnerReferenceBackfillProgress{
		Kinds: map[string]datastore.OwnerReferenceBackfillKindProgress{
			"Product": {Cursor: "stable-page-1", Scanned: 10, Repaired: 5},
		},
		UpdatedAt: start,
	}
	require.NoError(t, firstStore.SaveOwnerReferenceBackfillProgress(ctx, firstLease, progress))

	secondLease, acquired, err := secondStore.AcquireOwnerReferenceBackfillLease(ctx, "operator-b", start.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Greater(t, secondLease.FencingToken, firstLease.FencingToken)
	require.ErrorIs(t, firstStore.SaveOwnerReferenceBackfillProgress(ctx, firstLease, progress), datastore.ErrStaleBackfillLease)

	resumed, err := secondStore.LoadOwnerReferenceBackfillProgress(ctx)
	require.NoError(t, err)
	require.Equal(t, "stable-page-1", resumed.Kinds["Product"].Cursor)
	for _, kind := range datastore.OwnerReferenceBackfillKinds() {
		if _, ok := resumed.Kinds[kind]; !ok {
			resumed.Kinds[kind] = datastore.OwnerReferenceBackfillKindProgress{Complete: true}
		}
	}
	resumed.Kinds["Product"] = datastore.OwnerReferenceBackfillKindProgress{
		Cursor: "complete", Scanned: 10, Repaired: 5, Complete: true,
	}
	resumed.Completed = true
	resumed.UpdatedAt = start.Add(2 * time.Second)
	require.NoError(t, secondStore.SaveOwnerReferenceBackfillProgress(ctx, secondLease, resumed))

	require.NoError(t, first.Close())
	require.NoError(t, second.Close())
	restarted, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.Close()) })
	persisted, err := restarted.(datastore.OwnerReferenceBackfillStore).LoadOwnerReferenceBackfillProgress(ctx)
	require.NoError(t, err)
	require.True(t, persisted.Completed)
	require.Equal(t, "complete", persisted.Kinds["Product"].Cursor)
}

func TestOwnerReferenceIndexFailsClosedUntilBackfillIsVerified(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace = "gitstore_owner_gate_" + newID()[:8]
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	owners := store.(datastore.OwnerReferenceStore)
	backfill := store.(datastore.OwnerReferenceBackfillStore)
	ctx := t.Context()
	scope := datastore.OwnerReferenceScope{Namespace: "gate-test", RepositoryID: newID()}

	_, err = owners.HasBlockingOwnerDependents(ctx, scope, newID())
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady, "missing progress must not look like an empty index")

	lease, acquired, err := backfill.AcquireOwnerReferenceBackfillLease(ctx, "gate-test", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, backfill.SaveOwnerReferenceBackfillProgress(ctx, lease,
		datastore.OwnerReferenceBackfillProgress{Kinds: map[string]datastore.OwnerReferenceBackfillKindProgress{}}))
	_, err = owners.ListBlockingOwnerDependents(ctx, scope, newID(), "", 1)
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady, "incomplete progress must not permit indexed deletion")

	rawStore := newRawSessionForKeyspace(t, cfg.Keyspace)
	require.NoError(t, rawStore.Query(
		"UPDATE owner_reference_backfill_by_job SET progress_json=? WHERE job_id=?",
	).WithContext(ctx).Bind("{invalid", "owner-references").Exec())
	_, err = owners.ListNonBlockingProductOwnerDependents(ctx, scope, newID(), "", 1)
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady, "unreadable progress must fail closed")

	complete := datastore.OwnerReferenceBackfillProgress{
		Kinds:     make(map[string]datastore.OwnerReferenceBackfillKindProgress),
		Completed: true, UpdatedAt: time.Now().UTC(),
	}
	for _, kind := range datastore.OwnerReferenceBackfillKinds() {
		complete.Kinds[kind] = datastore.OwnerReferenceBackfillKindProgress{Complete: true}
	}
	require.NoError(t, backfill.SaveOwnerReferenceBackfillProgress(ctx, lease, complete))
	blocked, err := owners.HasBlockingOwnerDependents(ctx, scope, newID())
	require.NoError(t, err)
	require.False(t, blocked)
	require.NoError(t, backfill.ReleaseOwnerReferenceBackfillLease(ctx, lease))
}

func TestCollectionOwnerReferenceProjectionCreateUpdateAndDelete(t *testing.T) {
	store := newTestStore(t)
	markOwnerReferenceBackfillCompleteForTest(t, store)
	owners := store.(datastore.OwnerReferenceStore)
	ctx := t.Context()
	namespace, repositoryID := "collection-owner-refs-"+newID()[:8], newID()
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{
		UID: newID(), Name: namespace, CreationTimestamp: now,
	}))
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, &datastore.Repository{
		UID: repositoryID, Namespace: namespace, Name: "catalog", CreationTimestamp: now,
	}))
	ownerUID := newID()
	scope := datastore.OwnerReferenceScope{Namespace: namespace, RepositoryID: repositoryID}
	references, err := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository",
		Name: "catalog", UID: ownerUID, BlockOwnerDeletion: true, RepositoryID: repositoryID,
	}})
	require.NoError(t, err)
	collection := &datastore.Collection{
		UID: newID(), Namespace: namespace, RepositoryID: repositoryID, Name: "sale",
		ResourceVersion: "1", CreationTimestamp: now, OwnerReferences: references,
	}
	require.NoError(t, store.CreateCollection(ctx, collection))
	blocked, err := owners.HasBlockingOwnerDependents(ctx, scope, ownerUID)
	require.NoError(t, err)
	assert.True(t, blocked, "Collection create must publish its owner-reference projection")

	collection.ResourceVersion = "2"
	collection.OwnerReferences = nil
	require.NoError(t, store.UpdateCollection(ctx, collection, "1"))
	blocked, err = owners.HasBlockingOwnerDependents(ctx, scope, ownerUID)
	require.NoError(t, err)
	assert.False(t, blocked, "Collection update must remove obsolete owner projections")

	collection.ResourceVersion = "3"
	collection.OwnerReferences = references
	require.NoError(t, store.UpdateCollection(ctx, collection, "2"))
	require.NoError(t, store.DeleteCollectionWithResourceVersion(ctx, collection.UID, "3"))
	blocked, err = owners.HasBlockingOwnerDependents(ctx, scope, ownerUID)
	require.NoError(t, err)
	assert.False(t, blocked, "Collection delete must remove owner projections")
}

func TestCategoryTaxonomyCompletion_ScyllaFencesSameNameReplacement(t *testing.T) {
	store := newTestStore(t)
	markOwnerReferenceBackfillCompleteForTest(t, store)
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
