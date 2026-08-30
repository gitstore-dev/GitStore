// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatastoreNamespaceToGraphQL_DeclarativeProjection(t *testing.T) {
	ns := namespaceContractFixture("6a053cdd-1f95-47f2-b3bb-d950a52a6758", "acme")
	ns.APIVersion = namespaceAPIVersion
	ns.Kind = namespaceKind
	ns.Revision = "main@sha1:abc"
	ns.Labels = map[string]string{"team": "catalog"}
	ns.Annotations = map[string]string{"owner": "platform"}
	ns.OwnerReferences = json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"root","uid":"00000000-0000-0000-0000-000000000001"}]`)
	ns.Spec = json.RawMessage(`{"title":"Acme Store","tier":"USER","repositoryDefaults":{"visibility":"PRIVATE","defaultBranch":"trunk"},"pushPolicyDefaults":{"maxPackSizeBytes":1024,"maxFileSizeBytes":256}}`)
	ns.Body = "Namespace body."

	got := datastoreNamespaceToModel(ns)
	require.NotNil(t, got)
	assert.Equal(t, namespaceAPIVersion, got.APIVersion)
	assert.Equal(t, namespaceKind, got.Kind)

	require.NotNil(t, got.Metadata)
	assert.Equal(t, "acme", got.Metadata.Name)
	assert.Equal(t, ns.ID, got.Metadata.UID)
	assert.Equal(t, namespaceInitialResourceVersion, got.Metadata.ResourceVersion)
	assert.Equal(t, namespaceInitialGeneration, got.Metadata.Generation)
	assert.Equal(t, map[string]any{"team": "catalog"}, got.Metadata.Labels)
	assert.Equal(t, map[string]any{"owner": "platform"}, got.Metadata.Annotations)
	require.NotNil(t, got.Metadata.Revision)
	assert.Equal(t, ns.Revision, *got.Metadata.Revision)
	require.Len(t, got.Metadata.OwnerReferences, 1)
	assert.Equal(t, "root", got.Metadata.OwnerReferences[0].Name)
	assert.NotNil(t, got.Metadata.OwnerReferences)
	assert.Empty(t, got.Metadata.Finalizers)
	assert.NotNil(t, got.Metadata.Finalizers)
	assert.Equal(t, ns.CreationTimestamp, got.Metadata.CreationTimestamp)

	require.NotNil(t, got.Spec)
	require.NotNil(t, got.Spec.Title)
	assert.Equal(t, ns.Title, *got.Spec.Title)
	assert.Equal(t, model.NamespaceTierUser, got.Spec.Tier)
	require.NotNil(t, got.Spec.RepositoryDefaults)
	require.NotNil(t, got.Spec.RepositoryDefaults.Visibility)
	assert.Equal(t, model.RepositoryVisibilityPrivate, *got.Spec.RepositoryDefaults.Visibility)
	require.NotNil(t, got.Spec.RepositoryDefaults.DefaultBranch)
	assert.Equal(t, "trunk", *got.Spec.RepositoryDefaults.DefaultBranch)
	require.NotNil(t, got.Spec.PushPolicyDefaults)
	require.NotNil(t, got.Spec.PushPolicyDefaults.MaxPackSizeBytes)
	assert.Equal(t, int64(1024), *got.Spec.PushPolicyDefaults.MaxPackSizeBytes)

	require.NotNil(t, got.Status)
	assert.Equal(t, int32(0), got.Status.ObservedGeneration)
	assert.Nil(t, got.Status.LastAppliedRevision)
	assert.Empty(t, got.Status.Conditions)
	assert.NotNil(t, got.Status.Conditions)
	require.NotNil(t, got.Body)
	assert.Equal(t, ns.Body, *got.Body)
}

func TestDatastoreNamespaceToGraphQL_PreservesLegacyProjection(t *testing.T) {
	ns := namespaceContractFixture("00000000-0000-0000-0000-000000000044", "legacy")

	got := datastoreNamespaceToModel(ns)
	require.NotNil(t, got)
	assert.Equal(t, mustEncodeNodeID(nodeKindNamespace, ns.ID), got.ID)
	assert.Equal(t, ns.Name, got.Identifier)
	require.NotNil(t, got.DisplayName)
	assert.Equal(t, ns.Title, *got.DisplayName)
	assert.Equal(t, model.NamespaceTierUser, got.Tier)
	assert.Equal(t, ns.CreationTimestamp, got.CreatedAt)
	assert.Equal(t, ns.CreationActor, got.CreatedBy)
	assert.Equal(t, ns.UpdateTimestamp, got.UpdatedAt)
	assert.Equal(t, ns.UpdateActor, got.UpdatedBy)
}

func TestDatastoreNamespaceToGraphQL_IdentityAndVersionDefaultsArePerResource(t *testing.T) {
	first := namespaceContractFixture("00000000-0000-0000-0000-000000000101", "first")
	second := namespaceContractFixture("00000000-0000-0000-0000-000000000202", "second")
	second.CreationTimestamp = second.CreationTimestamp.Add(24 * time.Hour)

	firstModel := datastoreNamespaceToModel(first)
	secondModel := datastoreNamespaceToModel(second)

	require.NotNil(t, firstModel.Metadata)
	require.NotNil(t, secondModel.Metadata)
	assert.NotEqual(t, firstModel.ID, secondModel.ID)
	assert.Equal(t, first.ID, firstModel.Metadata.UID)
	assert.Equal(t, second.ID, secondModel.Metadata.UID)
	assert.NotEqual(t, firstModel.Metadata.UID, secondModel.Metadata.UID)
	assert.Equal(t, "1", firstModel.Metadata.ResourceVersion)
	assert.Equal(t, "1", secondModel.Metadata.ResourceVersion)
	assert.Equal(t, int32(1), firstModel.Metadata.Generation)
	assert.Equal(t, int32(1), secondModel.Metadata.Generation)
	assert.Equal(t, first.CreationTimestamp, firstModel.Metadata.CreationTimestamp)
	assert.Equal(t, second.CreationTimestamp, secondModel.Metadata.CreationTimestamp)
}

func TestDatastoreNamespaceToGraphQL_AdmissionAcceptedAndTerminatingRemainDistinct(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	deletionRequestedAt := acceptedAt.Add(time.Hour)
	ns := namespaceContractFixture("00000000-0000-0000-0000-000000000303", "terminating")
	ns.Generation = 3
	ns.DeletionTimestamp = &deletionRequestedAt
	ns.Status = json.RawMessage(`{
		"observedGeneration": 3,
		"lastAppliedRevision": "main@sha1:accepted",
		"conditions": [{
			"type": "AdmissionAccepted",
			"status": "True",
			"observedGeneration": 3,
			"lastTransitionTime": "2026-08-29T12:00:00Z",
			"reason": "AdmittedByHookPipeline",
			"message": "Namespace manifest admitted successfully."
		}]
	}`)

	got := datastoreNamespaceToModel(ns)

	require.NotNil(t, got)
	require.NotNil(t, got.Status)
	assert.Equal(t, int32(3), got.Status.ObservedGeneration)
	require.Len(t, got.Status.Conditions, 2)

	admission := namespaceModelConditionByType(t, got.Status.Conditions, catalog.ConditionAdmissionAccepted)
	assert.Equal(t, model.ConditionStatusTrue, admission.Status)
	require.NotNil(t, admission.ObservedGeneration)
	assert.Equal(t, int32(3), *admission.ObservedGeneration)
	assert.Equal(t, acceptedAt, admission.LastTransitionTime)
	require.NotNil(t, admission.Reason)
	assert.Equal(t, "AdmittedByHookPipeline", *admission.Reason)

	terminating := namespaceModelConditionByType(t, got.Status.Conditions, catalog.ConditionTerminating)
	assert.Equal(t, model.ConditionStatusTrue, terminating.Status)
	require.NotNil(t, terminating.ObservedGeneration)
	assert.Equal(t, int32(3), *terminating.ObservedGeneration)
	assert.Equal(t, deletionRequestedAt, terminating.LastTransitionTime)
	require.NotNil(t, terminating.Reason)
	assert.Equal(t, "DeletionRequested", *terminating.Reason)
}

func namespaceModelConditionByType(t *testing.T, conditions []*model.Condition, conditionType string) *model.Condition {
	t.Helper()
	for _, condition := range conditions {
		if condition != nil && condition.Type == conditionType {
			return condition
		}
	}
	t.Fatalf("condition %q not found in %+v", conditionType, conditions)
	return nil
}

func repositoryContractFixture() (*datastore.Repository, *datastore.Namespace) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	return &datastore.Repository{
			APIVersion:        repositoryAPIVersion,
			Kind:              repositoryKind,
			UID:               "01960000-0000-7000-8000-000000000045",
			Namespace:         "acme",
			Name:              "catalog",
			RepositoryID:      "01960000-0000-7000-8000-000000000045",
			Revision:          "main@sha1:def",
			Labels:            map[string]string{"team": "catalog"},
			Annotations:       map[string]string{"owner": "platform"},
			OwnerReferences:   json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"acme","uid":"01960000-0000-7000-8000-000000000046"}]`),
			Body:              "Repository body.",
			DefaultBranch:     "main",
			StorageClass:      "default",
			Generation:        3,
			ResourceVersion:   "8",
			Status:            json.RawMessage(`{"observedGeneration":2,"lastAppliedRevision":"main@sha1:abc","conditions":[]}`),
			CreationTimestamp: now,
			CreationActor:     "creator",
			UpdateTimestamp:   now.Add(time.Hour),
			UpdateActor:       "updater",
			MaxPackSizeBytes:  52428800,
			MaxFileSizeBytes:  10485760,
		}, &datastore.Namespace{
			ID:   "01960000-0000-7000-8000-000000000046",
			Name: "acme",
		}
}

