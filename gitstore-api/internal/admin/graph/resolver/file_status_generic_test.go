// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func TestFileListUsesNamespaceScopedBoundedRelayPages(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	now := time.Now().UTC()
	for i := range 4 {
		namespace := "shop"
		if i == 3 {
			namespace = "other"
		}
		require.NoError(t, store.CreateFile(t.Context(), &datastore.File{
			UID: uuid.NewString(), Namespace: namespace, Name: fmt.Sprintf("file-%d", i),
			CreationTimestamp: now.Add(time.Duration(i) * time.Second), ResourceVersion: "1",
			APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", Generation: 1,
		}))
	}
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	first := int32(2)
	page, err := r.Query().Files(t.Context(), "shop", &first, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, page.Edges, 2)
	require.Equal(t, "file-2", page.Edges[0].Node.Metadata.Name)
	require.True(t, page.PageInfo.HasNextPage)
	last, err := r.Query().Files(t.Context(), "shop", &first, page.PageInfo.EndCursor, nil, nil)
	require.NoError(t, err)
	require.Len(t, last.Edges, 1)
	require.Equal(t, "file-0", last.Edges[0].Node.Metadata.Name)
	require.False(t, last.PageInfo.HasNextPage)
	invalid := int32(-1)
	_, err = r.Query().Files(t.Context(), "shop", &invalid, nil, nil, nil)
	require.Error(t, err)
}

func TestAdditionalTranslationTargetWatchesReplayAcrossReplicas(t *testing.T) {
	for _, kind := range []string{"ProductVariant", "Collection"} {
		t.Run(kind, func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
			a, err := NewResolver(repositoryWatchResolverDeps(store, journal))
			require.NoError(t, err)
			b, err := NewResolver(repositoryWatchResolverDeps(store, journal))
			require.NoError(t, err)
			uid, namespace := uuid.NewString(), "shop"
			var update, remove func() error
			switch kind {
			case "ProductVariant":
				resource := &datastore.ProductVariant{
					UID: uid, Namespace: namespace, Name: "target", ResourceVersion: "1", Generation: 1,
					APIVersion: "catalog.gitstore.dev/v1beta1", Kind: kind, SKU: "sku", ProductRefName: "parent",
					Spec: json.RawMessage(`{"title":"initial","sku":"sku","productRef":{"name":"parent"}}`),
				}
				require.NoError(t, store.CreateProductVariant(t.Context(), resource))
				update = func() error {
					resource.ResourceVersion, resource.Generation, resource.Body = "2", 2, "\r\nexact target\r\n"
					return store.UpdateProductVariant(t.Context(), resource, "1")
				}
				remove = func() error { return store.DeleteProductVariantWithResourceVersion(t.Context(), uid, "2") }
			case "Collection":
				resource := &datastore.Collection{
					UID: uid, Namespace: namespace, Name: "target", ResourceVersion: "1", Generation: 1,
					APIVersion: "catalog.gitstore.dev/v1beta1", Kind: kind, Spec: json.RawMessage(`{"title":"initial"}`),
				}
				require.NoError(t, store.CreateCollection(t.Context(), resource))
				update = func() error {
					resource.ResourceVersion, resource.Generation, resource.Body = "2", 2, "\r\nexact target\r\n"
					return store.UpdateCollection(t.Context(), resource, "1")
				}
				remove = func() error { return store.DeleteCollectionWithResourceVersion(t.Context(), uid, "2") }
			}
			bounds, err := journal.Bounds(t.Context())
			require.NoError(t, err)
			cursor := watchjournal.EncodeCursor(bounds.Epoch, bounds.HighWater)
			require.NoError(t, update())
			require.NoError(t, remove())
			generic, err := b.Subscription().WatchResources(t.Context(), kind, &namespace, nil, &cursor)
			require.NoError(t, err)
			var nextTyped func() (model.WatchEventType, string, model.Node)
			switch kind {
			case "ProductVariant":
				typed, err := a.Subscription().WatchProductVariants(t.Context(), &namespace, nil, &cursor)
				require.NoError(t, err)
				nextTyped = func() (model.WatchEventType, string, model.Node) {
					event := receiveAdditionalTargetEvent(t, typed)
					return event.Type, event.ResourceVersion, event.ProductVariant
				}
			case "Collection":
				typed, err := a.Subscription().WatchCollections(t.Context(), &namespace, nil, &cursor)
				require.NoError(t, err)
				nextTyped = func() (model.WatchEventType, string, model.Node) {
					event := receiveAdditionalTargetEvent(t, typed)
					return event.Type, event.ResourceVersion, event.Collection
				}
			}
			eventType, version, node := nextTyped()
			require.Equal(t, model.WatchEventTypeModified, eventType)
			genericEvent := receiveGenericProductEvent(t, generic)
			require.Equal(t, version, genericEvent.ResourceVersion)
			require.Equal(t, node.GetID(), genericEvent.Object["id"])
			require.Equal(t, "\r\nexact target\r\n", genericEvent.Object["body"])
			eventType, version, node = nextTyped()
			require.Equal(t, model.WatchEventTypeDeleted, eventType)
			require.Nil(t, node)
			genericEvent = receiveGenericProductEvent(t, generic)
			require.Equal(t, version, genericEvent.ResourceVersion)
			require.Nil(t, genericEvent.Object)
		})
	}
}

func receiveAdditionalTargetEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event, open := <-events:
		require.True(t, open)
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("target watch did not replay its journal event")
		var zero T
		return zero
	}
}

func TestAdditionalTargetWatchRejectsMismatchedJournalImages(t *testing.T) {
	for _, kind := range []string{"ProductVariant", "Collection"} {
		for _, invalid := range []string{"null", "wrong-namespace", "wrong-name", "wrong-kind", "missing-uid"} {
			t.Run(kind+"/"+invalid, func(t *testing.T) {
				resource := map[string]string{"UID": uuid.NewString(), "Namespace": "shop", "Name": "target", "Kind": kind}
				switch invalid {
				case "wrong-namespace":
					resource["Namespace"] = "elsewhere"
				case "wrong-name":
					resource["Name"] = "another"
				case "wrong-kind":
					resource["Kind"] = "Product"
				case "missing-uid":
					delete(resource, "UID")
				}
				payload, err := json.Marshal(resource)
				require.NoError(t, err)
				if invalid == "null" {
					payload = []byte("null")
				}
				event := datastore.ResourceWatchEvent{
					Type: datastore.ResourceWatchAdded, Kind: kind, Namespace: "shop", Name: "target", Payload: payload,
				}
				_, err = catalogTargetJournalEventToGeneric(kind, event)
				require.Error(t, err)
			})
		}
		event, err := catalogTargetJournalEventToGeneric(kind, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark})
		require.NoError(t, err)
		require.Equal(t, kind, event.Kind)
		require.Nil(t, event.Object)
	}
}

func TestUpdateResourceStatusFileResolvedAndConflict(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.CreateFile(context.Background(), &datastore.File{
		UID: uuid.New().String(), Namespace: "ns", Name: "hero", Kind: "File",
		APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
		Status: json.RawMessage(`{"conditions":[]}`),
	}))
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	mr := &mutationResolver{Resolver: r}
	resolved := map[string]any{"resolvedVariants": []any{map[string]any{"name": "thumb", "url": "https://cdn/thumb"}}}
	got, err := mr.UpdateResourceStatus(context.Background(), model.UpdateResourceStatusInput{
		Kind: "File", Namespace: "ns", Name: "hero", ResourceVersion: "1", Resolved: resolved,
	})
	require.NoError(t, err)
	require.NotNil(t, got.Object)
	current, err := store.GetFileByName(context.Background(), "ns", "hero")
	require.NoError(t, err)
	require.Equal(t, "2", current.ResourceVersion)
	require.Contains(t, string(current.Status), "thumb")

	_, err = mr.UpdateResourceStatus(context.Background(), model.UpdateResourceStatusInput{
		Kind: "File", Namespace: "ns", Name: "hero", ResourceVersion: "1",
	})
	var graphErr *gqlerror.Error
	require.True(t, errors.As(err, &graphErr))
	require.Equal(t, "CONFLICT", graphErr.Extensions["code"])
	require.Contains(t, fmt.Sprint(graphErr.Extensions["diagnostics"]), "current resourceVersion is "+"2")
}

func TestFileProviderSchemaContract(t *testing.T) {
	schema := productContractSchema(t)
	requireGraphQLField(t, schema, "FileSpec", "provider", "String!")
	requireGraphQLField(t, schema, "FileSpec", "source", "FileSource!")
	requireGraphQLField(t, schema, "FileSource", "uri", "String!")
	require.Nil(t, schema.Types["FileSource"].Fields.ForName("type"))
}

