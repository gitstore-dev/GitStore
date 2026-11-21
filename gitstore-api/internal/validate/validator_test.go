// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate_test

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestManifestEnumCompatibilityAndCanonicalRoundTrip(t *testing.T) {
	tests := []struct {
		kind, group, spec, canonical, legacy string
	}{
		{"Product", "catalog", "title: ACTIVE\n  lifecycle: {state: %s}", "Active", "ACTIVE"},
		{"Product", "catalog", "title: RETIRED\n  lifecycle: {state: %s}", "Retired", "RETIRED"},
		{"Namespace", "", "title: USER\n  tier: %s", "User", "USER"},
		{"Namespace", "", "tier: %s", "Organization", "ORGANIZATION"},
		{"ProductVariant", "catalog", "title: deny\n  sku: backorder\n  productRef: {name: parent}\n  inventory: {managed: true, policy: %s}", "Deny", "deny"},
		{"ProductVariant", "catalog", "title: deny\n  sku: backorder\n  productRef: {name: parent}\n  inventory: {managed: true, policy: %s}", "Backorder", "backorder"},
	}
	for _, visibility := range []struct{ canonical, legacy string }{
		{"Public", "PUBLIC"}, {"Private", "PRIVATE"}, {"Internal", "INTERNAL"},
		{"Public", "public"}, {"Private", "private"}, {"Internal", "internal"},
	} {
		tests = append(tests, struct{ kind, group, spec, canonical, legacy string }{
			"Repository", "", "defaultBranch: PRIVATE\n  visibility: %s", visibility.canonical, visibility.legacy,
		}, struct{ kind, group, spec, canonical, legacy string }{
			"Namespace", "", "tier: Organization\n  repositoryDefaults: {visibility: %s, defaultBranch: PRIVATE}", visibility.canonical, visibility.legacy,
		})
	}
	for _, strategy := range []struct{ canonical, legacy string }{
		{"Fixed", "fixed"}, {"FixedUnitPrice", "fixedUnitPrice"}, {"Percentage", "percentage"}, {"Tiered", "tiered"},
	} {
		tests = append(tests, struct{ kind, group, spec, canonical, legacy string }{
			"ProductVariant", "catalog", "title: fixedUnitPrice\n  sku: fixed\n  productRef: {name: parent}\n  pricing:\n    priceSet:\n      name: fixed\n      prices:\n      - currencyCode: USD\n        amount: \"9.99\"\n        strategy: {type: %s}\n        eligibility: {operator: Any, constraints: []}",
			strategy.canonical, strategy.legacy,
		})
	}
	for _, provider := range []struct{ canonical, legacy string }{
		{"Git", "git"}, {"Lfs", "lfs"}, {"S3", "s3"}, {"Gcs", "gcs"},
	} {
		tests = append(tests, struct{ kind, group, spec, canonical, legacy string }{
			"File", "storage", "contentType: image/png\n  type: gitstore.dev/media\n  provider: %s\n  source: {uri: 's3://assets/deny.png'}",
			provider.canonical, provider.legacy,
		})
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.legacy, func(t *testing.T) {
			group := tt.group
			if group != "" {
				group += "."
			}
			metadata := "  name: enum-example\n  labels: {spelling: USER, operator: NotIn}\n  annotations: {policy: deny}\n"
			if tt.kind != "Namespace" {
				metadata += "  namespace: acme-store\n"
			}
			manifest := func(value string) string {
				return fmt.Sprintf("---\napiVersion: %sgitstore.dev/v1beta1\nkind: %s\nmetadata:\n%sspec:\n  %s\n---\nUSER deny NotIn fixedUnitPrice\n",
					group, tt.kind, metadata, fmt.Sprintf(tt.spec, value))
			}
			legacy, legacyBody, err := validate.NewParser().ParseResource(strings.NewReader(manifest(tt.legacy)))
			require.NoError(t, err)
			legacyAuthored, err := yaml.Marshal(parsedEnumResource(legacy))
			require.NoError(t, err)
			require.Contains(t, string(legacyAuthored), tt.canonical, "encoding a legacy manifest must author canonical spelling")
			canonical, canonicalBody, err := validate.NewParser().ParseResource(strings.NewReader(manifest(tt.canonical)))
			require.NoError(t, err)
			if legacy.Repository != nil {
				require.Equal(t, tt.legacy, legacy.Repository.Spec.Visibility, "legacy storage must remain stable on re-admission")
				legacy.Repository.Spec.Visibility = catalog.RepositoryVisibilityFromManifest(legacy.Repository.Spec.Visibility)
			}
			if legacy.Namespace != nil && legacy.Namespace.Spec.RepositoryDefaults != nil {
				require.Equal(t, tt.legacy, legacy.Namespace.Spec.RepositoryDefaults.Visibility, "legacy storage must remain stable on re-admission")
				legacy.Namespace.Spec.RepositoryDefaults.Visibility = catalog.RepositoryVisibilityFromManifest(legacy.Namespace.Spec.RepositoryDefaults.Visibility)
			}
			require.Equal(t, legacy, canonical, "canonical spelling must retain legacy internal semantics")
			require.Equal(t, legacyBody, canonicalBody)
			resource := parsedEnumResource(canonical)
			before, err := json.Marshal(resource)
			require.NoError(t, err)
			authored, err := yaml.Marshal(resource)
			require.NoError(t, err)
			require.Contains(t, string(authored), tt.canonical)
			require.Contains(t, string(authored), "spelling: USER")
			require.Contains(t, string(authored), "operator: NotIn")
			require.Contains(t, string(authored), "policy: deny", "annotations must not be rewritten")
			roundTrip, body, err := validate.NewParser().ParseResource(strings.NewReader("---\n" + string(authored) + "---\n" + string(canonicalBody)))
			require.NoError(t, err)
			reencoded, err := yaml.Marshal(parsedEnumResource(roundTrip))
			require.NoError(t, err)
			require.Equal(t, string(authored), string(reencoded))
			require.Equal(t, canonicalBody, body)
			after, err := json.Marshal(resource)
			require.NoError(t, err)
			require.Equal(t, before, after, "encoding must not mutate the admitted resource")

			_, _, err = validate.NewParser().ParseResource(strings.NewReader(manifest("nOtAnEnUm")))
			require.Error(t, err, "compatibility is bounded, not arbitrary case folding")
			mixed := strings.ToLower(tt.canonical[:1]) + strings.ToUpper(tt.canonical[1:])
			if mixed != tt.canonical && mixed != tt.legacy {
				_, _, err = validate.NewParser().ParseResource(strings.NewReader(manifest(mixed)))
				require.Error(t, err, "undocumented mixed-case aliases must not be invented")
			}
		})
	}
}

