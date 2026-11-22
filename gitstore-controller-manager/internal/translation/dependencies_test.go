// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTargetHandoffPersistsProofAndInvalidationBeforeAcknowledgement(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	store, err := checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	t.Cleanup(func() {
		if store != nil {
			require.NoError(t, store.Close())
		}
	})
	target := dependencyTarget()
	observed := TargetObservation{Target: target, ResourceVersion: "1", Generation: 1, Revision: "revision", ContentDigest: "material"}
	provider := targetObservationFunc(func(context.Context, string, string, string, string) (*TargetObservation, error) {
		copy := observed
		return &copy, nil
	})
	var items []checkpoint.DiskItem
	for _, pinned := range []bool{false, true} {
		source := Resource{Metadata: Metadata{UID: "source-id", Namespace: "shop", Name: fmt.Sprint(pinned), ResourceVersion: "1", Generation: 1},
			Spec: json.RawMessage(fmt.Sprintf(`{"targetRef":{"apiVersion":%q,"kind":%q,"name":%q,"resourceVersion":%q}}`,
				target.APIVersion, target.Kind, target.Name, map[bool]string{true: "1"}[pinned]))}
		indexes, err := SourceDependencyIndexes(source, nil)
		require.NoError(t, err)
		item := dependentItem(t, source.Metadata.Name, target, pinned)
		item.Key.Namespace = source.Metadata.Namespace
		item.Indexes = indexes
		items = append(items, item)
	}
	seedDependents(t, store, items)
	clearDependentWork(t, store)
	hint := TargetHint{Namespace: "shop", APIVersion: target.APIVersion, Kind: target.Kind, Name: target.Name, ResourceVersion: "1"}
	handoff := NewTargetHandoff(store, provider)
	require.NoError(t, handoff.Change(ctx, nil, &hint))
	finishDependentFanout(t, store)
	require.EqualValues(t, 2, store.Counts().Pending)
	clearDependentWork(t, store)

	require.NoError(t, store.Close())
	store = nil
	store, err = checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	handoff = NewTargetHandoff(store, provider)
	previous := hint
	hint.ResourceVersion, observed.ResourceVersion = "2", "2"
	require.NoError(t, handoff.Change(ctx, &previous, &hint))
	finishDependentFanout(t, store)
	work, err := store.Pending(ctx, "", 256)
	require.NoError(t, err)
	require.Len(t, work, 1, "proved status-only changes must not invalidate unpinned sources")
	require.Equal(t, "true", work[0].Key.Name)
	clearDependentWork(t, store)

	previous = hint
	hint.ResourceVersion, observed.ResourceVersion, observed.ContentDigest = "3", "3", "changed"
	require.NoError(t, handoff.Change(ctx, &previous, &hint))
	require.NoError(t, store.Close())
	store = nil
	store, err = checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	handoff = NewTargetHandoff(store, provider)
	require.NoError(t, handoff.Change(ctx, &previous, &hint), "a replay after the hint write cannot lose its durable fan-out")
	finishDependentFanout(t, store)
	require.EqualValues(t, 2, store.Counts().Pending)
	clearDependentWork(t, store)

	handoff = NewTargetHandoff(store, targetObservationFunc(func(context.Context, string, string, string, string) (*TargetObservation, error) {
		return nil, fmt.Errorf("unavailable proof")
	}))
	require.NoError(t, handoff.Change(ctx, &hint, nil))
	finishDependentFanout(t, store)
	require.EqualValues(t, 2, store.Counts().Pending, "deletion and unavailable proof require conservative invalidation, not a no-op")
}

