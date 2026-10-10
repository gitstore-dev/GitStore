// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemdb_NamespaceContractRoundTripAndConflict(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	ns := &datastore.Namespace{
		UID:               "00000000-0000-0000-0000-000000000111",
		Name:              "versioned-namespace",
		Title:             "Versioned Namespace",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: time.Now().UTC(),
		CreationActor:     "test",
		UpdateTimestamp:   time.Now().UTC(),
		UpdateActor:       "test",
		Spec:              json.RawMessage(`{"tier":"user"}`),
		Body:              "# Namespace body\n",
	}
	require.NoError(t, ds.CreateNamespace(ctx, ns))

	first, err := ds.GetNamespace(ctx, ns.UID)
	require.NoError(t, err)
	stale, err := ds.GetNamespace(ctx, ns.UID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.Generation)
	assert.Equal(t, "1", first.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(first.Status))
	assert.JSONEq(t, `{"tier":"user"}`, string(first.Spec))
	assert.Equal(t, "# Namespace body\n", first.Body)

	first.Title = "First writer"
	first.Body = "# Updated namespace body\n"
	datastore.AdvanceNamespaceSpecVersion(first)
	require.NoError(t, ds.UpdateNamespace(ctx, first, "1"))

	stale.Title = "Stale writer"
	datastore.AdvanceNamespaceSpecVersion(stale)
	require.ErrorIs(t, ds.UpdateNamespace(ctx, stale, "1"), datastore.ErrConflict)

	got, err := ds.GetNamespace(ctx, ns.UID)
	require.NoError(t, err)
	assert.Equal(t, "First writer", got.Title)
	assert.Equal(t, int64(2), got.Generation)
	assert.Equal(t, "2", got.ResourceVersion)
	assert.Equal(t, "# Updated namespace body\n", got.Body)
}

func TestMemdb_NamespaceFullEnvelopeAndBodyAreDeepCopied(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	originalDeletedAt := deletedAt
	namespace := &datastore.Namespace{
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Namespace",
		UID:               "00000000-0000-0000-0000-000000000112",
		Name:              "full-envelope",
		Generation:        3,
		ResourceVersion:   "7",
		Revision:          "main@sha1:abc",
		CreationTimestamp: deletedAt.Add(-time.Hour),
		CreationActor:     "creator",
		UpdateTimestamp:   deletedAt.Add(-time.Minute),
		UpdateActor:       "updater",
		Labels:            map[string]string{"tier": "gold"},
		Annotations:       map[string]string{"note": "original"},
		OwnerReferences:   json.RawMessage(`[{"uid":"owner"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		SourcePath:        "namespaces/full-envelope.md",
		GitCommitSHA:      "abc",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"title":"Full Envelope"}`),
		Body:              "# Namespace\n",
		Status:            json.RawMessage(`{"observedGeneration":3}`),
		Title:             "Full Envelope",
		Tier:              datastore.NamespaceTierOrganization,
	}
	require.NoError(t, ds.CreateNamespace(ctx, namespace))

	namespace.Labels["tier"] = "mutated"
	namespace.Annotations["note"] = "mutated"
	namespace.OwnerReferences[2] = 'X'
	namespace.Finalizers[0] = "mutated"
	namespace.Spec[2] = 'X'
	namespace.Status[2] = 'X'
	*namespace.DeletionTimestamp = deletedAt.Add(time.Hour)

	got, err := ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	assert.Equal(t, "gold", got.Labels["tier"])
	assert.Equal(t, "original", got.Annotations["note"])
	assert.JSONEq(t, `[{"uid":"owner"}]`, string(got.OwnerReferences))
	assert.Equal(t, []string{"gitstore.dev/test"}, got.Finalizers)
	assert.JSONEq(t, `{"title":"Full Envelope"}`, string(got.Spec))
	assert.JSONEq(t, `{"observedGeneration":3}`, string(got.Status))
	assert.Equal(t, originalDeletedAt, *got.DeletionTimestamp)
	assert.Equal(t, "# Namespace\n", got.Body)

	listed, err := ds.ListNamespaces(ctx, datastore.PageParams{First: 10})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	assert.Equal(t, "# Namespace\n", listed.Items[0].Body)
	assert.Equal(t, namespace.SourcePath, listed.Items[0].SourcePath)

	got.Labels["tier"] = "read-mutated"
	got.OwnerReferences[2] = 'Y'
	got.Finalizers[0] = "read-mutated"
	got.Spec[2] = 'Y'
	got.Status[2] = 'Y'
	*got.DeletionTimestamp = deletedAt.Add(2 * time.Hour)

	again, err := ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	assert.Equal(t, "gold", again.Labels["tier"])
	assert.JSONEq(t, `[{"uid":"owner"}]`, string(again.OwnerReferences))
	assert.Equal(t, []string{"gitstore.dev/test"}, again.Finalizers)
	assert.JSONEq(t, `{"title":"Full Envelope"}`, string(again.Spec))
	assert.JSONEq(t, `{"observedGeneration":3}`, string(again.Status))
	assert.Equal(t, originalDeletedAt, *again.DeletionTimestamp)
}

