// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommittedNamespaceUsesConfiguredJournalRetention(t *testing.T) {
	tests := []struct {
		name      string
		retention time.Duration
		wantCount int
	}{
		{name: "prunes outside configured window", retention: time.Hour, wantCount: 1},
		{name: "retains inside configured window", retention: 3 * time.Hour, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := New(tt.retention)
			require.NoError(t, err)
			memory := store.(*memdbDatastore)
			memory.namespaceWatchSequence = 1
			memory.namespaceWatchEvents = []datastore.NamespaceWatchEvent{{Sequence: 1, At: time.Now().UTC().Add(-2 * time.Hour)}}

			memory.recordCommittedNamespace(datastore.NamespaceWatchAdded, &datastore.Namespace{UID: "uid", Name: "shop"}, nil)

			assert.Len(t, memory.namespaceWatchEvents, tt.wantCount)
			assert.Equal(t, tt.retention, memory.namespaceWatchRetention)
		})
	}
}

func TestCDCProgressSeparatesSourcesAndPreservesLegacyNamespaceKeys(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(t.Context(), "progress-contract", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	for _, source := range []string{"Namespace", "Repository", "RBAC", "ServiceAccount"} {
		require.NoError(t, journal.SaveProgress(t.Context(), lease, datastore.ResourceCDCProgress{
			Source: source, StreamID: "shared-stream", Position: []byte(source), UpdatedAt: time.Now(),
		}))
	}
	for _, source := range []string{"Namespace", "Repository", "RBAC", "ServiceAccount"} {
		key := source + ":shared-stream"
		if source == "Namespace" {
			key = "shared-stream"
		}
		progress, err := journal.LoadProgress(t.Context(), key)
		require.NoError(t, err)
		require.Equal(t, source, string(progress.Position))
		progress.Position[0] = 'x'
		again, err := journal.LoadProgress(t.Context(), key)
		require.NoError(t, err)
		require.Equal(t, source, string(again.Position))
	}
}
