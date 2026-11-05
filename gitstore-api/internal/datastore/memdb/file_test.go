// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestFileCRUD(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	spec := []byte(`{"contentType":"image/jpeg","type":"hero","provider":"s3","source":{"uri":"s3://bucket/hero","checksum":{"algorithm":"sha256","value":"abc"}}}`)
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

func TestFileSnapshotsPreservePositionsAndIsolateCopies(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000151", Namespace: "ns", Name: "hero",
		ResourceVersion: "1", Labels: map[string]string{"team": "original"},
		Spec: json.RawMessage(`{"type":"hero"}`), Finalizers: []string{"keep"},
	}
	require.NoError(t, store.CreateFile(t.Context(), file))
	other := *file
	other.UID = "00000000-0000-0000-0000-000000000152"
	other.Namespace = "other"
	require.NoError(t, store.CreateFile(t.Context(), &other))
	for _, mode := range []string{"uid", "name"} {
		t.Run(mode, func(t *testing.T) {
			read := func() ([]*datastore.File, error) {
				if mode == "uid" {
					return store.GetFileSnapshots(t.Context(), []string{file.UID, "invalid", "00000000-0000-0000-0000-000000000153", file.UID})
				}
				return store.GetFileSnapshotsByName(t.Context(), "ns", []string{"hero", "absent", "missing", "hero"})
			}
			got, err := read()
			require.NoError(t, err)
			require.Len(t, got, 4)
			require.Nil(t, got[1])
			require.Nil(t, got[2])
			require.Equal(t, "ns", got[0].Namespace)
			require.Equal(t, file.UID, got[3].UID)
			got[0].Labels["team"] = "changed"
			got[0].Spec[0] = '!'
			got[0].Finalizers[0] = "changed"
			require.Equal(t, "original", got[3].Labels["team"])
			require.JSONEq(t, `{"type":"hero"}`, string(got[3].Spec))
			require.Equal(t, []string{"keep"}, got[3].Finalizers)
			fresh, err := read()
			require.NoError(t, err)
			require.Equal(t, "original", fresh[0].Labels["team"])
			require.JSONEq(t, `{"type":"hero"}`, string(fresh[0].Spec))
		})
	}
	file.ResourceVersion, file.Body = "2", "updated"
	require.NoError(t, store.UpdateFile(t.Context(), file, "1"))
	for _, read := range []func() ([]*datastore.File, error){
		func() ([]*datastore.File, error) { return store.GetFileSnapshots(t.Context(), []string{file.UID}) },
		func() ([]*datastore.File, error) {
			return store.GetFileSnapshotsByName(t.Context(), "ns", []string{"hero"})
		},
	} {
		got, err := read()
		require.NoError(t, err)
		require.Equal(t, "2", got[0].ResourceVersion)
		require.Equal(t, "updated", got[0].Body)
	}
}

func TestFileSnapshotsRejectUnboundedAndCanceledReads(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	_, err = store.GetFileSnapshots(t.Context(), make([]string, datastore.FileSnapshotBatchLimit+1))
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	_, err = store.GetFileSnapshotsByName(t.Context(), "ns", make([]string, datastore.FileNameSnapshotBatchLimit+1))
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	_, err = store.GetFileSnapshotsByName(t.Context(), "", nil)
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	_, err = store.GetFileSnapshotsByName(t.Context(), "ns", []string{""})
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.GetFileSnapshots(ctx, []string{"00000000-0000-0000-0000-000000000151"})
	require.ErrorIs(t, err, context.Canceled)
	_, err = store.GetFileSnapshotsByName(ctx, "ns", []string{"hero"})
	require.ErrorIs(t, err, context.Canceled)
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
	require.NoError(t, store.UpdateFile(t.Context(), file, "1"), "no-op must not emit a duplicate")
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
