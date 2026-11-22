// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

type targetAuditEpochHook struct {
	datastore.TranslationTargetSourceAuditStore
	reads      int
	beforeRead func()
}

type targetCoveragePageHook struct {
	datastore.TranslationTargetAuditStore
	afterList func()
}

func (s *targetCoveragePageHook) ListTranslationTargetSourceAudit(ctx context.Context, namespace, cursor string, limit int) (datastore.TranslationTargetSourceAuditPage, error) {
	page, err := s.TranslationTargetAuditStore.ListTranslationTargetSourceAudit(ctx, namespace, cursor, limit)
	if err == nil {
		s.afterList()
	}
	return page, err
}

func (s *targetAuditEpochHook) ReadTranslationTargetMutationEpoch(ctx context.Context, namespace string) (string, error) {
	s.reads++
	if s.reads == 2 {
		s.beforeRead()
	}
	return s.TranslationTargetSourceAuditStore.ReadTranslationTargetMutationEpoch(ctx, namespace)
}

func runTranslationTargetSourceAuditContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	auditor, ok := backend.(datastore.TranslationTargetSourceAuditStore)
	require.True(t, ok, "target audit must survive backend decoration")
	namespace := newNamespace(datastore.NamespaceTierOrganization)
	require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
	clock, ok := backend.(datastore.TranslationTargetMutationEpochStore)
	require.True(t, ok, "target mutation observations must survive backend decoration")
	epoch, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
	require.NoError(t, err)
	require.NotEmpty(t, epoch)
	expected := map[string]string{}
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		uid := newID()
		expected[uid] = kind
		require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name, uid, kind,
			time.Now().UTC().Truncate(time.Millisecond)))
		next, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
		require.NoError(t, err)
		require.NotEqual(t, epoch, next, "each kind's source publication must invalidate prior audit observations")
		epoch = next
	}
	cursor := ""
	seen := map[string]datastore.TranslationTargetSourceAuditHint{}
	for turn := 0; turn < 2048; turn++ {
		page, err := datastore.RecoverTranslationTargetSourceAuditPage(t.Context(), auditor, namespace.UID, cursor, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Scanned, 1)
		require.LessOrEqual(t, len(page.Items), page.Scanned)
		for _, hint := range page.Items {
			require.Equal(t, namespace.UID, hint.Target.NamespaceUID)
			require.Equal(t, expected[hint.UID], hint.Target.Kind)
			require.Equal(t, namespace.Name, hint.Namespace)
			require.NotContains(t, seen, hint.UID)
			fences := translationTargetWriteFences(t, backend)
			before, err := fences.GetTranslationTargetFence(t.Context(), hint.Target)
			require.NoError(t, err)
			require.NoError(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint))
			after, err := fences.GetTranslationTargetFence(t.Context(), hint.Target)
			require.NoError(t, err)
			require.Equal(t, before, after, "healthy audit must not rewrite source or fence material")
			seen[hint.UID] = hint
		}
		if page.Next == "" {
			break
		}
		require.NotEqual(t, cursor, page.Next)
		_, err = auditor.ListTranslationTargetSourceAudit(t.Context(), newID(), page.Next, 1)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
		cursor = page.Next
	}
	require.Len(t, seen, len(expected))
	afterAudit, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
	require.NoError(t, err)
	require.Equal(t, epoch, afterAudit, "healthy source verification is not a material mutation")
	other := newNamespace(datastore.NamespaceTierOrganization)
	require.NoError(t, backend.CreateNamespace(t.Context(), other))
	require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, "Product", other.Name, newID(), "other",
		time.Now().UTC().Truncate(time.Millisecond)))
	afterOther, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
	require.NoError(t, err)
	require.Equal(t, epoch, afterOther, "foreign namespaces must not invalidate this namespace's audit")
	for uid, hint := range seen {
		advanceTranslationTargetStatus(t, backend, expected[uid], namespace.Name, hint.Target.Name, uid)
		require.ErrorIs(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint), datastore.ErrConflict)
		hint.ResourceVersion = "2"
		require.NoError(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint))
		beforeDelete, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
		require.NoError(t, err)
		require.NoError(t, deleteTranslationMaterialTarget(t.Context(), backend, expected[uid], uid))
		afterDelete, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
		require.NoError(t, err)
		require.NotEqual(t, beforeDelete, afterDelete, "retirement must invalidate prior source observations")
	}
	interleaved := &targetAuditEpochHook{TranslationTargetSourceAuditStore: auditor, beforeRead: func() {
		require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, "Product", namespace.Name, newID(), "late",
			time.Now().UTC().Truncate(time.Millisecond)))
	}}
	raced, err := datastore.RecoverTranslationTargetSourceAuditPage(t.Context(), interleaved, namespace.UID, "", 1)
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.Empty(t, raced, "a source born outside the observed page must invalidate its continuation")
	beforeLegacy, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
	require.NoError(t, err)
	legacy := newProduct()
	legacy.Namespace, legacy.Kind = namespace.Name, ""
	require.NoError(t, backend.CreateProduct(t.Context(), legacy))
	afterLegacy, err := clock.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
	require.NoError(t, err)
	require.NotEqual(t, beforeLegacy, afterLegacy, "an unproved birth still invalidates namespace audit observations")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	page, err := auditor.ListTranslationTargetSourceAudit(canceled, namespace.UID, "", 1)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, page)
	_, err = auditor.ListTranslationTargetSourceAudit(t.Context(), namespace.UID, "", 0)
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	epoch, err = clock.ReadTranslationTargetMutationEpoch(canceled, namespace.UID)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, epoch)
	_, err = clock.ReadTranslationTargetMutationEpoch(t.Context(), "not-a-uid")
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	_, err = clock.ReadTranslationTargetMutationEpoch(t.Context(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func runTranslationTargetEvidenceProtocolContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	metadata, ok := backend.(datastore.TranslationTargetMetadataStore)
	require.True(t, ok, "thin target metadata must survive backend decoration")
	targets, ok := backend.(datastore.TranslationTargetFenceStore)
	require.True(t, ok, "authoritative target reads must survive backend decoration")
	fences := translationTargetWriteFences(t, backend)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			namespace := newNamespace(datastore.NamespaceTierOrganization)
			require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: kind, Name: "target",
			}
			if kind == "File" {
				key.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			read := func() (*catalog.TranslationTargetEvidence, error) {
				return targets.ReadTargetEvidence(t.Context(), key)
			}
			missing, err := read()
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Nil(t, missing, "a missing head is not authoritative absence")
			first, replacement := newID(), newID()
			createdAt := time.Now().UTC().Truncate(time.Millisecond)
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name, first, key.Name, createdAt))
			evidence, err := read()
			require.NoError(t, err)
			require.Equal(t, first, evidence.UID)
			require.Equal(t, "1", evidence.ResourceVersion)
			require.Equal(t, namespace.Name, evidence.Namespace)
			require.Equal(t, kind, evidence.Kind)
			require.NotEmpty(t, evidence.ContentDigest)
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			unavailable, err := targets.ReadTargetEvidence(canceled, key)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, unavailable)
			otherNamespace := key
			otherNamespace.NamespaceUID = newID()
			unavailable, err = targets.ReadTargetEvidence(t.Context(), otherNamespace)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Nil(t, unavailable, "name evidence cannot cross namespace incarnations")
			head, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			point, err := metadata.ReadTranslationTargetMetadata(t.Context(), head)
			require.NoError(t, err)
			require.True(t, point.Active)
			require.Equal(t, evidence, &point.Evidence)
			require.Equal(t, namespace.UID, point.SourceIdentity.NamespaceUID)
			require.Equal(t, head.Token, point.SourceIdentity.WriteToken)
			require.True(t, createdAt.Equal(point.CreationTimestamp))
			require.Equal(t, "target-owner", point.OwnerSub, "target authorization uses the same thin source observation")
			advanceTranslationTargetStatus(t, backend, kind, namespace.Name, key.Name, first)
			evidence, err = read()
			require.NoError(t, err)
			expected := *head.Evidence
			expected.ResourceVersion = "2"
			require.Equal(t, &expected, evidence, "pins use the actual version without invalidating unchanged material")
			afterStatus, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			if kind == "Product" || kind == "CategoryTaxonomy" || kind == "File" {
				require.Equal(t, head, afterStatus, "dedicated status patches retain the material fence")
			}
			require.NoError(t, deleteTranslationMaterialTarget(t.Context(), backend, kind, first))
			_, err = metadata.ReadTranslationTargetMetadata(t.Context(), head)
			require.ErrorIs(t, err, datastore.ErrNotFound, "an old head cannot hydrate a deleted source")
			evidence, err = read()
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Nil(t, evidence)
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name, replacement, key.Name, createdAt.Add(time.Second)))
			evidence, err = read()
			require.NoError(t, err)
			require.Equal(t, replacement, evidence.UID, "a replacement must not return old-incarnation evidence")
			head, err = fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			_, err = fences.BeginTranslationTargetWrite(t.Context(), key, datastore.TranslationTargetWriteIntent{
				ID: newID(), PreviousUID: replacement, NextUID: replacement,
				ExpectedResourceVersion: "1", SourceDigest: "pending", CreationTimestamp: head.CreationTimestamp,
			}, head.Token)
			require.NoError(t, err)
			evidence, err = read()
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Nil(t, evidence, "pending material writes cannot advertise current target evidence")
		})
	}
}

func runTranslationTargetSourceBackfillContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	auditor, ok := backend.(datastore.TranslationTargetSourceAuditStore)
	require.True(t, ok)
	t.Run("first-fenced-birth-preserves-empty-coverage", func(t *testing.T) {
		namespace := newNamespace(datastore.NamespaceTierOrganization)
		require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
		store := backend.(datastore.TranslationTargetAuditStore)
		var verified datastore.TranslationAuditProgress
		for range 2048 {
			progress, err := store.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
			require.NoError(t, err)
			if progress.Phase == "Verified" {
				verified = progress
				break
			}
		}
		require.Equal(t, "Verified", verified.Phase)
		for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
			version := "catalog.gitstore.dev/v1beta1"
			if kind == "File" {
				version = "storage.gitstore.dev/v1beta1"
			}
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: version, Kind: kind, Name: "first-" + kind,
			}
			fences := backend.(datastore.TranslationTargetWriteFenceStore)
			require.NoError(t, datastore.ReadTranslationTargetAbsence(t.Context(), key, store, fences.GetTranslationTargetFence))
			uid := newID()
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name,
				uid, key.Name, time.Now().UTC().Truncate(time.Millisecond)))
			epoch, err := datastore.ReadTranslationTargetAuditCoverage(t.Context(), store, namespace.UID)
			require.NoError(t, err)
			require.Equal(t, verified.TargetMutationEpoch, epoch)
			require.ErrorIs(t, datastore.ReadTranslationTargetAbsence(t.Context(), key, store, fences.GetTranslationTargetFence),
				datastore.ErrConflict, "coverage-preserving births must not become false absence")
			require.NoError(t, deleteTranslationMaterialTarget(t.Context(), backend, kind, uid))
			require.NoError(t, datastore.ReadTranslationTargetAbsence(t.Context(), key, store, fences.GetTranslationTargetFence))
		}
		reader, ok := backend.(interface {
			ReadTranslationTargetNamespaceMetadata(context.Context, string) (*datastore.TranslationTargetNamespaceMetadata, error)
		})
		require.True(t, ok, "target authorization requires serialized Namespace lifetime and ownership facts")
		previous, err := reader.ReadTranslationTargetNamespaceMetadata(t.Context(), namespace.Name)
		require.NoError(t, err)
		require.NoError(t, backend.DeleteNamespace(t.Context(), namespace.UID))
		replacement := *namespace
		replacement.UID, replacement.CreationActor = newID(), "replacement-owner"
		replacement.ID = replacement.UID
		require.NoError(t, backend.CreateNamespace(t.Context(), &replacement))
		current, err := reader.ReadTranslationTargetNamespaceMetadata(t.Context(), namespace.Name)
		require.NoError(t, err)
		require.Equal(t, namespace.UID, previous.UID)
		require.Equal(t, replacement.UID, current.UID)
		require.NotEqual(t, previous.OwnerSub, current.OwnerSub)
		require.True(t, current.Active)
		_, err = datastore.ReadTranslationTargetAuditCoverage(t.Context(), store, namespace.UID)
		require.ErrorIs(t, err, datastore.ErrNotFound, "an old lifetime's coverage cannot adopt its replacement")
	})
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			namespace := newNamespace(datastore.NamespaceTierOrganization)
			require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
			uid, created := newID(), time.Now().UTC().Truncate(time.Millisecond)
			require.NoError(t, createTranslationMaterialTargetWithVersion(t.Context(), backend, kind, namespace.Name, uid, "backfill", created, 0, ""))
			read := func() []byte {
				resource, err := translationMaterialTargetResource(t.Context(), backend, kind, uid)
				require.NoError(t, err)
				encoded, err := json.Marshal(resource)
				require.NoError(t, err)
				return encoded
			}
			resource, err := translationMaterialTargetResource(t.Context(), backend, kind, uid)
			require.NoError(t, err)
			update := func(generation int64, version, expected string) {
				t.Helper()
				switch current := resource.(type) {
				case *datastore.Product:
					current.Generation, current.ResourceVersion = generation, version
					current.Body = "unproved\r\nmaterial-" + version
					err = backend.UpdateProduct(t.Context(), current, expected)
				case *datastore.ProductVariant:
					current.Generation, current.ResourceVersion = generation, version
					current.Body = "unproved\r\nmaterial-" + version
					err = backend.UpdateProductVariant(t.Context(), current, expected)
				case *datastore.CategoryTaxonomy:
					current.Generation, current.ResourceVersion = generation, version
					current.Body = "unproved\r\nmaterial-" + version
					err = backend.UpdateCategoryTaxonomy(t.Context(), current, expected)
				case *datastore.Collection:
					current.Generation, current.ResourceVersion = generation, version
					current.Body = "unproved\r\nmaterial-" + version
					err = backend.UpdateCollection(t.Context(), current, expected)
				case *datastore.File:
					current.Generation, current.ResourceVersion = generation, version
					current.Body = "unproved\r\nmaterial-" + version
					err = backend.UpdateFile(t.Context(), current, expected)
				}
				require.NoError(t, err)
			}
			update(1, "1", "")
			version := "catalog.gitstore.dev/v1beta1"
			if kind == "File" {
				version = "storage.gitstore.dev/v1beta1"
			}
			hint := datastore.TranslationTargetSourceAuditHint{
				Target: datastore.TranslationTargetKey{
					NamespaceUID: namespace.UID, APIVersion: version, Kind: kind, Name: "backfill",
				},
				Namespace: namespace.Name, UID: uid, CreationTimestamp: created, ResourceVersion: "1",
			}
			_, err = backend.(datastore.TranslationTargetFenceStore).ReadTargetEvidence(t.Context(), hint.Target)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			before := read()
			require.NoError(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint))
			evidence, err := backend.(datastore.TranslationTargetFenceStore).ReadTargetEvidence(t.Context(), hint.Target)
			require.NoError(t, err)
			require.Equal(t, uid, evidence.UID)
			require.EqualValues(t, 1, evidence.Generation)
			require.Equal(t, "1", evidence.ResourceVersion)
			require.Equal(t, before, read(), "backfill changes proof, never the public source envelope")
			epoch, err := auditor.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
			require.NoError(t, err)
			require.NoError(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint))
			after, err := auditor.ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
			require.NoError(t, err)
			require.Equal(t, epoch, after, "a completed backfill must be idempotent")
			update(0, "2", "1")
			update(1, "3", "2")
			_, err = backend.(datastore.TranslationTargetFenceStore).ReadTargetEvidence(t.Context(), hint.Target)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			before = read()
			hint.ResourceVersion = "3"
			require.NoError(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint))
			evidence, err = backend.(datastore.TranslationTargetFenceStore).ReadTargetEvidence(t.Context(), hint.Target)
			require.NoError(t, err)
			require.Equal(t, uid, evidence.UID)
			require.Equal(t, "3", evidence.ResourceVersion)
			require.Equal(t, before, read(), "restoring invalidated proof must preserve the public envelope")
			coverage, ok := backend.(interface {
				ReadTranslationTargetCoverageEpoch(context.Context, string) (string, error)
			})
			require.True(t, ok, "fenced writers need a separate coverage-invalidating watermark")
			covered, err := coverage.ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
			require.NoError(t, err)
			update(2, "4", "3")
			fenced, err := coverage.ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
			require.NoError(t, err)
			require.Equal(t, covered, fenced, "a fully fenced material change preserves source coverage")
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name,
				newID(), "fenced-birth", time.Now().UTC().Truncate(time.Millisecond)))
			fenced, err = coverage.ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
			require.NoError(t, err)
			require.Equal(t, covered, fenced, "a canonical fenced birth preserves source coverage")
			currentVersion := 4
			changing := &targetCoveragePageHook{
				TranslationTargetAuditStore: backend.(datastore.TranslationTargetAuditStore),
				afterList: func() {
					next := currentVersion + 1
					update(int64(next), strconv.Itoa(next), strconv.Itoa(currentVersion))
					currentVersion = next
				},
			}
			var progress datastore.TranslationAuditProgress
			for range 2048 {
				progress, err = datastore.PlanTranslationTargetAuditTurn(t.Context(), changing, namespace.UID, progress, 256)
				require.NoError(t, err, "proved material writes inside every page must not prevent coverage")
				if progress.Phase == "Verified" {
					break
				}
			}
			require.Equal(t, "Verified", progress.Phase)
			update(0, strconv.Itoa(currentVersion+1), strconv.Itoa(currentVersion))
			unproved, err := coverage.ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
			require.NoError(t, err)
			require.NotEqual(t, covered, unproved, "invalidating material must invalidate namespace coverage")
		})
	}
	t.Run("missing-birth-authority", func(t *testing.T) {
		namespace := newNamespace(datastore.NamespaceTierOrganization)
		file := &datastore.File{
			UID: newID(), Namespace: namespace.Name, Name: "unowned",
			APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Generation:        1, ResourceVersion: "1", Spec: json.RawMessage(`{}`), Body: "unowned\r\nbody",
		}
		require.NoError(t, backend.CreateFile(t.Context(), file))
		require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
		hint := datastore.TranslationTargetSourceAuditHint{
			Target: datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: file.APIVersion, Kind: file.Kind, Name: file.Name,
			},
			Namespace: namespace.Name, UID: file.UID, CreationTimestamp: file.CreationTimestamp, ResourceVersion: "1",
		}
		before, err := backend.GetFile(t.Context(), file.UID)
		require.NoError(t, err)
		encoded, err := json.Marshal(before)
		require.NoError(t, err)
		require.ErrorIs(t, auditor.RecoverTranslationTargetSourceAudit(t.Context(), hint), datastore.ErrRepairRequired)
		after, err := backend.GetFile(t.Context(), file.UID)
		require.NoError(t, err)
		actual, err := json.Marshal(after)
		require.NoError(t, err)
		require.Equal(t, encoded, actual, "backfill cannot invent missing birth authority from today's namespace name")
		_, err = translationTargetWriteFences(t, backend).GetTranslationTargetFence(t.Context(), hint.Target)
		require.ErrorIs(t, err, datastore.ErrNotFound)
	})
	t.Run("durable-audit-progress", func(t *testing.T) {
		store, ok := backend.(datastore.TranslationTargetAuditStore)
		require.True(t, ok, "target backfill needs server-owned mutation-fenced traversal")
		namespace := newNamespace(datastore.NamespaceTierOrganization)
		require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
		for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name,
				newID(), "progress-"+kind, time.Now().UTC().Truncate(time.Millisecond)))
		}
		_, err := store.GetTranslationTargetAuditProgress(t.Context(), namespace.UID)
		require.ErrorIs(t, err, datastore.ErrNotFound)
		phases := map[string]bool{}
		var verified datastore.TranslationAuditProgress
		for range 2048 {
			progress, err := store.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
			require.NoError(t, err)
			stored, err := store.GetTranslationTargetAuditProgress(t.Context(), namespace.UID)
			require.NoError(t, err)
			require.Equal(t, progress, stored)
			phases[progress.Phase] = true
			if progress.Phase == "Verified" {
				verified = progress
				break
			}
		}
		require.Equal(t, "Verified", verified.Phase)
		require.True(t, phases["Repairing"])
		require.True(t, phases["Verifying"])
		epoch, err := datastore.ReadTranslationTargetAuditCoverage(t.Context(), store, namespace.UID)
		require.NoError(t, err)
		require.Equal(t, verified.TargetMutationEpoch, epoch)
		unchanged, err := store.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
		require.NoError(t, err)
		require.Equal(t, verified, unchanged, "idle verified coverage must not be rewritten")
		laterUID := newID()
		require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, "File", namespace.Name,
			laterUID, "after-verification", time.Now().UTC().Truncate(time.Millisecond)))
		_, err = datastore.ReadTranslationTargetAuditCoverage(t.Context(), store, namespace.UID)
		require.NoError(t, err, "canonical fenced births preserve completed coverage")
		file, err := backend.GetFile(t.Context(), laterUID)
		require.NoError(t, err)
		file.Generation, file.ResourceVersion = 0, "2"
		require.NoError(t, backend.UpdateFile(t.Context(), file, "1"))
		file.Generation, file.ResourceVersion = 1, "3"
		require.NoError(t, backend.UpdateFile(t.Context(), file, "2"))
		_, err = datastore.ReadTranslationTargetAuditCoverage(t.Context(), store, namespace.UID)
		require.ErrorIs(t, err, datastore.ErrConflict)
		restarted, err := store.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
		require.NoError(t, err)
		require.NotEqual(t, "Verified", restarted.Phase, "a later mutation invalidates the completed traversal")
		require.NotEqual(t, verified.RunID, restarted.RunID)
	})
}

