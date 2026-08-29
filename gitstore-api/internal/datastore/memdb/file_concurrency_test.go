// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFileConcurrentCreateSameIdentity_ExactlyOneWinnerPersists simulates
// multiple API replicas racing to admit the same brand-new File identity
// (namespace/name) concurrently against a single shared durable store — the
// same failure mode a duplicate git push webhook delivery, or two replicas
// both handling the first push of a File, would trigger in production
// (spec 051 T039). Exactly one CreateFile call must win; every other
// concurrent caller must observe datastore.ErrAlreadyExists rather than a
// silently duplicated row.
func TestFileConcurrentCreateSameIdentity_ExactlyOneWinnerPersists(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()

	const namespace, name = "ns", "hero"
	const attempts = 8

	var wg sync.WaitGroup
	var mu sync.Mutex
	var successes []string
	var conflicts int

	for i := 0; i < attempts; i++ {
		uid := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			err := store.CreateFile(context.Background(), &datastore.File{
				UID: uid, Namespace: namespace, Name: name,
				APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
				Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now(),
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes = append(successes, uid)
				return
			}
			if errors.Is(err, datastore.ErrAlreadyExists) {
				conflicts++
			}
		}(uid)
	}
	wg.Wait()

	require.Len(t, successes, 1, "exactly one concurrent create must win the namespace/name identity race")
	assert.Equal(t, attempts-1, conflicts, "every other concurrent create must observe ErrAlreadyExists")

	got, err := store.GetFileByName(context.Background(), namespace, name)
	require.NoError(t, err)
	assert.Equal(t, successes[0], got.UID)

	page, err := store.ListFiles(context.Background(), namespace, datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "the race must never leave more than one durable row for the identity")
}

// TestFileOwnerReferenceProjection_PreOwnerReferencesRecordRemainsReadable is
// the File-shaped analogue of owner_references_test.go's
// TestOwnerReferenceProjectionCapsProductPagesAndIgnoresLegacyRecords "pre-
// ownerReferences record from a rolling upgrade remains readable" case —
// the worked example this repo's production-readiness runbook
// (docs/runbooks/production-readiness-testing.md, Pattern 3) cites for
// rolling-upgrade coverage. It constructs a File with OwnerReferences,
// Spec, and Status left entirely unset (Go zero values) — exactly what a
// record predating those fields, or written by a code path that never
// populates them, would look like on disk — and asserts the *current*
// OwnerReferenceStore and CRUD paths read it back without error and
// without a false-positive blocking dependent (spec 051 T040).
func TestFileOwnerReferenceProjection_PreOwnerReferencesRecordRemainsReadable(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()
	owners, ok := any(store).(datastore.OwnerReferenceStore)
	require.True(t, ok)

	legacy := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000300", Namespace: "ns", Name: "legacy-hero",
		RepositoryID: "repo-1", ResourceVersion: "1", CreationTimestamp: time.Now(),
		// OwnerReferences, Spec, and Status are deliberately never set.
	}
	require.NoError(t, store.CreateFile(context.Background(), legacy))

	got, err := store.GetFileByName(context.Background(), "ns", "legacy-hero")
	require.NoError(t, err)
	assert.Nil(t, got.OwnerReferences)
	assert.Nil(t, got.Spec)
	assert.Nil(t, got.Status)

	blocking, err := owners.HasBlockingOwnerDependents(context.Background(), datastore.OwnerReferenceScope{Namespace: "ns", RepositoryID: "repo-1"}, "any-owner-uid")
	require.NoError(t, err)
	assert.False(t, blocking, "a legacy File with no OwnerReferences must never be misread as a blocking dependent")
}

// TestFileConcurrentStatusUpdatesAreLinearizedByResourceVersion simulates two
// or more replicas (or a replica racing its own retry) applying a status
// patch built from the same stale resourceVersion snapshot concurrently.
// UpdateFileStatus's optimistic-concurrency precondition must serialize the
// writes: exactly one caller succeeds and every other caller observes
// datastore.ErrConflict, never a silently lost update (spec 051 T039).
func TestFileConcurrentStatusUpdatesAreLinearizedByResourceVersion(t *testing.T) {
	store, err := New()
	require.NoError(t, err)
	defer store.Close()

	const namespace, name = "ns", "hero"
	require.NoError(t, store.CreateFile(context.Background(), &datastore.File{
		UID: "00000000-0000-0000-0000-000000000099", Namespace: namespace, Name: name,
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		ResourceVersion: "1", CreationTimestamp: time.Now(),
		Status: []byte(`{"observedGeneration":0}`),
	}))

	const attempts = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var successes, conflicts int

	for i := 0; i < attempts; i++ {
		gen := int64(i + 1)
		wg.Add(1)
		go func(gen int64) {
			defer wg.Done()
			_, err := store.UpdateFileStatus(context.Background(), namespace, name, datastore.FileStatusPatch{
				ResourceVersion: "1", ObservedGeneration: &gen,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, datastore.ErrConflict):
				conflicts++
			}
		}(gen)
	}
	wg.Wait()

	assert.Equal(t, 1, successes, "only one concurrent status write from the same base resourceVersion may succeed")
	assert.Equal(t, attempts-1, conflicts, "every other concurrent status write must observe ErrConflict")

	got, err := store.GetFileByName(context.Background(), namespace, name)
	require.NoError(t, err)
	assert.Equal(t, "2", got.ResourceVersion, "resourceVersion must advance exactly once regardless of contention")
}
