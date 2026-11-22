// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
	"github.com/stretchr/testify/require"
)

func translationCDCFixture(phase, coordination, version string) *translationSourceRow {
	row := toTranslationSourceRow(&datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		UID: "d531717a-78aa-4ca7-bc92-23cb0ea11cba", NamespaceID: "217ba9cd-6f97-478e-a4d3-e737ba81fc22",
		RepositoryID: "d3ca1140-670d-47f4-928a-ac1583afdb9b",
		Namespace:    "content", Name: "de-title", ResourceVersion: version, Generation: 1,
		CreationTimestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Labels:            map[string]string{"locale": "de-DE"}, SourceBlobOID: "blob", SourceDigest: "digest",
		Revision: "main@sha1:accepted", GitRef: "refs/heads/main", GitCommitSHA: "accepted",
		RawSource: []byte("---\r\naccepted\r\n---\r\n\r\nText\r\n"), Body: "\r\nText\r\n",
		Spec: json.RawMessage(`{"locale":"de-DE","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"Laptop"}}`),
	})
	row.Phase, row.CoordinationVersion = phase, coordination
	return row
}

func TestTranslationCDCOnlyPublishesAcceptedPublicTransitions(t *testing.T) {
	tests := []struct {
		name                        string
		beforePhase, afterPhase     string
		beforeVersion, afterVersion string
		want                        datastore.ResourceWatchEventType
	}{
		{name: "provisional", afterPhase: "Creating", afterVersion: "1"},
		{name: "unpublished-fence", afterPhase: "Retired", afterVersion: "1"},
		{name: "accepted", beforePhase: "Creating", afterPhase: "Stable", beforeVersion: "1", afterVersion: "1", want: datastore.ResourceWatchAdded},
		{name: "source-intent", beforePhase: "Stable", afterPhase: "Updating", beforeVersion: "1", afterVersion: "1"},
		{name: "source-commit", beforePhase: "Updating", afterPhase: "Stable", beforeVersion: "1", afterVersion: "2", want: datastore.ResourceWatchModified},
		{name: "status-commit", beforePhase: "Updating", afterPhase: "Stable", beforeVersion: "1", afterVersion: "2", want: datastore.ResourceWatchModified},
		{name: "binding-commit", beforePhase: "Updating", afterPhase: "Stable", beforeVersion: "2", afterVersion: "2"},
		{name: "delete-intent", beforePhase: "Stable", afterPhase: "Deleting", beforeVersion: "2", afterVersion: "2"},
		{name: "deleted", beforePhase: "Deleting", afterPhase: "Retired", beforeVersion: "2", afterVersion: "2", want: datastore.ResourceWatchDeleted},
		{name: "name-conflict", beforePhase: "Creating", afterPhase: "Retired", beforeVersion: "1", afterVersion: "1"},
		{name: "tombstone-compaction", beforePhase: "Retired", afterPhase: "Retired", beforeVersion: "2", afterVersion: "2"},
		{name: "stable-noop", beforePhase: "Stable", afterPhase: "Stable", beforeVersion: "2", afterVersion: "2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var before []*translationSourceRow
			if test.beforePhase != "" {
				before = []*translationSourceRow{translationCDCFixture(test.beforePhase, "3", test.beforeVersion)}
			}
			coordination := "4"
			if len(before) == 0 {
				coordination = "1"
			}
			after := translationCDCFixture(test.afterPhase, coordination, test.afterVersion)
			if test.name == "source-commit" {
				after.Body, after.Generation, after.Revision = "Neu\r\n", 2, "main@sha1:new"
			}
			if test.name == "status-commit" {
				after.Status = `{"conditions":[],"observedGeneration":1,"lastAppliedRevision":"main@sha1:accepted"}`
			}
			if test.afterPhase == "Retired" {
				after = &translationSourceRow{productRow: productRow{
					UID: after.UID, CreationTimestamp: after.CreationTimestamp, ResourceVersion: after.ResourceVersion,
				}, NamespaceUID: after.NamespaceUID, Phase: after.Phase, CoordinationVersion: after.CoordinationVersion}
			}
			at := gocql.TimeUUID()
			changes, err := translationCDCFrameChanges("translation-stream", at, before, []*translationSourceRow{after})
			require.NoError(t, err)
			if test.want == "" {
				require.Empty(t, changes, "private work must only advance the shared source checkpoint")
				return
			}
			require.Len(t, changes, 1)
			event, ok := watchjournal.ClassifyResource(changes[0], nil)
			require.True(t, ok)
			require.Equal(t, test.want, event.Type)
			require.Equal(t, "Translation", event.Kind)
			require.Equal(t, "content", event.Namespace)
			var source datastore.Translation
			require.NoError(t, json.Unmarshal(event.Payload, &source))
			expected := after.source()
			if test.want == datastore.ResourceWatchDeleted {
				expected = before[0].source()
			}
			expectedJSON, err := json.Marshal(expected)
			require.NoError(t, err)
			require.JSONEq(t, string(expectedJSON), string(event.Payload), "shared journal must preserve the full public source, not a private intent")
			require.Equal(t, expected.RawSource, source.RawSource)
			require.Equal(t, expected.Body, source.Body)
			replay, err := translationCDCFrameChanges("translation-stream", at, before, []*translationSourceRow{after})
			require.NoError(t, err)
			require.Equal(t, changes, replay)
		})
	}
}

