// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingNamespaceWriteStore struct {
	datastore.Datastore
	shouldBlock func(*datastore.Namespace) bool
	started     chan struct{}
	release     chan struct{}
	once        sync.Once
	conflicts   atomic.Int64
}

func (s *blockingNamespaceWriteStore) UpdateNamespace(
	ctx context.Context,
	namespace *datastore.Namespace,
	expectedResourceVersion string,
) error {
	if s.shouldBlock != nil && s.shouldBlock(namespace) {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := s.Datastore.UpdateNamespace(ctx, namespace, expectedResourceVersion)
	if errors.Is(err, datastore.ErrConflict) {
		s.conflicts.Add(1)
	}
	return err
}

func (s *blockingNamespaceWriteStore) MarkNamespaceDeletion(
	ctx context.Context,
	namespace *datastore.Namespace,
	expectedResourceVersion string,
) error {
	if s.shouldBlock != nil && s.shouldBlock(namespace) {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := s.Datastore.MarkNamespaceDeletion(ctx, namespace, expectedResourceVersion)
	if errors.Is(err, datastore.ErrConflict) {
		s.conflicts.Add(1)
	}
	return err
}

func TestNamespaceReplicaStaleUpdateCannotOverwriteConcurrentDeletion(t *testing.T) {
	base := newNamespacePolicyDatastore(t)
	name := "update-delete-race"
	oldCommit := strings.Repeat("a", 40)
	newCommit := strings.Repeat("b", 40)
	path := "namespaces/" + name + ".md"
	seedNamespaceForReplicaRace(t, base, name, "Original", oldCommit, path)

	store := &blockingNamespaceWriteStore{
		Datastore: base,
		shouldBlock: func(namespace *datastore.Namespace) bool {
			return namespace.Name == name &&
				namespace.DeletionTimestamp == nil &&
				namespace.Title == "Stale Update"
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	current := newCommit
	git := newTreeGitReader(&current, map[string]map[string][]byte{
		oldCommit: {path: namespaceManifest(name, "Original", "USER")},
		newCommit: {path: namespaceManifest(name, "Stale Update", "ORGANIZATION")},
	})
	updateReplica := newCatalogServer(t, store, git)

	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := updateReplica.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
			ActorSubject: "test-admission-actor",
			RepositoryId: testRepoID,
			OldCommitSha: oldCommit,
			NewCommitSha: newCommit,

			RefName:      "refs/heads/main",
			ChangedPaths: []string{path},
		})
		updateDone <- updateErr
	}()

	waitForReplicaRace(t, store.started, "stale update did not reach its conditional write")
	authorized, err := base.GetNamespaceByName(context.Background(), name)
	require.NoError(t, err)
	err = admission.MarkInfrastructureDeletion(context.Background(), store, authorized, time.Now().UTC(), "alice")
	require.NoError(t, err)
	close(store.release)
	require.NoError(t, waitForReplicaResult(t, updateDone))

	got, err := base.GetNamespaceByName(context.Background(), name)
	require.NoError(t, err)
	require.NotNil(t, got.DeletionTimestamp)
	assert.Equal(t, "Original", got.Title, "the stale policy decision must not overwrite the deletion winner")
	assert.Equal(t, "2", got.ResourceVersion)
	assert.Equal(t, int64(1), store.conflicts.Load(), "the stale update must observe exactly one resource-version conflict")
}

func TestNamespaceReplicaStaleDeleteCannotOverwriteConcurrentUpdate(t *testing.T) {
	base := newNamespacePolicyDatastore(t)
	name := "delete-update-race"
	oldCommit := strings.Repeat("c", 40)
	newCommit := strings.Repeat("d", 40)
	path := "namespaces/" + name + ".md"
	seedNamespaceForReplicaRace(t, base, name, "Original", oldCommit, path)

	store := &blockingNamespaceWriteStore{
		Datastore: base,
		shouldBlock: func(namespace *datastore.Namespace) bool {
			return namespace.Name == name && namespace.DeletionTimestamp != nil
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	current := newCommit
	updateReplica := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		oldCommit: {path: namespaceManifest(name, "Original", "USER")},
		newCommit: {path: namespaceManifest(name, "Winning Update", "ORGANIZATION")},
	}))

	authorized, err := base.GetNamespaceByName(context.Background(), name)
	require.NoError(t, err)
	deleteDone := make(chan error, 1)
	go func() {
		deleteErr := admission.MarkInfrastructureDeletion(context.Background(), store, authorized, time.Now().UTC(), "alice")
		deleteDone <- deleteErr
	}()

	waitForReplicaRace(t, store.started, "stale delete did not reach its conditional write")
	_, err = updateReplica.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		ActorSubject: "test-admission-actor",
		RepositoryId: testRepoID,
		OldCommitSha: oldCommit,
		NewCommitSha: newCommit,

		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
	})
	require.NoError(t, err)
	close(store.release)
	deleteErr := waitForReplicaResult(t, deleteDone)
	require.ErrorIs(t, deleteErr, datastore.ErrConflict)

	got, err := base.GetNamespaceByName(context.Background(), name)
	require.NoError(t, err)
	assert.Nil(t, got.DeletionTimestamp, "the stale deletion decision must conflict instead of marking the updated row")
	assert.Equal(t, "Winning Update", got.Title)
	assert.Equal(t, newCommit, got.GitCommitSHA)
	assert.Equal(t, "2", got.ResourceVersion)
	assert.Equal(t, int64(1), store.conflicts.Load(), "the stale delete must observe exactly one resource-version conflict")
}

func seedNamespaceForReplicaRace(
	t *testing.T,
	store datastore.Datastore,
	name, title, commit, path string,
) {
	t.Helper()
	now := time.Now().UTC()
	namespace := &datastore.Namespace{
		UID:               uuid.NewString(),
		Name:              name,
		Title:             title,
		Tier:              datastore.NamespaceTierUser,
		Generation:        1,
		ResourceVersion:   "1",
		Revision:          "main@sha1:" + commit,
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now,
		UpdateActor:       "alice",
		SourcePath:        path,
		GitCommitSHA:      commit,
		GitRef:            "refs/heads/main",
	}
	datastore.NormalizeNamespaceContract(namespace)
	require.NoError(t, store.CreateNamespace(context.Background(), namespace))
}

func waitForReplicaRace(t *testing.T, started <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func waitForReplicaResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("replica race did not complete")
		return nil
	}
}
