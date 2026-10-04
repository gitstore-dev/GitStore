// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func receiveCategoryData(t *testing.T, events <-chan *model.CategoryWatchEvent) *model.CategoryWatchEvent {
	t.Helper()
	timeout := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Type != model.WatchEventTypeBookmark {
				return event
			}
		case <-timeout:
			t.Fatal("timed out waiting for CategoryTaxonomy event")
			return nil
		}
	}
}

// Two API replicas share one durable journal: a CategoryTaxonomy committed
// through replica A's datastore is delivered to a watcher on replica B, which
// the process-local event bus could never guarantee.
func TestCategoryWatchIsDeliveredAcrossReplicasSharingTheJournal(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	appendRepositoryBookmark(t, journal, acquireRepositoryWatchLease(t, journal))

	replicaA, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)
	replicaB, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	namespace := "acme"
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "catalog"}}
	typed, err := replicaB.Subscription().WatchCategories(ctx, &namespace, selector, nil)
	require.NoError(t, err)
	generic, err := replicaB.Subscription().WatchResources(ctx, "CategoryTaxonomy", &namespace, selector, nil)
	require.NoError(t, err)

	category := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-0000000000a1", Namespace: namespace, Name: "shoes",
		Kind: "CategoryTaxonomy", Generation: 1, ResourceVersion: "1",
		Labels: map[string]string{"team": "catalog"}, CreationTimestamp: time.Now(),
	}
	require.NoError(t, replicaA.store.CreateCategoryTaxonomy(ctx, category))

	typedEvent := receiveCategoryData(t, typed)
	assert.Equal(t, model.WatchEventTypeAdded, typedEvent.Type)
	require.NotNil(t, typedEvent.Category)
	assert.Equal(t, "shoes", typedEvent.Category.Metadata.Name)
	genericEvent := receiveGenericProductEvent(t, generic)
	for genericEvent.Type == model.WatchEventTypeBookmark {
		genericEvent = receiveGenericProductEvent(t, generic)
	}
	assert.Equal(t, typedEvent.ResourceVersion, genericEvent.ResourceVersion)
	assert.Equal(t, "CategoryTaxonomy", genericEvent.Kind)
	require.NotNil(t, genericEvent.Object)
}

func TestCategoryWatchFailsClosedWithoutJournal(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	for _, cfg := range []config.NamespaceWatchConfig{{}, {ReadersEnabled: true}} {
		r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop(), NamespaceWatch: cfg})
		require.NoError(t, err)
		_, err = r.Subscription().WatchCategories(t.Context(), nil, nil, nil)
		require.Error(t, err)
		require.Equal(t, watchjournal.CodeUnavailable, err.(*gqlerror.Error).Extensions["code"])
		_, err = r.Subscription().WatchResources(t.Context(), "CategoryTaxonomy", nil, nil, nil)
		require.Error(t, err)
		require.Equal(t, watchjournal.CodeUnavailable, err.(*gqlerror.Error).Extensions["code"])
	}
}

func TestCategoryWatchBootstrapCursorNormalizes(t *testing.T) {
	assert.Equal(t, watchjournal.BootstrapCursor, normalizeResourceWatchCursor(categoryWatchBootstrapCursor))
}
