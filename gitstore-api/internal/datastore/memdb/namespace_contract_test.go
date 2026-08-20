// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
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
		ID:                "00000000-0000-0000-0000-000000000111",
		Name:              "versioned-namespace",
		Title:             "Versioned Namespace",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: time.Now().UTC(),
		CreationActor:     "test",
		UpdateTimestamp:   time.Now().UTC(),
		UpdateActor:       "test",
	}
	require.NoError(t, ds.CreateNamespace(ctx, ns))

	first, err := ds.GetNamespace(ctx, ns.ID)
	require.NoError(t, err)
	stale, err := ds.GetNamespace(ctx, ns.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.Generation)
	assert.Equal(t, "1", first.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(first.Status))

	first.Title = "First writer"
	datastore.AdvanceNamespaceSpecVersion(first)
	require.NoError(t, ds.UpdateNamespace(ctx, first, "1"))

	stale.Title = "Stale writer"
	datastore.AdvanceNamespaceSpecVersion(stale)
	require.ErrorIs(t, ds.UpdateNamespace(ctx, stale, "1"), datastore.ErrConflict)

	got, err := ds.GetNamespace(ctx, ns.ID)
	require.NoError(t, err)
	assert.Equal(t, "First writer", got.Title)
	assert.Equal(t, int64(2), got.Generation)
	assert.Equal(t, "2", got.ResourceVersion)
}
