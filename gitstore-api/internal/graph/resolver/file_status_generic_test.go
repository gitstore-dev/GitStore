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

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

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
		Spec: json.RawMessage(`{"contentType":"image/jpeg","source":{"type":"s3","uri":"s3://fixture"}}`)}
	require.NoError(t, store.CreateFile(t.Context(), file))
	for _, replica := range []*Resolver{a, b} {
		got, err := replica.Query().File(t.Context(), "ns", "hero")
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