func TestDatastoreRepositoryToModel_DeclarativeProjection(t *testing.T) {
	repo, ns := repositoryContractFixture()

	got := datastoreRepositoryToModel(repo, ns, "/var/lib/gitstore")

	require.NotNil(t, got)
	assert.Equal(t, repositoryAPIVersion, got.APIVersion)
	assert.Equal(t, repositoryKind, got.Kind)
	assert.Equal(t, got.ID, got.Metadata.UID)
	assert.Equal(t, repo.Name, got.Metadata.Name)
	assert.Equal(t, ns.Name, got.Metadata.Namespace)
	assert.Equal(t, repo.ResourceVersion, got.Metadata.ResourceVersion)
	assert.Equal(t, int32(repo.Generation), got.Metadata.Generation)
	assert.Equal(t, repo.CreationTimestamp, got.Metadata.CreationTimestamp)
	assert.Equal(t, map[string]any{"team": "catalog"}, got.Metadata.Labels)
	assert.Equal(t, map[string]any{"owner": "platform"}, got.Metadata.Annotations)
	require.NotNil(t, got.Metadata.Revision)
	assert.Equal(t, repo.Revision, *got.Metadata.Revision)
	assert.NotNil(t, got.Metadata.OwnerReferences)
	require.Len(t, got.Metadata.OwnerReferences, 1)
	assert.Equal(t, "acme", got.Metadata.OwnerReferences[0].Name)

	require.NotNil(t, got.Spec)
	assert.Equal(t, repo.DefaultBranch, got.Spec.DefaultBranch)
	assert.Equal(t, model.RepositoryVisibilityPrivate, got.Spec.Visibility)
	require.NotNil(t, got.Spec.PushPolicy)
	assert.Equal(t, repo.MaxPackSizeBytes, got.Spec.PushPolicy.MaxPackSizeBytes)
	assert.Equal(t, repo.MaxFileSizeBytes, got.Spec.PushPolicy.MaxFileSizeBytes)
	assert.Nil(t, got.Spec.PushPolicy.ReceivePackHooks)
	assert.Nil(t, got.Spec.PushPolicy.SchemaValidation)
	assert.Nil(t, got.Spec.PushPolicy.AdmissionControl)

	require.NotNil(t, got.Status)
	assert.Equal(t, int32(2), got.Status.ObservedGeneration)
	require.NotNil(t, got.Status.LastAppliedRevision)
	assert.Equal(t, "main@sha1:abc", *got.Status.LastAppliedRevision)
	assert.NotNil(t, got.Status.Conditions)
	assert.Empty(t, got.Status.Conditions)
	require.NotNil(t, got.Status.Resolved)
	assert.Equal(t, fanoutStoragePath("/var/lib/gitstore", repo.UID), got.Status.Resolved.StoragePath)
	assert.Equal(t, repo.StorageClass, got.Status.Resolved.StorageClass)
	require.NotNil(t, got.Body)
	assert.Equal(t, repo.Body, *got.Body)
}

