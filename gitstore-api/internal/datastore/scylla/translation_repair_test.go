// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTranslationTargetSourceAuditFindsAllKindsAndPreservesLegacyAuthority(t *testing.T) {
	first, second := newTestStores(t)
	namespace := &datastore.Namespace{
		UID: newID(), Name: "target-audit-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	expected := map[string]*datastore.Product{}
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		target := newProduct(namespace.Name, "audit-"+kind)
		target.Kind, target.Generation, target.ResourceVersion = kind, 1, "1"
		target.Spec, target.Body = json.RawMessage(`{"title":"Audit"}`), "unchanged\r\n"
		if kind == "File" {
			target.APIVersion = "storage.gitstore.dev/v1beta1"
		}
		require.NoError(t, writeNativeTranslationTarget(t.Context(), first, target, true, ""))
		expected[target.UID] = target
	}
	decorated := datastore.NewInstrumentedDatastore(second, "scylla", zap.NewNop())
	auditor, ok := decorated.(datastore.TranslationTargetSourceAuditStore)
	require.True(t, ok)
	seen := map[string]datastore.TranslationTargetSourceAuditHint{}
	cursor := ""
	for range 2048 {
		page, err := auditor.ListTranslationTargetSourceAudit(t.Context(), namespace.UID, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Scanned, 1)
		require.LessOrEqual(t, len(page.Items), page.Scanned)
		for _, hint := range page.Items {
			require.Equal(t, namespace.UID, hint.Target.NamespaceUID)
			require.Equal(t, namespace.Name, hint.Namespace)
			target := expected[hint.UID]
			require.NotNil(t, target)
			require.Equal(t, target.Kind, hint.Target.Kind)
			require.NoError(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint))
			seen[hint.UID] = hint
		}
		if page.Next == "" {
			break
		}
		require.NotEqual(t, cursor, page.Next)
		_, err = second.(datastore.TranslationSourceAuditStore).ListTranslationSourceAudit(t.Context(), namespace.UID, page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		_, err = auditor.ListTranslationTargetSourceAudit(t.Context(), newID(), page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		cursor = page.Next
	}
	require.Len(t, seen, len(expected))
	var product *datastore.Product
	for _, target := range expected {
		if target.Kind == "Product" {
			product = target
		}
	}
	hint := seen[product.UID]
	product.Generation, product.ResourceVersion, product.Body = 2, "2", "new body"
	restore := scylla.FailMutationBeforeForTest(first, "stabilize-target-source")
	err := writeNativeTranslationTarget(t.Context(), first, product, false, "1")
	restore()
	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	require.ErrorIs(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint), datastore.ErrConflict)
	hint.ResourceVersion = "2"
	require.ErrorIs(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint), datastore.ErrConflict,
		"pending recovery must reload the audit observation before accepting it")
	require.NoError(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint))
	raw := newRawSession(t)
	require.NoError(t, raw.Query("UPDATE products_by_key SET translation_source_identity=null WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID)).Exec())
	require.ErrorIs(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint), datastore.ErrRepairRequired,
		"never assign today's Namespace UID to unproved legacy source ownership")
	var identity *string
	require.NoError(t, raw.Query("SELECT translation_source_identity FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID)).Scan(&identity))
	require.Nil(t, identity)
}

func TestTranslationTargetAuditCoverageIgnoresPermanentlyRetiredSources(t *testing.T) {
	first, second := newTestStores(t)
	namespace := &datastore.Namespace{
		UID: newID(), Name: "retired-audit-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	raw := newRawSession(t)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		for _, mode := range []string{"negative-marker", "late-physical-replay"} {
			target := newProduct(namespace.Name, kind+"-"+mode)
			target.Kind, target.Generation, target.ResourceVersion = kind, 1, "1"
			if kind == "File" {
				target.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			require.NoError(t, writeNativeTranslationTarget(t.Context(), first, target, true, ""))
			require.NoError(t, deleteNativeTranslationTarget(t.Context(), first, kind, target.UID))
			table := map[string]string{
				"Product": "products_by_key", "ProductVariant": "product_variants_by_namespace",
				"CategoryTaxonomy": "category_taxonomies_by_namespace", "Collection": "collections_by_namespace", "File": "files_by_namespace",
			}[kind]
			if mode == "negative-marker" {
				require.NoError(t, raw.Query("INSERT INTO "+table+
					" (namespace,creation_timestamp,uid,translation_retired) VALUES (?,?,?,true)",
					namespace.Name, target.CreationTimestamp, mustTranslationUUID(t, target.UID)).Exec())
			} else {
				require.NoError(t, raw.Query("INSERT INTO "+table+
					" (namespace,creation_timestamp,uid,name,api_version,kind,generation,resource_version) VALUES (?,?,?,?,?,?,?,?)",
					namespace.Name, target.CreationTimestamp, mustTranslationUUID(t, target.UID),
					target.Name, target.APIVersion, kind, int64(1), "1").Exec())
			}
		}
	}
	auditor := second.(datastore.TranslationTargetAuditStore)
	var progress datastore.TranslationAuditProgress
	for range 2048 {
		var err error
		progress, err = auditor.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
		require.NoError(t, err)
		if progress.Phase == "Verified" {
			break
		}
	}
	require.Equal(t, "Verified", progress.Phase)
	epoch, err := datastore.ReadTranslationTargetAuditCoverage(t.Context(), auditor, namespace.UID)
	require.NoError(t, err)
	require.Equal(t, progress.TargetMutationEpoch, epoch)
}

func TestTranslationTargetAuditProgressFencesReplicasAndLateMutations(t *testing.T) {
	first, second := newTestStores(t)
	auditor, replacement := first.(datastore.TranslationTargetAuditStore), second.(datastore.TranslationTargetAuditStore)
	namespace := &datastore.Namespace{
		UID: newID(), Name: "audit-cas-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	initial, err := auditor.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	require.NoError(t, err)
	var winner datastore.TranslationAuditProgress
	restore := scylla.RunMutationBeforeForTest(first, "advance-target-audit", func() {
		var err error
		winner, err = replacement.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
		require.NoError(t, err)
	})
	_, err = auditor.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	restore()
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.NotEqual(t, initial.ResourceVersion, winner.ResourceVersion)
	stored, err := auditor.GetTranslationTargetAuditProgress(t.Context(), namespace.UID)
	require.NoError(t, err)
	require.Equal(t, winner, stored, "a stale replica cannot overwrite the winning page")
	restore = scylla.FailMutationAfterForTest(first, "advance-target-audit")
	_, err = auditor.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	restore()
	require.Error(t, err)
	stored, err = replacement.GetTranslationTargetAuditProgress(t.Context(), namespace.UID)
	require.NoError(t, err)
	require.NotEqual(t, winner.ResourceVersion, stored.ResourceVersion, "uncertain responses require reloading committed progress")
	for range 2048 {
		layout, _, err := datastore.DecodeTranslationTargetSourceAuditCursor(namespace.UID, "scylla", stored.Cursor, 256)
		require.NoError(t, err)
		if stored.Phase == "Verifying" && layout == 4 {
			page, err := replacement.ListTranslationTargetSourceAudit(t.Context(), namespace.UID, stored.Cursor, 256)
			require.NoError(t, err)
			if page.Next == "" {
				break
			}
		}
		stored, err = replacement.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
		require.NoError(t, err)
	}
	require.Equal(t, "Verifying", stored.Phase)
	restore = scylla.RunMutationBeforeForTest(first, "advance-target-audit", func() {
		target := newProduct(namespace.Name, "after-last-clock-read")
		target.Kind, target.APIVersion = "File", "storage.gitstore.dev/v1beta1"
		target.Generation, target.ResourceVersion = 0, "1"
		require.NoError(t, writeNativeTranslationTarget(t.Context(), second, target, true, ""))
	})
	verified, err := auditor.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	restore()
	require.NoError(t, err)
	require.Equal(t, "Verified", verified.Phase)
	epoch, err := datastore.ReadTranslationTargetAuditCoverage(t.Context(), replacement, namespace.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.Empty(t, epoch, "an unproved mutation between verification and progress CAS cannot publish current coverage")
	restarted, err := replacement.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	require.NoError(t, err)
	require.NotEqual(t, verified.RunID, restarted.RunID)
	require.Equal(t, "Repairing", restarted.Phase)
}

func TestTranslationTargetAuditOperatorPreviewsWithoutBackfillAndResumes(t *testing.T) {
	first, second := newTestStores(t)
	namespace := &datastore.Namespace{
		UID: newID(), Name: "operator-targets-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	service, err := scylla.OpenProjectionRepairService(testScyllaConfig(t))
	require.NoError(t, err)
	defer service.Close()
	inspection, err := service.InspectTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	require.NoError(t, err)
	require.Equal(t, "Repairing", inspection.NextPhase)
	raw := newRawSession(t)
	var clocks int
	require.NoError(t, raw.Query("SELECT count(*) FROM translation_target_mutations_by_namespace WHERE namespace=?",
		namespace.Name).Scan(&clocks))
	require.Zero(t, clocks, "inspection must not initialize a source mutation clock")
	target := newProduct(namespace.Name, "known-birth")
	target.Kind, target.APIVersion = "File", "storage.gitstore.dev/v1beta1"
	target.Generation, target.ResourceVersion = 0, ""
	require.NoError(t, writeNativeTranslationTarget(t.Context(), first, target, true, ""))
	target.Generation, target.ResourceVersion, target.Body = 1, "1", "retained\r\nbody"
	require.NoError(t, writeNativeTranslationTarget(t.Context(), first, target, false, ""))
	before, err := first.GetFile(t.Context(), target.UID)
	require.NoError(t, err)
	inspection, err = service.InspectTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	require.NoError(t, err)
	require.Equal(t, "Repairing", inspection.NextPhase)
	auditor := second.(datastore.TranslationTargetAuditStore)
	_, err = auditor.GetTranslationTargetAuditProgress(t.Context(), namespace.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	for range 2048 {
		progress, err := service.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
		if errors.Is(err, datastore.ErrConflict) {
			continue
		}
		require.NoError(t, err)
		stored, err := auditor.GetTranslationTargetAuditProgress(t.Context(), namespace.UID)
		require.NoError(t, err)
		require.Equal(t, progress, stored)
		if progress.Phase == "Verified" {
			break
		}
	}
	epoch, err := datastore.ReadTranslationTargetAuditCoverage(t.Context(), auditor, namespace.UID)
	require.NoError(t, err)
	require.NotEmpty(t, epoch)
	after, err := second.GetFile(t.Context(), target.UID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	inspection, err = service.InspectTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
	require.NoError(t, err)
	require.Equal(t, "Verified", inspection.NextPhase)
	require.Zero(t, inspection.Scanned)
}

func TestTranslationTargetCoverageEpochRejectsPriorNamespaceBirth(t *testing.T) {
	first, second := newTestStores(t)
	raw := newRawSession(t)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			namespace := &datastore.Namespace{
				UID: newID(), Name: "coverage-birth-" + newID(), Tier: datastore.NamespaceTierOrganization,
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			require.NoError(t, first.CreateNamespace(t.Context(), namespace))
			target := newProduct(namespace.Name, "old-birth")
			target.Kind, target.Generation, target.ResourceVersion = kind, 1, "1"
			if kind == "File" {
				target.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			require.NoError(t, writeNativeTranslationTarget(t.Context(), first, target, true, ""))
			readClock := func() string {
				var epoch string
				require.NoError(t, raw.Query(
					"SELECT translation_coverage_epoch FROM translation_target_mutations_by_namespace WHERE namespace=?",
					namespace.Name).Scan(&epoch))
				return epoch
			}
			before := readClock()
			replacement := *namespace
			replacement.UID = newID()
			restore := scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
				require.NoError(t, second.DeleteNamespace(t.Context(), namespace.UID))
				require.NoError(t, second.CreateNamespace(t.Context(), &replacement))
			})
			target.Generation, target.ResourceVersion, target.Body = 2, "2", "old-lifetime material"
			err := writeNativeTranslationTarget(t.Context(), first, target, false, "1")
			restore()
			require.NoError(t, err)
			require.NotEqual(t, before, readClock(), "a fenced writer for an old birth cannot preserve current namespace coverage")
		})
	}
}

func TestTranslationTargetAuditCoversPendingHeadsWithoutPositiveMaterial(t *testing.T) {
	first, second := newTestStores(t)
	auditor := second.(datastore.TranslationTargetAuditStore)
	fences := second.(datastore.TranslationTargetFenceStore)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			namespace := &datastore.Namespace{
				UID: newID(), Name: "pending-coverage-" + newID(), Tier: datastore.NamespaceTierOrganization,
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			require.NoError(t, first.CreateNamespace(t.Context(), namespace))
			target := newProduct(namespace.Name, "pending")
			target.Kind, target.Generation, target.ResourceVersion = kind, 1, "1"
			if kind == "File" {
				target.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			require.NoError(t, writeNativeTranslationTarget(t.Context(), first, target, true, ""))
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: target.APIVersion, Kind: kind, Name: target.Name,
			}
			before, err := auditor.ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
			require.NoError(t, err)
			restore := scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
				var progress datastore.TranslationAuditProgress
				for range 2048 {
					progress, err = auditor.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
					require.NoError(t, err, "a held proved source grant must not starve coverage")
					if progress.Phase == "Verified" {
						break
					}
				}
				require.Equal(t, "Verified", progress.Phase)
				epoch, err := datastore.ReadTranslationTargetAuditCoverage(t.Context(), auditor, namespace.UID)
				require.NoError(t, err)
				require.Equal(t, before, epoch)
			})
			target.Generation, target.ResourceVersion, target.Body = 2, "2", "paused proved material"
			err = writeNativeTranslationTarget(t.Context(), first, target, false, "1")
			restore()
			require.NoError(t, err, "coverage verification must leave the paused writer's grant intact")
			target.Generation, target.ResourceVersion, target.Body = 3, "3", "committed but pending"
			restore = scylla.FailMutationBeforeForTest(first, "stabilize-target-source")
			err = writeNativeTranslationTarget(t.Context(), first, target, false, "2")
			restore()
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			pending, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetUpdating, pending.Phase)
			require.NoError(t, auditor.VerifyTranslationTargetSourceCoverage(t.Context(), datastore.TranslationTargetSourceAuditHint{
				Target: key, Namespace: namespace.Name, UID: target.UID,
				CreationTimestamp: target.CreationTimestamp, ResourceVersion: "1",
			}), "coverage does not require the discovered material version to remain stationary")
			var progress datastore.TranslationAuditProgress
			for range 2048 {
				progress, err = auditor.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
				require.NoError(t, err)
				if progress.Phase == "Verified" {
					break
				}
			}
			require.Equal(t, "Verified", progress.Phase)
			epoch, err := datastore.ReadTranslationTargetAuditCoverage(t.Context(), auditor, namespace.UID)
			require.NoError(t, err)
			require.Equal(t, before, epoch)
			after, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, pending, after, "coverage verification must not cancel an in-flight material writer")
			evidence, err := fences.ReadTargetEvidence(t.Context(), key)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Nil(t, evidence, "recoverable coverage is not positive current material")
			require.NoError(t, second.(interface {
				RecoverTranslationTargetWrite(context.Context, datastore.TranslationTargetKey) error
			}).RecoverTranslationTargetWrite(t.Context(), key))
			evidence, err = fences.ReadTargetEvidence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, "3", evidence.ResourceVersion)
		})
	}
}

