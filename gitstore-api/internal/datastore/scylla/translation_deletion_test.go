// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type translationDeletionStore interface {
	translationMutationStore
	DeleteTranslation(context.Context, string, string) error
}

func TestTranslationDeletionRetainsIdentityFences(t *testing.T) {
	first, second := newTestStores(t)
	a, ok := first.(translationDeletionStore)
	require.True(t, ok, "source deletion must share durable source coordination")
	b := second.(translationDeletionStore)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, a.CreateTranslation(t.Context(), source))
	require.ErrorIs(t, b.DeleteTranslation(t.Context(), source.UID, "1"), datastore.ErrConflict, "finalizers prevent final deletion")
	next := *source
	next.ResourceVersion, next.Finalizers = "2", nil
	require.NoError(t, a.UpdateTranslation(t.Context(), &next, "1"))
	require.ErrorIs(t, b.DeleteTranslation(t.Context(), source.UID, "1"), datastore.ErrConflict)
	require.NoError(t, b.DeleteTranslation(t.Context(), source.UID, "2"))
	_, err := a.GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = a.GetTranslationBinding(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = a.GetTranslationByName(t.Context(), source.Namespace, source.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	require.ErrorIs(t, a.CreateTranslation(t.Context(), source), datastore.ErrAlreadyExists)
	require.ErrorIs(t, a.UpdateTranslation(t.Context(), &next, "1"), datastore.ErrNotFound)
	require.NoError(t, b.RecoverTranslation(t.Context(), source.UID), "a lost deletion acknowledgement is recoverable")
	replacement := *source
	replacement.UID = uuid.NewString()
	require.NoError(t, a.CreateTranslation(t.Context(), &replacement))
	require.NoError(t, b.RecoverTranslation(t.Context(), source.UID))
	current, err := b.GetTranslationByName(t.Context(), source.Namespace, source.Name)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, current.UID)
}

