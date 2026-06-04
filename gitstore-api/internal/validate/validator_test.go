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

// ── US1: Acceptance / rejection ───────────────────────────────────────────────

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
status:
  conditions: []
---
body
`
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status")
}

func TestParse_ReadOnlyMetadataUIDRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
  uid: some-uuid
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

func TestParse_LabelKeyTooLongRejected(t *testing.T) {
	longKey := strings.Repeat("a", 64) // exceeds 63-char segment limit
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: my-product\n  labels:\n    " +
		longKey + ": value\n---\nbody\n"
	_, _, err := validate.Parse(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "label")
}
