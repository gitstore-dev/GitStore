// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductLifecycleWritesPublishCommittedResourceWatchEvents(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	product := &datastore.Product{UID: uuid.NewString(), Namespace: "acme", Name: "widget", ResourceVersion: "1", Labels: map[string]string{"tier": "core"}}
	require.NoError(t, store.CreateProduct(t.Context(), product))
	lifecycle := any(store).(datastore.ProductLifecycleStore)
	terminating, err := lifecycle.MarkProductTerminating(t.Context(), product.UID, "1", "finalizer", time.Now())
	require.NoError(t, err)
	require.NoError(t, lifecycle.CompleteProductDeletion(t.Context(), product.UID, terminating.ResourceVersion))

	journal := any(store).(datastore.ResourceWatchCapable).ResourceWatchJournal()
	events, err := journal.ReadAfter(context.Background(), datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, []datastore.ResourceWatchEventType{datastore.ResourceWatchAdded, datastore.ResourceWatchModified, datastore.ResourceWatchDeleted}, []datastore.ResourceWatchEventType{events[0].Type, events[1].Type, events[2].Type})
	for _, event := range events {
		assert.Equal(t, "Product", event.Kind)
		assert.Equal(t, "acme", event.Namespace)
		assert.Equal(t, "widget", event.Name)
	}
	assert.NotEmpty(t, events[0].Payload)
	assert.NotEmpty(t, events[1].Payload)
	assert.Empty(t, events[2].Payload)
}
