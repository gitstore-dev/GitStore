// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Product's typed and generic streams must be two projections of the same
// durable record, including selector entry/exit semantics. This guards the
// controller's typed stream from accidentally falling back to process-local
// eventbus cursors.
func TestTypedAndGenericProductWatchShareDurableJournal(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(context.Background(), "product-watch-test", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = journal.Append(context.Background(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark, At: time.Now()}, time.Hour)
	require.NoError(t, err)

	r, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	namespace := "acme"
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "catalog"}}
	typed, err := r.Subscription().WatchProducts(ctx, &namespace, selector, nil)
	require.NoError(t, err)
	generic, err := r.Subscription().WatchResources(ctx, "Product", &namespace, selector, nil)
	require.NoError(t, err)

	product := productLifecycleFixture(namespace, "widget")
	product.Labels = map[string]string{"team": "catalog"}
	payload, err := json.Marshal(product)
	require.NoError(t, err)
	_, err = journal.Append(context.Background(), lease, datastore.ResourceWatchEvent{
		Type: datastore.ResourceWatchAdded, Kind: "Product", Namespace: namespace, Name: product.Name,
		Payload: payload, SelectorLabels: product.Labels, At: time.Now(),
	}, time.Hour)
	require.NoError(t, err)

	typedEvent := receiveTypedProductEvent(t, typed)
	genericEvent := receiveGenericProductEvent(t, generic)
	assert.Equal(t, model.WatchEventTypeAdded, typedEvent.Type)
	require.NotNil(t, typedEvent.Product)
	assert.Equal(t, "widget", typedEvent.Product.Metadata.Name)
	assert.Equal(t, typedEvent.ResourceVersion, genericEvent.ResourceVersion)
	assert.Equal(t, "Product", genericEvent.Kind)
	require.NotNil(t, genericEvent.Object)

	product.Labels = map[string]string{"team": "other"}
	payload, err = json.Marshal(product)
	require.NoError(t, err)
	_, err = journal.Append(context.Background(), lease, datastore.ResourceWatchEvent{
		Type: datastore.ResourceWatchModified, Kind: "Product", Namespace: namespace, Name: product.Name,
		Payload: payload, SelectorLabels: product.Labels, PreviousSelectorLabels: map[string]string{"team": "catalog"}, At: time.Now(),
	}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, model.WatchEventTypeDeleted, receiveTypedProductEvent(t, typed).Type)
	assert.Equal(t, model.WatchEventTypeDeleted, receiveGenericProductEvent(t, generic).Type)
}

func TestTypedProductWatchBootstrapCursorNormalizes(t *testing.T) {
	assert.Equal(t, watchjournal.BootstrapCursor, normalizeResourceWatchCursor(productWatchBootstrapCursor))
}

func receiveTypedProductEvent(t *testing.T, events <-chan *model.ProductWatchEvent) *model.ProductWatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for typed Product event")
		return nil
	}
}

func receiveGenericProductEvent(t *testing.T, events <-chan *model.WatchEvent) *model.WatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generic Product event")
		return nil
	}
}