func parsedEnumResource(parsed *validate.ParsedResource) any {
	switch parsed.Kind {
	case "Product":
		return parsed.Product
	case "Namespace":
		return parsed.Namespace
	case "Repository":
		return parsed.Repository
	case "ProductVariant":
		return parsed.ProductVariant
	case "File":
		return parsed.File
	default:
		panic("unexpected enum fixture")
	}
}

//go:embed testdata/*
var testFS embed.FS

func parseProduct(r io.Reader) (*catalog.ProductResource, []byte, error) {
	parsed, body, err := validate.NewParser().ParseResource(r)
	if err != nil || parsed == nil {
		return nil, body, err
	}
	if parsed.Product == nil {
		return nil, body, fmt.Errorf("expected Product resource, got %q", parsed.Kind)
	}
	return parsed.Product, body, nil
}

// ── US1: Valid product parsing ────────────────────────────────────────────────

func TestParse_ValidProductAccepted(t *testing.T) {
	f, err := testFS.Open("testdata/macbook-pro-64gb-1tb-ssd-m4.md")
	require.NoError(t, err)
	res, body, err := parseProduct(f)
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
  namespace: gitstore
  name: minimal-product
spec:
  title: Minimal Product
---
body
`
	res, body, err := parseProduct(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "minimal-product", res.Metadata.Name)
	assert.Nil(t, res.Spec.CategoryRef)
	assert.Empty(t, res.Spec.Tags)
	assert.Empty(t, res.Spec.Options)
	assert.NotEmpty(t, body)
}

func TestParse_Product_MissingTitle_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: minimal-product
spec: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.title")
}

func TestParse_ValidProduct_LabelsAndAnnotations(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: labelled-product
  labels:
    env: production
    tier: premium
  annotations:
    owner: catalog-team
spec:
  title: Labelled Product
---
body
`
	res, _, err := parseProduct(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "production", res.Metadata.Labels["env"])
	assert.Equal(t, "premium", res.Metadata.Labels["tier"])
	assert.Equal(t, "catalog-team", res.Metadata.Annotations["owner"])
}

func TestParse_ValidNamespaceAccepted(t *testing.T) {
	doc := `---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme-store
spec:
  title: Acme Store
  tier: USER
---
`

	parsed, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.NotNil(t, parsed.Namespace)
	assert.Equal(t, "acme-store", parsed.Namespace.Metadata.Name)
	assert.Equal(t, "Acme Store", parsed.Namespace.Spec.Title)
	assert.Equal(t, "USER", parsed.Namespace.Spec.Tier)
}