func TestTargetHandoffUnknownProofAndLifetimesNeverCertifyNoOp(t *testing.T) {
	for _, scenario := range []string{"unavailable", "denied", "namespace-replaced", "uid-replaced", "terminating", "lost-projection", "hint-write-failed"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := checkpoint.OpenDiskStore(t.TempDir(), "Translation")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			target := dependencyTarget()
			source := Resource{Metadata: Metadata{UID: "source-id", Namespace: "shop", Name: "fr", ResourceVersion: "1", Generation: 1},
				Spec: json.RawMessage(fmt.Sprintf(`{"targetRef":{"apiVersion":%q,"kind":%q,"name":%q}}`, target.APIVersion, target.Kind, target.Name))}
			bound := target.UID
			binding := &BindingState{TranslationID: source.Metadata.UID, SourceVersion: source.Metadata.ResourceVersion,
				BindingVersion: "1", ReferenceEpoch: "epoch", NamespaceUID: target.NamespaceUID, BoundUID: &bound}
			indexes, err := SourceDependencyIndexes(source, binding)
			require.NoError(t, err)
			item := dependentItem(t, "fr", target, false)
			item.Indexes = indexes
			seedDependents(t, store, []checkpoint.DiskItem{item})
			clearDependentWork(t, store)
			observed := TargetObservation{Target: target, ResourceVersion: "1", Generation: 1, ContentDigest: "material"}
			var proofErr error
			observer := targetObservationFunc(func(context.Context, string, string, string, string) (*TargetObservation, error) {
				copy := observed
				return &copy, proofErr
			})
			hint := TargetHint{Namespace: "shop", APIVersion: target.APIVersion, Kind: target.Kind, Name: target.Name, ResourceVersion: "1"}
			handoff := NewTargetHandoff(store, observer)
			require.NoError(t, handoff.Change(t.Context(), nil, &hint))
			finishDependentFanout(t, store)
			clearDependentWork(t, store)
			previous := hint
			before := &previous
			hint.ResourceVersion = "2"
			switch scenario {
			case "unavailable", "denied":
				proofErr = fmt.Errorf("%s", scenario)
			case "namespace-replaced":
				observed.Target.NamespaceUID = "new-namespace"
			case "uid-replaced":
				observed.Target.UID = "new-target"
			case "terminating":
				hint.Terminating = true
			case "lost-projection":
				before = nil
				observed.ContentDigest = "changed"
			case "hint-write-failed":
				observed.ContentDigest = "changed"
				failed := &failingHintStore{handoffStore: store}
				handoff = NewTargetHandoff(failed, observer)
				require.ErrorContains(t, handoff.Change(t.Context(), before, &hint), "hint write failed")
				handoff = NewTargetHandoff(store, observer)
			}
			require.NoError(t, handoff.Change(t.Context(), before, &hint))
			finishDependentFanout(t, store)
			require.EqualValues(t, 1, store.Counts().Pending)
			clearDependentWork(t, store)
			require.NoError(t, QueueNamespaceChange(t.Context(), store, "another-namespace"))
			finishDependentFanout(t, store)
			require.Zero(t, store.Counts().Pending)
			require.NoError(t, QueueNamespaceChange(t.Context(), store, "shop"))
			finishDependentFanout(t, store)
			require.EqualValues(t, 1, store.Counts().Pending)
		})
	}
}

type failingHintStore struct{ handoffStore }

func (*failingHintStore) WriteHint(context.Context, string, []byte) error {
	return fmt.Errorf("hint write failed")
}

type targetObservationFunc func(context.Context, string, string, string, string) (*TargetObservation, error)

func (f targetObservationFunc) ObserveTarget(ctx context.Context, namespace, apiVersion, kind, name string) (*TargetObservation, error) {
	return f(ctx, namespace, apiVersion, kind, name)
}

func TestTargetChangesSelectMaterialOrVersionPinnedDependents(t *testing.T) {
	ctx := t.Context()
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Translation")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	target := dependencyTarget()
	otherNamespace, otherKind := target, target
	otherNamespace.NamespaceUID = "another-namespace"
	otherKind.Kind = "Collection"
	items := []checkpoint.DiskItem{
		dependentItem(t, "unpinned", target, false),
		dependentItem(t, "pinned", target, true),
		dependentItem(t, "other-namespace", otherNamespace, true),
		dependentItem(t, "other-kind", otherKind, true),
	}
	seedDependents(t, store, items)
	clearDependentWork(t, store)
	previous := TargetObservation{Target: target, ResourceVersion: "1", Generation: 1, Revision: "main@one", ContentDigest: "one"}
	current := previous
	current.ResourceVersion = "2"
	require.NoError(t, QueueTargetChange(ctx, store, &previous, &current))
	finishDependentFanout(t, store)
	work, err := store.Pending(ctx, "", checkpoint.DiskPageItems)
	require.NoError(t, err)
	require.Len(t, work, 1)
	require.Equal(t, "pinned", work[0].Key.Name)
	clearDependentWork(t, store)

	previous = current
	current.Generation, current.ResourceVersion, current.ContentDigest = 2, "3", "two"
	require.NoError(t, QueueTargetChange(ctx, store, &previous, &current))
	finishDependentFanout(t, store)
	work, err = store.Pending(ctx, "", checkpoint.DiskPageItems)
	require.NoError(t, err)
	var names []string
	for _, pending := range work {
		names = append(names, pending.Key.Name)
	}
	require.ElementsMatch(t, []string{"pinned", "unpinned"}, names)
	clearDependentWork(t, store)
	require.NoError(t, QueueTargetChange(ctx, store, &current, &current))
	progress, err := store.ProcessFanout(ctx)
	require.NoError(t, err)
	require.False(t, progress, "an unchanged proved observation must not restart completed fan-out")

	for _, change := range []string{"unknown-material", "termination", "deleted-renamed-uid"} {
		t.Run(change, func(t *testing.T) {
			previous, next := current, current
			after := &next
			switch change {
			case "unknown-material":
				previous.ContentDigest, next.ContentDigest = "", ""
			case "termination":
				next.Terminating = true
			case "deleted-renamed-uid":
				previous.Target.Name = "a-later-name"
				after = nil
			}
			require.NoError(t, QueueTargetChange(t.Context(), store, &previous, after))
			finishDependentFanout(t, store)
			work, err := store.Pending(t.Context(), "", checkpoint.DiskPageItems)
			require.NoError(t, err)
			require.Len(t, work, 2, "unknown or changed material invalidates pinned and unpinned sources")
			clearDependentWork(t, store)
		})
	}
}