func TestTranslationAuditOperatorServiceInspectsWithoutMutationAndResumes(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), source))
	service, err := scylla.OpenProjectionRepairService(testScyllaConfig(t))
	require.NoError(t, err)
	defer service.Close()
	before, err := service.InspectTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
	require.NoError(t, err)
	require.Equal(t, "Sources", before.NextPhase)
	require.LessOrEqual(t, before.Scanned, 1)
	auditor := second.(datastore.TranslationAuditRepairStore)
	_, err = auditor.GetTranslationAuditProgress(t.Context(), source.NamespaceID)
	require.ErrorIs(t, err, datastore.ErrNotFound, "inspection must not initialize progress or repair sources")
	_, err = first.(datastore.TranslationSourceAuditStore).GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrRepairRequired)

	progress, err := service.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
	require.NoError(t, err)
	stored, err := auditor.GetTranslationAuditProgress(t.Context(), source.NamespaceID)
	require.NoError(t, err)
	require.Equal(t, progress, stored)
	replacement, err := scylla.OpenProjectionRepairService(testScyllaConfig(t))
	require.NoError(t, err)
	defer replacement.Close()
	for range 2048 {
		inspection, err := replacement.InspectTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
		require.NoError(t, err)
		require.Equal(t, progress, inspection.Progress)
		stored, err = auditor.GetTranslationAuditProgress(t.Context(), source.NamespaceID)
		require.NoError(t, err)
		require.Equal(t, progress, stored)
		progress, err = replacement.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
		require.NoError(t, err)
		if progress.Phase == "Scanned" {
			break
		}
	}
	require.Equal(t, "Scanned", progress.Phase)
	inspection, err := replacement.InspectTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
	require.NoError(t, err)
	require.Equal(t, progress, inspection.Progress)
	require.Equal(t, "Sources", inspection.NextPhase, "a completed traversal previews a fresh run")
	current, err := first.(datastore.TranslationStore).GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, current)
}

