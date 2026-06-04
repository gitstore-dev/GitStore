// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/adrg/frontmatter"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/go-playground/validator/v10"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validProductYAML = `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: macbook-pro-m4
  namespace: my-store
  labels:
    gitstore.dev/brand: apple
  annotations:
    gitstore.dev/notes: "flagship"
spec:
  title: MacBook Pro M4
  categoryRef:
    kind: CategoryTaxonomy
    name: personal-computers
  tags: [laptop, apple-silicon]
  options:
  - name: color
    title: Colour
    values: [silver, space-black]
  - name: ram
    values: [36GB, 64GB]
  media:
  - fileRef:
      kind: File
      name: hero-image
      optional: true
---
body content
`

// ── US1: Envelope parsing ─────────────────────────────────────────────────────

func TestProductResource_ParseValid(t *testing.T) {
	var res catalog.ProductResource
	_, err := frontmatter.Parse(strings.NewReader(validProductYAML), &res)
	require.NoError(t, err)
	assert.Equal(t, "catalog.gitstore.dev/v1beta1", res.APIVersion)
	assert.Equal(t, "Product", res.Kind)
	assert.Equal(t, "macbook-pro-m4", res.Metadata.Name)
	assert.Equal(t, "my-store", res.Metadata.Namespace)
	assert.Equal(t, "apple", res.Metadata.Labels["gitstore.dev/brand"])
}

func TestProductResource_MissingKindFailsValidation(t *testing.T) {
	yaml := `---
apiVersion: catalog.gitstore.dev/v1beta1
metadata:
  name: some-product
spec: {}
---
`
	var res catalog.ProductResource
	_, err := frontmatter.Parse(strings.NewReader(yaml), &res)
	require.NoError(t, err) // parsing succeeds; validation catches it
	v := validator.New()
	err = v.Struct(res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Kind")
}

func TestProductResource_WrongKindFailsValidation(t *testing.T) {
	yaml := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Category
metadata:
  name: some-product
spec: {}
---
`
	var res catalog.ProductResource
	_, err := frontmatter.Parse(strings.NewReader(yaml), &res)
	require.NoError(t, err)
	v := validator.New()
	err = v.Struct(res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Kind")
}

func TestProductResource_MissingNameFailsValidation(t *testing.T) {
	yaml := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: my-store
spec: {}
---
`
	var res catalog.ProductResource
	_, err := frontmatter.Parse(strings.NewReader(yaml), &res)
	require.NoError(t, err)
	v := validator.New()
	err = v.Struct(res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Name")
}

// ── US2: ProductSpec round-trip ───────────────────────────────────────────────

func TestProductSpec_AllFieldsRoundTrip(t *testing.T) {
	var res catalog.ProductResource
	_, err := frontmatter.Parse(strings.NewReader(validProductYAML), &res)
	require.NoError(t, err)

	spec := res.Spec
	assert.Equal(t, "MacBook Pro M4", spec.Title)
	require.NotNil(t, spec.CategoryRef)
	assert.Equal(t, "CategoryTaxonomy", spec.CategoryRef.Kind)
	assert.Equal(t, "personal-computers", spec.CategoryRef.Name)
	assert.Equal(t, []string{"laptop", "apple-silicon"}, spec.Tags)
	require.Len(t, spec.Options, 2)
	assert.Equal(t, "color", spec.Options[0].Name)
	assert.Equal(t, "Colour", spec.Options[0].Title)
	assert.Equal(t, []string{"silver", "space-black"}, spec.Options[0].Values)
	require.Len(t, spec.Media, 1)
	assert.Equal(t, "File", spec.Media[0].FileRef.Kind)
	assert.Equal(t, "hero-image", spec.Media[0].FileRef.Name)
	assert.True(t, spec.Media[0].FileRef.Optional)
}

func TestProductSpec_MinimalFieldsNoError(t *testing.T) {
	yaml := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: minimal-product
spec:
  title: Minimal
  categoryRef:
    name: some-cat
---
`
	var res catalog.ProductResource
	_, err := frontmatter.Parse(strings.NewReader(yaml), &res)
	require.NoError(t, err)
	v := validator.New()
	require.NoError(t, v.Struct(res))
	assert.Nil(t, res.Spec.Tags)
	assert.Nil(t, res.Spec.Media)
	assert.Nil(t, res.Spec.Options)
}