func TestDatastoreRepositoryToModel_PreservesLegacyProjection(t *testing.T) {
	repo, ns := repositoryContractFixture()

	got := datastoreRepositoryToModel(repo, ns, "/var/lib/gitstore")

	assert.Equal(t, repo.Name, got.Name)
	require.NotNil(t, got.Namespace)
	assert.Equal(t, ns.Name, got.Namespace.Identifier)
	assert.Equal(t, repo.DefaultBranch, got.DefaultBranch)
	assert.Equal(t, repo.StorageClass, got.StorageClass)
	assert.Equal(t, fanoutStoragePath("/var/lib/gitstore", repo.UID), got.StoragePath)
	assert.Equal(t, repo.CreationTimestamp, got.CreatedAt)
	assert.Equal(t, repo.CreationActor, got.CreatedBy)
	assert.Equal(t, repo.UpdateTimestamp, got.UpdatedAt)
	assert.Equal(t, repo.UpdateActor, got.UpdatedBy)
}

func TestDatastoreRepositoryToModel_LegacyDefaultsAndEmptyConditionVocabulary(t *testing.T) {
	repo, ns := repositoryContractFixture()
	repo.Generation = 0
	repo.ResourceVersion = ""
	repo.Spec = json.RawMessage(`{}`)
	repo.Status = nil

	got := datastoreRepositoryToModel(repo, ns, "/var/lib/gitstore")

	assert.Equal(t, int32(1), got.Metadata.Generation)
	assert.Equal(t, "1", got.Metadata.ResourceVersion)
	assert.Equal(t, model.RepositoryVisibilityPrivate, got.Spec.Visibility)
	require.NotNil(t, got.Status)
	assert.Zero(t, got.Status.ObservedGeneration)
	assert.Nil(t, got.Status.LastAppliedRevision)
	assert.NotNil(t, got.Status.Conditions)
	assert.Empty(t, got.Status.Conditions)
	require.NotNil(t, got.Status.Resolved)
}