func TestTranslationCDCRejectsUnprovableTransitionsBeforeCheckpoint(t *testing.T) {
	for _, invalid := range []string{
		"nil-image", "missing-key", "unknown-phase", "no-coordination", "regressing-coordination",
		"duplicate-preimage", "duplicate-postimage", "different-namespace-key", "different-creation-key",
		"renamed", "repository-changed", "repository-removed", "repository-added",
		"unaccepted-stable", "resurrected", "physical-delete", "skipped-delete-intent",
		"public-change-without-version", "source-changed-in-intent", "regressing-public-version",
		"private-version-changed", "bad-source", "too-many",
	} {
		t.Run(invalid, func(t *testing.T) {
			before := []*translationSourceRow{translationCDCFixture("Updating", "3", "1")}
			after := []*translationSourceRow{translationCDCFixture("Stable", "4", "2")}
			switch invalid {
			case "nil-image":
				after[0] = nil
			case "missing-key":
				after[0].NamespaceUID = gocql.UUID{}
			case "unknown-phase":
				after[0].Phase = "Unknown"
			case "no-coordination":
				after[0].CoordinationVersion = ""
			case "regressing-coordination":
				after[0].CoordinationVersion = "2"
			case "duplicate-preimage":
				before = append(before, before[0])
			case "duplicate-postimage":
				after = append(after, after[0])
			case "different-namespace-key":
				after[0].NamespaceUID = gocql.MustRandomUUID()
			case "different-creation-key":
				after[0].CreationTimestamp = after[0].CreationTimestamp.Add(time.Millisecond)
			case "renamed":
				after[0].Name = "another"
			case "repository-changed":
				after[0].RepositoryID = optionalUUID("e279d66d-c1e5-4b9b-a5fa-b3c1bec5ff25")
			case "repository-removed":
				after[0].RepositoryID = nil
			case "repository-added":
				before[0].RepositoryID = nil
			case "unaccepted-stable":
				before = nil
			case "resurrected":
				before[0].Phase = "Retired"
			case "physical-delete":
				after = nil
			case "skipped-delete-intent":
				before[0].Phase, after[0].Phase = "Stable", "Retired"
			case "public-change-without-version":
				after[0].ResourceVersion, after[0].Body = "1", "Changed"
			case "source-changed-in-intent":
				before[0].Phase, after[0].Phase = "Stable", "Updating"
				after[0].ResourceVersion, after[0].Body = "1", "Changed"
			case "regressing-public-version":
				before[0].ResourceVersion = "3"
			case "private-version-changed":
				before[0].Phase, after[0].Phase = "Stable", "Updating"
			case "bad-source":
				after[0].RawSource = nil
			case "too-many":
				after = make([]*translationSourceRow, catalogCDCMaxRowsPerChange+1)
			}
			changes, err := translationCDCFrameChanges("stream", gocql.TimeUUID(), before, after)
			require.ErrorIs(t, err, datastore.ErrNamespaceWatchDiscontinuity)
			require.Empty(t, changes, "corrupt frames cannot publish a valid subset")
		})
	}
}

