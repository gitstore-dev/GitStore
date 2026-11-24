// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

type reconciliationFixture struct {
	source      Resource
	snapshot    *ValidationSnapshot
	snapshotErr error
	bindErr     error
	writeErr    error
	patches     []*status.StatusPatch
	binds       int
}

func newReconciliationFixture() *reconciliationFixture {
	return &reconciliationFixture{
		source: Resource{Metadata: Metadata{UID: "source-id", Namespace: "acme", Name: "title", ResourceVersion: "3", Generation: 2, Revision: "accepted-source"},
			Spec: json.RawMessage(`{"locale":"iw-IL","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titre"}]}`)},
		snapshot: &ValidationSnapshot{
			SourceID: "source-id", SourceVersion: "3", SourceDigest: "accepted-source-digest", NamespaceUID: "namespace-uid",
			Binding: &BindingState{TranslationID: "source-id", SourceVersion: "3", BindingVersion: "1", ReferenceEpoch: "epoch", NamespaceUID: "namespace-uid"},
			Target: &ValidationTarget{
				Namespace: "acme",
				Observation: TargetObservation{
					Target: DependencyTarget{NamespaceUID: "namespace-uid", APIVersion: "catalog.gitstore.dev/v1beta1",
						Kind: "Product", Name: "laptop", UID: "target-uid"},
					ResourceVersion: "7", Generation: 1, Revision: "target-revision", ContentDigest: "target-material",
				},
				Fields: map[string]any{"title": "Laptop"},
			},
		},
	}
}

func (f *reconciliationFixture) ValidationSnapshot(context.Context, types.WorkItemKey, string, string) (*ValidationSnapshot, error) {
	return f.snapshot, f.snapshotErr
}

func (f *reconciliationFixture) BindTarget(_ context.Context, _, _ string, observed *BindingState, uid string) (*BindingState, error) {
	f.binds++
	if f.bindErr != nil {
		return nil, f.bindErr
	}
	bound := *observed
	bound.BoundUID = &uid
	return &bound, nil
}

func (f *reconciliationFixture) Apply(_ context.Context, _ types.WorkItemKey, patch *status.StatusPatch) error {
	f.patches = append(f.patches, patch)
	return f.writeErr
}

func (f *reconciliationFixture) reconciler() *Reconciler {
	return NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Resource, bool, error) {
		return f.source, true, nil
	}, f, f)
}

func reconciliationKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "Translation", Namespace: "acme", Name: "title"}
}

func TestReconcilerMaterializesValidationWithoutChangingTarget(t *testing.T) {
	fixture := newReconciliationFixture()
	before, err := json.Marshal(fixture.snapshot.Target)
	require.NoError(t, err)
	require.IsType(t, types.Success{}, fixture.reconciler().Reconcile(t.Context(), reconciliationKey()))
	require.Equal(t, 1, fixture.binds)
	require.Len(t, fixture.patches, 1)
	patch := fixture.patches[0]
	require.Equal(t, fixture.source.Metadata.UID, patch.UID)
	require.Equal(t, fixture.source.Metadata.ResourceVersion, patch.ResourceVersion)
	require.Equal(t, fixture.source.Metadata.Generation, *patch.ObservedGeneration)
	require.Equal(t, fixture.source.Metadata.Revision, *patch.LastAppliedRevision)
	require.Equal(t, "TRUE", patch.Conditions[len(patch.Conditions)-1].Status)
	var resolved map[string]any
	require.NoError(t, json.Unmarshal(patch.Resolved, &resolved))
	require.Equal(t, "he-il", resolved["canonicalLocale"])
	require.Equal(t, "accepted-source-digest", resolved["sourceDigest"])
	require.NotEmpty(t, resolved["descriptorDigest"])
	require.Equal(t, []any{"spec.title"}, resolved["validatedFieldPaths"])
	require.Equal(t, "target-uid", resolved["target"].(map[string]any)["uid"])
	after, err := json.Marshal(fixture.snapshot.Target)
	require.NoError(t, err)
	require.Equal(t, before, after, "validation must not translate or otherwise mutate target content")
	require.Contains(t, string(fixture.source.Spec), `"locale":"iw-IL"`)
}

