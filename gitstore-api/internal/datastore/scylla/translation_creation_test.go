// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type translationCreationStore interface {
	CreateTranslation(context.Context, *datastore.Translation) error
	GetTranslation(context.Context, string) (*datastore.Translation, error)
	GetTranslationByName(context.Context, string, string) (*datastore.Translation, error)
	RecoverTranslationCreation(context.Context, string) error
}

type translationRecoveryMetadataStore interface {
	GetTranslationMaintenance(context.Context, string) (*datastore.TranslationMaintenanceIntent, error)
	ListTranslationMaintenance(context.Context, string, string, int) (datastore.TranslationMaintenancePage, error)
}

func TestTranslationCreationPersistsCompleteSourceAndRecoversAcknowledgement(t *testing.T) {
	first, second := newTestStores(t)
	a, ok := first.(translationCreationStore)
	require.True(t, ok)
	b, ok := second.(translationCreationStore)
	require.True(t, ok)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, a.CreateTranslation(t.Context(), source))
	byUID, err := b.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, byUID)
	byName, err := b.GetTranslationByName(t.Context(), source.Namespace, source.Name)
	require.NoError(t, err)
	require.Equal(t, byUID, byName)
	require.ErrorIs(t, b.CreateTranslation(t.Context(), source), datastore.ErrAlreadyExists)
	byUID.RawSource[0] = '!'
	byUID.Labels["language"] = "changed"
	still, err := a.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, still)

	raw := newRawSession(t)
	uid, err := gocql.ParseUUID(source.UID)
	require.NoError(t, err)
	namespace, err := gocql.ParseUUID(source.NamespaceID)
	require.NoError(t, err)
	// Restore the persisted boundary after all projections were acknowledged
	// but before final source publication. This is not a physical process fault.
	require.NoError(t, raw.Query(
		"UPDATE translations_by_key SET phase='Creating',coordination_version='1' WHERE namespace_uid=? AND creation_timestamp=? AND uid=?",
		namespace, source.CreationTimestamp, uid,
	).Exec())
	_, err = a.GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	replacement, ok := newTestStore(t).(translationCreationStore)
	require.True(t, ok)
	for range 2 {
		err = recoverTranslationThroughAudit(t.Context(), replacement.(datastore.Datastore), source.UID)
		if !errors.Is(err, datastore.ErrConflict) {
			break
		}
	}
	require.NoError(t, err)
	recovered, err := replacement.GetTranslationByName(t.Context(), source.Namespace, source.Name)
	require.NoError(t, err)
	require.Equal(t, source, recovered)

	forged := *source
	forged.UID = uuid.NewString()
	forged.NamespaceID = uuid.NewString()
	require.ErrorIs(t, a.CreateTranslation(t.Context(), &forged), datastore.ErrConflict)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	forged = *source
	forged.UID, forged.Name = uuid.NewString(), "canceled"
	require.ErrorIs(t, a.CreateTranslation(ctx, &forged), context.Canceled)
	_, err = a.GetTranslation(t.Context(), forged.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestTranslationCreationConcurrentNameOwners(t *testing.T) {
	first, second := newTestStores(t)
	a := first.(translationCreationStore)
	b := second.(translationCreationStore)
	left := newTranslationCreationSource(t, first)
	right := *left
	right.UID = uuid.NewString()
	type result struct {
		uid string
		err error
	}
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for i, store := range []translationCreationStore{a, b} {
		workers.Go(func() {
			source := left
			if i == 1 {
				source = &right
			}
			var err error
			for range 8 {
				err = store.CreateTranslation(t.Context(), source)
				if !errors.Is(err, datastore.ErrConflict) {
					break
				}
			}
			results <- result{source.UID, err}
		})
	}
	workers.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			require.Empty(t, winner)
			winner = result.uid
		} else {
			require.ErrorIs(t, result.err, datastore.ErrAlreadyExists)
		}
	}
	require.NotEmpty(t, winner)
	current, err := b.GetTranslationByName(t.Context(), left.Namespace, left.Name)
	require.NoError(t, err)
	require.Equal(t, winner, current.UID)
	for _, uid := range []string{left.UID, right.UID} {
		if uid == winner {
			continue
		}
		_, err := a.GetTranslation(t.Context(), uid)
		require.ErrorIs(t, err, datastore.ErrNotFound)
	}
}