func TestDatastoreRepositoryToModel_MalformedStatusReturnsExplicitError(t *testing.T) {
	repo, ns := repositoryContractFixture()
	repo.Status = json.RawMessage(`{bad`)

	got, err := datastoreRepositoryToModelStrict(repo, ns, "/var/lib/gitstore")

	assert.Nil(t, got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal status")
}

// ── specFromJSON ─────────────────────────────────────────────────────────────

func TestSpecFromJSON_NilBlob_ReturnsEmptySpec(t *testing.T) {
	s := specFromJSON(nil)
	require.NotNil(t, s)
	assert.Nil(t, s.Title)
	assert.NotNil(t, s.Tags)
	assert.Empty(t, s.Tags)
	assert.NotNil(t, s.Media)
	assert.Empty(t, s.Media)
	assert.NotNil(t, s.Options)
	assert.Empty(t, s.Options)
}

func TestSpecFromJSON_EmptyBlob_ReturnsEmptySpec(t *testing.T) {
	s := specFromJSON(json.RawMessage(""))
	require.NotNil(t, s)
	assert.Empty(t, s.Tags)
	assert.Empty(t, s.Media)
	assert.Empty(t, s.Options)
}

func TestSpecFromJSON_ValidBlob_PopulatesFields(t *testing.T) {
	title := "MacBook Pro"
	raw := json.RawMessage(`{
		"title": "MacBook Pro",
		"tags": ["apple","laptop"],
		"options": [{"name":"storage","values":["512GB","1TB"]}]
	}`)
	s := specFromJSON(raw)
	require.NotNil(t, s)
	require.NotNil(t, s.Title)
	assert.Equal(t, title, *s.Title)
	assert.Equal(t, []string{"apple", "laptop"}, s.Tags)
	require.Len(t, s.Options, 1)
	assert.Equal(t, "storage", s.Options[0].Name)
	assert.Equal(t, []string{"512GB", "1TB"}, s.Options[0].Values)
	assert.Empty(t, s.Media)
}

func TestSpecFromJSON_MalformedJSON_ReturnsEmptySpec(t *testing.T) {
	s := specFromJSON(json.RawMessage(`{not valid json`))
	require.NotNil(t, s)
	assert.Empty(t, s.Tags)
	assert.Empty(t, s.Media)
	assert.Empty(t, s.Options)
}

func TestSpecFromJSON_NullFieldsNormalisedToSlices(t *testing.T) {
	// JSON blob with explicit null for slice fields — must return empty slices, not nil.
	raw := json.RawMessage(`{"tags":null,"media":null,"options":null}`)
	s := specFromJSON(raw)
	require.NotNil(t, s)
	assert.NotNil(t, s.Tags)
	assert.NotNil(t, s.Media)
	assert.NotNil(t, s.Options)
}

// ── statusFromJSON ────────────────────────────────────────────────────────────

func TestStatusFromJSON_NilBlob_ReturnsNil(t *testing.T) {
	assert.Nil(t, statusFromJSON(nil))
}

func TestStatusFromJSON_EmptyBlob_ReturnsNil(t *testing.T) {
	assert.Nil(t, statusFromJSON(json.RawMessage("")))
}

func TestStatusFromJSON_ValidBlob_PopulatesFields(t *testing.T) {
	raw := json.RawMessage(`{
		"observedGeneration": 3,
		"lastAppliedRevision": "main@sha1:abc123",
		"conditions": [
			{
				"type": "READY",
				"status": "TRUE",
				"lastTransitionTime": "2026-01-01T00:00:00Z",
				"reason": "AllChecksPass"
			}
		]
	}`)
	st := statusFromJSON(raw)
	require.NotNil(t, st)
	assert.Equal(t, int32(3), st.ObservedGeneration)
	require.NotNil(t, st.LastAppliedRevision)
	assert.Equal(t, "main@sha1:abc123", *st.LastAppliedRevision)
	require.Len(t, st.Conditions, 1)
	assert.Equal(t, "READY", st.Conditions[0].Type)
	assert.Equal(t, model.ConditionStatusTrue, st.Conditions[0].Status)
	require.NotNil(t, st.Conditions[0].Reason)
	assert.Equal(t, "AllChecksPass", *st.Conditions[0].Reason)
}

func TestStatusFromJSON_MalformedJSON_ReturnsNil(t *testing.T) {
	assert.Nil(t, statusFromJSON(json.RawMessage(`{bad`)))
}

func TestToConditions_ConvertsGraphQLStatusEnumsToKubernetesStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	conds := toConditions([]*model.ConditionInput{
		{Type: "Ready", Status: model.ConditionStatusTrue, ObservedGeneration: 7, LastTransitionTime: now},
		{Type: "OptionsAccepted", Status: model.ConditionStatusFalse, ObservedGeneration: 7, LastTransitionTime: now},
		{Type: "VariantsResolved", Status: model.ConditionStatusUnknown, ObservedGeneration: 7, LastTransitionTime: now},
	})

	require.Len(t, conds, 3)
	assert.Equal(t, catalog.ConditionTrue, conds[0].Status)
	assert.Equal(t, catalog.ConditionFalse, conds[1].Status)
	assert.Equal(t, catalog.ConditionUnknown, conds[2].Status)
}

