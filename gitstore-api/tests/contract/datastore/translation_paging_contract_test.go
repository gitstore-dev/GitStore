// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func runTranslationDependentPagingContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store := translationIndexes(t, backend)
	for _, kind := range []datastore.TranslationIndexKind{datastore.TranslationTargetNameIndex, datastore.TranslationTargetUIDIndex} {
		t.Run(string(kind), func(t *testing.T) {
			target := datastore.TranslationTargetKey{
				NamespaceUID: newID(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "Laptop",
			}
			targetUID := ""
			index := datastore.TranslationIndexKey{Kind: kind, NamespaceUID: target.NamespaceUID, Shard: 63}
			if kind == datastore.TranslationTargetNameIndex {
				index.Target = target
			} else {
				targetUID = newID()
				index.TargetUID = targetUID
			}
			expected := map[string]string{}
			for i := range 4 {
				uid := newID()
				for {
					shard, err := datastore.TranslationIndexShard(uid)
					require.NoError(t, err)
					if shard == index.Shard {
						break
					}
					uid = newID()
				}
				entry := datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1",
					ReferenceEpoch: newID(), CreationTimestamp: time.Now().UTC()}
				bytes := int64(1024)
				if i == 0 {
					bytes = datastore.MaxTranslationSegmentBytes
				}
				record, err := store.ReserveTranslationIndex(t.Context(), index, entry, newID(), bytes)
				require.NoError(t, err)
				require.NoError(t, store.CommitTranslationIndex(t.Context(), record))
				if i == 0 {
					require.NoError(t, store.RemoveTranslationIndex(t.Context(), record))
					entry.SourceVersion = "2"
					record, err = store.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
					require.NoError(t, err)
					require.EqualValues(t, 1, record.Index.Segment)
					require.NoError(t, store.CommitTranslationIndex(t.Context(), record))
				}
				expected[uid] = entry.SourceVersion
				other := index
				if kind == datastore.TranslationTargetNameIndex {
					other.Target.Name = "laptop"
				} else {
					other.TargetUID = newID()
				}
				excluded, err := store.ReserveTranslationIndex(t.Context(), other, entry, newID(), 1024)
				require.NoError(t, err)
				require.NoError(t, store.CommitTranslationIndex(t.Context(), excluded))
			}
			seen, continuations := map[string]string{}, map[string]bool{}
			cursor, empty := "", false
			for turn := range 20 {
				page, err := store.ListTranslationDependents(t.Context(), target, targetUID, cursor, 2)
				require.NoError(t, err)
				require.LessOrEqual(t, len(page.Items), 2)
				require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
				for _, item := range page.Items {
					_, duplicate := seen[item.TranslationUID]
					require.False(t, duplicate)
					seen[item.TranslationUID] = item.SourceVersion
				}
				if len(page.Items) == 0 && page.Next != "" {
					empty = true
				}
				if page.Next == "" {
					break
				}
				require.False(t, continuations[page.Next], "empty turns must advance rather than restart")
				continuations[page.Next] = true
				if turn == 0 {
					foreign := target
					foreign.NamespaceUID = newID()
					_, err := store.ListTranslationDependents(t.Context(), foreign, targetUID, page.Next, 2)
					require.ErrorIs(t, err, datastore.ErrInvalidArgument)
				}
				cursor = page.Next
				require.Less(t, turn, 19, "bounded continuations must finish the sparse traversal")
			}
			require.True(t, empty, "sparse shards need resumable empty pages")
			require.Equal(t, expected, seen, "retired cells and different exact target identities cannot leak into pages")
			_, err := store.ListTranslationDependents(t.Context(), target, targetUID, "invalid-cursor", 2)
			require.ErrorIs(t, err, datastore.ErrInvalidArgument)
			_, err = store.ListTranslationDependents(t.Context(), target, targetUID, "", -1)
			require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		})
	}
}
