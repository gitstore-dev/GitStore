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
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func TestRepositoryWatchSchemaIsTypedWithOptionalNamespaceFilter(t *testing.T) {
	schema := repositoryContractSchema(t)
	field := requireGraphQLField(t, schema, "Subscription", "watchRepositories", "RepositoryWatchEvent!")
	requireGraphQLField(t, schema, "RepositoryWatchEvent", "type", "WatchEventType!")
	requireGraphQLField(t, schema, "RepositoryWatchEvent", "namespace", "String!")
	requireGraphQLField(t, schema, "RepositoryWatchEvent", "name", "String!")
	requireGraphQLField(t, schema, "RepositoryWatchEvent", "resourceVersion", "String!")
	requireGraphQLField(t, schema, "RepositoryWatchEvent", "repository", "Repository")

	assert.Equal(t, "String", field.Arguments.ForName("namespace").Type.String())
	assert.Equal(t, "LabelSelectorInput", field.Arguments.ForName("selector").Type.String())
	assert.Equal(t, "String", field.Arguments.ForName("resourceVersion").Type.String())
}

func TestTypedAndGenericRepositoryWatchShareNamespaceProjection(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease := acquireRepositoryWatchLease(t, journal)
	appendRepositoryBookmark(t, journal, lease)
	r, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	namespace := "acme"
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "catalog"}}
	typed, err := r.Subscription().WatchRepositories(ctx, &namespace, selector, nil)
	require.NoError(t, err)
	generic, err := r.Subscription().WatchResources(ctx, "Repository", &namespace, selector, nil)
	require.NoError(t, err)

	appendRepositoryJournalEvent(t, journal, lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "Repository", Namespace: "other", Name: "ignored", Payload: repositoryWatchPayload(t, repositoryWatchFixture("other", "ignored", "payments")), SelectorLabels: map[string]string{"team": "payments"}})
	appendRepositoryJournalEvent(t, journal, lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "Repository", Namespace: namespace, Name: "catalog", Payload: repositoryWatchPayload(t, repositoryWatchFixture(namespace, "catalog", "catalog")), SelectorLabels: map[string]string{"team": "catalog"}})

	typedEvent := receiveTypedRepositoryEvent(t, typed)
	genericEvent := receiveGenericRepositoryEvent(t, generic)
	assert.Equal(t, model.WatchEventTypeAdded, typedEvent.Type)
	assert.Equal(t, namespace, typedEvent.Namespace)
	assert.Equal(t, "catalog", typedEvent.Name)
	require.NotNil(t, typedEvent.Repository)
	assert.Equal(t, typedEvent.Repository.ID, typedEvent.Repository.Metadata.UID)
	assert.Equal(t, typedEvent.ResourceVersion, genericEvent.ResourceVersion)
	assert.Equal(t, "Repository", genericEvent.Kind)
	assert.Equal(t, namespace, *genericEvent.Namespace)
	require.NotNil(t, genericEvent.Object)
	assert.Equal(t, typedEvent.Repository.ID, genericEvent.Object["id"])
}

func TestTypedAndGenericRepositoryWatchShareBootstrapBookmark(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease := acquireRepositoryWatchLease(t, journal)
	appendRepositoryBookmark(t, journal, lease)
	r, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)

	bootstrap := watchjournal.BootstrapCursor
	typed, err := r.Subscription().WatchRepositories(context.Background(), nil, nil, &bootstrap)
	require.NoError(t, err)
	generic, err := r.Subscription().WatchResources(context.Background(), "Repository", nil, nil, &bootstrap)
	require.NoError(t, err)

	typedEvent := receiveTypedRepositoryEvent(t, typed)
	genericEvent := receiveGenericRepositoryEvent(t, generic)
	assert.Equal(t, model.WatchEventTypeBookmark, typedEvent.Type)
	assert.Equal(t, typedEvent.ResourceVersion, genericEvent.ResourceVersion)
	assert.Empty(t, typedEvent.Namespace)
	assert.Nil(t, typedEvent.Repository)
	assert.Nil(t, genericEvent.Object)
}

func TestTypedAndGenericRepositoryWatchAllowGlobalProjection(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease := acquireRepositoryWatchLease(t, journal)
	appendRepositoryBookmark(t, journal, lease)
	r, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	typed, err := r.Subscription().WatchRepositories(ctx, nil, nil, nil)
	require.NoError(t, err)
	generic, err := r.Subscription().WatchResources(ctx, "Repository", nil, nil, nil)
	require.NoError(t, err)

	appendRepositoryJournalEvent(t, journal, lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "Repository", Namespace: "other", Name: "payments", Payload: repositoryWatchPayload(t, repositoryWatchFixture("other", "payments", "payments")), SelectorLabels: map[string]string{"team": "payments"}})
	assert.Equal(t, "other", receiveTypedRepositoryEvent(t, typed).Namespace)
	assert.Equal(t, "other", *receiveGenericRepositoryEvent(t, generic).Namespace)
}