func TestTranslationCreationRecoveryFencesUnpublishedSource(t *testing.T) {
	first, second := newTestStores(t)
	store := second.(translationMutationStore)
	indexes := first.(translationMutationIndexes)
	source := newTranslationCreationSource(t, first)
	token := func(purpose string) string {
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte("Translation/"+source.UID+"/"+purpose)).String()
	}
	raw := newRawSession(t)
	require.NoError(t, raw.Query(
		"INSERT INTO translations_by_uid (uid,namespace_uid,namespace,name,creation_timestamp,reservation_token,retired) VALUES (?,?,?,?,?,?,false)",
		gocql.UUID(uuid.MustParse(source.UID)), gocql.UUID(uuid.MustParse(source.NamespaceID)),
		source.Namespace, source.Name, source.CreationTimestamp, gocql.UUID(uuid.MustParse(token("namespace"))),
	).Exec())
	shard, err := datastore.TranslationIndexShard(source.UID)
	require.NoError(t, err)
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: source.NamespaceID, Shard: shard}
	entry := datastore.TranslationIndexEntry{TranslationUID: source.UID, SourceVersion: "1",
		ReferenceEpoch: token("reference"), CreationTimestamp: source.CreationTimestamp}
	anchor, err := indexes.ReserveTranslationIndex(t.Context(), index, entry, token("namespace"), 1024)
	require.NoError(t, err)
	require.NoError(t, indexes.CommitTranslationIndex(t.Context(), anchor))
	metadata := second.(translationRecoveryMetadataStore)
	state, err := metadata.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, "Unpublished", state.Phase)
	found, cursor := false, ""
	for range 32 {
		page, err := metadata.ListTranslationMaintenance(t.Context(), source.Namespace, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
		for _, item := range page.Items {
			require.Equal(t, source.UID, item.TranslationUID)
			require.Equal(t, "Unpublished", item.Phase)
			found = true
		}
		if page.Next == "" {
			break
		}
		require.NotEqual(t, cursor, page.Next)
		cursor = page.Next
	}
	require.True(t, found, "a provisional source must remain discoverable before its full row exists")
	require.NoError(t, store.RecoverTranslation(t.Context(), source.UID))
	_, err = store.GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	require.ErrorIs(t, store.CreateTranslation(t.Context(), source), datastore.ErrAlreadyExists)
	require.ErrorIs(t, indexes.CommitTranslationIndex(t.Context(), anchor), datastore.ErrConflict)
	require.NoError(t, store.RecoverTranslation(t.Context(), source.UID))
	applied, err := raw.Query(
		"INSERT INTO translations_by_key (namespace_uid,creation_timestamp,uid,phase,coordination_version) VALUES (?,?,?,'Creating','1') IF NOT EXISTS",
		gocql.UUID(uuid.MustParse(source.NamespaceID)), source.CreationTimestamp, gocql.UUID(uuid.MustParse(source.UID)),
	).MapScanCAS(make(map[string]any))
	require.NoError(t, err)
	require.False(t, applied, "a delayed provisional insert cannot pass the retained absent-source fence")
	replacement := *source
	replacement.UID = uuid.NewString()
	require.NoError(t, store.CreateTranslation(t.Context(), &replacement))
}

func newTranslationCreationSource(t *testing.T, backend datastore.Datastore) *datastore.Translation {
	t.Helper()
	namespace := &datastore.Namespace{
		UID: uuid.NewString(), Name: "translation-" + uuid.NewString(),
		Tier: datastore.NamespaceTierUser, CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
	return &datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		UID: uuid.NewString(), Namespace: namespace.Name, NamespaceID: namespace.UID, Name: "title-de",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		CreationActor: "contract", UpdateActor: "contract", Labels: map[string]string{"language": "de"},
		Annotations: map[string]string{"note": "preserve"}, Finalizers: []string{"test.finalizer"},
		OwnerReferences: json.RawMessage(`[{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Repository","name":"content"}]`),
		Revision:        "main@sha1:accepted", GitRef: "refs/heads/main", GitCommitSHA: "accepted",
		SourcePath: "translations/title-de.md", SourceBlobOID: "blob", SourceDigest: "digest",
		RawSource: []byte("---\r\naccepted source\r\n---\r\n\r\nText\r\n"), Body: "\r\nText\r\n",
		Spec: json.RawMessage(`{"locale":"de-DE","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titel"}]}`),
	}
}