func TestMemdb_NamespaceDuplicateUIDAndName(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	first := &datastore.Namespace{
		UID:               "00000000-0000-0000-0000-000000000113",
		Name:              "unique-namespace",
		CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, ds.CreateNamespace(ctx, first))

	duplicateUID := *first
	duplicateUID.Name = "different-name"
	require.ErrorIs(t, ds.CreateNamespace(ctx, &duplicateUID), datastore.ErrAlreadyExists)

	duplicateName := *first
	duplicateName.UID = "00000000-0000-0000-0000-000000000114"
	require.ErrorIs(t, ds.CreateNamespace(ctx, &duplicateName), datastore.ErrAlreadyExists)
}

func TestMemdb_NamespaceDeletionAllowsOnlyEmptyProvisionedSystemRepository(t *testing.T) {
	ctx := context.Background()
	ds := newBackend(t)
	ns := &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000201", Name: "deletion"}
	require.NoError(t, ds.CreateNamespace(ctx, ns))
	repo := &datastore.Repository{UID: "00000000-0000-0000-0000-000000000202", RepositoryID: "00000000-0000-0000-0000-000000000202", Namespace: ns.Name, Name: "gitstore-system"}
	require.NoError(t, ds.CreateRepositoryInActiveNamespace(ctx, repo))
	blocked, err := datastore.NamespaceDeletionBlocked(ctx, ds, ns)
	require.NoError(t, err)
	require.False(t, blocked)
	exists, err := ds.HasRepositories(ctx, ns.Name)
	require.NoError(t, err)
	require.True(t, exists, "generic existence must include the system repository")

	product := &datastore.Product{UID: "00000000-0000-0000-0000-000000000203", Namespace: ns.Name, Name: "product", RepositoryID: repo.UID}
	require.NoError(t, ds.CreateProduct(ctx, product))
	blocked, err = datastore.NamespaceDeletionBlocked(ctx, ds, ns)
	require.NoError(t, err)
	require.True(t, blocked)
	now := time.Now().UTC()
	ns.DeletionTimestamp = &now
	previous := ns.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(ns)
	require.ErrorIs(t, ds.MarkNamespaceDeletion(ctx, ns, previous), datastore.ErrNamespaceNotEmpty)
	require.NoError(t, ds.DeleteProduct(ctx, product.UID))
	require.NoError(t, ds.MarkNamespaceDeletion(ctx, ns, previous))
	require.ErrorIs(t, ds.CreateProduct(ctx, product), datastore.ErrNamespaceNotActive)
	require.ErrorIs(t, ds.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion), datastore.ErrNamespaceNotEmpty)
	repo.DeletionTimestamp = &now
	repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
	previous = repo.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(repo)
	require.NoError(t, ds.UpdateRepository(ctx, repo, previous))
	require.NoError(t, ds.(datastore.RepositoryDeletionStore).CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion))
	require.ErrorIs(t, ds.CreateRepository(ctx, repo), datastore.ErrConflict)
	require.NoError(t, ds.DeleteNamespaceWithResourceVersion(ctx, ns.UID, ns.ResourceVersion))
	require.ErrorIs(t, ds.CreateProduct(ctx, product), datastore.ErrNotFound)
}