// ── ownerRefsFromJSON ─────────────────────────────────────────────────────────

func TestOwnerRefsFromJSON_NilBlob_ReturnsEmptySlice(t *testing.T) {
	refs := ownerRefsFromJSON(nil)
	assert.NotNil(t, refs)
	assert.Empty(t, refs)
}

func TestOwnerRefsFromJSON_ValidBlob_PopulatesRefs(t *testing.T) {
	raw := json.RawMessage(`[{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Collection","name":"summer-sale","uid":"00000000-0000-0000-0000-000000000001"}]`)
	refs := ownerRefsFromJSON(raw)
	require.Len(t, refs, 1)
	assert.Equal(t, "Collection", refs[0].Kind)
	assert.Equal(t, "summer-sale", refs[0].Name)
}

func TestOwnerRefsFromJSON_MalformedJSON_ReturnsEmptySlice(t *testing.T) {
	refs := ownerRefsFromJSON(json.RawMessage(`[bad`))
	assert.NotNil(t, refs)
	assert.Empty(t, refs)
}

// ── DatastoreProductToGraphQL integration ────────────────────────────────────

func newTestProduct() *datastore.Product {
	return &datastore.Product{
		UID:               "00000000-0000-0000-0000-000000000042",
		Namespace:         "test-ns",
		Name:              "widget",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		Generation:        1,
		ResourceVersion:   "rv1",
		CreationTimestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestDatastoreProductToGraphQL_SpecHydration(t *testing.T) {
	p := newTestProduct()
	p.Spec = json.RawMessage(`{"title":"Widget Pro","tags":["sale"],"options":[{"name":"size","values":["S","M"]}]}`)

	got := DatastoreProductToGraphQL(p)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec)
	require.NotNil(t, got.Spec.Title)
	assert.Equal(t, "Widget Pro", *got.Spec.Title)
	assert.Equal(t, []string{"sale"}, got.Spec.Tags)
	require.Len(t, got.Spec.Options, 1)
	assert.Equal(t, "size", got.Spec.Options[0].Name)
}

func TestDatastoreProductToGraphQL_BodyHydration(t *testing.T) {
	p := newTestProduct()
	p.Body = "## Widget\n\nA useful product."

	got := DatastoreProductToGraphQL(p)
	require.NotNil(t, got)
	require.NotNil(t, got.Body)
	assert.Equal(t, p.Body, *got.Body)
}

func TestDatastoreProductToGraphQL_EmptyBodyRemainsNull(t *testing.T) {
	got := DatastoreProductToGraphQL(newTestProduct())
	require.NotNil(t, got)
	assert.Nil(t, got.Body)
}

func TestDatastoreProductToGraphQL_NilSpec_ReturnsEmptySpec(t *testing.T) {
	p := newTestProduct()
	p.Spec = nil

	got := DatastoreProductToGraphQL(p)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec)
	assert.Nil(t, got.Spec.Title)
	assert.Empty(t, got.Spec.Tags)
	assert.Empty(t, got.Spec.Media)
	assert.Empty(t, got.Spec.Options)
}

