// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type translationNamespaceStore interface {
	translationDeletionStore
	ListTranslations(context.Context, string, datastore.PageParams) (*datastore.TranslationPage, error)
}

func TestTranslationNamespaceSourcePages(t *testing.T) {
	first, second := newTestStores(t)
	a, ok := first.(translationNamespaceStore)
	require.True(t, ok, "namespace source lists must be bounded and resumable")
	b := second.(translationNamespaceStore)
	prototype := newTranslationCreationSource(t, first)
	prototype.Finalizers = nil
	var sources []*datastore.Translation
	for i := range 12 {
		source := *prototype
		source.UID, source.Name = uuid.NewString(), fmt.Sprintf("translation-%02d", i)
		source.CreationTimestamp = prototype.CreationTimestamp.Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, a.CreateTranslation(t.Context(), &source))
		sources = append(sources, &source)
	}
	slices.SortFunc(sources, func(a, b *datastore.Translation) int {
		left, err := datastore.TranslationIndexShard(a.UID)
		require.NoError(t, err)
		right, err := datastore.TranslationIndexShard(b.UID)
		require.NoError(t, err)
		if left != right {
			return int(left) - int(right)
		}
		return b.CreationTimestamp.Compare(a.CreationTimestamp)
	})
	for _, reverse := range []bool{false, true} {
		cursor := ""
		var actual []string
		for range 64 {
			params := datastore.PageParams{First: 2, After: cursor}
			if reverse {
				params = datastore.PageParams{Last: 2, Before: cursor}
			}
			page, err := b.ListTranslations(t.Context(), prototype.Namespace, params)
			require.NoError(t, err)
			require.LessOrEqual(t, len(page.Items), 2)
			for _, source := range page.Items {
				actual = append(actual, source.UID)
				require.Equal(t, prototype.Body, source.Body)
				require.Equal(t, prototype.RawSource, source.RawSource)
				require.NotEmpty(t, page.ItemCursors[source.UID])
			}
			next, more := page.NextCursor, page.HasNext
			if reverse {
				slices.Reverse(actual[len(actual)-len(page.Items):])
				next, more = page.PreviousCursor, page.HasPrevious
			}
			if !more {
				break
			}
			require.NotEqual(t, cursor, next)
			cursor = next
		}
		want := make([]string, len(sources))
		for i, source := range sources {
			want[i] = source.UID
		}
		if reverse {
			slices.Reverse(want)
		}
		require.Equal(t, want, actual)
	}
	source := sources[0]
	next := *source
	next.Generation, next.ResourceVersion, next.Body = 2, "2", "updated body"
	require.NoError(t, b.UpdateTranslation(t.Context(), &next, "1"))
	var firstPage *datastore.TranslationPage
	cursor := ""
	for range 8 {
		page, err := a.ListTranslations(t.Context(), prototype.Namespace, datastore.PageParams{First: 1, After: cursor})
		require.NoError(t, err)
		if len(page.Items) != 0 {
			firstPage = page
			break
		}
		cursor = page.NextCursor
	}
	require.NotNil(t, firstPage)
	require.Equal(t, source.UID, firstPage.Items[0].UID)
	require.Equal(t, "updated body", firstPage.Items[0].Body, "the immutable anchor does not freeze the public source snapshot")
	other := newTranslationCreationSource(t, first)
	_, err := b.ListTranslations(t.Context(), other.Namespace, datastore.PageParams{After: firstPage.NextCursor})
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	require.NoError(t, a.DeleteTranslation(t.Context(), source.UID, "2"))
	_, err = b.ListTranslations(t.Context(), prototype.Namespace, datastore.PageParams{First: 1, After: firstPage.ItemCursors[source.UID]})
	require.NoError(t, err, "deleting a cursor row must not invalidate its position")
}
