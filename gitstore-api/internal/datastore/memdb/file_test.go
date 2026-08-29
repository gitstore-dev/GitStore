// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

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
	require.NoError(t, store.UpdateFile(context.Background(), file))
	blocked, err = owners.HasBlockingOwnerDependents(context.Background(), datastore.OwnerReferenceScope{Namespace: "ns", RepositoryID: "owner-repo"}, "owner")
	require.NoError(t, err)
	require.False(t, blocked)
}