func TestFileWatchUsesDurableJournalAcrossReplicasAndReplacement(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	a, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)
	b, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)
	file := &datastore.File{UID: uuid.NewString(), Namespace: "ns", Name: "hero", Kind: "File",
		ResourceVersion: "1", Labels: map[string]string{"team": "media"},
		Spec: json.RawMessage(`{"contentType":"image/jpeg","provider":"s3","source":{"uri":"s3://fixture"}}`)}
	require.NoError(t, store.CreateFile(t.Context(), file))
	for _, replica := range []*Resolver{a, b} {
		got, err := replica.Query().File(t.Context(), model.FileBy{NamespacePath: &model.FileNamespacePath{Namespace: "ns", Name: "hero"}})
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, mustEncodeNodeID(nodeKindFile, file.UID), got.ID)
	}
	bootstrap := fileWatchBootstrapCursor
	bookmarks, err := a.Subscription().WatchFiles(t.Context(), &file.Namespace, nil, &bootstrap)
	require.NoError(t, err)
	bookmark := receiveFileWatchEvent(t, bookmarks)
	require.Equal(t, model.WatchEventTypeBookmark, bookmark.Type)
	require.Nil(t, bookmark.File)

	// These writes precede the new subscriptions; both replicas must replay.
	updated, err := store.UpdateFileStatus(t.Context(), "ns", "hero", datastore.FileStatusPatch{ResourceVersion: "1"})
	require.NoError(t, err)
	updated.Labels = map[string]string{"team": "other"}
	updated.ResourceVersion = "3"
	require.NoError(t, store.UpdateFile(t.Context(), updated, "2"))
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "media"}}
	typed, err := b.Subscription().WatchFiles(t.Context(), &file.Namespace, selector, &bookmark.ResourceVersion)
	require.NoError(t, err)
	generic, err := a.Subscription().WatchResources(t.Context(), "File", &file.Namespace, selector, &bookmark.ResourceVersion)
	require.NoError(t, err)
	observed := receiveFileWatchEvent(t, typed)
	require.Equal(t, model.WatchEventTypeModified, observed.Type)
	require.NotNil(t, observed.File)
	genericEvent := receiveGenericProductEvent(t, generic)
	require.Equal(t, observed.ResourceVersion, genericEvent.ResourceVersion)
	require.Equal(t, observed.File.ID, genericEvent.Object["id"])
	require.Equal(t, model.WatchEventTypeDeleted, receiveFileWatchEvent(t, typed).Type)
	require.Equal(t, model.WatchEventTypeDeleted, receiveGenericProductEvent(t, generic).Type)

	replacement, err := NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)
	require.NoError(t, store.DeleteFile(t.Context(), file.UID))
	replayed, err := replacement.Subscription().WatchFiles(t.Context(), &file.Namespace, nil, &observed.ResourceVersion)
	require.NoError(t, err)
	require.Equal(t, model.WatchEventTypeModified, receiveFileWatchEvent(t, replayed).Type)
	deleted := receiveFileWatchEvent(t, replayed)
	require.Equal(t, model.WatchEventTypeDeleted, deleted.Type)
	require.Nil(t, deleted.File)
}

func TestFileWatchRejectsLegacyCursorsAndFailsClosedWithoutJournal(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	_, err = r.Subscription().WatchFiles(t.Context(), nil, nil, nil)
	require.Error(t, err)
	require.Equal(t, watchjournal.CodeUnavailable, err.(*gqlerror.Error).Extensions["code"])
	_, err = r.Subscription().WatchResources(t.Context(), "File", nil, nil, nil)
	require.Error(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	r, err = NewResolver(repositoryWatchResolverDeps(store, journal))
	require.NoError(t, err)
	_, err = r.Subscription().WatchFiles(t.Context(), nil, nil, nil)
	require.Error(t, err)
	require.Equal(t, watchjournal.CodeUnavailable, err.(*gqlerror.Error).Extensions["code"])
	lease := acquireRepositoryWatchLease(t, journal)
	_, err = journal.Append(t.Context(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark, At: time.Now()}, time.Hour)
	require.NoError(t, err)
	for _, cursor := range []string{"0", "42", "invalid"} {
		_, err = r.Subscription().WatchFiles(t.Context(), nil, nil, &cursor)
		require.Error(t, err)
		require.Equal(t, watchjournal.CodeExpired, err.(*gqlerror.Error).Extensions["code"])
	}
}

func TestFileWatchFiltersNamespacesAndSelectorEntry(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	file := &datastore.File{UID: uuid.NewString(), Namespace: "ns", Name: "hero", ResourceVersion: "1", Labels: map[string]string{"team": "other"}}
	require.NoError(t, store.CreateFile(t.Context(), file))
	deps := repositoryWatchResolverDeps(store, journal)
	r, err := NewResolver(deps)
	require.NoError(t, err)
	selector := &model.LabelSelectorInput{MatchLabels: map[string]any{"team": "media"}}
	stream, err := r.Subscription().WatchFiles(t.Context(), &file.Namespace, selector, nil)
	require.NoError(t, err)
	peer := &datastore.File{UID: uuid.NewString(), Namespace: "other", Name: "private", Labels: map[string]string{"team": "media"}}
	require.NoError(t, store.CreateFile(t.Context(), peer))
	file.Labels = map[string]string{"team": "media"}
	file.ResourceVersion = "2"
	require.NoError(t, store.UpdateFile(t.Context(), file, "1"))
	entry := receiveFileWatchEvent(t, stream)
	require.Equal(t, "hero", entry.Name, "a different namespace must not leak into the stream")
	require.Equal(t, model.WatchEventTypeAdded, entry.Type)
	require.NotNil(t, entry.File)
}

func receiveFileWatchEvent(t *testing.T, events <-chan *model.FileWatchEvent) *model.FileWatchEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		require.True(t, ok, "File stream closed unexpectedly")
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for durable File event")
		return nil
	}
}