func recoverTranslationThroughAudit(ctx context.Context, store datastore.Datastore, uid string) error {
	hint, err := store.(datastore.TranslationIndexCapable).TranslationIndexStore().GetTranslationMaintenance(ctx, uid)
	if err != nil {
		return err
	}
	return store.(datastore.TranslationSourceAuditStore).RecoverTranslationSourceAudit(ctx, *hint)
}

func TestTranslationAuditProgressSurvivesReplacementAndDoesNotCertify(t *testing.T) {
	first, second := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), source))
	auditor, ok := first.(datastore.TranslationAuditRepairStore)
	require.True(t, ok)
	replacement, ok := second.(datastore.TranslationAuditRepairStore)
	require.True(t, ok)
	progress, err := auditor.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
	require.NoError(t, err)
	require.NotEmpty(t, progress.RunID)
	runID := progress.RunID
	phases := map[string]bool{progress.Phase: true}
	for range 2048 {
		stored, err := replacement.GetTranslationAuditProgress(t.Context(), source.NamespaceID)
		require.NoError(t, err)
		require.Equal(t, progress, stored)
		previous := progress
		progress, err = replacement.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
		require.NoError(t, err)
		require.Equal(t, runID, progress.RunID)
		require.NotEqual(t, previous.ResourceVersion, progress.ResourceVersion)
		require.GreaterOrEqual(t, progress.Scanned, previous.Scanned)
		require.LessOrEqual(t, progress.Scanned-previous.Scanned, int64(1))
		phases[progress.Phase] = true
		if progress.Phase == "Scanned" {
			break
		}
		auditor, replacement = replacement, auditor
	}
	require.Equal(t, "Scanned", progress.Phase)
	require.True(t, phases["Reservations"])
	require.True(t, phases["Cells"])
	require.True(t, phases["VerifySources"])
	watermark, err := second.(datastore.TranslationSourceAuditStore).GetTranslationSourceAuditWatermark(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source.ResourceVersion, watermark.SourceVersion)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = auditor.RepairTranslationAuditTurn(ctx, source.NamespaceID, 1)
	require.ErrorIs(t, err, context.Canceled)
	_, err = auditor.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 0)
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	stored, err := replacement.GetTranslationAuditProgress(t.Context(), source.NamespaceID)
	require.NoError(t, err)
	require.Equal(t, progress, stored)
	next, err := auditor.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
	require.NoError(t, err)
	require.NotEqual(t, runID, next.RunID, "a later audit must start a new traversal, not reuse Scanned as certification")
	require.NotEqual(t, progress.ResourceVersion, next.ResourceVersion)
}

