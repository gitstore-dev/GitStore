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
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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
	deleteReplica, err := resolver.NewService(resolver.ServiceDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)

	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := updateReplica.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
			RepositoryId: testRepoID,
			OldCommitSha: oldCommit,
			NewCommitSha: newCommit,
			CommitSha:    newCommit,
			RefName:      "refs/heads/main",
			ChangedPaths: []string{path},
		})
		updateDone <- updateErr
	}()

	waitForReplicaRace(t, store.started, "stale update did not reach its conditional write")
	authorized, err := base.GetNamespaceByName(context.Background(), name)
	require.NoError(t, err)
	outcome, err := deleteReplica.DeleteNamespace(context.Background(), authorized)
	require.NoError(t, err)
	assert.Equal(t, "TERMINATION_STARTED", string(outcome))
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
	registry := prometheus.NewRegistry()
	metrics := namespaceadmission.NewMetrics(registry)
	core, logs := observer.New(zapcore.InfoLevel)
	deleteReplica, err := resolver.NewService(resolver.ServiceDeps{
		Store:            store,
		Logger:           zap.New(core),
		NamespaceMetrics: metrics,
	})
	require.NoError(t, err)
	current := newCommit
	updateReplica := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		oldCommit: {path: namespaceManifest(name, "Original", "USER")},
		newCommit: {path: namespaceManifest(name, "Winning Update", "ORGANIZATION")},
	}))

	authorized, err := base.GetNamespaceByName(context.Background(), name)
	require.NoError(t, err)
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := deleteReplica.DeleteNamespace(context.Background(), authorized)
		deleteDone <- deleteErr
	}()

	waitForReplicaRace(t, store.started, "stale delete did not reach its conditional write")
	_, err = updateReplica.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: oldCommit,
		NewCommitSha: newCommit,
		CommitSha:    newCommit,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
	})
	require.NoError(t, err)
	close(store.release)
	deleteErr := waitForReplicaResult(t, deleteDone)
	var graphErr *gqlerror.Error
	require.ErrorAs(t, deleteErr, &graphErr)
	assert.Equal(t, namespaceadmission.CodeConflict, graphErr.Extensions["code"])
	assert.Equal(t, string(namespaceadmission.PhasePolicy), graphErr.Extensions["phase"])
	assert.Equal(t, string(namespaceadmission.ReasonResourceVersionConflict), graphErr.Extensions["reason"])

	got, err := base.GetNamespaceByName(context.Background(), name)
	require.NoError(t, err)
	assert.Nil(t, got.DeletionTimestamp, "the stale deletion decision must conflict instead of marking the updated row")
	assert.Equal(t, "Winning Update", got.Title)
	assert.Equal(t, newCommit, got.GitCommitSHA)
	assert.Equal(t, "2", got.ResourceVersion)
	assert.Equal(t, int64(1), store.conflicts.Load(), "the stale delete must observe exactly one resource-version conflict")
	assert.Equal(t, float64(1), namespaceRejectionCount(t, registry,
		namespaceadmission.PhasePolicy, namespaceadmission.ReasonResourceVersionConflict))

	conflictLogs := logs.FilterMessage("Namespace mutation rejected").All()
	require.Len(t, conflictLogs, 1)
	fields := conflictLogs[0].ContextMap()
	assert.Equal(t, "delete", fields["operation"])
	assert.Equal(t, string(namespaceadmission.PhasePolicy), fields["phase"])
	assert.Equal(t, string(namespaceadmission.ReasonResourceVersionConflict), fields["reason"])
	assert.Equal(t, name, fields["namespace"])
	assert.Equal(t, true, fields["conflict"])
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
