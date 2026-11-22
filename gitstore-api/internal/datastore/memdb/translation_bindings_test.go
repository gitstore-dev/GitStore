// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newTranslationSourceFixture(t *testing.T) (*memdbDatastore, *datastore.Translation) {
	t.Helper()
	backend, err := New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	store := backend.(*memdbDatastore)
	namespaceUID := uuid.NewString()
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: namespaceUID, Name: "binding-history", Tier: datastore.NamespaceTierUser, CreationTimestamp: time.Now(),
	}))
	source := &datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		UID: uuid.NewString(), Name: "title-de", Namespace: "binding-history", NamespaceID: namespaceUID,
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now(),
		Revision: "main@sha1:source", SourceDigest: "digest", RawSource: []byte("accepted source"),
		Spec: json.RawMessage(`{"locale":"de-DE","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titel"}]}`),
	}
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	return store, source
}

func TestTranslationMissingBindingHistoryFailsClosed(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	before, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)

	txn := store.db.Txn(true)
	raw, err := txn.First("translation_binding", "id", source.UID)
	require.NoError(t, err)
	require.NoError(t, txn.Delete("translation_binding", raw))
	txn.Commit()

	candidate := *before
	candidate.ResourceVersion, candidate.Generation = "2", 2
	candidate.Spec = json.RawMessage(strings.Replace(string(before.Spec), "de-DE", "fr-FR", 1))
	require.ErrorIs(t, store.UpdateTranslation(t.Context(), &candidate, "1"), datastore.ErrRepairRequired)
	_, err = store.UpdateTranslationStatus(t.Context(), source.UID, "1",
		json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:source","conditions":[]}`))
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	require.ErrorIs(t, store.DeleteTranslation(t.Context(), source.UID, "1"), datastore.ErrRepairRequired)
	after, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after, "lost history cannot be silently reinitialized by a source or status transition")
	_, err = store.GetTranslationBinding(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}
