// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationDependentPageBoundsIncludeRetiredCellsAndEmptySegments(t *testing.T) {
	target := datastore.TranslationTargetKey{
		NamespaceUID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "Laptop",
	}
	var cells []datastore.TranslationIndexCell
	for len(cells) < datastore.MaxTranslationPageSize {
		uid := uuid.NewString()
		shard, err := datastore.TranslationIndexShard(uid)
		require.NoError(t, err)
		if shard != 0 {
			continue
		}
		cells = append(cells, datastore.TranslationIndexCell{
			Entry: datastore.TranslationIndexEntry{TranslationUID: uid, SourceVersion: "1",
				ReferenceEpoch: uuid.NewString(), CreationTimestamp: time.Now().UTC(), Slot: uint32(len(cells))},
			Token: uuid.NewString(), Retired: true,
		})
	}
	headReads, segmentReads, scanned := 0, 0, 0
	readHead := func(_ context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
		headReads++
		return &datastore.TranslationIndexHead{Index: index, Segment: 18}, nil
	}
	readSegment := func(_ context.Context, index datastore.TranslationIndexKey, after string, limit int) ([]datastore.TranslationIndexCell, error) {
		segmentReads++
		if index.Segment == 0 && after == "" {
			require.Equal(t, datastore.MaxTranslationPageSize, limit, "oversized requests must be capped before I/O")
			scanned += len(cells)
			return cells, nil
		}
		return nil, nil
	}
	first, err := datastore.ReadTranslationDependentPage(t.Context(), target, "", "", 1000, readHead, readSegment)
	require.NoError(t, err)
	require.Empty(t, first.Items)
	require.NotEmpty(t, first.Next)
	require.Equal(t, 2, first.Probes)
	require.Equal(t, datastore.MaxTranslationPageSize, scanned, "retired rows consume the same turn budget")
	second, err := datastore.ReadTranslationDependentPage(t.Context(), target, "", first.Next, 1000, readHead, readSegment)
	require.NoError(t, err)
	require.Empty(t, second.Items)
	require.NotEmpty(t, second.Next)
	require.NotEqual(t, first.Next, second.Next)
	require.Equal(t, datastore.MaxTranslationIndexProbes, second.Probes)
	require.Equal(t, 1, headReads, "continuation resumes its recorded head without restarting")
	require.Equal(t, 1+datastore.MaxTranslationIndexProbes, segmentReads)
}

func TestTranslationDependentPageCancellationAndReadFailure(t *testing.T) {
	target := datastore.TranslationTargetKey{
		NamespaceUID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "Laptop",
	}

	ctx, cancel := context.WithCancel(t.Context())
	page, err := datastore.ReadTranslationDependentPage(ctx, target, "", "", 3,
		func(_ context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
			cancel()
			return &datastore.TranslationIndexHead{Index: index, Segment: 1}, nil
		},
		func(context.Context, datastore.TranslationIndexKey, string, int) ([]datastore.TranslationIndexCell, error) {
			t.Fatal("cancellation must prevent subsequent segment reads")
			return nil, nil
		})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, page)

	failure := errors.New("segment read unavailable")
	uid := uuid.NewString()
	for {
		shard, err := datastore.TranslationIndexShard(uid)
		require.NoError(t, err)
		if shard == 0 {
			break
		}
		uid = uuid.NewString()
	}
	page, err = datastore.ReadTranslationDependentPage(t.Context(), target, "", "", 3,
		func(_ context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
			return &datastore.TranslationIndexHead{Index: index, Segment: 1}, nil
		},
		func(_ context.Context, index datastore.TranslationIndexKey, _ string, _ int) ([]datastore.TranslationIndexCell, error) {
			if index.Segment == 1 {
				return nil, failure
			}
			return []datastore.TranslationIndexCell{{Entry: datastore.TranslationIndexEntry{
				TranslationUID: uid, SourceVersion: "1", CreationTimestamp: time.Now().UTC(),
			}}}, nil
		})
	require.ErrorIs(t, err, failure)
	require.Empty(t, page, "read errors cannot acknowledge a successful partial page")
}

func TestTranslationMaintenanceCursorsCannotCrossIndexOrNamespace(t *testing.T) {
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationTargetPendingIndex, NamespaceUID: uuid.NewString()}
	readHead := func(context.Context, datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
		return nil, nil
	}
	readSegment := func(context.Context, datastore.TranslationIndexKey, string, int) ([]datastore.TranslationIndexCell, error) {
		t.Fatal("empty heads must not read cells")
		return nil, nil
	}
	page, err := datastore.ReadTranslationIndexPage(t.Context(), index, "", 1, readHead, readSegment)
	require.NoError(t, err)
	require.NotEmpty(t, page.Next)
	for _, other := range []datastore.TranslationIndexKey{
		{Kind: index.Kind, NamespaceUID: uuid.NewString()},
		{Kind: datastore.TranslationTargetMaintenanceNamespaceIndex, NamespaceUID: index.NamespaceUID},
		{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: index.NamespaceUID},
	} {
		_, err := datastore.ReadTranslationIndexPage(t.Context(), other, page.Next, 1,
			func(context.Context, datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
				t.Fatal("foreign cursors must fail before I/O")
				return nil, nil
			}, readSegment)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	}
}
