// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTargetBackfillPreservesExistingVersionAndUnprovedAbort(t *testing.T) {
	target := datastore.TranslationTargetKey{
		NamespaceUID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "legacy",
	}
	uid := uuid.NewString()
	intent := datastore.TranslationTargetWriteIntent{
		ID: uuid.NewString(), PreviousUID: uid, NextUID: uid, ExpectedResourceVersion: "7",
		SourceDigest: "material", SourceProof: "frozen-private-preimage", Backfilling: true,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	pending, err := datastore.PlanTranslationTargetWrite(nil, target, intent, "")
	require.NoError(t, err)
	evidence := &catalog.TranslationTargetEvidence{
		APIVersion: target.APIVersion, Kind: target.Kind, Namespace: "content", Name: target.Name,
		UID: uid, Generation: 3, ResourceVersion: "7", ContentDigest: intent.SourceDigest,
	}
	stable, err := datastore.PlanTranslationTargetCompletion(pending, pending.Token, evidence)
	require.NoError(t, err)
	require.Equal(t, evidence, stable.Evidence)
	aborted, err := datastore.PlanTranslationTargetAbort(pending, pending.Token, nil)
	require.NoError(t, err)
	require.Nil(t, aborted.Evidence, "canceling proof construction cannot invent positive old evidence")
	retry, err := datastore.PlanTranslationTargetWrite(aborted, target, intent, aborted.Token)
	require.NoError(t, err)
	require.NotEqual(t, pending.Token, retry.Token)
	intent.ID = uuid.NewString()
	_, err = datastore.PlanTranslationTargetWrite(stable, target, intent, stable.Token)
	require.ErrorIs(t, err, datastore.ErrConflict, "backfill cannot replace already-proven material")
	for _, mutate := range []func(*datastore.TranslationTargetWriteIntent){
		func(value *datastore.TranslationTargetWriteIntent) { value.Deleting, value.NextUID = true, "" },
		func(value *datastore.TranslationTargetWriteIntent) { value.Invalidating = true },
		func(value *datastore.TranslationTargetWriteIntent) { value.NextUID = uuid.NewString() },
		func(value *datastore.TranslationTargetWriteIntent) { value.SourceProof = "" },
	} {
		bad := intent
		mutate(&bad)
		_, err := datastore.PlanTranslationTargetWrite(nil, target, bad, "")
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	}
}

func TestTargetAbortRetainsProvenMaterialAndNeverReusesOldToken(t *testing.T) {
	target := datastore.TranslationTargetKey{
		NamespaceUID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "target",
	}
	old := &datastore.TranslationTargetFence{
		Target: target, Token: uuid.NewString(), Phase: datastore.TranslationTargetStable,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Evidence: &catalog.TranslationTargetEvidence{
			APIVersion: target.APIVersion, Kind: target.Kind, Namespace: "content", Name: target.Name,
			UID: uuid.NewString(), Generation: 1, ResourceVersion: "1", Revision: "accepted", ContentDigest: "original",
		},
	}
	pending, err := datastore.PlanTranslationTargetWrite(old, target, datastore.TranslationTargetWriteIntent{
		ID: uuid.NewString(), PreviousUID: old.Evidence.UID, NextUID: old.Evidence.UID,
		ExpectedResourceVersion: "2", SourceDigest: "desired", CreationTimestamp: old.CreationTimestamp,
	}, old.Token)
	require.NoError(t, err)
	actual := *old.Evidence
	actual.ResourceVersion = "3"
	aborted, err := datastore.PlanTranslationTargetAbort(pending, pending.Token, &actual)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, aborted.Phase)
	require.Nil(t, aborted.Intent)
	require.Equal(t, pending.Token, aborted.Token)
	require.NotEqual(t, old.Token, aborted.Token, "aborting must not recreate a previously visible fence token")
	require.Equal(t, &actual, aborted.Evidence)
	require.Equal(t, old.CreationTimestamp, aborted.CreationTimestamp)
	require.Equal(t, datastore.TranslationTargetUpdating, pending.Phase, "planning cannot mutate retained state")
	replayed, err := datastore.PlanTranslationTargetAbort(aborted, aborted.Token, &actual)
	require.NoError(t, err)
	require.Equal(t, aborted, replayed)
	aborted.Evidence.Revision = "caller change"
	require.Equal(t, "accepted", replayed.Evidence.Revision)
	for _, mutate := range []func(*catalog.TranslationTargetEvidence){
		func(value *catalog.TranslationTargetEvidence) { value.UID = uuid.NewString() },
		func(value *catalog.TranslationTargetEvidence) { value.Namespace = "other" },
		func(value *catalog.TranslationTargetEvidence) { value.Generation++ },
		func(value *catalog.TranslationTargetEvidence) { value.Revision = "other" },
		func(value *catalog.TranslationTargetEvidence) { value.ContentDigest = "desired" },
		func(value *catalog.TranslationTargetEvidence) { value.ResourceVersion = "1" },
	} {
		contradictory := actual
		mutate(&contradictory)
		_, err := datastore.PlanTranslationTargetAbort(pending, pending.Token, &contradictory)
		require.ErrorIs(t, err, datastore.ErrConflict)
	}
	_, err = datastore.PlanTranslationTargetAbort(pending, old.Token, &actual)
	require.ErrorIs(t, err, datastore.ErrConflict)
	_, err = datastore.PlanTranslationTargetAbort(pending, pending.Token, nil)
	require.ErrorIs(t, err, datastore.ErrConflict, "a live source cannot be turned into absence by aborting")
}

