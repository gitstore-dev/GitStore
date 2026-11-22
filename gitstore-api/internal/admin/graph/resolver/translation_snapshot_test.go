// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranslationStoredObservationDisclosesOnlyAuthorizedTargetEvidence(t *testing.T) {
	evidence := catalog.TranslationTargetEvidence{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme", Name: "laptop",
		UID: uuid.NewString(), ResourceVersion: "7", Generation: 1, Revision: "accepted-target", ContentDigest: "material",
	}
	stored, err := json.Marshal(catalog.TranslationStatus{Resolved: &catalog.ResolvedTranslation{
		Target: &evidence, DescriptorDigest: "descriptor", SourceDigest: "source", ValidatedFieldPaths: []string{"spec.title"},
	}})
	require.NoError(t, err)
	observed, err := translationStoredObservation(stored, evidence)
	require.NoError(t, err)
	require.Contains(t, observed.Resolved, "target")
	evidence.UID = uuid.NewString()
	observed, err = translationStoredObservation(stored, evidence)
	require.NoError(t, err)
	require.NotContains(t, observed.Resolved, "target", "retargeting cannot expose proof for a previously authorized target")
	require.Equal(t, "descriptor", observed.Resolved["descriptorDigest"])
	require.Equal(t, "source", observed.Resolved["sourceDigest"])
	require.Contains(t, string(stored), "accepted-target", "read-time redaction must not modify storage")
}

func TestTranslationValidationSnapshotRequiresCertifiedAuthorizedAbsence(t *testing.T) {
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			root, _, source := newTranslationReadFixture(t)
			version := "catalog.gitstore.dev/v1beta1"
			if kind == "File" {
				version = "storage.gitstore.dev/v1beta1"
			}
			source.Spec = json.RawMessage(fmt.Sprintf(`{"locale":"fr","targetRef":{"apiVersion":%q,"kind":%q,"name":"laptop"},"patches":[]}`, version, kind))
			source.SourceDigest, source.ResourceVersion, source.Generation = "accepted-source", "4", 3
			require.NoError(t, root.store.(datastore.TranslationCapable).TranslationStore().UpdateTranslation(t.Context(), source, "3"))
			controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
				ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
			root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
			require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
				UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
			}))
			ctx := auth.ContextWithPrincipal(t.Context(), controller)
			id := mustEncodeNodeID(nodeKindTranslation, source.UID)
			snapshot, err := root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
			require.Error(t, err, "a missing head without coverage is unavailable")
			require.Nil(t, snapshot)
			audit := root.store.(datastore.TranslationTargetAuditStore)
			for turn := 0; ; turn++ {
				require.Less(t, turn, 64)
				progress, err := audit.RepairTranslationTargetAuditTurn(ctx, source.NamespaceID, 1)
				require.NoError(t, err)
				if progress.Phase == "Verified" {
					break
				}
			}
			snapshot, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
			require.NoError(t, err)
			require.NotNil(t, snapshot)
			require.Nil(t, snapshot.Target, "certified absence is an authorized negative snapshot")
			require.Nil(t, snapshot.Binding.BoundUID)
			require.Equal(t, source.SourceDigest, snapshot.SourceDigest)
			require.Equal(t, source.NamespaceID, snapshot.NamespaceUID)
			require.NotContains(t, snapshot.Observation.Resolved, "target")
			data, rejected := managedContractRequest(t, root, controller, fmt.Sprintf(`query {
				translationValidationSnapshot(namespace:%q,name:%q,sourceID:%q,sourceVersion:%q) {
					sourceID target { evidence { uid } }
				}
			}`, source.Namespace, source.Name, id, source.ResourceVersion), nil)
			require.Empty(t, rejected)
			require.Contains(t, data["translationValidationSnapshot"], "target")
			require.Nil(t, data["translationValidationSnapshot"]["target"])
			if kind == "Product" {
				provider := &translationControllerAuthorizer{AuthZProvider: root.registry.AuthZ(), denyTarget: true}
				root.registry = auth.NewProviderRegistry(nil, provider, nil)
				snapshot, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
				require.Error(t, err, "coverage must not bypass target-read permission")
				require.Nil(t, snapshot)
				provider.denyTarget = false
				provider.onTarget = func() {
					changed := *source
					changed.ResourceVersion, changed.SourceDigest = "5", "later-source"
					changed.Body += "later source body"
					changed.Generation++
					require.NoError(t, root.store.(datastore.TranslationCapable).TranslationStore().UpdateTranslation(ctx, &changed, source.ResourceVersion))
				}
				snapshot, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
				require.Error(t, err, "negative snapshots require the same final source-version fence")
				require.Nil(t, snapshot)
			}
		})
	}
}

