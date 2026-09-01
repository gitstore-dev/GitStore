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