func TestRepositoryWatchSelectorProjectsModifiedExitAsDeleted(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease := acquireRepositoryWatchLease(t, journal)
	appendRepositoryBookmark(t, journal, lease)
	r, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "catalog"}}
	typed, err := r.Subscription().WatchRepositories(ctx, nil, selector, nil)
	require.NoError(t, err)

	repo := repositoryWatchFixture("acme", "catalog", "payments")
	appendRepositoryJournalEvent(t, journal, lease, datastore.ResourceWatchEvent{
		Type: datastore.ResourceWatchModified, Kind: "Repository", Namespace: "acme", Name: "catalog",
		Payload: repositoryWatchPayload(t, repo), SelectorLabels: repo.Labels,
		PreviousSelectorLabels: map[string]string{"team": "catalog"},
	})

	event := receiveTypedRepositoryEvent(t, typed)
	assert.Equal(t, model.WatchEventTypeDeleted, event.Type)
	assert.Nil(t, event.Repository)
}

func TestRepositoryWatchFailsClosedWhenDurableReadersAreDisabled(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)

	invalidCursor := "revealing-invalid-cursor"
	_, err = r.Subscription().WatchRepositories(context.Background(), nil, nil, &invalidCursor)
	require.Error(t, err)
	assert.Equal(t, "WATCH_UNAVAILABLE", err.(*gqlerror.Error).Extensions["code"])
	assert.Equal(t, "MATERIALIZER_NOT_READY", err.(*gqlerror.Error).Extensions["reason"])

	_, err = r.Subscription().WatchResources(context.Background(), "Repository", nil, nil, &invalidCursor)
	require.Error(t, err)
	assert.Equal(t, "WATCH_UNAVAILABLE", err.(*gqlerror.Error).Extensions["code"])
	assert.Equal(t, "MATERIALIZER_NOT_READY", err.(*gqlerror.Error).Extensions["reason"])
}

func repositoryWatchResolverDeps(store datastore.Datastore, journal datastore.ResourceWatchJournal) ResolverDeps {
	return ResolverDeps{
		Store: store, Logger: zap.NewNop(), ResourceJournal: journal,
		NamespaceWatch: config.NamespaceWatchConfig{ReadersEnabled: true, ReadBatchSize: 256, MaxReplayEvents: 100000, SubscriberBuffer: 64, SubscriberBackpressureMillis: 1000, PollMinMillis: 10, PollMaxMillis: 20, MaxMaterializerLagSeconds: 60},
	}
}

func acquireRepositoryWatchLease(t *testing.T, journal datastore.ResourceWatchJournal) datastore.ResourceWatchLease {
	t.Helper()
	lease, acquired, err := journal.AcquireLease(context.Background(), "repository-watch-test", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	return lease
}

func appendRepositoryBookmark(t *testing.T, journal datastore.ResourceWatchJournal, lease datastore.ResourceWatchLease) {
	t.Helper()
	_, err := journal.Append(context.Background(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark, At: time.Now()}, time.Hour)
	require.NoError(t, err)
}

func appendRepositoryJournalEvent(t *testing.T, journal datastore.ResourceWatchJournal, lease datastore.ResourceWatchLease, event datastore.ResourceWatchEvent) {
	t.Helper()
	event.At = time.Now()
	_, err := journal.Append(context.Background(), lease, event, time.Hour)
	require.NoError(t, err)
}

func repositoryWatchPayload(t *testing.T, repository *datastore.Repository) []byte {
	t.Helper()
	payload, err := json.Marshal(repository)
	require.NoError(t, err)
	return payload
}

func repositoryWatchFixture(namespace, name, team string) *datastore.Repository {
	return &datastore.Repository{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository",
		UID: "018f47d2-cd4b-7a11-9c35-4b4c423d56cb", Namespace: namespace, Name: name,
		DefaultBranch: "main", StorageClass: "default", Labels: map[string]string{"team": team},
		CreationTimestamp: time.Now().UTC(),
	}
}

func receiveTypedRepositoryEvent(t *testing.T, events <-chan *model.RepositoryWatchEvent) *model.RepositoryWatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for typed Repository event")
		return nil
	}
}

func receiveGenericRepositoryEvent(t *testing.T, events <-chan *model.WatchEvent) *model.WatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generic Repository event")
		return nil
	}
}