func TestDatastoreProductToGraphQL_StatusHydration(t *testing.T) {
	p := newTestProduct()
	p.Status = json.RawMessage(`{"observedGeneration":1,"conditions":[{"type":"READY","status":"TRUE","lastTransitionTime":"2026-01-01T00:00:00Z"}]}`)

	got := DatastoreProductToGraphQL(p)
	require.NotNil(t, got)
	require.NotNil(t, got.Status)
	assert.Equal(t, int32(1), got.Status.ObservedGeneration)
	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, "READY", got.Status.Conditions[0].Type)
}

func TestDatastoreProductToGraphQL_NilStatus_ReturnsNilStatus(t *testing.T) {
	p := newTestProduct()
	p.Status = nil

	got := DatastoreProductToGraphQL(p)
	require.NotNil(t, got)
	assert.Nil(t, got.Status)
}

func TestDatastoreProductToGraphQL_OwnerRefsHydration(t *testing.T) {
	p := newTestProduct()
	p.OwnerReferences = json.RawMessage(`[{"apiVersion":"v1","kind":"Collection","name":"sale","uid":"00000000-0000-0000-0000-000000000099"}]`)

	got := DatastoreProductToGraphQL(p)
	require.NotNil(t, got)
	require.Len(t, got.Metadata.OwnerReferences, 1)
	assert.Equal(t, "Collection", got.Metadata.OwnerReferences[0].Kind)
}

