// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestFileSnapshotsPreservePositionsFreshnessAndIsolation(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	first := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000151", Namespace: "first", Name: "file",
		ResourceVersion: "1", Labels: map[string]string{"original": "value"},
		Spec: []byte(`{"value":1}`), Finalizers: []string{"original"},
	}
	second := &datastore.File{UID: "00000000-0000-0000-0000-000000000152", Namespace: "second", Name: "file", ResourceVersion: "1"}
	require.NoError(t, store.CreateFile(t.Context(), first))
	require.NoError(t, store.CreateFile(t.Context(), second))
	ids := []string{second.UID, first.UID, "absent", first.UID}
	files, err := store.GetFileSnapshots(t.Context(), ids)
	require.NoError(t, err)
	require.Len(t, files, len(ids))
	require.Equal(t, second.UID, files[0].UID)
	require.Nil(t, files[2])
	files[1].Labels["original"] = "changed"
	files[1].Spec[0] = '!'
	files[1].Finalizers[0] = "changed"
	require.Equal(t, "value", files[3].Labels["original"])
	require.JSONEq(t, `{"value":1}`, string(files[3].Spec))
	require.Equal(t, []string{"original"}, files[3].Finalizers)
	first.ResourceVersion = "2"
	first.Body = "fresh"
	require.NoError(t, store.UpdateFile(t.Context(), first, "1"))
	files, err = store.GetFileSnapshots(t.Context(), ids)
	require.NoError(t, err)
	require.Equal(t, "fresh", files[1].Body)
	require.Equal(t, "2", files[3].ResourceVersion)
	require.NoError(t, store.DeleteFile(t.Context(), first.UID))
	first.UID = "00000000-0000-0000-0000-000000000153"
	require.NoError(t, store.CreateFile(t.Context(), first))
	files, err = store.GetFileSnapshots(t.Context(), append(ids, first.UID))
	require.NoError(t, err)
	require.Nil(t, files[1])
	require.Nil(t, files[3])
	require.Equal(t, first.UID, files[4].UID)
}

func TestFileSnapshotsBoundAndCancellation(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	ids := make([]string, datastore.FileSnapshotBatchLimit)
	for i := range ids {
		file := &datastore.File{UID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i), Namespace: "bound", Name: fmt.Sprint(i)}
		require.NoError(t, store.CreateFile(t.Context(), file))
		ids[i] = file.UID
	}
	files, err := store.GetFileSnapshots(t.Context(), ids)
	require.NoError(t, err)
	require.Len(t, files, datastore.FileSnapshotBatchLimit)
	for i, file := range files {
		require.Equal(t, ids[i], file.UID)
	}
	files, err = store.GetFileSnapshots(t.Context(), append(ids, "extra"))
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	require.Nil(t, files)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files, err = store.GetFileSnapshots(ctx, ids)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, files)
}