func TestTranslationAuditFailedPageDoesNotAdvanceOwnedCursor(t *testing.T) {
	first, _ := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), source))
	auditor, ok := first.(datastore.TranslationAuditRepairStore)
	require.True(t, ok)
	_, err := auditor.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
	require.NoError(t, err)
	raw := newRawSession(t)
	require.NoError(t, raw.Query("DELETE FROM translations_by_uid WHERE uid=?", mustTranslationUUID(t, source.UID)).Exec())
	failed := false
	for range 2048 {
		before, err := auditor.GetTranslationAuditProgress(t.Context(), source.NamespaceID)
		require.NoError(t, err)
		_, err = auditor.RepairTranslationAuditTurn(t.Context(), source.NamespaceID, 1)
		if err == nil {
			continue
		}
		require.True(t, errors.Is(err, datastore.ErrRepairRequired) || errors.Is(err, datastore.ErrNotFound))
		after, readErr := auditor.GetTranslationAuditProgress(t.Context(), source.NamespaceID)
		require.NoError(t, readErr)
		require.Equal(t, before, after, "a failed authoritative repair cannot advance server-owned progress")
		failed = true
		break
	}
	require.True(t, failed)
}

func TestTranslationAuditPreservesUnownedPreparingAllocation(t *testing.T) {
	first, _ := newTestStores(t)
	source := newTranslationCreationSource(t, first)
	require.NoError(t, first.(datastore.TranslationStore).CreateTranslation(t.Context(), source))
	binding, err := first.(datastore.TranslationBindingStore).GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	target := binding.Epochs[0].Target
	target.Name = "unowned-allocation"
	shard, err := datastore.TranslationIndexShard(source.UID)
	require.NoError(t, err)
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationTargetNameIndex,
		NamespaceUID: source.NamespaceID, Target: target, Shard: shard}
	entry := datastore.TranslationIndexEntry{TranslationUID: source.UID, SourceVersion: source.ResourceVersion,
		ReferenceEpoch: binding.CurrentEpoch, CreationTimestamp: source.CreationTimestamp}
	indexes := first.(datastore.TranslationIndexStore)
	reserved, err := indexes.ReserveTranslationIndex(t.Context(), index, entry, newID(), 1024)
	require.NoError(t, err)
	raw := newRawSession(t)
	require.NoError(t, raw.Query("UPDATE translation_index_reservations_by_token SET phase='Preparing' WHERE reservation_token=?",
		mustTranslationUUID(t, reserved.Token)).Exec())
	before, err := indexes.GetTranslationIndexReservation(t.Context(), reserved.Token)
	require.NoError(t, err)
	require.ErrorIs(t, first.(datastore.TranslationIndexAuditStore).RecoverTranslationIndexAudit(t.Context(), source.NamespaceID, reserved.Token),
		datastore.ErrRepairRequired, "do not invent allocation/cancellation authority for an unowned Preparing record")
	after, err := indexes.GetTranslationIndexReservation(t.Context(), reserved.Token)
	require.NoError(t, err)
	require.Equal(t, before, after)
	unchanged, err := first.(datastore.TranslationStore).GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source, unchanged)
}
