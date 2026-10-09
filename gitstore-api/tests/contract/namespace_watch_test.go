// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceWatchPayloadMatrix(t *testing.T) {
	namespace := &datastore.Namespace{Kind: "Namespace", Name: "shop", UID: "018f47d2-cd4b-7a11-9c35-4b4c423d56cb", ResourceVersion: "9"}
	for _, tc := range []struct {
		typeValue datastore.NamespaceWatchEventType
		payload   *datastore.Namespace
		want      bool
	}{
		{typeValue: datastore.NamespaceWatchAdded, payload: namespace, want: true},
		{typeValue: datastore.NamespaceWatchModified, payload: namespace, want: true},
		{typeValue: datastore.NamespaceWatchDeleted, want: false},
		{typeValue: datastore.NamespaceWatchBookmark, want: false},
	} {
		event, err := resolver.NamespaceJournalEventToGraphQL(datastore.NamespaceWatchEvent{Type: tc.typeValue, Name: "shop", Epoch: "018f47d2-cd4b-7a11-9c35-4b4c423d56cc", Sequence: 1}, tc.payload)
		require.NoError(t, err)
		assert.Equal(t, model.WatchEventType(tc.typeValue), event.Type)
		assert.Equal(t, tc.want, event.Namespace != nil)
	}
}
