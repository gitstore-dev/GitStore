// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationRevalidationCoalescesAcrossIndependentAndReplacementStores(t *testing.T) {
	first, second := newTestStores(t)
	a := first.(datastore.TranslationCapable).TranslationStore()
	b := second.(datastore.TranslationCapable).TranslationStore()
	source := newTranslationCreationSource(t, first)
	require.NoError(t, a.CreateTranslation(t.Context(), source))
	requested, err := a.RequestTranslationRevalidation(t.Context(), source)
	require.NoError(t, err)
	require.True(t, requested)
	for _, store := range []datastore.TranslationStore{
		b, newTestStore(t).(datastore.TranslationCapable).TranslationStore(),
	} {
		requested, err := store.RequestTranslationRevalidation(t.Context(), source)
		require.NoError(t, err)
		require.False(t, requested, "private coalescing state must survive API replacement")
		current, err := store.GetTranslation(t.Context(), source.UID)
		require.NoError(t, err)
		require.Equal(t, source, current)
	}
	status := json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"` + source.Revision + `","conditions":[]}`)
	current, err := b.UpdateTranslationStatus(t.Context(), source.UID, source.ResourceVersion, status)
	require.NoError(t, err)
	requested, err = a.RequestTranslationRevalidation(t.Context(), current)
	require.NoError(t, err)
	require.True(t, requested, "a later source version owns a new bounded revalidation obligation")
	_, err = b.RequestTranslationRevalidation(t.Context(), source)
	require.ErrorIs(t, err, datastore.ErrConflict)
}

type translationMutationStore interface {
	translationCreationStore
	UpdateTranslation(context.Context, *datastore.Translation, string) error
	UpdateTranslationStatus(context.Context, string, string, json.RawMessage) (*datastore.Translation, error)
	GetTranslationBinding(context.Context, string) (*datastore.TranslationBinding, error)
	CompareAndSwapTranslationBinding(context.Context, *datastore.TranslationBinding, string) error
	RecoverTranslation(context.Context, string) error
}

func TestTranslationMutationCoordinatesSourceStatusAndBinding(t *testing.T) {
	first, second := newTestStores(t)
	a, ok := first.(translationMutationStore)
	require.True(t, ok, "source, status and binding writers must share durable coordination")
	b, ok := second.(translationMutationStore)
	require.True(t, ok)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, a.CreateTranslation(t.Context(), source))
	binding, err := b.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	bound := datastore.CloneTranslationBinding(binding)
	bound.ResourceVersion = "2"
	bound.Epochs[0].BoundUID = uuid.NewString()
	require.NoError(t, b.CompareAndSwapTranslationBinding(t.Context(), bound, "1"))
	unchanged, err := a.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, unchanged, "private binding cannot manufacture a public source revision")
	status := json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:accepted","conditions":[]}`)
	observed, err := a.UpdateTranslationStatus(t.Context(), source.UID, "1", status)
	require.NoError(t, err)
	require.Equal(t, "2", observed.ResourceVersion)
	require.JSONEq(t, string(status), string(observed.Status))
	afterStatus, err := b.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, "2", afterStatus.SourceVersion)
	require.Equal(t, bound.Epochs[0].BoundUID, afterStatus.Epochs[0].BoundUID)
	require.ErrorIs(t, b.CompareAndSwapTranslationBinding(t.Context(), bound, "1"), datastore.ErrConflict)

	next := *observed
	next.ResourceVersion, next.Generation = "3", 2
	next.Spec = json.RawMessage(strings.Replace(string(source.Spec), "de-DE", "fr-FR", 1))
	next.RawSource = []byte("complete newer accepted source")
	next.Revision = "main@sha1:newer"
	next.Status = json.RawMessage(`{"forged":true}`)
	require.NoError(t, b.UpdateTranslation(t.Context(), &next, "2"))
	current, err := a.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, next.Spec, current.Spec)
	require.Equal(t, next.RawSource, current.RawSource)
	require.Equal(t, observed.Status, current.Status, "authored writes cannot overwrite observed status")
	latest, err := a.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, afterStatus.CurrentEpoch, latest.CurrentEpoch)
	require.Equal(t, bound.Epochs[0].BoundUID, latest.Epochs[0].BoundUID)
	require.Equal(t, "3", latest.SourceVersion)
	require.ErrorIs(t, a.UpdateTranslation(t.Context(), &next, "2"), datastore.ErrConflict)
	_, err = a.UpdateTranslationStatus(t.Context(), source.UID, "3", status)
	require.ErrorIs(t, err, datastore.ErrConflict, "old revision/generation evidence cannot commit")
}

type translationMutationIndexes interface {
	ReserveTranslationIndex(context.Context, datastore.TranslationIndexKey, datastore.TranslationIndexEntry, string, int64) (datastore.TranslationIndexReservation, error)
	CommitTranslationIndex(context.Context, datastore.TranslationIndexReservation) error
	RemoveTranslationIndex(context.Context, datastore.TranslationIndexReservation) error
	ListTranslationDependents(context.Context, datastore.TranslationTargetKey, string, string, int) (datastore.TranslationDependentPage, error)
}