func TestReconcilerUsesSharedDescriptorsForEveryDeliveredKind(t *testing.T) {
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newReconciliationFixture()
			var spec map[string]any
			require.NoError(t, json.Unmarshal(fixture.source.Spec, &spec))
			version := "catalog.gitstore.dev/v1beta1"
			if kind == "File" {
				version = "storage.gitstore.dev/v1beta1"
				spec["patches"] = []any{}
				fixture.source.Body = "\r\n# Texte \u00e9crit\n\n---\n"
			}
			spec["targetRef"].(map[string]any)["kind"] = kind
			spec["targetRef"].(map[string]any)["apiVersion"] = version
			fixture.source.Spec, _ = json.Marshal(spec)
			fixture.snapshot.Target.Observation.Target.Kind = kind
			fixture.snapshot.Target.Observation.Target.APIVersion = version
			before := fixture.source.Body
			require.IsType(t, types.Success{}, fixture.reconciler().Reconcile(t.Context(), reconciliationKey()))
			require.Equal(t, before, fixture.source.Body)
			require.Len(t, fixture.patches, 1)
		})
	}
}

func TestReconcilerUsesAuthorizedObservationWhenPublicStatusIsMasked(t *testing.T) {
	for name, invalidTarget := range map[string]bool{"valid": false, "invalid": true} {
		t.Run(name, func(t *testing.T) {
			fixture := newReconciliationFixture()
			if invalidTarget {
				fixture.snapshot.Target.Fields["title"] = nil
			}
			reconciler := fixture.reconciler()
			reconciler.Reconcile(t.Context(), reconciliationKey())
			require.Len(t, fixture.patches, 1)
			patch := fixture.patches[0]
			fixture.snapshot.Observation = &status.ResourceStatus{
				ResourceVersion:    fixture.source.Metadata.ResourceVersion,
				ObservedGeneration: *patch.ObservedGeneration, LastAppliedRevision: *patch.LastAppliedRevision,
				Conditions: patch.Conditions, Resolved: patch.Resolved,
			}
			fixture.source.Status = status.ResourceStatus{}
			if !invalidTarget {
				uid := "target-uid"
				fixture.snapshot.Binding.BoundUID = &uid
			}
			reconciler.Reconcile(t.Context(), reconciliationKey())
			require.Len(t, fixture.patches, 1, "a read-time mask must not cause another status write")
			require.Empty(t, fixture.source.Status.Conditions, "reading the snapshot must not rewrite the cache")
		})
	}
}

