// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemdb_NamespaceContractRoundTripAndConflict(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	ns := &datastore.Namespace{
		UID:               "00000000-0000-0000-0000-000000000111",
		Name:              "versioned-namespace",
		Title:             "Versioned Namespace",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: time.Now().UTC(),
		CreationActor:     "test",
		UpdateTimestamp:   time.Now().UTC(),
		UpdateActor:       "test",
		Spec:              json.RawMessage(`{"tier":"user"}`),
		Body:              "# Namespace body\n",
	}
	require.NoError(t, ds.CreateNamespace(ctx, ns))

	first, err := ds.GetNamespace(ctx, ns.UID)
	require.NoError(t, err)
	stale, err := ds.GetNamespace(ctx, ns.UID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.Generation)
	assert.Equal(t, "1", first.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(first.Status))
	assert.JSONEq(t, `{"tier":"user"}`, string(first.Spec))
	assert.Equal(t, "# Namespace body\n", first.Body)

	first.Title = "First writer"
	first.Body = "# Updated namespace body\n"
	datastore.AdvanceNamespaceSpecVersion(first)
	require.NoError(t, ds.UpdateNamespace(ctx, first, "1"))

	stale.Title = "Stale writer"
	datastore.AdvanceNamespaceSpecVersion(stale)
	require.ErrorIs(t, ds.UpdateNamespace(ctx, stale, "1"), datastore.ErrConflict)

	got, err := ds.GetNamespace(ctx, ns.UID)
	require.NoError(t, err)
	assert.Equal(t, "First writer", got.Title)
	assert.Equal(t, int64(2), got.Generation)
	assert.Equal(t, "2", got.ResourceVersion)
	assert.Equal(t, "# Updated namespace body\n", got.Body)
}

func TestMemdb_NamespaceFullEnvelopeAndBodyAreDeepCopied(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	originalDeletedAt := deletedAt
	namespace := &datastore.Namespace{
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Namespace",
		UID:               "00000000-0000-0000-0000-000000000112",
		Name:              "full-envelope",
		Generation:        3,
		ResourceVersion:   "7",
		Revision:          "main@sha1:abc",
		CreationTimestamp: deletedAt.Add(-time.Hour),
		CreationActor:     "creator",
		UpdateTimestamp:   deletedAt.Add(-time.Minute),
		UpdateActor:       "updater",
		Labels:            map[string]string{"tier": "gold"},
		Annotations:       map[string]string{"note": "original"},
		OwnerReferences:   json.RawMessage(`[{"uid":"owner"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		SourcePath:        "namespaces/full-envelope.md",
		GitCommitSHA:      "abc",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"title":"Full Envelope"}`),
		Body:              "# Namespace\n",
		Status:            json.RawMessage(`{"observedGeneration":3}`),
		Title:             "Full Envelope",
		Tier:              datastore.NamespaceTierOrganization,
	}
	require.NoError(t, ds.CreateNamespace(ctx, namespace))

	namespace.Labels["tier"] = "mutated"
	namespace.Annotations["note"] = "mutated"
	namespace.OwnerReferences[2] = 'X'
	namespace.Finalizers[0] = "mutated"
	namespace.Spec[2] = 'X'
	namespace.Status[2] = 'X'
	*namespace.DeletionTimestamp = deletedAt.Add(time.Hour)

	got, err := ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	assert.Equal(t, "gold", got.Labels["tier"])
	assert.Equal(t, "original", got.Annotations["note"])
	assert.JSONEq(t, `[{"uid":"owner"}]`, string(got.OwnerReferences))
	assert.Equal(t, []string{"gitstore.dev/test"}, got.Finalizers)
	assert.JSONEq(t, `{"title":"Full Envelope"}`, string(got.Spec))
	assert.JSONEq(t, `{"observedGeneration":3}`, string(got.Status))
	assert.Equal(t, originalDeletedAt, *got.DeletionTimestamp)
	assert.Equal(t, "# Namespace\n", got.Body)

	listed, err := ds.ListNamespaces(ctx, datastore.PageParams{First: 10})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	assert.Equal(t, "# Namespace\n", listed.Items[0].Body)
	assert.Equal(t, namespace.SourcePath, listed.Items[0].SourcePath)

	got.Labels["tier"] = "read-mutated"
	got.OwnerReferences[2] = 'Y'
	got.Finalizers[0] = "read-mutated"
	got.Spec[2] = 'Y'
	got.Status[2] = 'Y'
	*got.DeletionTimestamp = deletedAt.Add(2 * time.Hour)

	again, err := ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	assert.Equal(t, "gold", again.Labels["tier"])
	assert.JSONEq(t, `[{"uid":"owner"}]`, string(again.OwnerReferences))
	assert.Equal(t, []string{"gitstore.dev/test"}, again.Finalizers)
	assert.JSONEq(t, `{"title":"Full Envelope"}`, string(again.Spec))
	assert.JSONEq(t, `{"observedGeneration":3}`, string(again.Status))
	assert.Equal(t, originalDeletedAt, *again.DeletionTimestamp)
}