func translationMaterialTargetResource(ctx context.Context, backend datastore.Datastore, kind, uid string) (any, error) {
	switch kind {
	case "Product":
		return backend.GetProduct(ctx, uid)
	case "ProductVariant":
		return backend.GetProductVariant(ctx, uid)
	case "CategoryTaxonomy":
		return backend.GetCategoryTaxonomy(ctx, uid)
	case "Collection":
		return backend.GetCollection(ctx, uid)
	default:
		return backend.GetFile(ctx, uid)
	}
}

func advanceTranslationTargetStatus(t *testing.T, backend datastore.Datastore, kind, namespace, name, uid string) {
	t.Helper()
	observed := int64(1)
	var err error
	switch kind {
	case "Product":
		_, err = backend.UpdateProductStatus(t.Context(), namespace, name, datastore.ProductStatusPatch{
			ResourceVersion: "1", ObservedGeneration: &observed,
		})
	case "CategoryTaxonomy":
		_, err = backend.UpdateCategoryTaxonomyStatus(t.Context(), namespace, name, datastore.CategoryTaxonomyStatusPatch{
			ResourceVersion: "1", ObservedGeneration: &observed,
		})
	case "File":
		_, err = backend.UpdateFileStatus(t.Context(), namespace, name, datastore.FileStatusPatch{
			ResourceVersion: "1", ObservedGeneration: &observed,
		})
	case "ProductVariant":
		current, readErr := backend.GetProductVariant(t.Context(), uid)
		require.NoError(t, readErr)
		current.ResourceVersion, current.Status = "2", json.RawMessage(`{"observedGeneration":1}`)
		err = backend.UpdateProductVariant(t.Context(), current, "1")
	case "Collection":
		current, readErr := backend.GetCollection(t.Context(), uid)
		require.NoError(t, readErr)
		current.ResourceVersion, current.Status = "2", json.RawMessage(`{"observedGeneration":1}`)
		err = backend.UpdateCollection(t.Context(), current, "1")
	}
	require.NoError(t, err)
}

func runTranslationTargetRetiredUIDContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			namespace := newNamespace(datastore.NamespaceTierOrganization)
			require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
			first, replacement := newID(), newID()
			createdAt := time.Now().UTC().Truncate(time.Millisecond)
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name, first, "original", createdAt))
			require.Error(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name, first, "live-alias", createdAt.Add(time.Second)),
				"a second create cannot move a live UID to another authoritative key")
			_, err := translationMaterialTargetUID(t.Context(), backend, kind, namespace.Name, "live-alias")
			require.ErrorIs(t, err, datastore.ErrNotFound, "rejected creation must not publish a phantom alias")
			original, err := translationMaterialTargetUID(t.Context(), backend, kind, namespace.Name, "original")
			require.NoError(t, err)
			require.Equal(t, first, original)
			var staleDelete error
			switch kind {
			case "Product":
				staleDelete = backend.DeleteProductWithResourceVersion(t.Context(), first, "0")
			case "ProductVariant":
				staleDelete = backend.DeleteProductVariantWithResourceVersion(t.Context(), first, "0")
			case "Collection":
				staleDelete = backend.DeleteCollectionWithResourceVersion(t.Context(), first, "0")
			case "File":
				staleDelete = backend.DeleteFileWithResourceVersion(t.Context(), first, "0")
			}
			if kind != "CategoryTaxonomy" {
				require.ErrorIs(t, staleDelete, datastore.ErrConflict)
				current, err := translationMaterialTargetUID(t.Context(), backend, kind, namespace.Name, "original")
				require.NoError(t, err, "a rejected delete CAS must not retire the current UID")
				require.Equal(t, first, current)
			}
			require.NoError(t, deleteTranslationMaterialTarget(t.Context(), backend, kind, first))
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name, replacement, "original", createdAt.Add(time.Second)))
			require.Error(t, createTranslationMaterialTarget(t.Context(), backend, kind, namespace.Name, first, "another-name", createdAt.Add(2*time.Second)),
				"a deleted UID cannot be reused under another authoritative key or name")
			_, err = translationMaterialTargetUID(t.Context(), backend, kind, namespace.Name, "another-name")
			require.ErrorIs(t, err, datastore.ErrNotFound)
			current, err := translationMaterialTargetUID(t.Context(), backend, kind, namespace.Name, "original")
			require.NoError(t, err)
			require.Equal(t, replacement, current)
			otherKind := "Product"
			if kind == otherKind {
				otherKind = "File"
			}
			require.NoError(t, createTranslationMaterialTarget(t.Context(), backend, otherKind, namespace.Name, first, "another-kind", createdAt),
				"retirement is scoped to the target kind, not all resources sharing UUID bytes")
		})
	}
}