func TestMemdb_RepositoryCompletionGuardsAndMappingIdentity(t *testing.T) {
	ctx := context.Background()
	ds := newBackend(t)
	repo := &datastore.Repository{UID: "00000000-0000-0000-0000-000000000204", Namespace: "shop", Name: "catalog"}
	require.NoError(t, ds.CreateRepository(ctx, repo))
	completion := ds.(datastore.RepositoryDeletionStore)
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion), datastore.ErrConflict)
	now := time.Now().UTC()
	repo.DeletionTimestamp = &now
	repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer, "other/finalizer"}
	previous := repo.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(repo)
	require.NoError(t, ds.UpdateRepository(ctx, repo, previous))
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, previous), datastore.ErrConflict)
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion), datastore.ErrConflict)
	previous = repo.ResourceVersion
	repo.Finalizers = []string{datastore.RepositoryForegroundDeletionFinalizer}
	datastore.AdvanceRepositorySystemVersion(repo)
	require.NoError(t, ds.UpdateRepository(ctx, repo, previous))
	reversed := *repo
	reversed.DeletionTimestamp = nil
	datastore.AdvanceRepositorySystemVersion(&reversed)
	require.ErrorIs(t, ds.UpdateRepository(ctx, &reversed, repo.ResourceVersion), datastore.ErrConflict)
	require.NoError(t, ds.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: repo.Namespace, Name: repo.Name, RepositoryID: "00000000-0000-0000-0000-000000000205"}))
	require.NoError(t, completion.CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion))
	mapping, err := ds.LookupRepository(ctx, repo.Namespace, repo.Name)
	require.NoError(t, err)
	require.Equal(t, "00000000-0000-0000-0000-000000000205", mapping.RepositoryID)
}
func TestMemdb_DeletionIntentFencesRepositoryAndCatalogAdmission(t *testing.T) {
	ctx := context.Background()
	ds := newBackend(t)
	ns := &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000211", Name: "intent"}
	require.NoError(t, ds.CreateNamespace(ctx, ns))
	repository := &datastore.Repository{UID: "00000000-0000-0000-0000-000000000212", Namespace: ns.Name, Name: "catalog"}
	require.NoError(t, ds.CreateRepositoryInActiveNamespace(ctx, repository))
	ns.Status = json.RawMessage(`{"deletionIntent":{"uid":"intent"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
	previous := ns.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(ns)
	require.ErrorIs(t, ds.UpdateNamespace(ctx, ns, previous), datastore.ErrNamespaceNotEmpty)
	require.NoError(t, ds.DeleteRepository(ctx, repository.UID))
	require.NoError(t, ds.UpdateNamespace(ctx, ns, previous))
	require.ErrorIs(t, ds.CreateRepositoryInActiveNamespace(ctx, repository), datastore.ErrNamespaceNotActive)
	product := &datastore.Product{UID: "00000000-0000-0000-0000-000000000213", Namespace: ns.Name, Name: "product"}
	require.ErrorIs(t, ds.CreateProduct(ctx, product), datastore.ErrNamespaceNotActive)
	current, err := ds.GetNamespace(ctx, ns.UID)
	require.NoError(t, err)
	current.Status = json.RawMessage(`{"conditions":[]}`)
	previous = current.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, ds.UpdateNamespace(ctx, current, previous))
	require.True(t, datastore.HasDeletionIntent(current.Status))
}

func TestMemdb_TerminalRepositoryRejectsCatalogCreatesAndUpdates(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		create func(datastore.Datastore, string) error
		update func(datastore.Datastore, string) error
	}{
		{"product", func(ds datastore.Datastore, repo string) error {
			return ds.CreateProduct(ctx, &datastore.Product{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo})
		}, func(ds datastore.Datastore, repo string) error {
			return ds.UpdateProduct(ctx, &datastore.Product{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo})
		}},
		{"variant", func(ds datastore.Datastore, repo string) error {
			return ds.CreateProductVariant(ctx, &datastore.ProductVariant{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo, SKU: "sku", ProductRefName: "product"})
		}, func(ds datastore.Datastore, repo string) error {
			return ds.UpdateProductVariant(ctx, &datastore.ProductVariant{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo, SKU: "sku", ProductRefName: "product"})
		}},
		{"category", func(ds datastore.Datastore, repo string) error {
			return ds.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo})
		}, func(ds datastore.Datastore, repo string) error {
			return ds.UpdateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo})
		}},
		{"collection", func(ds datastore.Datastore, repo string) error {
			return ds.CreateCollection(ctx, &datastore.Collection{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo})
		}, func(ds datastore.Datastore, repo string) error {
			return ds.UpdateCollection(ctx, &datastore.Collection{UID: "00000000-0000-0000-0000-000000000221", Namespace: "terminal", Name: "catalog", RepositoryID: repo})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := newBackend(t)
			repo := &datastore.Repository{UID: "00000000-0000-0000-0000-000000000222", Namespace: "terminal", Name: "repo"}
			require.NoError(t, ds.CreateRepository(ctx, repo))
			require.NoError(t, tc.create(ds, repo.UID))
			require.NoError(t, ds.DeleteRepository(ctx, repo.UID))
			now := time.Now().UTC()
			repo.DeletionTimestamp = &now
			require.NoError(t, ds.CreateRepository(ctx, repo))
			require.ErrorIs(t, tc.create(ds, repo.UID), datastore.ErrConflict)
			require.ErrorIs(t, tc.update(ds, repo.UID), datastore.ErrConflict)
			require.ErrorIs(t, ds.(datastore.RepositoryDeletionStore).CompleteRepositoryDeletion(ctx, repo.UID, repo.ResourceVersion), datastore.ErrConflict)
		})
	}
}

func TestMemdb_NamespaceRepositoryLifecycleCoordination(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               "00000000-0000-0000-0000-000000000115",
		Name:              "repository-lifecycle",
		CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, ds.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{
		UID:               "00000000-0000-0000-0000-000000000116",
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC(),
	}

	require.NoError(t, ds.CreateRepositoryInActiveNamespace(ctx, repository))
	current, err := ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	deletedAt := time.Now().UTC()
	current.DeletionTimestamp = &deletedAt
	expectedResourceVersion := current.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(current)
	require.ErrorIs(t, ds.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrNamespaceNotEmpty)

	require.NoError(t, ds.DeleteRepository(ctx, repository.UID))
	current, err = ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion = current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, ds.MarkNamespaceDeletion(ctx, current, expectedResourceVersion))

	lateRepository := *repository
	lateRepository.UID = "00000000-0000-0000-0000-000000000117"
	lateRepository.Name = "late"
	require.ErrorIs(t, ds.CreateRepositoryInActiveNamespace(ctx, &lateRepository), datastore.ErrNamespaceNotActive)
}
