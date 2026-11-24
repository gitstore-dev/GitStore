// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type translationDependentStore interface {
	translationPublicationStore
	ListTranslationDependents(context.Context, datastore.TranslationTargetKey, string, string, int) (datastore.TranslationDependentPage, error)
}

func TestTranslationDependentCursorResumesAcrossStoresWithBoundedNameKey(t *testing.T) {
	first, second := newTestStores(t)
	a, ok := first.(translationDependentStore)
	require.True(t, ok)
	b, ok := second.(translationDependentStore)
	require.True(t, ok)
	uid := uuid.NewString()
	for {
		shard, err := datastore.TranslationIndexShard(uid)
		require.NoError(t, err)
		if shard == 63 {
			break
		}
		uid = uuid.NewString()
	}
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationTargetNameIndex, NamespaceUID: uuid.NewString(), Shard: 63}
	index.Target = datastore.TranslationTargetKey{
		NamespaceUID: index.NamespaceUID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		Name: "Laptop-" + strings.Repeat("x", 70_000),
	}
	entry := datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "opaque-source",
		ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC()}
	record, err := a.ReserveTranslationIndex(t.Context(), index, entry, uuid.NewString(), datastore.TranslationIndexPayloadBytes(index, entry))
	require.NoError(t, err)
	require.NoError(t, a.CommitTranslationIndex(t.Context(), record))
	page, err := a.ListTranslationDependents(t.Context(), index.Target, "", "", 1)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.NotEmpty(t, page.Next)
	replacement, ok := newTestStore(t).(translationDependentStore)
	require.True(t, ok)
	var seen []datastore.TranslationIndexEntry
	for turn := range 12 {
		reader := b
		if turn%2 == 0 {
			reader = replacement
		}
		page, err = reader.ListTranslationDependents(t.Context(), index.Target, "", page.Next, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
		seen = append(seen, page.Items...)
		if page.Next == "" {
			break
		}
		require.Less(t, turn, 11)
	}
	require.Equal(t, []datastore.TranslationIndexEntry{record.Entry}, seen, "empty continuations must not depend on process-local state")
}