func runTranslationTargetPageValidationContract(t *testing.T, backend datastore.Datastore) {
	t.Helper()
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			for _, page := range []datastore.PageParams{{First: -1}, {Last: -1}} {
				var err error
				switch kind {
				case "Product":
					_, err = backend.ListProducts(t.Context(), "invalid-page", page)
				case "ProductVariant":
					_, err = backend.ListProductVariants(t.Context(), "invalid-page", page)
				case "CategoryTaxonomy":
					_, err = backend.ListCategoryTaxonomies(t.Context(), "invalid-page", page)
				case "Collection":
					_, err = backend.ListCollections(t.Context(), "invalid-page", page)
				case "File":
					_, err = backend.ListFiles(t.Context(), "invalid-page", page)
				}
				require.ErrorIs(t, err, datastore.ErrInvalidArgument, "reject negative page sizes before lookup work")
			}
		})
	}
}

func createTranslationMaterialTarget(ctx context.Context, backend datastore.Datastore, kind, namespace, uid, name string, createdAt time.Time) error {
	return createTranslationMaterialTargetWithVersion(ctx, backend, kind, namespace, uid, name, createdAt, 1, "1")
}

func createTranslationMaterialTargetWithVersion(ctx context.Context, backend datastore.Datastore, kind, namespace, uid, name string, createdAt time.Time, generation int64, version string) error {
	base := datastore.Product{
		UID: uid, Namespace: namespace, Name: name, Kind: kind, APIVersion: "catalog.gitstore.dev/v1beta1",
		CreationTimestamp: createdAt, Generation: generation, ResourceVersion: version,
		CreationActor: "target-owner",
		Spec:          json.RawMessage(`{"title":"Target"}`), Body: "original body",
	}
	switch kind {
	case "Product":
		return backend.CreateProduct(ctx, &base)
	case "ProductVariant":
		return backend.CreateProductVariant(ctx, &datastore.ProductVariant{
			UID: uid, Namespace: namespace, Name: name, Kind: kind, APIVersion: base.APIVersion,
			CreationTimestamp: createdAt, Generation: generation, ResourceVersion: version,
			CreationActor: "target-owner",
			Spec:          base.Spec, Body: base.Body, SKU: "sku-" + uid, ProductRefName: "parent",
		})
	case "CategoryTaxonomy":
		return backend.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
			UID: uid, Namespace: namespace, Name: name, Kind: kind, APIVersion: base.APIVersion,
			CreationTimestamp: createdAt, Generation: generation, ResourceVersion: version, Spec: base.Spec, Body: base.Body,
			CreationActor: "target-owner",
		})
	case "Collection":
		collection := datastore.Collection(base)
		return backend.CreateCollection(ctx, &collection)
	default:
		file := datastore.File(base)
		file.APIVersion, file.Spec = "storage.gitstore.dev/v1beta1", json.RawMessage(`{}`)
		return backend.CreateFile(ctx, &file)
	}
}