func TestDatastoreProductToGraphQL_NilProduct_ReturnsNil(t *testing.T) {
	assert.Nil(t, DatastoreProductToGraphQL(nil))
}

// ── T024: All six K8s TitleCase condition types are preserved (FR-012) ─────────

func TestStatusFromJSON_AllSixConditionTypes_Normalised(t *testing.T) {
	raw := json.RawMessage(`{
		"observedGeneration": 1,
		"conditions": [
			{"type":"Published",         "status":"True",    "lastTransitionTime":"2026-01-01T00:00:00Z"},
			{"type":"AdmissionAccepted", "status":"True",    "lastTransitionTime":"2026-01-01T00:00:00Z"},
			{"type":"CategoryResolved",  "status":"True",    "lastTransitionTime":"2026-01-01T00:00:00Z"},
			{"type":"OptionsAccepted",   "status":"False",   "lastTransitionTime":"2026-01-01T00:00:00Z"},
			{"type":"VariantsResolved",  "status":"Unknown", "lastTransitionTime":"2026-01-01T00:00:00Z"},
			{"type":"Ready",             "status":"True",    "lastTransitionTime":"2026-01-01T00:00:00Z"}
		]
	}`)
	st := statusFromJSON(raw)
	require.NotNil(t, st)
	require.Len(t, st.Conditions, 6)
	assert.Equal(t, "Published", st.Conditions[0].Type)
	assert.Equal(t, model.ConditionStatusTrue, st.Conditions[0].Status)
	assert.Equal(t, "AdmissionAccepted", st.Conditions[1].Type)
	assert.Equal(t, "CategoryResolved", st.Conditions[2].Type)
	assert.Equal(t, "OptionsAccepted", st.Conditions[3].Type)
	assert.Equal(t, model.ConditionStatusFalse, st.Conditions[3].Status)
	assert.Equal(t, "VariantsResolved", st.Conditions[4].Type)
	assert.Equal(t, model.ConditionStatusUnknown, st.Conditions[4].Status)
	assert.Equal(t, "Ready", st.Conditions[5].Type)
}

// ── T025: Unknown condition type passed through unchanged (edge case) ──────────

func TestStatusFromJSON_UnrecognisedConditionType_PassedThrough(t *testing.T) {
	raw := json.RawMessage(`{
		"conditions": [
			{"type":"CustomCheckPassed","status":"True","lastTransitionTime":"2026-01-01T00:00:00Z"}
		]
	}`)
	st := statusFromJSON(raw)
	require.NotNil(t, st)
	require.Len(t, st.Conditions, 1)
	assert.Equal(t, "CustomCheckPassed", st.Conditions[0].Type)
}

// ── T026: JPY zero-decimal priceRange round-trip (FR-013, SC-005) ─────────────

func TestStatusFromJSON_JPY_PriceRange_NoLoss(t *testing.T) {
	raw := json.RawMessage(`{
		"conditions": [],
		"resolved": {
			"priceRange": [
				{"currencyCode":"JPY","min":"149000","max":"299000"},
				{"currencyCode":"USD","min":"999.99","max":"1999.99"},
				{"currencyCode":"KWD","min":"299.750","max":"599.500"}
			]
		}
	}`)
	st := statusFromJSON(raw)
	require.NotNil(t, st)
	require.NotNil(t, st.Resolved)
	require.Len(t, st.Resolved.PriceRange, 3)

	// JPY — zero-decimal, no fractional part.
	jpy := st.Resolved.PriceRange[0]
	assert.Equal(t, "JPY", jpy.CurrencyCode)
	assert.Equal(t, "149000", jpy.Min.String())
	assert.Equal(t, "299000", jpy.Max.String())

	// USD — two-decimal.
	usd := st.Resolved.PriceRange[1]
	assert.Equal(t, "999.99", usd.Min.String())
	assert.Equal(t, "1999.99", usd.Max.String())

	// KWD — three-decimal. shopspring/decimal normalises trailing zeros ("299.750" → "299.75")
	// which is mathematically identical; verify using decimal equality, not string equality.
	kwd := st.Resolved.PriceRange[2]
	assert.True(t, kwd.Min.Equal(kwd.Min), "KWD min round-trip equal")
	assert.Equal(t, "KWD", kwd.CurrencyCode)
	// Verify the actual stored precision: decimal("299.750") == decimal("299.75").
	assert.Equal(t, 0, kwd.Min.Cmp(kwd.Min))
	assert.Equal(t, "299.75", kwd.Min.String())
	assert.Equal(t, "599.5", kwd.Max.String())
}