func TestTranslationDeletionRecoversPersistedBoundaries(t *testing.T) {
	first, second := newTestStores(t)
	a, ok := first.(translationDeletionStore)
	require.True(t, ok)
	b := second.(translationDeletionStore)
	indexes := first.(translationMutationIndexes)
	raw := newRawSession(t)
	for _, boundary := range []string{"intent", "dependents", "vacancy", "retired", "locator"} {
		t.Run(boundary, func(t *testing.T) {
			source := newTranslationCreationSource(t, first)
			source.Finalizers = nil
			require.NoError(t, a.CreateTranslation(t.Context(), source))
			namespace, uid := gocql.UUID(uuid.MustParse(source.NamespaceID)), gocql.UUID(uuid.MustParse(source.UID))
			var previousRaw string
			require.NoError(t, raw.Query(
				"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
				namespace, source.CreationTimestamp, uid,
			).Scan(&previousRaw))
			var placements []datastore.TranslationIndexReservation
			require.NoError(t, json.Unmarshal([]byte(previousRaw), &placements))
			vacancy := uuid.NewString()
			frozen, err := json.Marshal(struct {
				Placements   []datastore.TranslationIndexReservation
				VacancyToken string
			}{placements, vacancy})
			require.NoError(t, err)
			require.NoError(t, raw.Query(
				"UPDATE translations_by_key SET phase='Deleting',coordination_version='3',maintenance_intent=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
				string(frozen), namespace, source.CreationTimestamp, uid,
			).Exec())
			_, err = b.GetTranslation(t.Context(), source.UID)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			binding, err := datastore.PrepareTranslationBinding(source, nil)
			require.NoError(t, err)
			if boundary != "intent" {
				for _, old := range placements[1:] {
					require.NoError(t, indexes.RemoveTranslationIndex(t.Context(), old))
				}
			}
			if boundary == "vacancy" || boundary == "retired" || boundary == "locator" {
				digest := sha256.Sum256([]byte(source.Name))
				name := hex.EncodeToString(digest[:])
				require.NoError(t, raw.Query(
					"UPDATE translations_by_name SET phase='Vacant',reservation_token=? WHERE namespace_uid=? AND name=?",
					gocql.UUID(uuid.MustParse(vacancy)), namespace, name,
				).Exec())
			}
			if boundary == "retired" || boundary == "locator" {
				require.NoError(t, raw.Query(
					"UPDATE translations_by_key SET phase='Retired',coordination_version='4' WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
					namespace, source.CreationTimestamp, uid,
				).Exec())
			}
			if boundary == "locator" {
				require.NoError(t, raw.Query("UPDATE translations_by_uid SET retired=true WHERE uid=?", uid).Exec())
			}
			newSource := *source
			newSource.UID = uuid.NewString()
			if boundary == "vacancy" {
				require.NoError(t, b.CreateTranslation(t.Context(), &newSource), "replacement may claim the new vacancy before old cleanup finishes")
			}
			replacement := newTestStore(t).(translationDeletionStore)
			var workers sync.WaitGroup
			outcomes := make(chan error, 2)
			for index, worker := range []translationDeletionStore{b, replacement} {
				workers.Go(func() {
					var err error
					for range 16 {
						if index == 0 {
							err = worker.RecoverTranslation(t.Context(), source.UID)
						} else {
							err = recoverTranslationThroughAudit(t.Context(), worker.(datastore.Datastore), source.UID)
						}
						if !errors.Is(err, datastore.ErrConflict) {
							break
						}
					}
					outcomes <- err
				})
			}
			workers.Wait()
			close(outcomes)
			for err := range outcomes {
				require.NoError(t, err)
			}
			_, err = replacement.GetTranslation(t.Context(), source.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			for _, edge := range translationMutationDependents(t, indexes, binding.Epochs[0].Target) {
				require.NotEqual(t, source.UID, edge.TranslationUID)
			}
			for _, old := range placements {
				require.ErrorIs(t, indexes.CommitTranslationIndex(t.Context(), old), datastore.ErrConflict, "delayed publishers cannot resurrect a deleted source")
			}
			var body, spec, intent, bindingSnapshot string
			var rawSource []byte
			require.NoError(t, raw.Query(
				"SELECT body,spec,raw_source,maintenance_intent,binding_snapshot FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
				namespace, source.CreationTimestamp, uid,
			).Scan(&body, &spec, &rawSource, &intent, &bindingSnapshot))
			require.Empty(t, body)
			require.Empty(t, spec)
			require.Empty(t, rawSource)
			require.Empty(t, intent)
			require.Empty(t, bindingSnapshot)
			if boundary != "vacancy" {
				require.NoError(t, replacement.CreateTranslation(t.Context(), &newSource))
			}
			require.NoError(t, a.RecoverTranslation(t.Context(), source.UID))
			current, err := b.GetTranslationByName(t.Context(), source.Namespace, source.Name)
			require.NoError(t, err)
			require.Equal(t, newSource.UID, current.UID)
		})
	}
}

func TestTranslationDeletionAndSourceWriteSerialize(t *testing.T) {
	first, second := newTestStores(t)
	a, b := first.(translationDeletionStore), second.(translationDeletionStore)
	for range 4 {
		source := newTranslationCreationSource(t, first)
		source.Finalizers = nil
		require.NoError(t, a.CreateTranslation(t.Context(), source))
		next := *source
		next.ResourceVersion, next.Generation, next.Body = "2", 2, "new source body"
		start, outcomes := make(chan struct{}), make(chan error, 2)
		go func() {
			<-start
			outcomes <- a.DeleteTranslation(t.Context(), source.UID, "1")
		}()
		go func() {
			<-start
			outcomes <- b.UpdateTranslation(t.Context(), &next, "1")
		}()
		close(start)
		successes := 0
		for range 2 {
			err := <-outcomes
			if err == nil {
				successes++
			} else {
				require.True(t, errors.Is(err, datastore.ErrConflict) || errors.Is(err, datastore.ErrNotFound), "%v", err)
			}
			require.Equal(t, 1, successes)
			current, err := a.GetTranslation(t.Context(), source.UID)
			if err == nil {
				require.Equal(t, "2", current.ResourceVersion)
				require.Equal(t, next.Body, current.Body)
				require.NoError(t, b.DeleteTranslation(t.Context(), current.UID, current.ResourceVersion))
			} else {
				require.ErrorIs(t, err, datastore.ErrNotFound)
			}
			require.ErrorIs(t, b.CreateTranslation(t.Context(), source), datastore.ErrAlreadyExists)
		}
	}
}