func TestTranslationPublicGuardsPreserveOnlyCertifiedAuthorizedAbsence(t *testing.T) {
	root, ctx, source := newTranslationReadFixture(t)
	store := root.store.(datastore.TranslationCapable).TranslationStore()
	status, err := json.Marshal(catalog.TranslationStatus{
		ObservedGeneration: source.Generation, LastAppliedRevision: source.Revision,
		Conditions: []catalog.Condition{{Type: catalog.ConditionReady, Status: catalog.ConditionFalse,
			ObservedGeneration: source.Generation, Reason: "TargetNotFound"}},
	})
	require.NoError(t, err)
	source, err = store.UpdateTranslationStatus(ctx, source.UID, source.ResourceVersion, status)
	require.NoError(t, err)
	for _, certified := range []bool{false, true} {
		if certified {
			audit := root.store.(datastore.TranslationTargetAuditStore)
			for turn := 0; ; turn++ {
				require.Less(t, turn, 64)
				progress, err := audit.RepairTranslationTargetAuditTurn(ctx, source.NamespaceID, 1)
				require.NoError(t, err)
				if progress.Phase == "Verified" {
					break
				}
			}
		}
		expected, reason := catalog.ConditionUnknown, "Unavailable"
		if certified {
			expected, reason = catalog.ConditionFalse, "TargetNotFound"
		}
		_, observed, err := root.guardTranslationStatus(ctx, source)
		require.NoError(t, err)
		requireGuardCondition(t, observed, expected, reason)
		page, err := root.guardTranslationStatusPage(ctx, []*datastore.Translation{source})
		require.NoError(t, err)
		require.Len(t, page, 1)
		requireGuardCondition(t, page[0].status, expected, reason)
	}
	provider := &translationControllerAuthorizer{AuthZProvider: root.registry.AuthZ(), denyTarget: true}
	root.registry = auth.NewProviderRegistry(nil, provider, nil)
	_, observed, err := root.guardTranslationStatus(ctx, source)
	require.NoError(t, err)
	requireGuardCondition(t, observed, catalog.ConditionUnknown, "Forbidden")
}