func TestTargetInvalidationResumesBoundedPagesAfterRestart(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	store, err := checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	t.Cleanup(func() {
		if store != nil {
			require.NoError(t, store.Close())
		}
	})
	target := dependencyTarget()
	target.UID = ""
	var items []checkpoint.DiskItem
	for i := range checkpoint.DiskPageItems + 1 {
		items = append(items, dependentItem(t, fmt.Sprintf("source-%04d", i), target, false))
	}
	seedDependents(t, store, items)
	clearDependentWork(t, store)
	change := TargetObservation{Target: target, ResourceVersion: "1", Generation: 1, ContentDigest: "created"}
	change.Target.UID = "new-target"
	require.NoError(t, QueueTargetChange(ctx, store, nil, &change))
	require.NoError(t, store.Close())
	store = nil
	store, err = checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	require.Zero(t, store.Counts().Pending)
	for range 4 {
		progress, err := store.ProcessFanout(ctx)
		require.NoError(t, err)
		require.True(t, progress)
		if store.Counts().Pending > 0 {
			break
		}
	}
	require.EqualValues(t, checkpoint.DiskPageItems, store.Counts().Pending)
	require.NoError(t, store.Close())
	store = nil
	store, err = checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	finishDependentFanout(t, store)
	require.EqualValues(t, len(items), store.Counts().Pending)
	_, cursor, err := store.Active(ctx)
	require.NoError(t, err)
	require.Equal(t, "translation-journal@start", cursor, "target invalidation must not overwrite the source watch cursor")
}

func TestTargetInvalidationProgressesDuringContinuousChanges(t *testing.T) {
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Translation")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	target := dependencyTarget()
	items := make([]checkpoint.DiskItem, 2*checkpoint.DiskPageItems+3)
	for i := range items {
		items[i] = dependentItem(t, fmt.Sprintf("translation-%04d", i), target, false)
	}
	coldTarget := target
	coldTarget.Name, coldTarget.UID = "cold-target", "cold-target-uid"
	items = append(items, dependentItem(t, "cold-translation", coldTarget, true))
	seedDependents(t, store, items)
	clearDependentWork(t, store)
	coldPrevious := TargetObservation{Target: coldTarget, ResourceVersion: "1", Generation: 1, ContentDigest: "cold"}
	coldChange := coldPrevious
	coldChange.ResourceVersion = "2"
	require.NoError(t, QueueTargetChange(t.Context(), store, &coldPrevious, &coldChange))
	previous := TargetObservation{Target: target, ResourceVersion: "1", Generation: 1, ContentDigest: "first"}
	for turn := 0; turn < 20 && store.Counts().Pending < uint64(len(items)); turn++ {
		current := previous
		current.ResourceVersion = fmt.Sprint(turn + 2)
		current.Generation++
		current.ContentDigest = fmt.Sprint("material-", turn)
		require.NoError(t, QueueTargetChange(t.Context(), store, &previous, &current))
		progress, err := store.ProcessFanout(t.Context())
		require.NoError(t, err)
		require.True(t, progress)
		previous = current
	}
	require.EqualValues(t, len(items), store.Counts().Pending,
		"continuous target changes must not restart every bounded page at the same prefix")
}