func TestTranslationNamespaceAnchorSurvivesEveryMutation(t *testing.T) {
	first, _ := newTestStores(t)
	store := first.(translationMutationStore)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	raw := newRawSession(t)
	anchor := func() datastore.TranslationIndexReservation {
		var frozen string
		require.NoError(t, raw.Query(
			"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
			gocql.UUID(uuid.MustParse(source.NamespaceID)), source.CreationTimestamp, gocql.UUID(uuid.MustParse(source.UID)),
		).Scan(&frozen))
		var placements []datastore.TranslationIndexReservation
		require.NoError(t, json.Unmarshal([]byte(frozen), &placements))
		require.NotEmpty(t, placements)
		return placements[0]
	}
	original := anchor()
	binding, err := store.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	binding.ResourceVersion, binding.Epochs[0].BoundUID = "2", uuid.NewString()
	require.NoError(t, store.CompareAndSwapTranslationBinding(t.Context(), binding, "1"))
	require.Equal(t, original, anchor(), "private binding cannot move a namespace page position")
	current, err := store.UpdateTranslationStatus(t.Context(), source.UID, "1",
		json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:accepted","conditions":[]}`))
	require.NoError(t, err)
	require.Equal(t, original, anchor(), "status cannot move a namespace page position")
	next := *current
	next.ResourceVersion, next.Generation = "3", 2
	next.Spec = json.RawMessage(strings.Replace(string(source.Spec), `"name":"laptop"`, `"name":"replacement"`, 1))
	require.NoError(t, store.UpdateTranslation(t.Context(), &next, "2"))
	require.Equal(t, original, anchor(), "retargeting cannot move a namespace page position")
}

func TestTranslationMutationRecoversPersistedBoundaries(t *testing.T) {
	first, second := newTestStores(t)
	a, b := first.(translationMutationStore), second.(translationMutationStore)
	indexes := first.(translationMutationIndexes)
	raw := newRawSession(t)
	for _, boundary := range []string{"intent", "reserved", "published", "removed"} {
		t.Run(boundary, func(t *testing.T) {
			source := newTranslationCreationSource(t, first)
			require.NoError(t, a.CreateTranslation(t.Context(), source))
			binding, err := a.GetTranslationBinding(t.Context(), source.UID)
			require.NoError(t, err)
			var previousRaw string
			namespace, uid := gocql.UUID(uuid.MustParse(source.NamespaceID)), gocql.UUID(uuid.MustParse(source.UID))
			require.NoError(t, raw.Query(
				"SELECT maintenance_intent FROM translations_by_key WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
				namespace, source.CreationTimestamp, uid,
			).Scan(&previousRaw))
			var previous []datastore.TranslationIndexReservation
			require.NoError(t, json.Unmarshal([]byte(previousRaw), &previous))
			next := *source
			next.ResourceVersion, next.Generation = "2", 2
			next.Spec = json.RawMessage(strings.Replace(string(source.Spec), `"name":"laptop"`, `"name":"replacement"`, 1))
			next.RawSource = []byte("complete replacement source\r\n")
			desired, err := datastore.PrepareTranslationBinding(&next, binding)
			require.NoError(t, err)
			tokens := []string{uuid.NewString()}
			// Inject a durable boundary, not a physical process fault. Raw JSON
			// envelope fields are frozen as bytes, not re-encoded JSON values.
			frozen, err := json.Marshal(struct {
				Mode   string
				Source struct {
					*datastore.Translation
					Spec, Status, OwnerReferences []byte
				}
				Binding  *datastore.TranslationBinding
				Previous []datastore.TranslationIndexReservation
				Tokens   []string
			}{
				Mode: "Source",
				Source: struct {
					*datastore.Translation
					Spec, Status, OwnerReferences []byte
				}{&next, next.Spec, next.Status, next.OwnerReferences},
				Binding: desired, Previous: previous, Tokens: tokens,
			})
			require.NoError(t, err)
			require.NoError(t, raw.Query(
				"UPDATE translations_by_key SET phase='Updating',coordination_version='3',maintenance_intent=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
				string(frozen), namespace, source.CreationTimestamp, uid,
			).Exec())
			state, err := second.(translationRecoveryMetadataStore).GetTranslationMaintenance(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, "Updating", state.Phase)
			require.Equal(t, "3", state.ResourceVersion)
			_, err = b.GetTranslation(t.Context(), source.UID)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			_, err = b.GetTranslationBinding(t.Context(), source.UID)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			shard, err := datastore.TranslationIndexShard(source.UID)
			require.NoError(t, err)
			if boundary != "intent" {
				entry := datastore.TranslationIndexEntry{
					TranslationUID: source.UID, SourceVersion: "2",
					ReferenceEpoch: desired.CurrentEpoch, CreationTimestamp: source.CreationTimestamp,
				}
				key := datastore.TranslationIndexKey{Kind: datastore.TranslationTargetNameIndex, NamespaceUID: source.NamespaceID,
					Shard: shard, Target: desired.Epochs[len(desired.Epochs)-1].Target}
				reserved, err := indexes.ReserveTranslationIndex(t.Context(), key, entry, tokens[0], max(1024, datastore.TranslationIndexPayloadBytes(key, entry)))
				require.NoError(t, err)
				if boundary != "reserved" {
					require.NoError(t, indexes.CommitTranslationIndex(t.Context(), reserved))
				}
				if boundary == "removed" {
					for _, old := range previous[1:] {
						require.NoError(t, indexes.RemoveTranslationIndex(t.Context(), old))
					}
				}
			}
			replacement := newTestStore(t).(translationMutationStore)
			var workers sync.WaitGroup
			outcomes := make(chan error, 2)
			for index, worker := range []translationMutationStore{b, replacement} {
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
			current, err := replacement.GetTranslation(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, &next, current)
			actual, err := replacement.GetTranslationBinding(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, desired, actual)
			require.NoError(t, replacement.RecoverTranslation(t.Context(), source.UID))
			for _, old := range previous[1:] {
				require.ErrorIs(t, indexes.CommitTranslationIndex(t.Context(), old), datastore.ErrConflict)
			}
			oldEdges := translationMutationDependents(t, indexes, binding.Epochs[0].Target)
			require.Empty(t, oldEdges)
			newEdges := translationMutationDependents(t, indexes, desired.Epochs[len(desired.Epochs)-1].Target)
			require.Len(t, newEdges, 1)
			require.Equal(t, source.UID, newEdges[0].TranslationUID)
			require.Equal(t, desired.CurrentEpoch, newEdges[0].ReferenceEpoch)
			require.Equal(t, "2", newEdges[0].SourceVersion)
		})
	}
}

func translationMutationDependents(t *testing.T, indexes translationMutationIndexes, target datastore.TranslationTargetKey) []datastore.TranslationIndexEntry {
	t.Helper()
	var entries []datastore.TranslationIndexEntry
	cursor := ""
	for range 16 {
		page, err := indexes.ListTranslationDependents(t.Context(), target, "", cursor, 256)
		require.NoError(t, err)
		entries = append(entries, page.Items...)
		if page.Next == "" {
			return entries
		}
		require.NotEqual(t, cursor, page.Next)
		cursor = page.Next
	}
	t.Fatal("dependent enumeration failed to make bounded progress")
	return nil
}

func TestTranslationMutationSourceAndBindingRace(t *testing.T) {
	first, second := newTestStores(t)
	a, b := first.(translationMutationStore), second.(translationMutationStore)
	for range 4 {
		source := newTranslationCreationSource(t, first)
		require.NoError(t, a.CreateTranslation(t.Context(), source))
		binding, err := a.GetTranslationBinding(t.Context(), source.UID)
		require.NoError(t, err)
		bound := datastore.CloneTranslationBinding(binding)
		bound.ResourceVersion = "2"
		bound.Epochs[0].BoundUID = uuid.NewString()
		next := *source
		next.ResourceVersion, next.Generation = "2", 2
		next.Spec = json.RawMessage(strings.Replace(string(source.Spec), `"name":"laptop"`, `"name":"replacement"`, 1))
		start := make(chan struct{})
		sourceResult, bindingResult := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			sourceResult <- a.UpdateTranslation(t.Context(), &next, "1")
		}()
		go func() {
			<-start
			bindingResult <- b.CompareAndSwapTranslationBinding(t.Context(), bound, "1")
		}()
		close(start)
		sourceErr, bindingErr := <-sourceResult, <-bindingResult
		for _, err := range []error{sourceErr, bindingErr} {
			if err != nil {
				require.ErrorIs(t, err, datastore.ErrConflict)
			}
		}
		require.True(t, sourceErr == nil || bindingErr == nil)
		current, err := a.GetTranslation(t.Context(), source.UID)
		require.NoError(t, err)
		actual, err := b.GetTranslationBinding(t.Context(), source.UID)
		require.NoError(t, err)
		require.Equal(t, current.ResourceVersion, actual.SourceVersion)
		if sourceErr == nil {
			require.Equal(t, next.Spec, current.Spec)
			require.Len(t, actual.Epochs, 2)
		} else {
			require.Equal(t, source.Spec, current.Spec)
			require.Len(t, actual.Epochs, 1)
		}
		if bindingErr == nil {
			require.Equal(t, bound.Epochs[0].BoundUID, actual.Epochs[0].BoundUID, "first-bound incarnation must survive a racing retarget")
		} else {
			require.Empty(t, actual.Epochs[0].BoundUID)
		}
	}
}