func TestTargetCreateAbortCanOnlyRetainPreviouslyProvenAbsence(t *testing.T) {
	target := datastore.TranslationTargetKey{
		NamespaceUID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "target",
	}

	intent := datastore.TranslationTargetWriteIntent{
		ID: uuid.NewString(), NextUID: uuid.NewString(), SourceDigest: "desired",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	pending, err := datastore.PlanTranslationTargetWrite(nil, target, intent, "")
	require.NoError(t, err)
	aborted, err := datastore.PlanTranslationTargetAbort(pending, pending.Token, nil)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, aborted.Phase)
	require.Nil(t, aborted.Evidence)
	require.True(t, aborted.CreationTimestamp.IsZero(), "a canceled create has no authoritative lifetime")
	intent.PreviousUID, intent.ExpectedResourceVersion = intent.NextUID, "1"
	unproven, err := datastore.PlanTranslationTargetWrite(nil, target, intent, "")
	require.NoError(t, err)
	_, err = datastore.PlanTranslationTargetAbort(unproven, unproven.Token, nil)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "missing old evidence cannot prove absence for an existing UID")
}

func TestTargetInvalidationRevokesEvidenceWithoutClaimingUIDRetirement(t *testing.T) {
	target := datastore.TranslationTargetKey{
		NamespaceUID: uuid.NewString(), APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", Name: "file",
	}
	current := &datastore.TranslationTargetFence{
		Target: target, Token: uuid.NewString(), Phase: datastore.TranslationTargetStable,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Evidence: &catalog.TranslationTargetEvidence{
			APIVersion: target.APIVersion, Kind: target.Kind, Namespace: "content", Name: target.Name,
			UID: uuid.NewString(), Generation: 1, ResourceVersion: "1", ContentDigest: "original",
		},
	}
	intent := datastore.TranslationTargetWriteIntent{
		ID: uuid.NewString(), PreviousUID: current.Evidence.UID, NextUID: current.Evidence.UID,
		ExpectedResourceVersion: "1", Invalidating: true, CreationTimestamp: current.CreationTimestamp,
	}
	pending, err := datastore.PlanTranslationTargetWrite(current, target, intent, current.Token)
	require.NoError(t, err)
	_, err = datastore.PlanTranslationTargetCompletion(pending, pending.Token, current.Evidence)
	require.ErrorIs(t, err, datastore.ErrConflict, "an invalidating write cannot publish positive evidence")
	unavailable, err := datastore.PlanTranslationTargetCompletion(pending, pending.Token, nil)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, unavailable.Phase)
	require.NotEqual(t, current.Token, unavailable.Token)
	require.Nil(t, unavailable.Evidence)
	require.True(t, unavailable.CreationTimestamp.IsZero())
	canceled, err := datastore.PlanTranslationTargetAbort(pending, pending.Token, current.Evidence)
	require.NoError(t, err)
	require.Equal(t, current.Evidence, canceled.Evidence, "a durably canceled invalidation can retain the old material")
	intent.Deleting = true
	_, err = datastore.PlanTranslationTargetWrite(current, target, intent, current.Token)
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
}
