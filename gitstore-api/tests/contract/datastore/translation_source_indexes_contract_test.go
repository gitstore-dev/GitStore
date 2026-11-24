// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func runTranslationSourceIndexesContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store, source := newTranslationSource(t, backend)
	indexes := translationIndexes(t, backend)
	list := func(target datastore.TranslationTargetKey, uid string) []datastore.TranslationIndexEntry {
		var entries []datastore.TranslationIndexEntry
		cursor := ""
		for range 32 {
			page, err := indexes.ListTranslationDependents(t.Context(), target, uid, cursor, 256)
			require.NoError(t, err)
			entries = append(entries, page.Items...)
			if page.Next == "" {
				return entries
			}
			require.NotEqual(t, cursor, page.Next)
			cursor = page.Next
		}
		t.Fatal("dependent traversal failed to finish")
		return nil
	}
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	target := datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "laptop",
	}
	entries := list(target, "")
	require.Len(t, entries, 1)
	require.Equal(t, source.UID, entries[0].TranslationUID)
	bindings := translationBindings(t, backend)
	binding, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	binding.ResourceVersion, binding.Epochs[0].BoundUID = "2", newID()
	require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), binding, "1"))
	require.Len(t, list(target, binding.Epochs[0].BoundUID), 1)
	next := *source
	next.ResourceVersion, next.Generation = "2", 2
	next.Spec = json.RawMessage(strings.Replace(string(source.Spec), `"name":"laptop"`, `"name":"replacement"`, 1))
	require.NoError(t, store.UpdateTranslation(t.Context(), &next, "1"))
	require.Empty(t, list(target, ""))
	require.Empty(t, list(target, binding.Epochs[0].BoundUID))
	target.Name = "replacement"
	entries = list(target, "")
	require.Len(t, entries, 1)
	require.Equal(t, "2", entries[0].SourceVersion)
	require.NotEqual(t, binding.CurrentEpoch, entries[0].ReferenceEpoch)
	observed, err := store.UpdateTranslationStatus(t.Context(), source.UID, "2",
		json.RawMessage(`{"observedGeneration":2,"lastAppliedRevision":"main@sha1:accepted-source","conditions":[]}`))
	require.NoError(t, err)
	entries = list(target, "")
	require.Len(t, entries, 1)
	require.Equal(t, observed.ResourceVersion, entries[0].SourceVersion)
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, observed.ResourceVersion))
	require.Empty(t, list(target, ""))
}
