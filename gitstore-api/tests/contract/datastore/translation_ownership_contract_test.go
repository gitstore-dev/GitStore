// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func runTranslationOwnershipContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	t.Run("NamespaceScoped", func(t *testing.T) {
		store, source := newTranslationSource(t, backend)
		owners := backend.(datastore.OwnerReferenceStore)
		scope := datastore.OwnerReferenceScope{Namespace: source.Namespace}
		owner := source.NamespaceID
		source.OwnerReferences = json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"` +
			source.Namespace + `","uid":"` + owner + `","blockOwnerDeletion":true}]`)
		require.Empty(t, source.RepositoryID)
		require.NoError(t, store.CreateTranslation(t.Context(), source))
		check := func(want bool) {
			t.Helper()
			has, err := owners.HasBlockingOwnerDependents(t.Context(), scope, owner)
			require.NoError(t, err)
			require.Equal(t, want, has)
			page, err := owners.ListBlockingOwnerDependents(t.Context(), scope, owner, "", 1)
			require.NoError(t, err)
			if want {
				require.Len(t, page.Items, 1)
				require.Equal(t, source.UID, page.Items[0].DependentUID)
				require.Equal(t, "Translation", page.Items[0].DependentKind)
			} else {
				require.Empty(t, page.Items)
			}
		}
		check(true)
		for _, other := range []datastore.OwnerReferenceScope{
			{Namespace: source.Namespace, RepositoryID: newID()},
			{Namespace: "another-" + source.Namespace},
		} {
			has, err := owners.HasBlockingOwnerDependents(t.Context(), other, owner)
			require.NoError(t, err)
			require.False(t, has, "empty repository scope is an exact scope, not a wildcard")
		}
		original := source.OwnerReferences
		source.ResourceVersion, source.OwnerReferences = "2", json.RawMessage(`[]`)
		require.NoError(t, store.UpdateTranslation(t.Context(), source, "1"))
		check(false)
		source.ResourceVersion, source.OwnerReferences = "3", original
		require.NoError(t, store.UpdateTranslation(t.Context(), source, "2"))
		check(true)
		require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "3"))
		check(false)
	})
	store, source := newTranslationSource(t, backend)
	repositoryUID := newID()
	require.NoError(t, backend.CreateRepositoryInActiveNamespace(t.Context(), &datastore.Repository{
		UID: repositoryUID, ID: repositoryUID, RepositoryID: repositoryUID,
		Name: "translation-sources", Namespace: source.Namespace, NamespaceID: source.Namespace,
		CreationTimestamp: source.CreationTimestamp,
	}))
	source.RepositoryID = repositoryUID
	owners := backend.(datastore.OwnerReferenceStore)
	scope := datastore.OwnerReferenceScope{Namespace: source.Namespace, RepositoryID: repositoryUID}
	firstOwner, secondOwner := newID(), newID()
	references := func(uid string) json.RawMessage {
		raw, err := json.Marshal([]catalog.OwnerReference{{
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "laptop",
			UID: uid, BlockOwnerDeletion: true,
		}})
		require.NoError(t, err)
		return raw
	}
	source.OwnerReferences = references(firstOwner)
	has, err := backend.HasCatalogResources(t.Context(), repositoryUID)
	require.NoError(t, err)
	require.False(t, has)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	check := func(owner, version string, want bool) {
		t.Helper()
		has, err := owners.HasBlockingOwnerDependents(t.Context(), scope, owner)
		require.NoError(t, err)
		require.Equal(t, want, has)
		page, err := owners.ListBlockingOwnerDependents(t.Context(), scope, owner, "", 1)
		require.NoError(t, err)
		if !want {
			require.Empty(t, page.Items)
			return
		}
		require.Equal(t, []datastore.OwnerDependent{{
			DependentUID: source.UID, DependentKind: "Translation", Name: source.Name, ResourceVersion: version,
		}}, page.Items)
		require.Empty(t, page.NextCursor)
	}
	has, err = backend.HasCatalogResources(t.Context(), repositoryUID)
	require.NoError(t, err)
	require.True(t, has, "a repository cannot finalize while its accepted Translation exists")
	check(firstOwner, "1", true)
	reassigned := *source
	reassigned.ResourceVersion, reassigned.OwnerReferences = "2", references(secondOwner)
	require.NoError(t, store.UpdateTranslation(t.Context(), &reassigned, "1"))
	check(firstOwner, "", false)
	check(secondOwner, "2", true)
	stale := *source
	stale.ResourceVersion = "3"
	require.ErrorIs(t, store.UpdateTranslation(t.Context(), &stale, "1"), datastore.ErrConflict)
	check(firstOwner, "", false)
	check(secondOwner, "2", true)
	returned := reassigned
	returned.ResourceVersion, returned.OwnerReferences = "3", references(firstOwner)
	require.NoError(t, store.UpdateTranslation(t.Context(), &returned, "2"))
	check(firstOwner, "3", true)
	check(secondOwner, "", false)
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "3"))
	check(firstOwner, "", false)
	check(secondOwner, "", false)
	has, err = backend.HasCatalogResources(t.Context(), repositoryUID)
	require.NoError(t, err)
	require.False(t, has, "retired identities cannot become permanent repository blockers")
}
