// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemdbNamespaceWatchJournalContract(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	capable, ok := store.(datastore.NamespaceWatchCapable)
	require.True(t, ok)
	journal := capable.NamespaceWatchJournal()

	lease, acquired, err := journal.AcquireLease(ctx, "replica-a", time.Now(), 30*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)

	for i := 0; i < 300; i++ {
		_, err = journal.Append(ctx, lease, datastore.NamespaceWatchEvent{
			Type:    datastore.NamespaceWatchAdded,
			Name:    "namespace",
			Payload: json.RawMessage(`{"kind":"Namespace"}`),
			At:      time.Now(),
		}, 7*24*time.Hour)
		require.NoError(t, err)
	}

	bounds, err := journal.Bounds(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), bounds.Oldest)
	assert.Equal(t, uint64(300), bounds.HighWater)

	first, err := journal.ReadAfter(ctx, datastore.NamespaceWatchCursor{Epoch: bounds.Epoch}, watchjournal.DefaultReadBatchSize)
	require.NoError(t, err)
	require.Len(t, first, 256)
	assert.Equal(t, uint64(1), first[0].Sequence)
	assert.Equal(t, uint64(256), first[255].Sequence)

	second, err := journal.ReadAfter(ctx, datastore.NamespaceWatchCursor{Epoch: bounds.Epoch, Sequence: 256}, watchjournal.DefaultReadBatchSize)
	require.NoError(t, err)
	require.Len(t, second, 44)
	assert.Equal(t, uint64(257), second[0].Sequence)
}