// ── US3: ProductStatus round-trip ─────────────────────────────────────────────

func TestProductStatus_FullRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	original := catalog.ProductStatus{
		ObservedGeneration:  3,
		LastAppliedRevision: "main@sha1:abc123",
		Conditions: []catalog.Condition{
			{Type: catalog.ConditionPublished, Status: catalog.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: now, Reason: "Released", Message: "published"},
			{Type: catalog.ConditionAdmissionAccepted, Status: catalog.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: now},
			{Type: catalog.ConditionCategoryResolved, Status: catalog.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: now},
			{Type: catalog.ConditionOptionsAccepted, Status: catalog.ConditionFalse, ObservedGeneration: 3, LastTransitionTime: now, Reason: "MissingOption"},
			{Type: catalog.ConditionVariantsResolved, Status: catalog.ConditionUnknown, ObservedGeneration: 3, LastTransitionTime: now},
			{Type: catalog.ConditionReady, Status: catalog.ConditionFalse, ObservedGeneration: 3, LastTransitionTime: now},
		},
		Resolved: &catalog.ResolvedProductDefinition{
			Category:       &catalog.ResolvedCategoryDefinition{Name: "Laptops", Path: []string{"Electronics", "Laptops"}},
			PriceRange:     []catalog.PriceRangeDefinition{{CurrencyCode: "USD", Min: decimal.NewFromFloat(999.00), Max: decimal.NewFromFloat(1999.00)}},
			TotalInventory: 42,
			VariantSummary: &catalog.VariantSummaryDefinition{Total: 4, Ready: 3, Unavailable: 1},
			Media:          []catalog.ResolvedFileDefinition{{Name: "hero", URL: "https://cdn.example.com/hero.jpg", ContentType: "image/jpeg"}},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var got catalog.ProductStatus
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, original.ObservedGeneration, got.ObservedGeneration)
	assert.Equal(t, original.LastAppliedRevision, got.LastAppliedRevision)
	assert.Len(t, got.Conditions, 6)
	assert.Equal(t, catalog.ConditionPublished, got.Conditions[0].Type)
	assert.Equal(t, catalog.ConditionTrue, got.Conditions[0].Status)
	require.NotNil(t, got.Resolved)
	assert.Equal(t, "Laptops", got.Resolved.Category.Name)
	assert.Equal(t, []string{"Electronics", "Laptops"}, got.Resolved.Category.Path)
	assert.True(t, original.Resolved.PriceRange[0].Min.Equal(got.Resolved.PriceRange[0].Min))
	assert.True(t, original.Resolved.PriceRange[0].Max.Equal(got.Resolved.PriceRange[0].Max))
	assert.Equal(t, int64(42), got.Resolved.TotalInventory)
	assert.Equal(t, int64(3), got.Resolved.VariantSummary.Ready)
	assert.Equal(t, "https://cdn.example.com/hero.jpg", got.Resolved.Media[0].URL)
}

func TestProductStatus_EmptyConditionsNoError(t *testing.T) {
	s := catalog.ProductStatus{}
	data, err := json.Marshal(s)
	require.NoError(t, err)
	var got catalog.ProductStatus
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Empty(t, got.Conditions)
	assert.Nil(t, got.Resolved)
}

func TestCondition_InvalidStatusFailsValidation(t *testing.T) {
	c := catalog.Condition{
		Type:   catalog.ConditionReady,
		Status: "Maybe", // invalid
	}
	v := validator.New()
	err := v.Struct(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Status")
}
