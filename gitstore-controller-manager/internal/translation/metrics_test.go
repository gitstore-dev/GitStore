// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestTargetHandoffMetricsDistinguishProofNoOpAndConservativeWork(t *testing.T) {
	registry := prometheus.NewRegistry()
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Translation")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	target := dependencyTarget()
	seedDependents(t, store, []checkpoint.DiskItem{dependentItem(t, "private-source", target, false)})
	clearDependentWork(t, store)
	observed := TargetObservation{Target: target, ResourceVersion: "1", Generation: 1, Revision: "revision", ContentDigest: "material"}
	var proofErr error
	handoff := NewTargetHandoff(store, targetObservationFunc(func(context.Context, string, string, string, string) (*TargetObservation, error) {
		if proofErr != nil {
			return nil, proofErr
		}
		copy := observed
		return &copy, nil
	}))
	handoff.metrics = newMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/controller"))
	hint := TargetHint{Namespace: "shop", APIVersion: target.APIVersion, Kind: target.Kind, Name: target.Name, UID: target.UID, ResourceVersion: "1"}
	require.NoError(t, handoff.Change(t.Context(), nil, &hint))
	finishDependentFanout(t, store)
	require.EqualValues(t, 1, store.Counts().Pending)
	clearDependentWork(t, store)
	require.NoError(t, handoff.Change(t.Context(), &hint, &hint))
	previous := hint
	hint.ResourceVersion, observed.ResourceVersion = "2", "2"
	require.NoError(t, handoff.Change(t.Context(), &previous, &hint))
	finishDependentFanout(t, store)
	require.Zero(t, store.Counts().Pending, "proved status-only changes do not fan out to unpinned sources")
	proofErr = errors.New("private target, locale and unavailable provider")
	require.NoError(t, handoff.Change(t.Context(), &hint, &hint))
	finishDependentFanout(t, store)
	require.EqualValues(t, 1, store.Counts().Pending, "unavailable proof must enqueue conservative material work")
	families, err := registry.Gather()
	require.NoError(t, err)
	counts, durations := map[string]float64{}, map[string]float64{}
	for _, family := range families {
		if family.GetName() != "gitstore_translation_target_handoff_total" &&
			family.GetName() != "gitstore_translation_target_handoff_duration_seconds" {
			continue
		}
		for _, sample := range family.Metric {
			labels := map[string]string{}
			for _, label := range sample.Label {
				labels[label.GetName()] = label.GetValue()
			}
			require.Len(t, labels, 2)
			key := labels["scope"] + "/" + labels["outcome"]
			if sample.Counter != nil {
				counts[key] += sample.GetCounter().GetValue()
			} else {
				durations[key] += float64(sample.GetHistogram().GetSampleCount())
			}
		}
	}
	require.Equal(t, map[string]float64{"material/queued": 2, "pinned/queued": 1, "none/no_op": 1}, counts)
	require.Equal(t, counts, durations)
}

func TestReconciliationMetricsFollowActualNoOpConflictAndFailurePaths(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/controller"))
	fixture := newReconciliationFixture()
	reconciler := fixture.reconciler()
	reconciler.metrics = metrics
	require.IsType(t, types.Success{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	patch := fixture.patches[0]
	fixture.snapshot.Observation = &status.ResourceStatus{
		ResourceVersion:    fixture.source.Metadata.ResourceVersion,
		ObservedGeneration: *patch.ObservedGeneration, LastAppliedRevision: *patch.LastAppliedRevision,
		Conditions: patch.Conditions, Resolved: patch.Resolved,
	}

	uid := "target-uid"
	fixture.snapshot.Binding.BoundUID = &uid
	require.IsType(t, types.Success{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	require.Len(t, fixture.patches, 1)

	fixture.snapshot.Observation = nil
	fixture.writeErr = types.ErrConflict
	require.IsType(t, types.RequeueAfter{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	fixture.writeErr, fixture.snapshot.Target = nil, nil
	require.IsType(t, types.RequeueAfter{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	fixture.snapshotErr = types.ErrAPIUnavailable
	require.IsType(t, types.TransientFailure{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.IsType(t, types.TransientFailure{}, reconciler.Reconcile(ctx, reconciliationKey()))

	families, err := registry.Gather()
	require.NoError(t, err)
	attempts, durations, writes := map[string]float64{}, map[string]float64{}, map[string]float64{}
	lookups := map[string]float64{}
	for _, family := range families {
		for _, sample := range family.Metric {
			labels := map[string]string{}
			for _, label := range sample.Label {
				labels[label.GetName()] = label.GetValue()
			}
			switch family.GetName() {
			case "gitstore_translation_reconciliation_total":
				require.Len(t, labels, 1)
				attempts[labels["outcome"]] += sample.GetCounter().GetValue()
			case "gitstore_translation_reconciliation_duration_seconds":
				require.Len(t, labels, 1)
				durations[labels["outcome"]] += float64(sample.GetHistogram().GetSampleCount())
			case "gitstore_translation_status_writes_total":
				require.Len(t, labels, 1)
				writes[labels["outcome"]] += sample.GetCounter().GetValue()
			case "gitstore_translation_lookup_total":
				require.Len(t, labels, 2)
				lookups[labels["stage"]+"/"+labels["outcome"]] += sample.GetCounter().GetValue()
			}
		}
	}
	require.Equal(t, map[string]float64{"success": 2, "requeue": 2, "transient": 1, "canceled": 1}, attempts)
	require.Equal(t, attempts, durations)
	require.Equal(t, map[string]float64{"applied": 2, "no_op": 1, "conflict": 1}, writes)
	require.Equal(t, map[string]float64{
		"projection/found": 5, "snapshot/found": 4, "snapshot/unavailable": 1, "binding/found": 1,
	}, lookups)
}