func TestFileNameSnapshotsPreserveBoundsFreshnessAndIsolation(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	first := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000161", Namespace: "first", Name: "file",
		ResourceVersion: "1", Labels: map[string]string{"original": "value"},
		Spec: []byte(`{"value":1}`), Finalizers: []string{"original"},
	}
	second := &datastore.File{UID: "00000000-0000-0000-0000-000000000162", Namespace: "second", Name: "file"}
	require.NoError(t, store.CreateFile(t.Context(), first))
	require.NoError(t, store.CreateFile(t.Context(), second))
	names := []string{"absent", first.Name, first.Name, "other"}
	files, err := store.GetFileSnapshotsByName(t.Context(), first.Namespace, names)
	require.NoError(t, err)
	require.Len(t, files, datastore.FileNameSnapshotBatchLimit)
	require.Nil(t, files[0])
	require.Nil(t, files[3])
	require.Equal(t, first.UID, files[1].UID)
	files[1].Labels["original"], files[1].Spec[0], files[1].Finalizers[0] = "changed", '!', "changed"
	require.Equal(t, "value", files[2].Labels["original"])
	require.JSONEq(t, `{"value":1}`, string(files[2].Spec))
	require.Equal(t, []string{"original"}, files[2].Finalizers)
	first.ResourceVersion, first.Body = "2", "fresh"
	require.NoError(t, store.UpdateFile(t.Context(), first, "1"))
	files, err = store.GetFileSnapshotsByName(t.Context(), first.Namespace, names)
	require.NoError(t, err)
	require.Equal(t, "fresh", files[1].Body)
	require.NoError(t, store.DeleteFile(t.Context(), first.UID))
	first.UID = "00000000-0000-0000-0000-000000000163"
	require.NoError(t, store.CreateFile(t.Context(), first))
	files, err = store.GetFileSnapshotsByName(t.Context(), first.Namespace, names)
	require.NoError(t, err)
	require.Equal(t, first.UID, files[1].UID)
	require.Equal(t, first.UID, files[2].UID)
	files, err = store.GetFileSnapshotsByName(t.Context(), second.Namespace, []string{second.Name})
	require.NoError(t, err)
	require.Equal(t, second.UID, files[0].UID)
	for _, input := range []struct {
		namespace string
		names     []string
	}{{first.Namespace, append(names, "extra")}, {"", names}, {first.Namespace, []string{""}}} {
		files, err = store.GetFileSnapshotsByName(t.Context(), input.namespace, input.names)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		require.Nil(t, files)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files, err = store.GetFileSnapshotsByName(ctx, first.Namespace, names)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, files)
	files, err = store.GetFileSnapshotsByName(t.Context(), first.Namespace, nil)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestFileCRUD(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	spec := []byte(`{"contentType":"image/jpeg","type":"hero","source":{"type":"s3","uri":"s3://bucket/hero","checksum":{"algorithm":"sha256","value":"abc"}}}`)
	ownerRefs := []byte(`[{"kind":"Repository","name":"repo","uid":"owner","repositoryID":"repo-1"}]`)
	file := &datastore.File{UID: "00000000-0000-0000-0000-000000000051", Namespace: "ns", Name: "hero", RepositoryID: "repo-1", APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", Spec: spec, OwnerReferences: ownerRefs}
	require.NoError(t, store.CreateFile(context.Background(), file))
	got, err := store.GetFileByName(context.Background(), "ns", "hero")
	require.NoError(t, err)
	require.Equal(t, file.UID, got.UID)
	require.Equal(t, file.RepositoryID, got.RepositoryID)
	require.Equal(t, spec, []byte(got.Spec))
	require.Equal(t, ownerRefs, []byte(got.OwnerReferences))
	require.NoError(t, store.DeleteFile(context.Background(), file.UID))
	_, err = store.GetFile(context.Background(), file.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestFileStatusUpdateUsesResourceVersionGuard(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000052", Namespace: "ns", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		ResourceVersion: "1", Status: []byte(`{"observedGeneration":1}`),
	}

	require.NoError(t, store.CreateFile(context.Background(), file))
	generation := int64(2)
	updated, err := store.UpdateFileStatus(context.Background(), "ns", "hero", datastore.FileStatusPatch{
		ResourceVersion: "1", ObservedGeneration: &generation,
	})
	require.NoError(t, err)
	require.Equal(t, "2", updated.ResourceVersion)
	_, err = store.UpdateFileStatus(context.Background(), "ns", "hero", datastore.FileStatusPatch{ResourceVersion: "1"})
	require.ErrorIs(t, err, datastore.ErrConflict)
}

func TestFileUpdateUsesResourceVersionGuard(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000054", Namespace: "ns", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", ResourceVersion: "1",
	}
	require.NoError(t, store.CreateFile(context.Background(), file))

	winner := *file
	winner.ResourceVersion = "2"
	winner.Body = "winner"
	require.NoError(t, store.UpdateFile(context.Background(), &winner, "1"))

	stale := *file
	stale.ResourceVersion = "2"
	stale.Body = "stale"
	err = store.UpdateFile(context.Background(), &stale, "1")
	require.ErrorIs(t, err, datastore.ErrConflict)
	durable, err := store.GetFile(context.Background(), file.UID)
	require.NoError(t, err)
	require.Equal(t, "winner", durable.Body)
}

func TestFileOwnerReferenceProjectionIsRepositoryScoped(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	owners, ok := any(store).(datastore.OwnerReferenceStore)
	require.True(t, ok)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000053", Namespace: "ns", Name: "hero",
		RepositoryID: "dependent-repo", ResourceVersion: "1",
		OwnerReferences: []byte(`[{"uid":"owner","kind":"Repository","repositoryID":"owner-repo","blockOwnerDeletion":true}]`),
	}
	require.NoError(t, store.CreateFile(context.Background(), file))
	blocked, err := owners.HasBlockingOwnerDependents(context.Background(), datastore.OwnerReferenceScope{Namespace: "ns", RepositoryID: "owner-repo"}, "owner")
	require.NoError(t, err)
	require.True(t, blocked)
	blocked, err = owners.HasBlockingOwnerDependents(context.Background(), datastore.OwnerReferenceScope{Namespace: "ns", RepositoryID: "dependent-repo"}, "owner")
	require.NoError(t, err)
	require.False(t, blocked)
	file.OwnerReferences = nil
	file.ResourceVersion = "2"
	require.NoError(t, store.UpdateFile(context.Background(), file, "1"))
	blocked, err = owners.HasBlockingOwnerDependents(context.Background(), datastore.OwnerReferenceScope{Namespace: "ns", RepositoryID: "owner-repo"}, "owner")
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestFileJournalRejectsUnserializablePayloadBeforeCommit(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	file := &datastore.File{UID: "00000000-0000-0000-0000-000000000124", Namespace: "ns", Name: "hero", ResourceVersion: "1", Spec: json.RawMessage("{")}
	require.ErrorIs(t, store.CreateFile(t.Context(), file), datastore.ErrInvalidArgument)
	_, err = store.GetFile(t.Context(), file.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	file.Spec = json.RawMessage("{}")
	require.NoError(t, store.CreateFile(t.Context(), file))
	file.Status = json.RawMessage("{")
	file.ResourceVersion = "2"
	require.ErrorIs(t, store.UpdateFile(t.Context(), file, "1"), datastore.ErrInvalidArgument)
	current, err := store.GetFile(t.Context(), file.UID)
	require.NoError(t, err)
	require.Equal(t, "1", current.ResourceVersion)
	events, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestFileJournalRecordsOnlyCommittedWritesInVersionOrder(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	file := &datastore.File{UID: "00000000-0000-0000-0000-000000000099", Namespace: "ns", Name: "journal",
		ResourceVersion: "1", Labels: map[string]string{"team": "a"}}
	require.NoError(t, store.CreateFile(t.Context(), file))
	require.ErrorIs(t, store.CreateFile(t.Context(), file), datastore.ErrAlreadyExists)
	require.ErrorIs(t, store.UpdateFile(t.Context(), file, "1"), datastore.ErrConflict, "same-version replacement must not emit a duplicate")
	file.Labels["team"] = "b"
	file.ResourceVersion = "2"
	require.NoError(t, store.UpdateFile(t.Context(), file, "1"))
	require.ErrorIs(t, store.UpdateFile(t.Context(), file, "1"), datastore.ErrConflict)

	var writers sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		writers.Go(func() {
			for {
				current, err := store.GetFile(t.Context(), file.UID)
				if err != nil {
					failures <- err
					return
				}
				_, err = store.UpdateFileStatus(t.Context(), "ns", "journal", datastore.FileStatusPatch{ResourceVersion: current.ResourceVersion})
				if errors.Is(err, datastore.ErrConflict) {
					continue
				}
				if err != nil {
					failures <- err
				}
				return
			}
		})
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.ErrorIs(t, store.DeleteFileWithResourceVersion(t.Context(), file.UID, "1"), datastore.ErrConflict)
	require.NoError(t, store.DeleteFile(t.Context(), file.UID))
	events, err := journal.ReadAfter(t.Context(), datastore.ResourceWatchCursor{}, 32)
	require.NoError(t, err)
	require.Len(t, events, 11)
	for index, event := range events {
		require.Equal(t, "File", event.Kind)
		require.Equal(t, "ns", event.Namespace)
		require.Equal(t, uint64(index+1), event.Sequence)
		if index == 10 {
			require.Equal(t, datastore.ResourceWatchDeleted, event.Type)
			require.Empty(t, event.Payload)
			require.Equal(t, "b", event.SelectorLabels["team"])
			continue
		}
		var observed datastore.File
		require.NoError(t, json.Unmarshal(event.Payload, &observed))
		require.Equal(t, strconv.Itoa(index+1), observed.ResourceVersion)
	}
	require.Equal(t, "a", events[0].SelectorLabels["team"], "authored object mutation cannot change retained payloads")
	require.Equal(t, "a", events[1].PreviousSelectorLabels["team"])
}
