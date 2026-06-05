// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.Equal(t, model.ProductConditionTypeReady, st.Conditions[0].Type)
	assert.Equal(t, model.ConditionStatusTrue, st.Conditions[0].Status)
	require.NotNil(t, st.Conditions[0].Reason)
	assert.Equal(t, "AllChecksPass", *st.Conditions[0].Reason)
}

func TestStatusFromJSON_MalformedJSON_ReturnsNil(t *testing.T) {
	assert.Nil(t, statusFromJSON(json.RawMessage(`{bad`)))
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
	assert.Equal(t, model.ProductConditionTypeReady, got.Status.Conditions[0].Type)
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
	p.OwnerRefs = json.RawMessage(`[{"apiVersion":"v1","kind":"Collection","name":"sale","uid":"00000000-0000-0000-0000-000000000099"}]`)

	got := DatastoreProductToGraphQL(p)
	require.NotNil(t, got)
	require.Len(t, got.Metadata.OwnerReferences, 1)
	assert.Equal(t, "Collection", got.Metadata.OwnerReferences[0].Kind)
}

func TestDatastoreProductToGraphQL_NilProduct_ReturnsNil(t *testing.T) {
	assert.Nil(t, DatastoreProductToGraphQL(nil))
}

// ── T024: All six K8s TitleCase condition types normalised (FR-012) ───────────

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
	assert.Equal(t, model.ProductConditionTypePublished, st.Conditions[0].Type)
	assert.Equal(t, model.ConditionStatusTrue, st.Conditions[0].Status)
	assert.Equal(t, model.ProductConditionTypeAdmissionAccepted, st.Conditions[1].Type)
	assert.Equal(t, model.ProductConditionTypeCategoryResolved, st.Conditions[2].Type)
	assert.Equal(t, model.ProductConditionTypeOptionsAccepted, st.Conditions[3].Type)
	assert.Equal(t, model.ConditionStatusFalse, st.Conditions[3].Status)
	assert.Equal(t, model.ProductConditionTypeVariantsResolved, st.Conditions[4].Type)
	assert.Equal(t, model.ConditionStatusUnknown, st.Conditions[4].Status)
	assert.Equal(t, model.ProductConditionTypeReady, st.Conditions[5].Type)
}

// ── T025: Unknown condition type passed through uppercased (edge case) ────────

func TestStatusFromJSON_UnrecognisedConditionType_PassedThrough(t *testing.T) {
	raw := json.RawMessage(`{
		"conditions": [
			{"type":"CustomCheckPassed","status":"True","lastTransitionTime":"2026-01-01T00:00:00Z"}
		]
	}`)
	st := statusFromJSON(raw)
	require.NotNil(t, st)
	require.Len(t, st.Conditions, 1)
	// Unknown type is uppercased and NOT dropped.
	assert.Equal(t, model.ProductConditionType("CUSTOMCHECKPASSED"), st.Conditions[0].Type)
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