func TestMemdb_NamespaceDuplicateUIDAndName(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	first := &datastore.Namespace{
		UID:               "00000000-0000-0000-0000-000000000113",
		Name:              "unique-namespace",
		CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, ds.CreateNamespace(ctx, first))

	duplicateUID := *first
	duplicateUID.Name = "different-name"
	require.ErrorIs(t, ds.CreateNamespace(ctx, &duplicateUID), datastore.ErrAlreadyExists)

	duplicateName := *first
	duplicateName.UID = "00000000-0000-0000-0000-000000000114"
	require.ErrorIs(t, ds.CreateNamespace(ctx, &duplicateName), datastore.ErrAlreadyExists)
}

func TestMemdb_NamespaceRepositoryLifecycleCoordination(t *testing.T) {
	ds := newBackend(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               "00000000-0000-0000-0000-000000000115",
		Name:              "repository-lifecycle",
		CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, ds.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{
		UID:               "00000000-0000-0000-0000-000000000116",
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC(),
	}

	require.NoError(t, ds.CreateRepositoryInActiveNamespace(ctx, repository))
	current, err := ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	deletedAt := time.Now().UTC()
	current.DeletionTimestamp = &deletedAt
	expectedResourceVersion := current.ResourceVersion
	datastore.AdvanceNamespaceSystemVersion(current)
	require.ErrorIs(t, ds.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrNamespaceNotEmpty)

	require.NoError(t, ds.DeleteRepository(ctx, repository.UID))
	current, err = ds.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion = current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, ds.MarkNamespaceDeletion(ctx, current, expectedResourceVersion))

	lateRepository := *repository
	lateRepository.UID = "00000000-0000-0000-0000-000000000117"
	lateRepository.Name = "late"
	require.ErrorIs(t, ds.CreateRepositoryInActiveNamespace(ctx, &lateRepository), datastore.ErrNamespaceNotActive)
}

func TestMemdb_RepositoryTransferAndTargetTerminationHaveOneWinner(t *testing.T) {
	const trials = 50
	for trial := 0; trial < trials; trial++ {
		trial := trial
		t.Run(fmt.Sprintf("trial-%d", trial), func(t *testing.T) {
			t.Parallel()
			ds := newBackend(t)
			ctx := context.Background()
			source := &datastore.Namespace{
				UID:               fmt.Sprintf("10000000-0000-0000-0000-%012d", trial*3+1),
				Name:              fmt.Sprintf("transfer-source-%d", trial),
				CreationTimestamp: time.Now().UTC(),
			}
			target := &datastore.Namespace{
				UID:               fmt.Sprintf("10000000-0000-0000-0000-%012d", trial*3+2),
				Name:              fmt.Sprintf("transfer-target-%d", trial),
				CreationTimestamp: time.Now().UTC(),
			}
			require.NoError(t, ds.CreateNamespace(ctx, source))
			require.NoError(t, ds.CreateNamespace(ctx, target))
			repository := &datastore.Repository{
				UID:               fmt.Sprintf("10000000-0000-0000-0000-%012d", trial*3+3),
				Namespace:         source.Name,
				Name:              "catalog",
				CreationTimestamp: time.Now().UTC(),
			}
			require.NoError(t, ds.CreateRepositoryInActiveNamespace(ctx, repository))
			require.NoError(t, ds.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
				Namespace:    source.Name,
				Name:         repository.Name,
				RepositoryID: repository.UID,
			}))

			currentTarget, err := ds.GetNamespace(ctx, target.UID)
			require.NoError(t, err)
			expectedResourceVersion := currentTarget.ResourceVersion
			deletedAt := time.Now().UTC()
			currentTarget.DeletionTimestamp = &deletedAt
			datastore.AdvanceNamespaceSystemVersion(currentTarget)

			start := make(chan struct{})
			results := make(chan error, 2)
			var workers sync.WaitGroup
			workers.Add(2)
			go func() {
				defer workers.Done()
				<-start
				results <- ds.TransferRepository(ctx, repository.UID, source.Name, target.Name)
			}()
			go func() {
				defer workers.Done()
				<-start
				results <- ds.MarkNamespaceDeletion(ctx, currentTarget, expectedResourceVersion)
			}()
			close(start)
			workers.Wait()
			close(results)

			var succeeded int
			var failed error
			for err := range results {
				if err == nil {
					succeeded++
				} else {
					failed = err
				}
			}
			require.Equal(t, 1, succeeded)
			require.True(t,
				errors.Is(failed, datastore.ErrNamespaceNotActive) ||
					errors.Is(failed, datastore.ErrNamespaceNotEmpty),
				"unexpected losing error: %v", failed,
			)

			persistedRepository, err := ds.GetRepository(ctx, repository.UID)
			require.NoError(t, err)
			persistedTarget, err := ds.GetNamespace(ctx, target.UID)
			require.NoError(t, err)
			if persistedRepository.Namespace == target.Name {
				assert.Nil(t, persistedTarget.DeletionTimestamp)
			} else {
				assert.Equal(t, source.Name, persistedRepository.Namespace)
				assert.NotNil(t, persistedTarget.DeletionTimestamp)
			}
		})
	}
}
