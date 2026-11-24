// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestTranslationTargetFenceIntentSurvivesReplacementStore(t *testing.T) {
	first, second := newTestStores(t)
	a, b := first.(datastore.TranslationTargetWriteFenceStore), second.(datastore.TranslationTargetWriteFenceStore)
	target := datastore.TranslationTargetKey{NamespaceUID: newID(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "replacement-reader"}
	intent := datastore.TranslationTargetWriteIntent{
		ID: newID(), NextUID: newID(), SourceDigest: "material", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	pending, err := a.BeginTranslationTargetWrite(t.Context(), target, intent, "")
	require.NoError(t, err)
	persisted, err := b.GetTranslationTargetFence(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, pending, persisted)
	replayed, err := b.BeginTranslationTargetWrite(t.Context(), target, intent, "")
	require.NoError(t, err)
	require.Equal(t, pending, replayed)
	replacement := newTestStore(t).(datastore.TranslationTargetWriteFenceStore)
	evidence := &catalog.TranslationTargetEvidence{
		APIVersion: target.APIVersion, Kind: target.Kind, Namespace: "content", Name: target.Name,
		UID: intent.NextUID, Generation: 1, ResourceVersion: "1", ContentDigest: intent.SourceDigest,
	}
	require.NoError(t, replacement.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, evidence))
	stable, err := b.GetTranslationTargetFence(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, stable.Phase)
	require.Equal(t, evidence, stable.Evidence)
	require.Equal(t, intent.CreationTimestamp, stable.CreationTimestamp)
	require.NoError(t, a.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, evidence))
}

func TestTranslationTargetFenceCorruptionFailsClosed(t *testing.T) {
	backend := newTestStore(t).(datastore.TranslationTargetWriteFenceStore)
	raw := newRawSession(t)
	for _, corrupt := range []struct{ name, assignment string }{
		{"wrong-logical-name", "target_name='another-name'"},
		{"unknown-phase", "phase='Stable'"},
		{"missing-intent", "write_intent=null"},
		{"malformed-intent", "write_intent='{}'"},
		{"invalid-token", "fence_token='reused'"},
		{"malformed-evidence", "evidence='{}'"},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			target := datastore.TranslationTargetKey{NamespaceUID: newID(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "corrupt"}
			_, err := backend.BeginTranslationTargetWrite(t.Context(), target,
				datastore.TranslationTargetWriteIntent{ID: newID(), NextUID: newID(), SourceDigest: "material"}, "")
			require.NoError(t, err)
			key, err := datastore.TranslationTargetFenceID(target)
			require.NoError(t, err)
			require.NoError(t, raw.Query(
				"UPDATE translation_target_fences_by_name SET "+corrupt.assignment+" WHERE namespace_uid=? AND api_version=? AND kind=? AND name=?",
				mustTranslationUUID(t, target.NamespaceUID), target.APIVersion, target.Kind, key,
			).Exec())
			_, err = backend.GetTranslationTargetFence(t.Context(), target)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
		})
	}
}
