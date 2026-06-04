// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate_test

import (
	"embed"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/*
var testFS embed.FS

// ── US1: Valid product parsing ────────────────────────────────────────────────

func TestParse_ValidProductAccepted(t *testing.T) {
	f, err := testFS.Open("testdata/macbook-pro-64gb-1tb-ssd-m4.md")
	require.NoError(t, err)
	res, body, err := validate.Parse(f)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "catalog.gitstore.dev/v1beta1", res.APIVersion)
	assert.Equal(t, "Product", res.Kind)
	assert.Equal(t, "macbook-pro-64gb-1tb-ssd-m4", res.Metadata.Name)
	assert.NotEmpty(t, body)
}

func TestParse_ValidProduct_OptionalFieldsOmitted(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: minimal-product
spec: {}
---
body
`
	res, body, err := validate.Parse(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "minimal-product", res.Metadata.Name)
	assert.Nil(t, res.Spec.CategoryRef)
	assert.Empty(t, res.Spec.Tags)
	assert.Empty(t, res.Spec.Options)
	assert.NotEmpty(t, body)
}

func TestParse_ValidProduct_LabelsAndAnnotations(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: labelled-product
  labels:
    env: production
    tier: premium
  annotations:
    owner: catalog-team
spec: {}
---
body
`
	res, _, err := validate.Parse(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "production", res.Metadata.Labels["env"])
	assert.Equal(t, "premium", res.Metadata.Labels["tier"])
	assert.Equal(t, "catalog-team", res.Metadata.Annotations["owner"])
}

// ── US2: Kind validation ──────────────────────────────────────────────────────

func TestParse_WrongKindRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Category
metadata:
  name: some-product
spec: {}
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kind")
}

func TestParse_MissingNameRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: my-store
spec: {}
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestParse_KindLowercaseRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: product
metadata:
  name: my-product
spec: {}
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kind")
}

// ── US3: Missing required fields ──────────────────────────────────────────────

func TestParse_LegacyFrontmatterRejected(t *testing.T) {
	doc := `---
kind: Product
metadata:
  name: old-product
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration is not supported in alpha")
}

func TestParse_StatusKeyRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
spec: {}
status:
  conditions: []
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status")
}

func TestParse_WrongApiVersionRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1
kind: Product
metadata:
  name: my-product
spec: {}
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catalog.gitstore.dev/v1beta1")
}

func TestParse_SpecAbsentRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec")
}

func TestParse_EmptyNameRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: ""
spec: {}
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

// ── US4: Forbidden system-managed fields ──────────────────────────────────────

func TestParse_ReadOnlyMetadataUIDRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
  uid: some-uuid
spec: {}
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uid")
}

// ── US2: Spec-level validation ────────────────────────────────────────────────

func TestParse_OptionMissingNameRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
spec:
  options:
  - values: [red, blue]
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "options[0].name")
}

func TestParse_DuplicateOptionNamesRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
spec:
  options:
  - name: color
  - name: color
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "color")
}

func TestParse_ReadOnlyMetadataOwnerReferencesRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
  ownerReferences: []
spec: {}
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ownerReferences")
}

func TestParse_ReadOnlyMetadataAllFieldsRejected(t *testing.T) {
	readOnlyFields := []struct {
		field string
		yaml  string
	}{
		{"resourceVersion", "  resourceVersion: \"1\""},
		{"generation", "  generation: 1"},
		{"creationTimestamp", "  creationTimestamp: \"2026-01-01T00:00:00Z\""},
		{"revision", "  revision: main@sha1:abc"},
	}
	for _, tc := range readOnlyFields {
		t.Run(tc.field, func(t *testing.T) {
			doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: my-product\n" +
				tc.yaml + "\nspec: {}\n---\nbody\n"
			_, _, err := validate.Parse(strings.NewReader(doc))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
		})
	}
}

// ── US5: Constraint rules + opt-in skip ───────────────────────────────────────

func TestParse_LabelKeyTooLongRejected(t *testing.T) {
	longKey := strings.Repeat("a", 64) // exceeds 63-char segment limit
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: my-product\n  labels:\n    " +
		longKey + ": value\nspec: {}\n---\nbody\n"
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "label")
}

func TestParse_LabelKeyPrefixTooLongRejected(t *testing.T) {
	longPrefix := strings.Repeat("a", 254) // exceeds 253-char prefix limit
	key := longPrefix + "/name"
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: my-product\n  labels:\n    \"" +
		key + "\": value\nspec: {}\n---\nbody\n"
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prefix")
}

func TestParse_LabelValueTooLongRejected(t *testing.T) {
	longVal := strings.Repeat("v", 64) // exceeds 63-char value limit
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: my-product\n  labels:\n    env: " +
		longVal + "\nspec: {}\n---\nbody\n"
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "label value")
}

func TestParse_MultipleViolationsReportedTogether(t *testing.T) {
	// Both kind wrong AND duplicate options — both violations must appear.
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Widget
metadata:
  name: my-product
spec:
  options:
  - name: color
  - name: color
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kind")
	assert.Contains(t, err.Error(), "duplicate")
}

func TestParse_NoFrontmatterSkipped(t *testing.T) {
	doc := "# README\n\nThis is a plain Markdown file with no frontmatter.\n"
	res, body, err := validate.Parse(strings.NewReader(doc))
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.NotEmpty(t, body)
}
