// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTranslationOwnerProjectionFencesDelayedCreationRetargetAndDeletion(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	source.Finalizers = nil
	ownerA, ownerB := newID(), newID()
	refs := func(uid string) json.RawMessage {
		raw, err := json.Marshal([]catalog.OwnerReference{{
			Kind: "Product", APIVersion: "catalog.gitstore.dev/v1beta1", Name: "laptop", UID: uid, BlockOwnerDeletion: true,
		}})
		require.NoError(t, err)
		return raw
	}
	source.OwnerReferences = refs(ownerA)
	store, owners := first.(datastore.TranslationStore), second.(datastore.OwnerReferenceStore)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	scope := datastore.OwnerReferenceScope{Namespace: source.Namespace}
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
	}
	check(ownerA, "1", true)
	reassigned := *source
	reassigned.ResourceVersion, reassigned.OwnerReferences = "2", refs(ownerB)
	require.NoError(t, store.UpdateTranslation(t.Context(), &reassigned, "1"))
	require.NoError(t, scylla.ReplayTranslationOwnersForTest(t.Context(), first, "1", nil, source))
	check(ownerA, "", false)
	check(ownerB, "2", true)
	returned := reassigned
	returned.ResourceVersion, returned.OwnerReferences = "3", refs(ownerA)
	require.NoError(t, store.UpdateTranslation(t.Context(), &returned, "2"))
	require.NoError(t, scylla.ReplayTranslationOwnersForTest(t.Context(), second, "3", source, &reassigned))
	check(ownerA, "3", true)
	check(ownerB, "", false)
	raw := newRawSession(t)
	require.NoError(t, raw.Query(
		"DELETE FROM owner_reference_dependents WHERE namespace=? AND repository_id='' AND owner_uid=? AND block_owner_deletion=true AND dependent_kind='Translation' AND dependent_uid=?",
		source.Namespace, ownerA, source.UID,
	).Exec())
	require.NoError(t, second.(datastore.TranslationIndexStore).RecoverTranslation(t.Context(), source.UID))
	check(ownerA, "3", true)
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "3"))
	require.NoError(t, scylla.ReplayTranslationOwnersForTest(t.Context(), first, "1", nil, source))
	require.NoError(t, scylla.ReplayTranslationOwnersForTest(t.Context(), second, "5", &reassigned, &returned))
	check(ownerA, "", false)
	check(ownerB, "", false)
	var coordination string
	var retired bool
	require.NoError(t, raw.Query(
		"SELECT translation_coordination_version,translation_projection_retired FROM owner_reference_dependents WHERE namespace=? AND repository_id='' AND owner_uid=? AND block_owner_deletion=true AND dependent_kind='' AND dependent_uid=?",
		source.Namespace, ownerA, source.UID,
	).Scan(&coordination, &retired))
	require.Equal(t, "7", coordination)
	require.True(t, retired, "small retry fences survive final source compaction")
}

func TestTranslationOwnerProjectionRejectsCorruptRetryFences(t *testing.T) {
	first, _ := newTestStores(t)
	raw := newRawSession(t)
	for _, invalid := range []struct {
		name, version string
		retired       any
	}{
		{"missing-retirement", "2", nil},
		{"invalid-version", "broken", false},
		{"conflicting-retirement", "2", true},
		{"noncanonical-version", "02", false},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			source := newTranslationCreationSource(t, first)
			owner := newID()
			source.OwnerReferences = json.RawMessage(`[{"uid":"` + owner + `","blockOwnerDeletion":true}]`)
			require.NoError(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), source))
			require.NoError(t, raw.Query(
				"UPDATE owner_reference_dependents SET translation_coordination_version=?,translation_projection_retired=? WHERE namespace=? AND repository_id='' AND owner_uid=? AND block_owner_deletion=true AND dependent_kind='' AND dependent_uid=?",
				invalid.version, invalid.retired, source.Namespace, owner, source.UID,
			).Exec())
			require.ErrorIs(t, first.(datastore.TranslationIndexStore).RecoverTranslation(t.Context(), source.UID), datastore.ErrRepairRequired)
			current, err := first.(datastore.TranslationStore).GetTranslation(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, "1", current.ResourceVersion)
		})
	}
}

func TestTranslationRepositoryMembershipRetainsRetryFencesAndSkipsGenericRepair(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_translation_ownership"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	open := func() datastore.Datastore {
		store, err := scylla.New(cfg, zap.NewNop(), 0)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	}
	first, second := open(), open()
	source := newTranslationCreationSource(t, first)
	source.Finalizers = nil
	repository := newRepository()
	repository.Namespace, repository.NamespaceID = source.Namespace, source.Namespace
	require.NoError(t, first.CreateRepositoryInActiveNamespace(t.Context(), repository))
	source.RepositoryID = repository.UID
	store := first.(datastore.TranslationStore)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	check := func(want bool) {
		t.Helper()
		has, err := second.HasCatalogResources(t.Context(), repository.UID)
		require.NoError(t, err)
		require.Equal(t, want, has)
	}
	audit := func() {
		t.Helper()
		service, err := scylla.OpenProjectionRepairService(cfg)
		require.NoError(t, err)
		defer service.Close()
		plan, err := service.Audit(t.Context())
		require.NoError(t, err)
		for _, action := range plan.Actions {
			require.NotEqual(t, source.UID, action.UID, "generic repair must not bypass Translation's retained fences")
		}
	}
	check(true)
	audit()
	raw := newRawSession(t)
	for shard := int8(0); shard < 32; shard++ {
		require.NoError(t, raw.Query(
			"DELETE FROM "+cfg.Keyspace+".resources_by_repository WHERE repository_id=? AND shard=? AND kind='Translation' AND uid=?",
			mustTranslationUUID(t, repository.UID), shard, mustTranslationUUID(t, source.UID),
		).Exec())
	}
	check(false)
	require.NoError(t, second.(datastore.TranslationIndexStore).RecoverTranslation(t.Context(), source.UID))
	check(true)
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "1"))
	check(false)
	require.NoError(t, scylla.ReplayTranslationProjectionsForTest(t.Context(), first, "1", nil, source))
	check(false)
	audit()
}
