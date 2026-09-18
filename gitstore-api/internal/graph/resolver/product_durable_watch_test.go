// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
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

// Product streams use the shared durable Resource Watch delivery boundary.
// A slow Product subscriber therefore has a bounded failure mode and emits
// the same scrapeable overflow/expiry signals used by the alert contract.
func TestProductWatchOutputRecordsBoundedOverflowMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := watchjournal.NewMetrics(registry)
	require.NoError(t, err)

	err = sendNamespaceWatchOutput(context.Background(), make(chan int), 1, time.Millisecond, metrics)
	terminal, ok := watchjournal.AsTerminal(err)
	require.True(t, ok)
	assert.Equal(t, watchjournal.CodeExpired, terminal.Code)
	assert.Equal(t, watchjournal.ReasonSubscriberOverflow, terminal.Reason)
	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP gitstore_namespace_watch_expired_total Namespace watches terminated because continuity was not provable.
# TYPE gitstore_namespace_watch_expired_total counter
gitstore_namespace_watch_expired_total{reason="SUBSCRIBER_OVERFLOW"} 1
# HELP gitstore_namespace_watch_overflow_total Namespace subscriber buffer overflows.
# TYPE gitstore_namespace_watch_overflow_total counter
gitstore_namespace_watch_overflow_total 1
`), "gitstore_namespace_watch_expired_total", "gitstore_namespace_watch_overflow_total"))
}

// Product watches must fail closed when the shared journal materializer is not
// ready. In particular, they must not fall back to an event-bus cursor that
// cannot be resumed on another API replica.
func TestProductWatchFailsClosedWhenDurableReadersAreDisabled(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	r, err := NewResolver(ResolverDeps{
		Store: store, Logger: zap.NewNop(), ResourceJournal: journal,
		NamespaceWatch: config.NamespaceWatchConfig{ReadersEnabled: false},
	})
	require.NoError(t, err)

	invalidCursor := "revealing-invalid-cursor"
	_, err = r.Subscription().WatchProducts(context.Background(), nil, nil, &invalidCursor)
	require.Error(t, err)
	assert.Equal(t, "WATCH_UNAVAILABLE", err.(*gqlerror.Error).Extensions["code"])
	assert.Equal(t, "MATERIALIZER_NOT_READY", err.(*gqlerror.Error).Extensions["reason"])

	_, err = r.Subscription().WatchResources(context.Background(), "Product", nil, nil, &invalidCursor)
	require.Error(t, err)
	assert.Equal(t, "WATCH_UNAVAILABLE", err.(*gqlerror.Error).Extensions["code"])
	assert.Equal(t, "MATERIALIZER_NOT_READY", err.(*gqlerror.Error).Extensions["reason"])
}

// Bootstrap is a shared journal operation: a typed Product subscriber and a
// generic Product subscriber must receive the same bookmark before live
// events, so either stream can resume from the returned cursor.
func TestTypedAndGenericProductWatchShareBootstrapBookmark(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(context.Background(), "product-bootstrap-test", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = journal.Append(context.Background(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark, At: time.Now()}, time.Hour)
	require.NoError(t, err)

	r, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)
	bootstrap := watchjournal.BootstrapCursor
	typed, err := r.Subscription().WatchProducts(context.Background(), nil, nil, &bootstrap)
	require.NoError(t, err)
	generic, err := r.Subscription().WatchResources(context.Background(), "Product", nil, nil, &bootstrap)
	require.NoError(t, err)

	typedBookmark := receiveTypedProductEvent(t, typed)
	genericBookmark := receiveGenericProductEvent(t, generic)
	assert.Equal(t, model.WatchEventTypeBookmark, typedBookmark.Type)
	assert.Equal(t, typedBookmark.ResourceVersion, genericBookmark.ResourceVersion)
	assert.Nil(t, typedBookmark.Product)
	assert.Nil(t, genericBookmark.Object)
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
