// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func translationTargetWriteFences(t *testing.T, backend datastore.Datastore) datastore.TranslationTargetWriteFenceStore {
	t.Helper()
	store, ok := backend.(datastore.TranslationTargetWriteFenceStore)
	require.True(t, ok, "target material writers need shared durable fencing")
	return store
}

func runTranslationTargetFenceContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store := translationTargetWriteFences(t, backend)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			target := datastore.TranslationTargetKey{NamespaceUID: newID(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: kind, Name: "Laptop"}
			if kind == "File" {
				target.APIVersion = "storage.gitstore.dev/v1beta1"
				target.Name = strings.Repeat("LongCaseSensitiveName", 4000)
			}
			_, err := store.GetTranslationTargetFence(t.Context(), target)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			createdAt := time.Now().UTC().Truncate(time.Millisecond)
			intent := datastore.TranslationTargetWriteIntent{
				ID: newID(), NextUID: newID(), SourceDigest: "material-one", CreationTimestamp: createdAt,
			}
			pending, err := store.BeginTranslationTargetWrite(t.Context(), target, intent, "")
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetUpdating, pending.Phase)
			require.Equal(t, intent, *pending.Intent)
			require.NotEmpty(t, pending.Token)
			replayed, err := store.BeginTranslationTargetWrite(t.Context(), target, intent, "")
			require.NoError(t, err)
			require.Equal(t, pending, replayed, "a lost begin reply resumes the same frozen intent and token")
			replayed.Intent.SourceDigest = "caller mutation"
			persisted, err := store.GetTranslationTargetFence(t.Context(), target)
			require.NoError(t, err)
			require.Equal(t, intent, *persisted.Intent)
			contradictory := intent
			contradictory.SourceDigest = "different material"
			_, err = store.BeginTranslationTargetWrite(t.Context(), target, contradictory, "")
			require.ErrorIs(t, err, datastore.ErrConflict)
			evidence := &catalog.TranslationTargetEvidence{
				APIVersion: target.APIVersion, Kind: kind, Namespace: "content", Name: target.Name,
				UID: intent.NextUID, Generation: 1, ResourceVersion: "1", ContentDigest: intent.SourceDigest,
			}
			require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, newID(), evidence), datastore.ErrConflict)
			wrong := *evidence
			wrong.UID = newID()
			require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, &wrong), datastore.ErrConflict)
			require.NoError(t, store.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, evidence))
			require.NoError(t, store.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, evidence), "lost completion replies are safe to retry")
			stable, err := store.GetTranslationTargetFence(t.Context(), target)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, stable.Phase)
			require.Nil(t, stable.Intent)
			require.Equal(t, evidence, stable.Evidence)
			require.Equal(t, createdAt, stable.CreationTimestamp, "recovery retains the exact authoritative resource key, not a lookup projection")
			updated := datastore.TranslationTargetWriteIntent{
				ID: newID(), PreviousUID: evidence.UID, NextUID: evidence.UID, ExpectedResourceVersion: "1", SourceDigest: "material-two", CreationTimestamp: createdAt,
			}
			moved := updated
			moved.CreationTimestamp = createdAt.Add(time.Second)
			_, err = store.BeginTranslationTargetWrite(t.Context(), target, moved, stable.Token)
			require.ErrorIs(t, err, datastore.ErrConflict, "a live UID cannot move to another authoritative resource key")
			next, err := store.BeginTranslationTargetWrite(t.Context(), target, updated, stable.Token)
			require.NoError(t, err)
			require.NotEqual(t, stable.Token, next.Token)
			require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, evidence), datastore.ErrConflict)
			require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, next.Token, evidence), datastore.ErrConflict)
			changed := *evidence
			changed.ResourceVersion, changed.Generation, changed.ContentDigest = "2", 2, updated.SourceDigest
			require.NoError(t, store.CompleteTranslationTargetWrite(t.Context(), target, next.Token, &changed))
			deleting := datastore.TranslationTargetWriteIntent{ID: newID(), PreviousUID: changed.UID, ExpectedResourceVersion: "2", Deleting: true}
			removed, err := store.BeginTranslationTargetWrite(t.Context(), target, deleting, next.Token)
			require.NoError(t, err)
			require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, removed.Token, &changed), datastore.ErrConflict)
			require.NoError(t, store.CompleteTranslationTargetWrite(t.Context(), target, removed.Token, nil))
			absent, err := store.GetTranslationTargetFence(t.Context(), target)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, absent.Phase)
			require.Nil(t, absent.Evidence)
			replacement := datastore.TranslationTargetWriteIntent{ID: newID(), NextUID: newID(), SourceDigest: "replacement"}
			_, err = store.BeginTranslationTargetWrite(t.Context(), target, replacement, stable.Token)
			require.ErrorIs(t, err, datastore.ErrConflict)
			last, err := store.BeginTranslationTargetWrite(t.Context(), target, replacement, absent.Token)
			require.NoError(t, err)
			require.NotEqual(t, removed.Token, last.Token)
			require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, removed.Token, nil), datastore.ErrConflict)
			otherNamespace := target
			otherNamespace.NamespaceUID = newID()
			_, err = store.GetTranslationTargetFence(t.Context(), otherNamespace)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			otherCase := target
			otherCase.Name = strings.ToLower(target.Name)
			_, err = store.GetTranslationTargetFence(t.Context(), otherCase)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = store.GetTranslationTargetFence(canceled, target)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func runTranslationTargetFenceValidationContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store := translationTargetWriteFences(t, backend)
	target := datastore.TranslationTargetKey{NamespaceUID: newID(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "bounded"}
	prototype := datastore.TranslationTargetWriteIntent{ID: newID(), NextUID: newID(), SourceDigest: "material"}
	for _, invalid := range []struct {
		name   string
		mutate func(*datastore.TranslationTargetKey, *datastore.TranslationTargetWriteIntent)
	}{
		{"unknown-kind", func(key *datastore.TranslationTargetKey, _ *datastore.TranslationTargetWriteIntent) {
			key.Kind = "Custom"
		}},
		{"invalid-namespace", func(key *datastore.TranslationTargetKey, _ *datastore.TranslationTargetWriteIntent) {
			key.NamespaceUID = "unknown"
		}},
		{"invalid-utf8", func(key *datastore.TranslationTargetKey, _ *datastore.TranslationTargetWriteIntent) {
			key.Name = string([]byte{0xff})
		}},
		{"empty-operation", func(_ *datastore.TranslationTargetKey, intent *datastore.TranslationTargetWriteIntent) {
			intent.ID = ""
		}},
		{"missing-cas", func(_ *datastore.TranslationTargetKey, intent *datastore.TranslationTargetWriteIntent) {
			intent.PreviousUID = newID()
		}},
		{"invalid-delete", func(_ *datastore.TranslationTargetKey, intent *datastore.TranslationTargetWriteIntent) {
			intent.Deleting = true
		}},
		{"intent-bytes", func(_ *datastore.TranslationTargetKey, intent *datastore.TranslationTargetWriteIntent) {
			intent.SourcePath = strings.Repeat("x", datastore.MaxTranslationTargetIntentBytes)
		}},
		{"encoded-intent-bytes", func(_ *datastore.TranslationTargetKey, intent *datastore.TranslationTargetWriteIntent) {
			intent.SourcePath = strings.Repeat("\n", datastore.MaxTranslationTargetIntentBytes/2)
		}},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			key, intent := target, prototype
			invalid.mutate(&key, &intent)
			_, err := store.BeginTranslationTargetWrite(t.Context(), key, intent, "")
			require.ErrorIs(t, err, datastore.ErrInvalidArgument)
			_, err = store.GetTranslationTargetFence(t.Context(), target)
			require.ErrorIs(t, err, datastore.ErrNotFound, "invalid preflight cannot reserve a target identity")
		})
	}
}

func runTranslationTargetFenceStatusRetryContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store := translationTargetWriteFences(t, backend)
	target := datastore.TranslationTargetKey{NamespaceUID: newID(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "status-retry"}
	create := datastore.TranslationTargetWriteIntent{ID: newID(), NextUID: newID(), SourceDigest: "initial"}
	initial, err := store.BeginTranslationTargetWrite(t.Context(), target, create, "")
	require.NoError(t, err)
	evidence := &catalog.TranslationTargetEvidence{
		APIVersion: target.APIVersion, Kind: target.Kind, Namespace: "content", Name: target.Name,
		UID: create.NextUID, Generation: 1, ResourceVersion: "1", ContentDigest: create.SourceDigest,
	}
	require.NoError(t, store.CompleteTranslationTargetWrite(t.Context(), target, initial.Token, evidence))
	intent := datastore.TranslationTargetWriteIntent{
		ID: newID(), PreviousUID: evidence.UID, NextUID: evidence.UID, ExpectedResourceVersion: "2", SourceDigest: "updated",
	}
	pending, err := store.BeginTranslationTargetWrite(t.Context(), target, intent, initial.Token)
	require.NoError(t, err)
	retry := intent
	retry.ExpectedResourceVersion = "1"
	_, err = store.BeginTranslationTargetWrite(t.Context(), target, retry, pending.Token)
	require.ErrorIs(t, err, datastore.ErrConflict, "a retry cannot regress the original source CAS expectation")
	retry.ExpectedResourceVersion = "3"
	resumed, err := store.BeginTranslationTargetWrite(t.Context(), target, retry, pending.Token)
	require.NoError(t, err, "a status-only CAS conflict must not permanently wedge the same material intent")
	require.Equal(t, pending, resumed, "retry retains the frozen original intent and non-reused write token")
	changed := *evidence
	changed.ContentDigest, changed.Generation = intent.SourceDigest, 2
	require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, &changed), datastore.ErrConflict)
	changed.ResourceVersion = "2"
	require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, &changed), datastore.ErrConflict)
	changed.ResourceVersion = "4"
	require.NoError(t, store.CompleteTranslationTargetWrite(t.Context(), target, pending.Token, &changed))
	stable, err := store.GetTranslationTargetFence(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, &changed, stable.Evidence)
	stale := intent
	stale.ID, stale.ExpectedResourceVersion = newID(), "3"
	_, err = store.BeginTranslationTargetWrite(t.Context(), target, stale, stable.Token)
	require.ErrorIs(t, err, datastore.ErrConflict, "a stale source CAS cannot reserve a new fence token")
	stale.ExpectedResourceVersion = "4"
	latest, err := store.BeginTranslationTargetWrite(t.Context(), target, stale, stable.Token)
	require.NoError(t, err)
	regressed := changed
	regressed.ResourceVersion, regressed.Generation = "5", 1
	require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, latest.Token, &regressed), datastore.ErrConflict)
	regressed.Generation, regressed.Namespace = changed.Generation, "another-namespace"
	require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, latest.Token, &regressed), datastore.ErrConflict)
	regressed.Namespace, regressed.Revision = changed.Namespace, "invalid\x00revision"
	require.ErrorIs(t, store.CompleteTranslationTargetWrite(t.Context(), target, latest.Token, &regressed), datastore.ErrInvalidArgument)
	regressed.Revision = changed.Revision
	require.NoError(t, store.CompleteTranslationTargetWrite(t.Context(), target, latest.Token, &regressed))
}

func runTranslationTargetFenceRaceContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store := translationTargetWriteFences(t, backend)
	target := datastore.TranslationTargetKey{NamespaceUID: newID(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "race"}
	results := make(chan error, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range cap(results) {
		workers.Go(func() {
			<-start
			_, err := store.BeginTranslationTargetWrite(t.Context(), target,
				datastore.TranslationTargetWriteIntent{ID: newID(), NextUID: newID(), SourceDigest: "material"}, "")
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, datastore.ErrConflict) {
			conflicts++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 7, conflicts)
}