func TestTargetInvalidationRepeatsProcessedPrefixAfterRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	t.Cleanup(func() {
		if store != nil {
			require.NoError(t, store.Close())
		}
	})
	target := dependencyTarget()
	target.UID = ""
	items := make([]checkpoint.DiskItem, checkpoint.DiskPageItems+1)
	for i := range items {
		items[i] = dependentItem(t, fmt.Sprintf("translation-%04d", i), target, false)
	}
	seedDependents(t, store, items)
	clearDependentWork(t, store)
	change := TargetObservation{Target: target, ResourceVersion: "1", Generation: 1, ContentDigest: "first"}
	change.Target.UID = "target-uid"
	require.NoError(t, QueueTargetChange(t.Context(), store, nil, &change))
	progress, err := store.ProcessFanout(t.Context())
	require.NoError(t, err)
	require.True(t, progress)
	require.EqualValues(t, checkpoint.DiskPageItems, store.Counts().Pending)
	clearDependentWork(t, store)

	previous := change
	change.Generation, change.ResourceVersion, change.ContentDigest = 2, "2", "second"
	require.NoError(t, QueueTargetChange(t.Context(), store, &previous, &change))
	require.NoError(t, store.Close())
	store = nil
	store, err = checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	finishDependentFanout(t, store)
	require.EqualValues(t, len(items), store.Counts().Pending,
		"a change behind the durable cursor must requeue even acknowledged sources")
	_, cursor, err := store.Active(t.Context())
	require.NoError(t, err)
	require.Equal(t, "translation-journal@start", cursor)
}

func TestTargetInvalidationFailureRemainsReplayable(t *testing.T) {
	ctx := t.Context()
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Translation")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	target := dependencyTarget()
	oldName, newName := target, target
	newName.Name = "renamed"
	items := []checkpoint.DiskItem{
		dependentItem(t, "old-name", oldName, false),
		dependentItem(t, "new-name", newName, false),
	}
	seedDependents(t, store, items)
	clearDependentWork(t, store)
	previous := TargetObservation{Target: oldName, ResourceVersion: "1", Generation: 1, ContentDigest: "one"}
	current := previous
	current.Target, current.ResourceVersion = newName, "2"
	requests := 0
	failed := fanoutRequestFunc(func(ctx context.Context, index string) error {
		requests++
		if requests == 2 {
			return fmt.Errorf("injected checkpoint failure")
		}
		return store.RequestCoalescedFanout(ctx, index)
	})
	require.ErrorContains(t, QueueTargetChange(ctx, failed, &previous, &current), "injected checkpoint failure")
	require.NoError(t, QueueTargetChange(ctx, store, &previous, &current))
	finishDependentFanout(t, store)
	work, err := store.Pending(ctx, "", checkpoint.DiskPageItems)
	require.NoError(t, err)
	require.Len(t, work, 2)
}

type fanoutRequestFunc func(context.Context, string) error

func (request fanoutRequestFunc) RequestCoalescedFanout(ctx context.Context, index string) error {
	return request(ctx, index)
}

func dependencyTarget() DependencyTarget {
	return DependencyTarget{
		NamespaceUID: "namespace-uid", APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind: "Product", Name: "target", UID: "target-uid",
	}
}

func dependentItem(t *testing.T, name string, target DependencyTarget, pinned bool) checkpoint.DiskItem {
	t.Helper()
	indexes, err := DependencyIndexes(target, pinned)
	require.NoError(t, err)
	return checkpoint.DiskItem{
		Key:     types.WorkItemKey{Kind: "Translation", Namespace: "tenant", Name: name},
		Version: "1", Value: json.RawMessage(`{"source":"accepted"}`), Indexes: indexes,
	}
}

func seedDependents(t *testing.T, store *checkpoint.DiskStore, items []checkpoint.DiskItem) {
	t.Helper()
	generation, err := store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	for len(items) > 0 {
		count := min(len(items), checkpoint.DiskPageItems)
		require.NoError(t, store.PutSnapshotPage(t.Context(), generation, items[:count]))
		items = items[count:]
	}
	require.NoError(t, store.FinishSnapshot(t.Context(), generation, "translation-journal@start"))
}

func clearDependentWork(t *testing.T, store *checkpoint.DiskStore) {
	t.Helper()
	for {
		work, err := store.Pending(t.Context(), "", checkpoint.DiskPageItems)
		require.NoError(t, err)
		if len(work) == 0 {
			return
		}
		for _, pending := range work {
			acknowledged, err := store.Acknowledge(t.Context(), pending.Key, pending.Token)
			require.NoError(t, err)
			require.True(t, acknowledged)
		}
	}
}

func finishDependentFanout(t *testing.T, store *checkpoint.DiskStore) {
	t.Helper()
	for range 16 {
		progress, err := store.ProcessFanout(t.Context())
		require.NoError(t, err)
		if !progress {
			return
		}
	}
	t.Fatal("fan-out failed to complete within its expected bounded turns")
}
