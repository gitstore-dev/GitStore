// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslationAdditionalTargetWritesPublishCommittedEvents(t *testing.T) {
	for _, kind := range []string{"ProductVariant", "Collection"} {
		t.Run(kind, func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			uid := uuid.NewString()
			switch kind {
			case "ProductVariant":
				resource := &datastore.ProductVariant{
					UID: uid, Namespace: "acme", Name: "target", ResourceVersion: "1",
					Generation: 1, Spec: json.RawMessage(`{"title":"before"}`),
					SKU: "sku", ProductRefName: "parent",
				}
				require.NoError(t, store.CreateProductVariant(t.Context(), resource))
				resource.Spec, resource.Generation, resource.ResourceVersion = json.RawMessage(`{"title":"after"}`), 2, "2"
				require.NoError(t, store.UpdateProductVariant(t.Context(), resource, "1"))
				require.ErrorIs(t, store.UpdateProductVariant(t.Context(), resource, "1"), datastore.ErrConflict)
				require.NoError(t, store.DeleteProductVariant(t.Context(), uid))
			case "Collection":
				resource := &datastore.Collection{
					UID: uid, Namespace: "acme", Name: "target", ResourceVersion: "1",
					Generation: 1, Spec: json.RawMessage(`{"title":"before"}`),
				}
				require.NoError(t, store.CreateCollection(t.Context(), resource))
				resource.Spec, resource.Generation, resource.ResourceVersion = json.RawMessage(`{"title":"after"}`), 2, "2"
				require.NoError(t, store.UpdateCollection(t.Context(), resource, "1"))
				require.ErrorIs(t, store.UpdateCollection(t.Context(), resource, "1"), datastore.ErrConflict)
				require.NoError(t, store.DeleteCollection(t.Context(), uid))
			}
			journal := any(store).(datastore.ResourceWatchCapable).ResourceWatchJournal()
			events, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 10)
			require.NoError(t, err)
			require.Len(t, events, 3)
			require.Equal(t, []datastore.ResourceWatchEventType{datastore.ResourceWatchAdded, datastore.ResourceWatchModified, datastore.ResourceWatchDeleted},
				[]datastore.ResourceWatchEventType{events[0].Type, events[1].Type, events[2].Type})
			for i, event := range events {
				require.Equal(t, kind, event.Kind)
				require.Equal(t, "acme", event.Namespace)
				require.Equal(t, "target", event.Name)
				if i != 0 {
					require.Greater(t, event.Sequence, events[i-1].Sequence)
				}
			}
			require.Contains(t, string(events[0].Payload), "before")
			require.NotContains(t, string(events[0].Payload), "after")
			require.Contains(t, string(events[1].Payload), "after")
			require.Empty(t, events[2].Payload)
		})
	}
}

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
