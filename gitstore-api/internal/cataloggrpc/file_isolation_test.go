// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secondNamespaceRepoID is a distinct repository/namespace pairing used
// alongside newTestDatastore's "gitstore" namespace/testRepoID to exercise
// cross-namespace isolation for File identity (spec 051 T041).
const secondNamespaceRepoID = "00000000-0000-0000-0000-000000000099"

// seedSecondNamespace adds a second namespace ("acme-other") and repository
// to the store returned by newTestDatastore, so a test can admit File
// documents into two different namespaces and assert they never collide or
// leak into one another.
func seedSecondNamespace(t *testing.T, store datastore.Datastore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	ns := &datastore.Namespace{
		ID: uuid.New().String(), Name: "acme-other", Title: "Acme Other",
		Tier: datastore.NamespaceTierUser, CreationTimestamp: now, CreationActor: "test",
		UpdateTimestamp: now, UpdateActor: "test",
	}
	require.NoError(t, store.CreateNamespace(ctx, ns))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID: secondNamespaceRepoID, Namespace: ns.Name, Name: "catalog",
		DefaultBranch: "main", StorageClass: "local",
		CreationTimestamp: now, CreationActor: "test", UpdateTimestamp: now, UpdateActor: "test",
	}))
}

// TestAdmitResources_FileIdentityIsolatedAcrossNamespaces admits a File
// named "hero" into two different namespaces (via two different
// repositories) and verifies each namespace's row is independent: distinct
// UIDs, distinct RepositoryID/body, and neither namespace's ListFiles/
// GetFileByName call can observe the other namespace's record. File
// identity is (namespace, name), so a same-named File in a different
// namespace must never collide or become visible cross-namespace (spec 051
// T041).
func TestAdmitResources_FileIdentityIsolatedAcrossNamespaces(t *testing.T) {
	store := newTestDatastore(t)
	seedSecondNamespace(t, store)

	commitA := "4444444444444444444444444444444444444444"
	gitA := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"files/hero.md"}, nil
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return makeFileWithBody("hero", "gitstore namespace body"), nil
		},
	}
	srvA := newCatalogServer(t, store, gitA)
	_, err := srvA.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, CommitSha: commitA, RefName: "refs/heads/main",
	})
	require.NoError(t, err)

	commitB := "5555555555555555555555555555555555555555"
	gitB := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"files/hero.md"}, nil
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return makeFileWithBody("hero", "acme-other namespace body"), nil
		},
	}
	srvB := newCatalogServer(t, store, gitB)
	_, err = srvB.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: secondNamespaceRepoID, CommitSha: commitB, RefName: "refs/heads/main",
	})
	require.NoError(t, err)

	fileA, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	fileB, err := store.GetFileByName(context.Background(), "acme-other", "hero")
	require.NoError(t, err)

	assert.NotEqual(t, fileA.UID, fileB.UID)
	assert.Equal(t, testRepoID, fileA.RepositoryID)
	assert.Equal(t, secondNamespaceRepoID, fileB.RepositoryID)
	assert.Equal(t, "gitstore namespace body", fileA.Body)
	assert.Equal(t, "acme-other namespace body", fileB.Body)

	pageA, err := store.ListFiles(context.Background(), "gitstore", datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, pageA.Items, 1)
	assert.Equal(t, fileA.UID, pageA.Items[0].UID, "listing one namespace must never surface the other namespace's File")

	pageB, err := store.ListFiles(context.Background(), "acme-other", datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, pageB.Items, 1)
	assert.Equal(t, fileB.UID, pageB.Items[0].UID)
}

// TestAdmitResources_FileCrossNamespaceCredentialsRefNeverPersistedAtAdmission
// pushes a File whose spec.source.credentialsRef.namespace names a
// different namespace than the File's own namespace directly through
// AdmitResources (bypassing the separate, optional ValidateResources
// pre-receive hook entirely). Per ADR-0001/FR-007, cross-namespace SecretRef
// resolution is never permitted; this proves the boundary is enforced at
// the actual write path itself — the File must never become durable — not
// merely surfaced as an advisory validation error a caller could ignore
// (spec 051 T041).
func TestAdmitResources_FileCrossNamespaceCredentialsRefNeverPersistedAtAdmission(t *testing.T) {
	store := newTestDatastore(t)
	commit := "6666666666666666666666666666666666666666"
	content := []byte("---\napiVersion: storage.gitstore.dev/v1beta1\nkind: File\nmetadata:\n  name: leaky\n  namespace: gitstore\nspec:\n  contentType: image/jpeg\n  source:\n    type: s3\n    uri: s3://bucket/leaky\n    credentialsRef:\n      kind: SecretRef\n      name: cloud-creds\n      namespace: acme-other\n---\nShould never be stored\n")
	git := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"files/leaky.md"}, nil
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return content, nil
		},
	}
	srv := newCatalogServer(t, store, git)

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, CommitSha: commit, RefName: "refs/heads/main",
	})
	require.NoError(t, err, "admission must tolerate the rejected file without failing the whole push")

	_, err = store.GetFileByName(context.Background(), "gitstore", "leaky")
	require.ErrorIs(t, err, datastore.ErrNotFound, "a cross-namespace credentialsRef must never result in a durable File row")
}