func deleteTranslationMaterialTarget(ctx context.Context, backend datastore.Datastore, kind, uid string) error {
	switch kind {
	case "Product":
		return backend.DeleteProduct(ctx, uid)
	case "ProductVariant":
		return backend.DeleteProductVariant(ctx, uid)
	case "CategoryTaxonomy":
		return backend.DeleteCategoryTaxonomy(ctx, uid)
	case "Collection":
		return backend.DeleteCollection(ctx, uid)
	default:
		return backend.DeleteFile(ctx, uid)
	}
}

func translationMaterialTargetUID(ctx context.Context, backend datastore.Datastore, kind, namespace, name string) (string, error) {
	switch kind {
	case "Product":
		resource, err := backend.GetProductByName(ctx, namespace, name)
		if err != nil {
			return "", err
		}
		return resource.UID, nil
	case "ProductVariant":
		resource, err := backend.GetProductVariantByName(ctx, namespace, name)
		if err != nil {
			return "", err
		}
		return resource.UID, nil
	case "CategoryTaxonomy":
		resource, err := backend.GetCategoryTaxonomyByName(ctx, namespace, name)
		if err != nil {
			return "", err
		}
		return resource.UID, nil
	case "Collection":
		resource, err := backend.GetCollectionByName(ctx, namespace, name)
		if err != nil {
			return "", err
		}
		return resource.UID, nil
	default:
		resource, err := backend.GetFileByName(ctx, namespace, name)
		if err != nil {
			return "", err
		}
		return resource.UID, nil
	}
}