func TestTranslationValidationSnapshotPreservesRawFieldsAndFences(t *testing.T) {
	root, human, source := newTranslationReadFixture(t)
	source.SourceDigest, source.ResourceVersion = "accepted-source-digest", "4"
	require.NoError(t, root.store.(datastore.TranslationCapable).TranslationStore().UpdateTranslation(t.Context(), source, "3"))
	id := mustEncodeNodeID(nodeKindTranslation, source.UID)
	_, err := root.readTranslationValidationSnapshot(human, source.Namespace, source.Name, id, source.ResourceVersion)
	require.Error(t, err)
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
		ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
	root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
	require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
	}))
	ctx := auth.ContextWithPrincipal(t.Context(), controller)
	_, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
	require.Error(t, err, "missing proof is not an authoritative missing target")
	target := &datastore.Product{
		UID: uuid.NewString(), Namespace: source.Namespace, Name: "laptop", APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		CreationActor: "target-owner", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Spec:              json.RawMessage(`{"title":null,"options":[{"name":"size","title":"Size"}]}`), Body: "exact\r\n",
	}

	t.Run("all target kinds", func(t *testing.T) {
		for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
			t.Run(kind, func(t *testing.T) {
				root, _, source := newTranslationReadFixture(t)
				version := "catalog.gitstore.dev/v1beta1"
				if kind == "File" {
					version = "storage.gitstore.dev/v1beta1"
				}
				source.Spec = json.RawMessage(fmt.Sprintf(`{"locale":"fr","targetRef":{"apiVersion":%q,"kind":%q,"name":"laptop"},"patches":[]}`, version, kind))
				source.SourceDigest, source.ResourceVersion, source.Generation = "accepted-source-digest", "4", 3
				require.NoError(t, root.store.(datastore.TranslationCapable).TranslationStore().UpdateTranslation(t.Context(), source, "3"))
				controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
					ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
				root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
				require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
					UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
				}))
				ctx := auth.ContextWithPrincipal(t.Context(), controller)
				uid, created := uuid.NewString(), time.Now().UTC().Truncate(time.Millisecond)
				spec := json.RawMessage(`{"title":null}`)
				if kind == "ProductVariant" {
					spec = json.RawMessage(`{"title":null,"productRef":{"name":"laptop"}}`)
				}
				var err error
				switch kind {
				case "Product":
					err = root.store.CreateProduct(ctx, &datastore.Product{UID: uid, Namespace: source.Namespace,
						Name: "laptop", APIVersion: version, Kind: kind, CreationTimestamp: created,
						ResourceVersion: "1", Generation: 1, Spec: spec, Body: "exact\r\n"})
				case "ProductVariant":
					err = root.store.CreateProductVariant(ctx, &datastore.ProductVariant{UID: uid, Namespace: source.Namespace, ProductRefName: "laptop", SKU: "laptop",
						Name: "laptop", APIVersion: version, Kind: kind, CreationTimestamp: created,
						ResourceVersion: "1", Generation: 1, Spec: spec, Body: "exact\r\n"})
				case "CategoryTaxonomy":
					err = root.store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{UID: uid, Namespace: source.Namespace,
						Name: "laptop", APIVersion: version, Kind: kind, CreationTimestamp: created,
						ResourceVersion: "1", Generation: 1, Spec: spec, Body: "exact\r\n"})
				case "Collection":
					err = root.store.CreateCollection(ctx, &datastore.Collection{UID: uid, Namespace: source.Namespace,
						Name: "laptop", APIVersion: version, Kind: kind, CreationTimestamp: created,
						ResourceVersion: "1", Generation: 1, Spec: spec, Body: "exact\r\n"})
				case "File":
					err = root.store.CreateFile(ctx, &datastore.File{UID: uid, Namespace: source.Namespace,
						Name: "laptop", APIVersion: version, Kind: kind, CreationTimestamp: created,
						ResourceVersion: "1", Generation: 1, Spec: spec, Body: "exact\r\n"})
				}
				require.NoError(t, err)
				snapshot, err := root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name,
					mustEncodeNodeID(nodeKindTranslation, source.UID), source.ResourceVersion)
				require.NoError(t, err)
				require.Equal(t, uid, snapshot.Target.Evidence.UID)
				require.Equal(t, kind, snapshot.Target.Evidence.Kind)
				require.Contains(t, snapshot.Target.Fields, "title")
				require.Nil(t, snapshot.Target.Fields["title"])
				current, err := root.translationValidationMaterial(ctx, kind, uid)
				require.NoError(t, err)
				require.Equal(t, spec, current.spec)
				require.Equal(t, "exact\r\n", current.body)
				require.Equal(t, "1", current.evidence.ResourceVersion)
			})
		}
	})
	require.NoError(t, root.store.CreateProduct(ctx, target))
	snapshot, err := root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
	require.NoError(t, err)
	require.Equal(t, id, snapshot.SourceID)
	require.Equal(t, source.SourceDigest, snapshot.SourceDigest)
	require.Equal(t, source.NamespaceID, snapshot.NamespaceUID)
	require.Equal(t, source.ResourceVersion, snapshot.Binding.SourceVersion)
	require.EqualValues(t, 1, snapshot.Observation.ObservedGeneration, "stored observations are not a currentness assertion")
	require.Equal(t, "TRUE", string(snapshot.Observation.Conditions[0].Status), "reuse the common YAML-to-GraphQL enum boundary")
	require.Equal(t, "main@sha1:older-observation", snapshot.Observation.LastAppliedRevision)
	public, err := translationSourceToGraphQL(source)
	require.NoError(t, err)
	require.Nil(t, public.Status, "ordinary reads must not expose the private observation")
	require.Contains(t, snapshot.Target.Fields, "title")
	require.Nil(t, snapshot.Target.Fields["title"], "null is not an empty text leaf")
	require.NotContains(t, snapshot.Target.Fields, "media", "missing fields must not be invented")
	require.Equal(t, target.UID, snapshot.Target.Evidence.UID)
	require.NotEmpty(t, snapshot.Target.Evidence.ContentDigest)
	data, rejected := managedContractRequest(t, root, controller, fmt.Sprintf(`query {
		translationValidationSnapshot(namespace:%q,name:%q,sourceID:%q,sourceVersion:%q) {
			sourceID sourceVersion sourceDigest namespaceUID
			binding { translationID sourceVersion bindingVersion referenceEpoch boundUID }
			observation { observedGeneration lastAppliedRevision conditions { type status } resolved }
			target { namespace evidence { uid contentDigest } fields }
		}
	}`, source.Namespace, source.Name, id, source.ResourceVersion), nil)
	require.Empty(t, rejected)
	require.Equal(t, id, data["translationValidationSnapshot"]["sourceID"])
	observed := data["translationValidationSnapshot"]["observation"].(map[string]any)
	require.Equal(t, float64(1), observed["observedGeneration"])
	require.Equal(t, "TRUE", observed["conditions"].([]any)[0].(map[string]any)["status"])
	require.Equal(t, "private-evidence", observed["resolved"].(map[string]any)["descriptorDigest"])
	provider := &translationControllerAuthorizer{AuthZProvider: root.registry.AuthZ()}
	root.registry = auth.NewProviderRegistry(nil, provider, nil)
	provider.denyTarget = true
	_, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
	require.Error(t, err)
	provider.denyTarget = false
	provider.onTarget = func() {
		current, err := root.store.GetProduct(ctx, target.UID)
		require.NoError(t, err)
		current.Spec, current.ResourceVersion, current.Generation = json.RawMessage(`{"title":"Changed"}`), "2", 2
		require.NoError(t, root.store.UpdateProduct(ctx, current, "1"))
	}
	_, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
	require.Error(t, err, "content must match the target proof used for authorization")
	provider.onTarget = func() {
		current, err := root.store.GetProduct(ctx, target.UID)
		require.NoError(t, err)
		current.ResourceVersion = "3"
		require.NoError(t, root.store.UpdateProduct(ctx, current, "2"))
	}
	snapshot, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
	require.NoError(t, err, "status-only version churn must not invalidate an unpinned material snapshot")
	require.Equal(t, "3", snapshot.Target.Evidence.ResourceVersion)
	_, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, mustEncodeNodeID(nodeKindTranslation, uuid.NewString()), source.ResourceVersion)
	require.Error(t, err)
	_, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, "old")
	require.Error(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = root.readTranslationValidationSnapshot(canceled, source.Namespace, source.Name, id, source.ResourceVersion)
	require.Error(t, err)
	provider.onTarget = func() {
		store := root.store.(datastore.TranslationCapable).TranslationStore()
		require.NoError(t, store.DeleteTranslation(ctx, source.UID, source.ResourceVersion))
		replacement := *source
		replacement.UID = uuid.NewString()
		replacement.CreationTimestamp = source.CreationTimestamp.Add(time.Millisecond)
		replacement.Generation, replacement.ResourceVersion, replacement.Status = 1, "1", nil
		replacement.Body = "before desired body"
		require.NoError(t, store.CreateTranslation(ctx, &replacement))
		second, err := store.UpdateTranslationStatus(ctx, replacement.UID, "1",
			json.RawMessage(fmt.Sprintf(`{"observedGeneration":1,"lastAppliedRevision":%q,"conditions":[]}`, replacement.Revision)))
		require.NoError(t, err)
		second.Generation, second.ResourceVersion, second.Body = source.Generation, "3", source.Body
		require.NoError(t, store.UpdateTranslation(ctx, second, "2"))
		_, err = store.UpdateTranslationStatus(ctx, replacement.UID, "3",
			json.RawMessage(fmt.Sprintf(`{"observedGeneration":%d,"lastAppliedRevision":%q,"conditions":[]}`, source.Generation, source.Revision)))
		require.NoError(t, err)
	}
	_, err = root.readTranslationValidationSnapshot(ctx, source.Namespace, source.Name, id, source.ResourceVersion)
	require.Error(t, err, "source recreation with identical version and digest cannot authorize an old snapshot")
}