func TestTranslationCDCWholeFramesHaveStableDistinctDeduplicationKeys(t *testing.T) {
	first := translationCDCFixture("Stable", "2", "1")
	second := *first
	second.UID, second.Name = gocql.MustRandomUUID(), "fr-title"
	creating := []*translationSourceRow{translationCDCFixture("Creating", "1", "1"), nil}
	other := second
	other.Phase, other.CoordinationVersion = "Creating", "1"
	creating[1] = &other
	accepted := []*translationSourceRow{first, &second}
	at := gocql.TimeUUID()
	changes, err := translationCDCFrameChanges("stream", at, creating, accepted)
	require.NoError(t, err)
	require.Len(t, changes, 2)
	require.NotEqual(t, changes[0].DeduplicationKey, changes[1].DeduplicationKey)
	slices.Reverse(creating)
	slices.Reverse(accepted)
	reordered, err := translationCDCFrameChanges("stream", at, creating, accepted)
	require.NoError(t, err)
	require.Equal(t, changes, reordered)
}

func TestTranslationCDCRevalidationPreservesPublicSourceAndCoalesces(t *testing.T) {
	before := translationCDCFixture("Stable", "3", "2")
	after := *before
	after.RevalidationVersion = after.ResourceVersion
	changes, err := translationCDCFrameChanges("stream", gocql.TimeUUID(), []*translationSourceRow{before}, []*translationSourceRow{&after})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	event, ok := watchjournal.ClassifyResource(changes[0], nil)
	require.True(t, ok, "a durable revalidation hint must survive unchanged public payload classification")
	require.Equal(t, datastore.ResourceWatchModified, event.Type)
	expected, err := json.Marshal(before.source())
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(event.Payload))
	require.NotContains(t, string(event.Payload), "revalidation")
	changes, err = translationCDCFrameChanges("stream", gocql.TimeUUID(), []*translationSourceRow{&after}, []*translationSourceRow{&after})
	require.NoError(t, err)
	require.Empty(t, changes, "same-version revalidation is coalesced before journal append")
	after.RevalidationVersion = "another-source-version"
	changes, err = translationCDCFrameChanges("stream", gocql.TimeUUID(), []*translationSourceRow{before}, []*translationSourceRow{&after})
	require.ErrorIs(t, err, datastore.ErrNamespaceWatchDiscontinuity)
	require.Empty(t, changes)
}

func TestTranslationCDCRejectsDeltasWithoutFullImages(t *testing.T) {
	source, ok := catalogCDCSources["Translation"]
	require.True(t, ok, "Translation must reuse the registered catalog CDC runner")
	require.NotNil(t, source.changes)
	changes, err := source.changes("stream", scyllacdc.Change{})
	require.NoError(t, err)
	require.Empty(t, changes)
	changes, err = source.changes("stream", scyllacdc.Change{Delta: []*scyllacdc.ChangeRow{{}}})
	require.ErrorIs(t, err, datastore.ErrNamespaceWatchDiscontinuity)
	require.Empty(t, changes)
	for _, change := range []scyllacdc.Change{
		{PostImage: []*scyllacdc.ChangeRow{nil}},
		{PostImage: []*scyllacdc.ChangeRow{{}}},
		{PostImage: make([]*scyllacdc.ChangeRow, catalogCDCMaxRowsPerChange+1)},
	} {
		consumer := catalogCDCConsumer{kind: "Translation", decodeChanges: source.changes}
		require.ErrorIs(t, consumer.Consume(t.Context(), change), datastore.ErrNamespaceWatchDiscontinuity)
	}
}