func TestParse_NamespaceAuthoredStatusRejected(t *testing.T) {
	doc := `---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: status-ignored
spec:
  title: Status Ignored
  tier: USER
status:
  observedGeneration: 999
  lastAppliedRevision: forged
  conditions:
  - type: Ready
    status: "True"
---
`

	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status is system-managed")
}

func TestParse_NamespaceInvalidTierRejected(t *testing.T) {
	doc := `---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme-store
spec:
  tier: ENTERPRISE
---
`

	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.tier")
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
	_, _, err := parseProduct(strings.NewReader(doc))
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
	_, _, err := parseProduct(strings.NewReader(doc))
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
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kind")
}

// ── US3: Missing required fields ──────────────────────────────────────────────

func TestParse_LegacyFrontmatterRejected(t *testing.T) {
	doc := `---
kind: Product
metadata:
  namespace: gitstore
  name: old-product
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration is not supported in alpha")
}

func TestParse_StatusKeyRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec: {}
status:
  conditions: []
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status")
}

func TestParse_WrongApiVersionRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catalog.gitstore.dev/v1beta1")
}

func TestParse_SpecAbsentRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec")
}

func TestParse_EmptyNameRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: ""
spec: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

// ── US4: Forbidden system-managed fields ──────────────────────────────────────

func TestParse_ReadOnlyMetadataUIDRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
  uid: some-uuid
spec: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uid")
}

// ── US2: Spec-level validation ────────────────────────────────────────────────

func TestParse_OptionMissingNameRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  options:
  - values: [red, blue]
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "options[0].name")
}

func TestParse_DuplicateOptionNamesRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  options:
  - name: color
  - name: color
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "color")
}

func TestParse_ReadOnlyMetadataOwnerReferencesRejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
  ownerReferences: []
spec: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
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
			doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  namespace: gitstore\n  name: my-product\n" +
				tc.yaml + "\nspec: {}\n---\nbody\n"
			_, _, err := parseProduct(strings.NewReader(doc))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
		})
	}
}

// ── US5: Constraint rules + opt-in skip ───────────────────────────────────────

func TestParse_LabelKeyTooLongRejected(t *testing.T) {
	longKey := strings.Repeat("a", 64) // exceeds 63-char segment limit
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  namespace: gitstore\n  name: my-product\n  labels:\n    " +
		longKey + ": value\nspec: {}\n---\nbody\n"
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "label")
}

func TestParse_LabelKeyPrefixTooLongRejected(t *testing.T) {
	longPrefix := strings.Repeat("a", 254) // exceeds 253-char prefix limit
	key := longPrefix + "/name"
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  namespace: gitstore\n  name: my-product\n  labels:\n    \"" +
		key + "\": value\nspec: {}\n---\nbody\n"
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prefix")
}

func TestParse_LabelValueTooLongRejected(t *testing.T) {
	longVal := strings.Repeat("v", 64) // exceeds 63-char value limit
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  namespace: gitstore\n  name: my-product\n  labels:\n    env: " +
		longVal + "\nspec: {}\n---\nbody\n"
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "label value")
}

func TestParse_MultipleViolationsReportedTogether(t *testing.T) {
	// Duplicate options and invalid label value must both appear for a Product.
	longVal := strings.Repeat("v", 64)
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
  labels:
    env: ` + longVal + `
spec:
  options:
  - name: color
  - name: color
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "label value")
}

func TestParse_NoFrontmatterSkipped(t *testing.T) {
	doc := "# README\n\nThis is a plain Markdown file with no frontmatter.\n"
	res, body, err := parseProduct(strings.NewReader(doc))
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.NotEmpty(t, body)
}

// ── US4: Spec field constraints (016-product-spec-hydration) ─────────────────

func TestParse_SpecTitle_TooLong_Rejected(t *testing.T) {
	longTitle := strings.Repeat("x", 201) // 201 chars — exceeds max=200
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  namespace: gitstore\n  name: my-product\nspec:\n  title: \"" +
		longTitle + "\"\n---\nbody\n"
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title")
}

func TestParse_SpecTitle_MaxLength_Accepted(t *testing.T) {
	exactTitle := strings.Repeat("x", 200) // 200 chars — at limit, must pass
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  namespace: gitstore\n  name: my-product\nspec:\n  title: \"" +
		exactTitle + "\"\n---\nbody\n"
	res, _, err := parseProduct(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
}

func TestParse_SpecMedia_EmptyFileRefName_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  media:
    - fileRef:
        name: ""
        kind: "File"
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// go-playground/validator reports the leaf field name; "name" is the field
	// inside FileReference that failed the "required" constraint.
	assert.Contains(t, err.Error(), "name")
}

func TestParse_SpecMedia_EmptyFileRefKind_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  media:
    - fileRef:
        name: "hero-image"
        kind: ""
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// go-playground/validator reports the leaf field name; "kind" is the field
	// inside FileReference that failed the "required" constraint.
	assert.Contains(t, err.Error(), "kind")
}

func TestParse_SpecMedia_ValidFileRef_Accepted(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  title: My Product
  media:
    - fileRef:
        name: "hero-image"
        kind: "File"
---
body
`
	res, _, err := parseProduct(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Spec.Media, 1)
	assert.Equal(t, "hero-image", res.Spec.Media[0].FileRef.Name)
}

