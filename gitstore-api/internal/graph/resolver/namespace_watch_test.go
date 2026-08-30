// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTypedAndGenericNamespaceWatchShareBootstrapAndEvents(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	lease, acquired, err := journal.AcquireLease(context.Background(), "test", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = journal.Append(context.Background(), lease, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchBookmark, At: time.Now()}, time.Hour)
	require.NoError(t, err)

	r, err := NewResolver(ResolverDeps{
		Store: store, Logger: zap.NewNop(), NamespaceJournal: journal,
		NamespaceWatch: config.NamespaceWatchConfig{ReadersEnabled: true, ReadBatchSize: 256, MaxReplayEvents: 100000, SubscriberBuffer: 64, SubscriberBackpressureMillis: 1000, PollMinMillis: 10, PollMaxMillis: 20, MaxMaterializerLagSeconds: 60},
	})
	require.NoError(t, err)
	bootstrap := watchjournal.BootstrapCursor
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "catalog"}}
	typed, err := r.Subscription().WatchNamespaces(context.Background(), selector, &bootstrap)
	require.NoError(t, err)
	generic, err := r.Subscription().WatchResources(context.Background(), "Namespace", nil, selector, &bootstrap)
	require.NoError(t, err)

	typedBookmark := receiveTypedNamespaceEvent(t, typed)
	genericBookmark := receiveGenericNamespaceEvent(t, generic)
	assert.Equal(t, model.WatchEventTypeBookmark, typedBookmark.Type)
	assert.Equal(t, typedBookmark.ResourceVersion, genericBookmark.ResourceVersion)
	assert.Nil(t, typedBookmark.Namespace)
	assert.Nil(t, genericBookmark.Object)

	namespace := &datastore.Namespace{APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", UID: "018f47d2-cd4b-7a11-9c35-4b4c423d56cb", Name: "shop", ResourceVersion: "7", Generation: 2, Labels: map[string]string{"team": "catalog"}}
	payload, err := json.Marshal(namespace)
	require.NoError(t, err)
	_, err = journal.Append(context.Background(), lease, datastore.NamespaceWatchEvent{Type: datastore.NamespaceWatchAdded, Name: "shop", Payload: payload, At: time.Now()}, time.Hour)
	require.NoError(t, err)

	typedAdded := receiveTypedNamespaceEvent(t, typed)
	genericAdded := receiveGenericNamespaceEvent(t, generic)
	assert.Equal(t, model.WatchEventTypeAdded, typedAdded.Type)
	assert.Equal(t, typedAdded.ResourceVersion, genericAdded.ResourceVersion)
	require.NotNil(t, typedAdded.Namespace)
	assert.Equal(t, "shop", typedAdded.Namespace.Metadata.Name)
	assert.Equal(t, "shop", genericAdded.Name)
	assert.Nil(t, genericAdded.Namespace)

	_, err = journal.Append(context.Background(), lease, datastore.NamespaceWatchEvent{
		Type: datastore.NamespaceWatchDeleted, Name: "shop",
		SelectorLabels: map[string]string{"team": "catalog"}, At: time.Now(),
	}, time.Hour)
	require.NoError(t, err)

	typedDeleted := receiveTypedNamespaceEvent(t, typed)
	genericDeleted := receiveGenericNamespaceEvent(t, generic)
	assert.Equal(t, model.WatchEventTypeDeleted, typedDeleted.Type)
	assert.Equal(t, typedDeleted.ResourceVersion, genericDeleted.ResourceVersion)
	assert.Nil(t, typedDeleted.Namespace)
	assert.Nil(t, genericDeleted.Object)
}

func TestNamespaceWatchSelectorProjectsModifiedTransitions(t *testing.T) {
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "catalog"}}
	tests := []struct {
		name     string
		previous map[string]string
		current  map[string]string
		wantType datastore.NamespaceWatchEventType
		want     bool
		payload  bool
	}{
		{name: "remains outside", previous: map[string]string{"team": "payments"}, current: map[string]string{"team": "storefront"}},
		{name: "enters selector", previous: map[string]string{"team": "payments"}, current: map[string]string{"team": "catalog"}, wantType: datastore.NamespaceWatchAdded, want: true, payload: true},
		{name: "remains inside", previous: map[string]string{"team": "catalog"}, current: map[string]string{"team": "catalog"}, wantType: datastore.NamespaceWatchModified, want: true, payload: true},
		{name: "leaves selector", previous: map[string]string{"team": "catalog"}, current: map[string]string{"team": "storefront"}, wantType: datastore.NamespaceWatchDeleted, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, ok := projectNamespaceJournalEventForSelector(datastore.NamespaceWatchEvent{
				Type: datastore.NamespaceWatchModified, Payload: json.RawMessage(`{"Name":"shop"}`),
				SelectorLabels: tt.current, PreviousSelectorLabels: tt.previous,
			}, selector)
			assert.Equal(t, tt.want, ok)
			if !tt.want {
				return
			}
			assert.Equal(t, tt.wantType, event.Type)
			assert.Equal(t, tt.payload, len(event.Payload) > 0)
		})
	}
}

func receiveTypedNamespaceEvent(t *testing.T, events <-chan *model.NamespaceWatchEvent) *model.NamespaceWatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for typed Namespace event")
		return nil
	}
}

func receiveGenericNamespaceEvent(t *testing.T, events <-chan *model.WatchEvent) *model.WatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generic Namespace event")
		return nil
	}
}