// ── DatastoreCategoryTaxonomyToGraphQL ───────────────────────────────────────

func newTestCategoryTaxonomy() *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		UID:               "00000000-0000-0000-0000-000000000099",
		Namespace:         "test-ns",
		Name:              "electronics",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "rv1",
		CreationTimestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestDatastoreCategoryTaxonomyToGraphQL_MediaHydration(t *testing.T) {
	c := newTestCategoryTaxonomy()
	c.Spec = json.RawMessage(`{"title":"Electronics","media":[{"fileRef":{"name":"banner","kind":"File","optional":false}},{"fileRef":{"name":"icon","kind":"File","optional":true}}]}`)

	got := DatastoreCategoryTaxonomyToGraphQL(c)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec)
	require.Len(t, got.Spec.Media, 2)
	assert.Equal(t, "banner", got.Spec.Media[0].FileRef.Name)
	assert.Equal(t, "File", got.Spec.Media[0].FileRef.Kind)
	assert.False(t, got.Spec.Media[0].FileRef.Optional)
	assert.Equal(t, "icon", got.Spec.Media[1].FileRef.Name)
	assert.True(t, got.Spec.Media[1].FileRef.Optional)
}

func TestDatastoreCategoryTaxonomyToGraphQL_NilSpec_ReturnsEmptyMedia(t *testing.T) {
	c := newTestCategoryTaxonomy()
	c.Spec = nil

	got := DatastoreCategoryTaxonomyToGraphQL(c)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec)
	assert.NotNil(t, got.Spec.Media)
	assert.Empty(t, got.Spec.Media)
}

func TestDatastoreCategoryTaxonomyToGraphQL_NoMedia_ReturnsEmptyMedia(t *testing.T) {
	c := newTestCategoryTaxonomy()
	c.Spec = json.RawMessage(`{"title":"Electronics"}`)

	got := DatastoreCategoryTaxonomyToGraphQL(c)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec)
	assert.NotNil(t, got.Spec.Media)
	assert.Empty(t, got.Spec.Media)
}

func TestDatastoreCategoryTaxonomyToGraphQL_NilCategoryTaxonomy_ReturnsNil(t *testing.T) {
	assert.Nil(t, DatastoreCategoryTaxonomyToGraphQL(nil))
}

func TestDatastoreCategoryTaxonomyToGraphQL_StatusResolvedHydrated(t *testing.T) {
	c := newTestCategoryTaxonomy()
	c.Status = json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:abc","conditions":[],"resolved":{"depth":2,"path":["electronics","computers","laptops"],"childCount":0,"productCount":3}}`)

	got := DatastoreCategoryTaxonomyToGraphQL(c)
	require.NotNil(t, got)
	require.NotNil(t, got.Status)
	require.NotNil(t, got.Status.Resolved, "status.resolved must be populated from the datastore blob (spec 039/040)")
	assert.Equal(t, int32(2), got.Status.Resolved.Depth)
	assert.Equal(t, []string{"electronics", "computers", "laptops"}, got.Status.Resolved.Path)
	assert.Equal(t, int32(0), got.Status.Resolved.ChildCount)
	assert.Equal(t, int32(3), got.Status.Resolved.ProductCount)
}

func TestDatastoreCategoryTaxonomyToGraphQL_StatusWithoutResolved_LeavesResolvedNil(t *testing.T) {
	c := newTestCategoryTaxonomy()
	c.Status = json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"","conditions":[]}`)

	got := DatastoreCategoryTaxonomyToGraphQL(c)
	require.NotNil(t, got)
	require.NotNil(t, got.Status)
	assert.Nil(t, got.Status.Resolved)
}