// ── T010–T014: US1 spec field constraint tests ────────────────────────────────

func TestParse_SpecTitle_TooLong_FieldNamedInError(t *testing.T) {
	longTitle := strings.Repeat("x", 201)
	doc := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  namespace: gitstore\n  name: my-product\nspec:\n  title: \"" +
		longTitle + "\"\n---\nbody\n"
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// Must name the qualified field path and the limit (FR-001).
	assert.Contains(t, err.Error(), "spec.title")
	assert.Contains(t, err.Error(), "200")
}

func TestParse_SpecMedia_MissingFileRefName_IndexedError(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  media:
    - fileRef:
        kind: "File"
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// Must name the indexed path (FR-002).
	assert.Contains(t, err.Error(), "spec.media[0]")
	assert.Contains(t, err.Error(), "fileref.name")
}

func TestParse_CategoryRef_MissingName_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  categoryRef:
    kind: CategoryTaxonomy
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// Must name the qualified path (FR-005).
	assert.Contains(t, err.Error(), "categoryref.name")
}

func TestParse_OptionsEmptyList_Accepted(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  title: My Product
  options: []
---
body
`
	res, _, err := parseProduct(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Spec.Options)
}

func TestParse_OptionsAbsent_Accepted(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  title: My Product
---
body
`
	res, _, err := parseProduct(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Nil(t, res.Spec.Options)
}

func TestParse_MediaOptionalTrue_NoFileRefName_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  media:
    - fileRef:
        kind: "File"
        optional: true
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// optional: true does NOT waive the name requirement.
	assert.Contains(t, err.Error(), "fileref.name")
}

// ── T020–T021: US2 system-managed field tests ─────────────────────────────────

func TestParse_StatusEmptyMap_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec: {}
status: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// Presence of key, not content, triggers rejection (FR-007).
	assert.Contains(t, err.Error(), "status")
	assert.Contains(t, err.Error(), "system-managed")
}

func TestParse_MultipleReadOnlyFields_AllNamed(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
  uid: some-uuid
  resourceVersion: "1"
  generation: 3
spec: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// All three forbidden fields must appear (FR-008, FR-009).
	assert.Contains(t, err.Error(), "uid")
	assert.Contains(t, err.Error(), "resourceVersion")
	assert.Contains(t, err.Error(), "generation")
}

// ── T006: Multi-error for forbidden metadata fields ───────────────────────────

func TestParse_MultipleReadOnlyFieldsReportedTogether(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
  uid: some-uuid
  resourceVersion: "1"
spec: {}
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// Both forbidden fields must appear in the single error response (FR-008, FR-009).
	assert.Contains(t, err.Error(), "uid")
	assert.Contains(t, err.Error(), "resourceVersion")
}

// ── T007: Full field path in struct-tag error messages ────────────────────────

func TestParse_SpecMedia_FieldPathInError(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  media:
    - fileRef:
        kind: "File"
---
body
`
	_, _, err := parseProduct(strings.NewReader(doc))
	require.Error(t, err)
	// The error must name the full qualified path, not just the leaf "name" (FR-002).
	assert.Contains(t, err.Error(), "spec.media[0]")
	assert.Contains(t, err.Error(), "fileref.name")
}

// ── T018: ParseResource tests (must fail before T017 implementation) ──────────

func TestParseResource_CategoryTaxonomy_ValidAllFields(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore
  name: electronics
spec:
  title: Electronics
---
body
`
	res, body, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.CategoryTaxonomy)
	assert.Equal(t, "CategoryTaxonomy", res.Kind)
	assert.Equal(t, "electronics", res.CategoryTaxonomy.Metadata.Name)
	assert.Equal(t, "Electronics", res.CategoryTaxonomy.Spec.Title)
	assert.NotEmpty(t, body)
}

