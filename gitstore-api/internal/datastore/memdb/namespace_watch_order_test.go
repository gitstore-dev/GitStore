// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestNamespaceMutationSerializesCommitAndJournalPublication(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	memory := store.(*memdbDatastore)

	memory.namespaceMutationMu.Lock()
	locked := true
	defer func() {
		if locked {
			memory.namespaceMutationMu.Unlock()
		}
	}()

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- memory.CreateNamespace(context.Background(), &datastore.Namespace{
			UID:               "01990000-0000-7000-8000-000000000001",
			Name:              "ordered",
			ResourceVersion:   datastore.NamespaceInitialResourceVersion,
			CreationTimestamp: time.Now().UTC(),
			UpdateTimestamp:   time.Now().UTC(),
		})
	}()
	<-started

	require.Never(t, func() bool {
		_, getErr := memory.GetNamespaceByName(context.Background(), "ordered")
		return getErr == nil
	}, 25*time.Millisecond, 5*time.Millisecond)
	bounds, err := memory.Bounds(context.Background())
	require.NoError(t, err)
	require.Zero(t, bounds.HighWater)

	memory.namespaceMutationMu.Unlock()
	locked = false
	require.NoError(t, <-result)

	_, err = memory.GetNamespaceByName(context.Background(), "ordered")
	require.NoError(t, err)
	bounds, err = memory.Bounds(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(1), bounds.HighWater)
}
