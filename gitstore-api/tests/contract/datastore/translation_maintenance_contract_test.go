// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func runTranslationSourceAuditContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	audit, ok := backend.(datastore.TranslationSourceAuditStore)
	require.True(t, ok)
	store, live := newTranslationSource(t, backend)
	require.NoError(t, store.CreateTranslation(t.Context(), live))
	retired := *live
	retired.UID, retired.Name = newID(), "retired"
	require.NoError(t, store.CreateTranslation(t.Context(), &retired))
	require.NoError(t, store.DeleteTranslation(t.Context(), retired.UID, "1"))
	otherStore, other := newTranslationSource(t, backend)
	require.NoError(t, otherStore.CreateTranslation(t.Context(), other))
	observed := map[string]string{}
	cursor, finished, emptyTurns := "", false, 0
	for range 2048 {
		page, err := datastore.RecoverTranslationSourceAuditPage(t.Context(), audit, live.NamespaceID, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Scanned, 1)
		require.LessOrEqual(t, len(page.Items), page.Scanned)
		if len(page.Items) == 0 {
			emptyTurns++
		}
		for _, state := range page.Items {
			require.Equal(t, live.NamespaceID, state.NamespaceUID)
			require.NotContains(t, observed, state.TranslationUID, "a stable scan must not repeat a source")
			observed[state.TranslationUID] = state.Phase
			require.NotEmpty(t, state.ID)
			require.NotEmpty(t, state.ResourceVersion)
		}
		if page.Next == "" {
			finished = true
			break
		}
		require.NotEqual(t, cursor, page.Next)
		_, err = audit.ListTranslationSourceAudit(t.Context(), other.NamespaceID, page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument, "audit continuations must retain exact namespace scope")
		cursor = page.Next
	}
	require.True(t, finished, "bounded source discovery did not terminate")
	require.Positive(t, emptyTurns, "foreign namespaces must produce bounded empty turns, not leaked resources or early EOF")
	require.Equal(t, map[string]string{live.UID: "Stable", retired.UID: "Retired"}, observed)
	unchanged, err := store.GetTranslation(t.Context(), live.UID)
	require.NoError(t, err)
	require.Equal(t, live, unchanged, "audit turns must preserve authored bytes, source/status and accepted versions")
	_, err = store.GetTranslation(t.Context(), retired.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound, "audit turns cannot revive a retired UID")
	for _, limit := range []int{0, -1, datastore.MaxTranslationPageSize + 1} {
		_, err := audit.ListTranslationSourceAudit(t.Context(), live.NamespaceID, "", limit)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	}
	for _, invalid := range []string{"not-a-cursor!", "e30", strings.Repeat("x", 32<<10+1)} {
		_, err := audit.ListTranslationSourceAudit(t.Context(), live.NamespaceID, invalid, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = audit.ListTranslationSourceAudit(ctx, live.NamespaceID, "", 1)
	require.ErrorIs(t, err, context.Canceled)
}

func runTranslationMaintenanceContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	store, source := newTranslationSource(t, backend)
	maintenance := translationIndexes(t, backend)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	before, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, "Stable", before.Phase)
	require.Equal(t, source.ResourceVersion, before.SourceVersion)
	require.Equal(t, source.NamespaceID, before.NamespaceUID)
	require.NotEmpty(t, before.ResourceVersion)
	cursor, found := "", false
	for range 32 {
		page, err := maintenance.ListTranslationMaintenance(t.Context(), source.Namespace, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Items), 1)
		require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
		for _, item := range page.Items {
			require.Equal(t, before, &item)
			found = true
		}

		if page.Next == "" {
			break
		}
		require.NotEqual(t, cursor, page.Next)
		cursor = page.Next
	}
	require.True(t, found)
	bindings := translationBindings(t, backend)
	binding, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	binding.ResourceVersion, binding.Epochs[0].BoundUID = "2", newID()
	require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), binding, "1"))
	after, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.NotEqual(t, before.ResourceVersion, after.ResourceVersion, "private binding progress also fences maintenance")
	require.Equal(t, before.SourceVersion, after.SourceVersion)
	require.NoError(t, maintenance.RecoverTranslation(t.Context(), source.UID))
	unchanged, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, after, unchanged, "healthy repair must not allocate fresh slots or versions")
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "1"))
	require.NoError(t, maintenance.RecoverTranslation(t.Context(), source.UID), "recovery must acknowledge an already completed deletion")
}

func runTranslationSourceAuditRecoveryContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	audit, ok := backend.(datastore.TranslationSourceAuditStore)
	require.True(t, ok)
	store, source := newTranslationSource(t, backend)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	maintenance := translationIndexes(t, backend)
	before, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	_, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "source existence does not certify an audit")
	require.NoError(t, audit.RecoverTranslationSourceAudit(t.Context(), *before))
	watermark, err := audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, watermark)
	after, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after, "healthy audit repair cannot advance source/private versions")
	for _, mode := range []string{"namespace", "version", "lifetime", "malformed-uid", "oversized-private-version", "oversized-source-version", "unknown-phase"} {
		t.Run(mode, func(t *testing.T) {
			hint := *before
			want := datastore.ErrConflict
			switch mode {
			case "namespace":
				hint.NamespaceUID = newID()
			case "version":
				hint.SourceVersion = "999"
			case "lifetime":
				hint.CreationTimestamp = hint.CreationTimestamp.Add(time.Millisecond)
			case "malformed-uid":
				hint.TranslationUID = "not-a-uid"
				want = datastore.ErrInvalidArgument
			case "oversized-private-version":
				hint.ResourceVersion = strings.Repeat("1", 257)
				hint.ID = hint.TranslationUID + "/" + hint.ResourceVersion
				want = datastore.ErrInvalidArgument
			case "oversized-source-version":
				hint.SourceVersion = strings.Repeat("1", 257)
				want = datastore.ErrInvalidArgument
			case "unknown-phase":
				hint.Phase = "Certified"
				want = datastore.ErrInvalidArgument
			}
			err := audit.RecoverTranslationSourceAudit(t.Context(), hint)
			require.ErrorIs(t, err, want)
			unchanged, err := store.GetTranslation(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, source, unchanged)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, audit.RecoverTranslationSourceAudit(ctx, *before), context.Canceled)
	next := *source
	next.ResourceVersion, next.Generation, next.Body = "2", 2, source.Body+"new accepted body"
	require.NoError(t, store.UpdateTranslation(t.Context(), &next, "1"))
	_, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "a source mutation invalidates the previously published repair watermark")
	require.ErrorIs(t, audit.RecoverTranslationSourceAudit(t.Context(), *before), datastore.ErrConflict)
	current, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.NoError(t, audit.RecoverTranslationSourceAudit(t.Context(), *current))
	watermark, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, current, watermark)
	unchanged, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, &next, unchanged)
	bindings := translationBindings(t, backend)
	binding, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	expectedBindingVersion := binding.ResourceVersion
	binding.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(expectedBindingVersion)
	require.NoError(t, err)
	binding.Epochs[0].BoundUID = newID()
	require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), binding, expectedBindingVersion))
	_, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "binding-only progress invalidates repair without a public source mutation")
	current, err = maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, "2", current.SourceVersion)
	require.NoError(t, audit.RecoverTranslationSourceAudit(t.Context(), *current))
	watermark, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, current, watermark)
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, "2"))
	_, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "retirement cannot inherit a live-source watermark")
	retired, err := maintenance.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.NoError(t, audit.RecoverTranslationSourceAudit(t.Context(), *retired))
	watermark, err = audit.GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, retired, watermark)
	_, err = store.GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func runTranslationIndexAuditContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	audit, ok := backend.(datastore.TranslationIndexAuditStore)
	require.True(t, ok)
	sources, source := newTranslationSource(t, backend)
	require.NoError(t, sources.CreateTranslation(t.Context(), source))
	indexes := translationIndexes(t, backend)
	before, err := indexes.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	binding, err := translationBindings(t, backend).GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	shard, err := datastore.TranslationIndexShard(source.UID)
	require.NoError(t, err)
	var orphans []datastore.TranslationIndexReservation
	for _, published := range []bool{false, true} {
		target := binding.Epochs[0].Target
		target.Name = "obsolete-" + newID()
		index := datastore.TranslationIndexKey{Kind: datastore.TranslationTargetNameIndex,
			NamespaceUID: source.NamespaceID, Target: target, Shard: shard}
		entry := datastore.TranslationIndexEntry{TranslationUID: source.UID, SourceVersion: source.ResourceVersion,
			ReferenceEpoch: binding.CurrentEpoch, CreationTimestamp: source.CreationTimestamp}
		reserved, err := indexes.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
		require.NoError(t, err)
		if published {
			require.NoError(t, indexes.CommitTranslationIndex(t.Context(), reserved))
		}
		orphans = append(orphans, reserved)
	}
	observed, cursor, finished := map[string]bool{}, "", false
	for range 4096 {
		page, err := audit.ListTranslationIndexAudit(t.Context(), source.NamespaceID, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Scanned, 1)
		require.LessOrEqual(t, len(page.Tokens), page.Scanned)
		for _, token := range page.Tokens {
			require.False(t, observed[token], "a stable reservation scan cannot repeat tokens")
			observed[token] = true
			require.NoError(t, audit.RecoverTranslationIndexAudit(t.Context(), source.NamespaceID, token))
		}
		if page.Next == "" {
			finished = true
			break
		}
		require.NotEqual(t, cursor, page.Next)
		_, err = audit.ListTranslationIndexAudit(t.Context(), newID(), page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		_, err = backend.(datastore.TranslationSourceAuditStore).ListTranslationSourceAudit(t.Context(), source.NamespaceID, page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument, "reservation cursors cannot resume a source scan")
		cursor = page.Next
	}
	require.True(t, finished)
	for _, orphan := range orphans {
		require.True(t, observed[orphan.Token])
		current, err := indexes.GetTranslationIndexReservation(t.Context(), orphan.Token)
		require.NoError(t, err)
		require.Equal(t, datastore.TranslationReservationRemoved, current.Phase)
		require.ErrorIs(t, indexes.CommitTranslationIndex(t.Context(), orphan), datastore.ErrConflict,
			"an interrupted publisher cannot revive an audited stale edge")
		require.NoError(t, audit.RecoverTranslationIndexAudit(t.Context(), source.NamespaceID, orphan.Token))
		require.ErrorIs(t, audit.RecoverTranslationIndexAudit(t.Context(), newID(), orphan.Token), datastore.ErrInvalidArgument)
	}
	after, err := indexes.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	unchanged, err := sources.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, unchanged)
	currentBinding, err := translationBindings(t, backend).GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, binding, currentBinding)
	target := binding.Epochs[0].Target
	target.Name = "retired-source-" + newID()
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationTargetNameIndex,
		NamespaceUID: source.NamespaceID, Target: target, Shard: shard}
	entry := datastore.TranslationIndexEntry{TranslationUID: source.UID, SourceVersion: source.ResourceVersion,
		ReferenceEpoch: binding.CurrentEpoch, CreationTimestamp: source.CreationTimestamp}
	stale, err := indexes.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
	require.NoError(t, err)
	require.NoError(t, indexes.CommitTranslationIndex(t.Context(), stale))
	require.NoError(t, sources.DeleteTranslation(t.Context(), source.UID, source.ResourceVersion))
	require.NoError(t, audit.RecoverTranslationIndexAudit(t.Context(), source.NamespaceID, stale.Token))
	removed, err := indexes.GetTranslationIndexReservation(t.Context(), stale.Token)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationReservationRemoved, removed.Phase)
	require.ErrorIs(t, indexes.CommitTranslationIndex(t.Context(), stale), datastore.ErrConflict)

	entry.TranslationUID = newID()
	index.Shard, err = datastore.TranslationIndexShard(entry.TranslationUID)
	require.NoError(t, err)
	unknown, err := indexes.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
	require.NoError(t, err)
	require.NoError(t, indexes.CommitTranslationIndex(t.Context(), unknown))
	require.ErrorIs(t, audit.RecoverTranslationIndexAudit(t.Context(), source.NamespaceID, unknown.Token), datastore.ErrRepairRequired,
		"a missing source without permanent retirement proof cannot authorize index removal")
	retained, err := indexes.GetTranslationIndexReservation(t.Context(), unknown.Token)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationReservationCommitted, retained.Phase)
}

func runTranslationIndexCellAuditContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	audit, ok := backend.(datastore.TranslationIndexCellAuditStore)
	require.True(t, ok)
	sources, source := newTranslationSource(t, backend)
	require.NoError(t, sources.CreateTranslation(t.Context(), source))
	bindings, indexes := translationBindings(t, backend), translationIndexes(t, backend)
	binding, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	binding.ResourceVersion, binding.Epochs[0].BoundUID = "2", newID()
	require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), binding, "1"))
	before, err := indexes.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	shard, err := datastore.TranslationIndexShard(source.UID)
	require.NoError(t, err)
	target := binding.Epochs[0].Target
	target.Name = "obsolete-cell-" + newID()
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationTargetNameIndex,
		NamespaceUID: source.NamespaceID, Target: target, Shard: shard}
	entry := datastore.TranslationIndexEntry{TranslationUID: source.UID, SourceVersion: source.ResourceVersion,
		ReferenceEpoch: binding.CurrentEpoch, CreationTimestamp: source.CreationTimestamp}
	orphan, err := indexes.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
	require.NoError(t, err)
	require.NoError(t, indexes.CommitTranslationIndex(t.Context(), orphan))
	kinds, seenOrphan, cursor, finished := map[datastore.TranslationIndexKind]bool{}, false, "", false
	for range 4096 {
		page, err := audit.ListTranslationIndexCellAudit(t.Context(), source.NamespaceID, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Scanned, 1)
		require.LessOrEqual(t, len(page.Items), page.Scanned)
		for _, item := range page.Items {
			require.Equal(t, source.NamespaceID, item.Index.NamespaceUID)
			kinds[item.Index.Kind] = true
			seenOrphan = seenOrphan || item.Cell.Token == orphan.Token
			require.ErrorIs(t, audit.RecoverTranslationIndexCellAudit(t.Context(), newID(), item), datastore.ErrInvalidArgument)
			forged := item
			forged.Cell.Entry.SourceVersion = "999999"
			require.ErrorIs(t, audit.RecoverTranslationIndexCellAudit(t.Context(), source.NamespaceID, forged), datastore.ErrRepairRequired)
			require.NoError(t, audit.RecoverTranslationIndexCellAudit(t.Context(), source.NamespaceID, item))
		}
		if page.Next == "" {
			finished = true
			break
		}
		require.NotEqual(t, cursor, page.Next)
		_, err = backend.(datastore.TranslationIndexAuditStore).ListTranslationIndexAudit(t.Context(), source.NamespaceID, page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		_, err = audit.ListTranslationIndexCellAudit(t.Context(), newID(), page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		cursor = page.Next
	}
	require.True(t, finished)
	require.True(t, seenOrphan)
	require.Len(t, kinds, 3, "scan all source-index layouts independently of reservation discovery")
	current, err := indexes.GetTranslationIndexReservation(t.Context(), orphan.Token)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationReservationRemoved, current.Phase)
	require.ErrorIs(t, indexes.CommitTranslationIndex(t.Context(), orphan), datastore.ErrConflict)
	after, err := indexes.GetTranslationMaintenance(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	unchanged, err := sources.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, unchanged)
	currentBinding, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, binding, currentBinding)
}