func TestReconcilerRestoresMissingPrivateObservationEvidence(t *testing.T) {
	fixture := newReconciliationFixture()
	reconciler := fixture.reconciler()
	require.IsType(t, types.Success{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	patch := fixture.patches[0]
	var projected map[string]any
	require.NoError(t, json.Unmarshal(patch.Resolved, &projected))
	delete(projected, "sourceDigest")
	delete(projected, "descriptorDigest")
	public, err := json.Marshal(projected)
	require.NoError(t, err)
	fixture.snapshot.Observation = &status.ResourceStatus{
		ResourceVersion: fixture.source.Metadata.ResourceVersion, ObservedGeneration: *patch.ObservedGeneration,
		LastAppliedRevision: *patch.LastAppliedRevision, Conditions: patch.Conditions, Resolved: public,
	}
	uid := "target-uid"
	fixture.snapshot.Binding.BoundUID = &uid
	require.IsType(t, types.Success{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	require.Len(t, fixture.patches, 2, "missing private fingerprints must be restored even when public fields match")
}

func TestReconcilerRestoresMissingNegativeObservationEvidence(t *testing.T) {
	fixture := newReconciliationFixture()
	fixture.snapshot.Target = nil
	reconciler := fixture.reconciler()
	require.IsType(t, types.RequeueAfter{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	patch := fixture.patches[0]
	fixture.snapshot.Observation = &status.ResourceStatus{
		ResourceVersion: fixture.source.Metadata.ResourceVersion, ObservedGeneration: *patch.ObservedGeneration,
		LastAppliedRevision: *patch.LastAppliedRevision, Conditions: patch.Conditions,
	}
	require.IsType(t, types.RequeueAfter{}, reconciler.Reconcile(t.Context(), reconciliationKey()))
	require.Len(t, fixture.patches, 2)
}

type concurrentValidationStatus struct {
	mu     sync.Mutex
	writes int
}

func (s *concurrentValidationStatus) Apply(context.Context, types.WorkItemKey, *status.StatusPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writes != 0 {
		return types.ErrConflict
	}
	s.writes++
	return nil
}

func TestReconcilerDuplicateWorkersRespectStatusCAS(t *testing.T) {
	fixture := newReconciliationFixture()
	uid := "target-uid"
	fixture.snapshot.Binding.BoundUID = &uid
	writer := &concurrentValidationStatus{}
	results := make(chan types.ReconcileResult, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			reconciler := fixture.reconciler()
			reconciler.statusClient = writer
			results <- reconciler.Reconcile(t.Context(), reconciliationKey())
		})
	}
	workers.Wait()
	close(results)
	success, conflict := 0, 0
	for result := range results {
		switch result.(type) {
		case types.Success:
			success++
		case types.RequeueAfter:
			conflict++
		default:
			t.Fatalf("unexpected duplicate-worker outcome: %#v", result)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
	require.Equal(t, 1, writer.writes)
}

func TestReconcilerPersistsBoundedNegativeOutcomes(t *testing.T) {
	for _, scenario := range []string{"missing", "replaced", "uid-pin", "version-pin", "terminating", "field"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newReconciliationFixture()
			switch scenario {
			case "missing":
				fixture.snapshot.Target = nil
			case "replaced":
				uid := "previous-incarnation"
				fixture.snapshot.Binding.BoundUID = &uid
			case "uid-pin", "version-pin":
				var spec map[string]any
				require.NoError(t, json.Unmarshal(fixture.source.Spec, &spec))
				key, value := "uid", "different-target"
				if scenario == "version-pin" {
					key, value = "resourceVersion", "6"
				}
				spec["targetRef"].(map[string]any)[key] = value
				fixture.source.Spec, _ = json.Marshal(spec)
			case "terminating":
				fixture.snapshot.Target.Observation.Terminating = true
			case "field":
				fixture.snapshot.Target.Fields = map[string]any{"title": false}
			}
			require.IsType(t, types.RequeueAfter{}, fixture.reconciler().Reconcile(t.Context(), reconciliationKey()))
			if scenario == "field" {
				require.Equal(t, 1, fixture.binds, "field invalidity must not discard the first observed lifetime")
			} else {
				require.Zero(t, fixture.binds, "missing or ineligible references must not establish a first binding")
			}
			require.Len(t, fixture.patches, 1)
			require.Equal(t, "FALSE", fixture.patches[0].Conditions[len(fixture.patches[0].Conditions)-1].Status)
			require.Equal(t, fixture.source.Metadata.Revision, *fixture.patches[0].LastAppliedRevision)
		})
	}
}

func TestReconcilerInvalidFieldsCannotSilentlyAdoptReplacement(t *testing.T) {
	fixture := newReconciliationFixture()
	fixture.snapshot.Target.Fields["title"] = false
	require.IsType(t, types.RequeueAfter{}, fixture.reconciler().Reconcile(t.Context(), reconciliationKey()))
	require.Equal(t, 1, fixture.binds, "bind the first available permitted incarnation even when fields are invalid")
	originalUID := fixture.snapshot.Target.Observation.Target.UID
	fixture.snapshot.Binding.BoundUID = &originalUID
	fixture.snapshot.Target.Observation.Target.UID = "replacement-uid"
	fixture.snapshot.Target.Fields["title"] = "Valid on replacement"
	fixture.patches = nil
	require.IsType(t, types.RequeueAfter{}, fixture.reconciler().Reconcile(t.Context(), reconciliationKey()))
	require.Equal(t, 1, fixture.binds, "a later same-name incarnation cannot replace a sticky binding")
	require.Len(t, fixture.patches, 1)
	require.Equal(t, "TargetReplaced", fixture.patches[0].Conditions[len(fixture.patches[0].Conditions)-1].Reason)
}

func TestReconcilerOperationalFailuresNeverWriteAnObservation(t *testing.T) {
	for _, scenario := range []string{"lookup", "snapshot", "source-lifetime", "source-version", "binding-epoch", "bind-conflict", "status-conflict", "budget", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newReconciliationFixture()
			reconciler := fixture.reconciler()
			ctx := t.Context()
			switch scenario {
			case "lookup":
				reconciler = NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Resource, bool, error) {
					return Resource{}, false, errors.New("disk read failed")
				}, fixture, fixture)
			case "snapshot":
				fixture.snapshotErr = types.ErrAPIUnavailable
			case "source-lifetime":
				fixture.snapshot.SourceID = "recreated-source"
			case "source-version":
				fixture.snapshot.SourceVersion = "later-version"
			case "binding-epoch":
				fixture.snapshot.Binding.ReferenceEpoch = ""
			case "bind-conflict":
				fixture.bindErr = types.ErrConflict
			case "status-conflict":
				fixture.writeErr = types.ErrConflict
			case "budget":
				fixture.snapshot.Target.Fields["other"] = make([]any, 65_536)
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			result := reconciler.Reconcile(ctx, reconciliationKey())
			_, succeeded := result.(types.Success)
			require.False(t, succeeded)
			if scenario != "status-conflict" {
				require.Empty(t, fixture.patches)
			} else {
				requeue, ok := result.(types.RequeueAfter)
				require.True(t, ok)
				require.Greater(t, requeue.After, time.Duration(0))
			}
		})
	}
}
