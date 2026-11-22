// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationSourceRowPreservesEnvelopeAndBindingEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	source := &datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		UID: uuid.NewString(), NamespaceID: uuid.NewString(), RepositoryID: uuid.NewString(),
		Namespace: "content", Name: "de-title", Generation: 1, ResourceVersion: "7",
		CreationTimestamp: now, CreationActor: "creator", UpdateTimestamp: now, UpdateActor: "updater",
		Labels: map[string]string{"locale": "de-DE"}, Annotations: map[string]string{"keep": "Case"},
		OwnerReferences: json.RawMessage(`[{"kind":"Repository","name":"content"}]`),
		Finalizers:      []string{"catalog.cleanup"}, DeletionTimestamp: &now,
		SourcePath: "Translations/de.md", GitRef: "refs/heads/main", GitCommitSHA: "accepted",
		Revision: "main@sha1:accepted", SourceBlobOID: "blob", SourceDigest: "digest",
		RawSource: []byte("---\r\naccepted\r\n---\r\n\r\nText\r\n"), Body: "\r\nText\r\n",
		Spec:   json.RawMessage(`{"locale":"de-DE","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"Laptop"}}`),
		Status: json.RawMessage(`{"conditions":[],"observedGeneration":1,"lastAppliedRevision":"main@sha1:accepted"}`),
	}
	row := toTranslationSourceRow(source)
	require.Equal(t, source, row.source(), "row conversion must not discard status, source attribution or exact bytes")
	binding, err := datastore.PrepareTranslationBinding(source, nil)
	require.NoError(t, err)
	raw, err := json.Marshal(binding)
	require.NoError(t, err)
	row.BindingSnapshot, row.ReferenceEpoch = string(raw), binding.CurrentEpoch
	decoded, err := translationRowBinding(row)
	require.NoError(t, err)
	require.Equal(t, binding, decoded)
	row.ResourceVersion = "8"
	_, err = translationRowBinding(row)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "binding/source evidence cannot silently span two revisions")
	row.ResourceVersion = "7"
	row.ReferenceEpoch = uuid.NewString()
	_, err = translationRowBinding(row)
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
}

func TestTranslationSourceTransitionRejectsImmutableIdentityAndWrongGeneration(t *testing.T) {
	current := &datastore.Translation{
		UID: uuid.NewString(), Namespace: "content", NamespaceID: uuid.NewString(), Name: "de",
		RepositoryID: uuid.NewString(), CreationActor: "creator", CreationTimestamp: time.Now().UTC(),
		ResourceVersion: "7", Generation: 3, Spec: json.RawMessage(`{"locale":"de"}`), Body: "old\r\n",
	}
	next := *current
	next.ResourceVersion = "8"
	require.NoError(t, validateTranslationSourceTransition(current, &next, "7"))
	next.Body = "changed\r\n"
	require.ErrorIs(t, validateTranslationSourceTransition(current, &next, "7"), datastore.ErrConflict)
	next.Generation = 4
	require.NoError(t, validateTranslationSourceTransition(current, &next, "7"))
	for _, mutate := range []func(*datastore.Translation){
		func(s *datastore.Translation) { s.NamespaceID = uuid.NewString() },
		func(s *datastore.Translation) { s.RepositoryID = uuid.NewString() },
		func(s *datastore.Translation) { s.Name = "different" },
		func(s *datastore.Translation) { s.CreationActor = "different" },
		func(s *datastore.Translation) { s.CreationTimestamp = s.CreationTimestamp.Add(time.Millisecond) },
	} {
		proposed := next
		mutate(&proposed)
		require.ErrorIs(t, validateTranslationSourceTransition(current, &proposed, "7"), datastore.ErrConflict)
	}
	require.ErrorIs(t, validateTranslationSourceTransition(current, &next, "6"), datastore.ErrConflict)
}

func TestTranslationMutationRejectsMismatchedRetirementBeforeIndexWrites(t *testing.T) {
	source := &datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		UID: uuid.NewString(), Namespace: "content", NamespaceID: uuid.NewString(), Name: "de",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), ResourceVersion: "1", Generation: 1,
		RawSource: []byte("---\naccepted source\n---\n"),
		Spec:      json.RawMessage(`{"locale":"de-DE","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"Laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titel"}]}`),
	}
	require.NoError(t, datastore.ValidateTranslationSource(source))
	binding, err := datastore.PrepareTranslationBinding(source, nil)
	require.NoError(t, err)
	row := toTranslationSourceRow(source)
	row.Phase, row.CoordinationVersion, row.ReferenceEpoch = "Updating", "3", binding.CurrentEpoch
	raw, err := json.Marshal(binding)
	require.NoError(t, err)
	row.BindingSnapshot = string(raw)
	next := *source
	next.ResourceVersion = "2"
	updated, err := datastore.PrepareTranslationBinding(&next, binding)
	require.NoError(t, err)
	bad := datastore.TranslationIndexReservation{
		Index: datastore.TranslationIndexKey{NamespaceUID: source.NamespaceID},
		Entry: datastore.TranslationIndexEntry{
			TranslationUID: uuid.NewString(), SourceVersion: source.ResourceVersion, ReferenceEpoch: binding.CurrentEpoch,
		},
	}
	intent := translationSourceMutation{
		Mode: "Source", Source: &next, Binding: updated,
		Previous: []datastore.TranslationIndexReservation{bad, bad},
		Tokens:   []string{uuid.NewString()},
	}
	raw, err = json.Marshal(intent)
	require.NoError(t, err)
	row.MaintenanceIntent = string(raw)
	require.ErrorIs(t, (&scyllaDatastore{}).completeTranslationSourceMutation(t.Context(), row), datastore.ErrRepairRequired)
}

func TestTranslationMutationIntentPreservesRawEnvelopeBytes(t *testing.T) {
	for _, status := range []json.RawMessage{nil, json.RawMessage(`{ "conditions": [] }`)} {
		source := &datastore.Translation{
			UID: uuid.NewString(), NamespaceID: uuid.NewString(),
			Spec:   json.RawMessage("{\n \"locale\": \"de-DE\" }"),
			Status: status, OwnerReferences: json.RawMessage(`[ { "name": "content" } ]`),
			RawSource: []byte("---\r\naccepted\r\n---\r\n\r\nText\r\n"), Body: "\r\nText\r\n",
		}
		intent := translationSourceMutation{Mode: "Binding", Source: source}
		raw, err := json.Marshal(intent)
		require.NoError(t, err)
		var decoded translationSourceMutation
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.Equal(t, source, decoded.Source)
	}
}