func TestParseResource_CategoryTaxonomy_MissingTitle(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore
  name: electronics
spec: {}
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.title")
}

func TestParseResource_CategoryTaxonomy_MissingName(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata: {}
spec:
  title: Electronics
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata.name")
}

func TestParseResource_UnknownKind(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: UnknownKind
metadata:
  name: something
spec:
  title: Foo
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UnknownKind")
	assert.Contains(t, err.Error(), "not a recognized")
}

func TestParseResource_Repository(t *testing.T) {
	doc := `---
apiVersion: gitstore.dev/v1beta1
kind: Repository
metadata:
  name: catalog
  namespace: acme
spec:
  defaultBranch: main
  visibility: PRIVATE
  storageClass: standard
---
Repository description.
`
	parsed, body, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, parsed.Repository)
	assert.Equal(t, "catalog", parsed.Repository.Metadata.Name)
	assert.Equal(t, "acme", parsed.Repository.Metadata.Namespace)
	assert.Equal(t, "Repository description.\n", string(body))
}

func TestParseResource_CategoryTaxonomy_SelfReference(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore
  name: electronics
spec:
  title: Electronics
  parentRef:
    name: electronics
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not reference the category itself")
}

func TestParseResource_Product_Regression(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  title: My Product
---
body
`
	res, body, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.Product)
	assert.Equal(t, "Product", res.Kind)
	assert.Equal(t, "my-product", res.Product.Metadata.Name)
	assert.NotEmpty(t, body)
}

// ── T037: CategoryTaxonomy media validation ────────────────────────────────────

func TestParseResource_CategoryTaxonomy_ValidMedia_Accepted(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore
  name: electronics
spec:
  title: Electronics
  media:
    - fileRef:
        name: electronics-hero
        kind: ImageFile
---
body
`
	res, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res.CategoryTaxonomy)
	require.Len(t, res.CategoryTaxonomy.Spec.Media, 1)
	assert.Equal(t, "electronics-hero", res.CategoryTaxonomy.Spec.Media[0].FileRef.Name)
}

func TestParseResource_CategoryTaxonomy_MediaMissingFileRefName_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore
  name: electronics
spec:
  title: Electronics
  media:
    - fileRef:
        kind: ImageFile
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media[0]")
	assert.Contains(t, err.Error(), "name")
}

func TestParseResource_CategoryTaxonomy_MediaMissingFileRefKind_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore
  name: electronics
spec:
  title: Electronics
  media:
    - fileRef:
        name: electronics-hero
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media[0]")
	assert.Contains(t, err.Error(), "kind")
}

func TestParseResource_CategoryTaxonomy_OptionalMediaMissingFile_Accepted(t *testing.T) {
	// optional:true means the File resource need not exist at push time.
	// Push-time validation only checks struct fields, not File existence.
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore
  name: electronics
spec:
  title: Electronics
  media:
    - fileRef:
        name: optional-hero
        kind: ImageFile
        optional: true
---
body
`
	res, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res.CategoryTaxonomy)
	assert.True(t, res.CategoryTaxonomy.Spec.Media[0].FileRef.Optional)
}

// ── T034: Product single-category constraint ───────────────────────────────────

func TestParseResource_Product_SingleCategoryRef_Accepted(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  title: My Product
  categoryRef:
    name: electronics
    kind: CategoryTaxonomy
---
body
`
	res, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res.Product)
	require.NotNil(t, res.Product.Spec.CategoryRef)
	assert.Equal(t, "electronics", res.Product.Spec.CategoryRef.Name)
}

func TestParseResource_Product_NoCategoryRef_Accepted(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  title: My Product
---
body
`
	res, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, res.Product)
	assert.Nil(t, res.Product.Spec.CategoryRef)
}

func TestParseResource_Product_CategoryRefArray_Rejected(t *testing.T) {
	// YAML sequence under categoryRef cannot unmarshal into *ObjectReference.
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  categoryRef:
    - name: electronics
    - name: computers
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
}

func TestParseResource_Product_CategoryRefPresentButEmptyName_Rejected(t *testing.T) {
	doc := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore
  name: my-product
spec:
  categoryRef:
    kind: CategoryTaxonomy
---
body
`
	_, _, err := validate.NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "categoryref.name")
}
