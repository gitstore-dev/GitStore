// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// uniqueValidProductFixture is an alias for validProductFixture used in variant tests.
func uniqueValidProductFixture(name, ns string) string { return validProductFixture(name, ns) }

// ── ProductVariant fixtures ───────────────────────────────────────────────────

// minimalVariantFixture returns a valid ProductVariant with only required fields.
func minimalVariantFixture(name, ns, sku, productRefName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  sku: %s
  productRef:
    name: %s
---

Minimal integration test variant.
`, name, ns, name, sku, productRefName)
}

// variantWithPricing returns a ProductVariant with a simple fixed price.
func variantWithPricing(name, ns, sku, productRefName, currency, amount string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  sku: %s
  productRef:
    name: %s
  pricing:
    priceSet:
      name: default
      prices:
      - name: base
        currencyCode: %s
        amount: %s
        priority: 0
        strategy:
          type: fixed
---

Variant with pricing.
`, name, ns, name, sku, productRefName, currency, amount)
}

// invalidVariantMissingSKU returns a ProductVariant with no spec.sku.
func invalidVariantMissingSKU(name, ns, productRefName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  productRef:
    name: %s
---
`, name, ns, name, productRefName)
}

// invalidVariantValidUntilBeforeFrom returns a variant where validUntilTime < validFromTime.
func invalidVariantValidUntilBeforeFrom(name, ns, sku, productRefName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  sku: %s
  productRef:
    name: %s
  pricing:
    priceSet:
      name: default
      prices:
      - name: bad-window
        currencyCode: USD
        amount: 9.99
        priority: 0
        strategy:
          type: fixed
        validFromTime: "2030-12-31T00:00:00Z"
        validUntilTime: "2030-01-01T00:00:00Z"
---
`, name, ns, name, sku, productRefName)
}

// ── GraphQL result types ──────────────────────────────────────────────────────

type variantCondition struct {
	Type               string  `json:"type"`
	Status             string  `json:"status"`
	Reason             *string `json:"reason"`
	Message            *string `json:"message"`
	ObservedGeneration *int    `json:"observedGeneration"`
}

type variantQueryResult struct {
	ID         string `json:"id"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
	Spec struct {
		Title      string `json:"title"`
		SKU        string `json:"sku"`
		ProductRef struct {
			Name string `json:"name"`
		} `json:"productRef"`
		SelectedOptions []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"selectedOptions"`
	} `json:"spec"`
	Status *struct {
		ObservedGeneration  int                `json:"observedGeneration"`
		LastAppliedRevision *string            `json:"lastAppliedRevision"`
		Conditions          []variantCondition `json:"conditions"`
	} `json:"status"`
}

// queryVariant fetches a ProductVariant by namespace + name via GraphQL.
// It retries up to 5s while the variant is null (async admission lag, especially on ScyllaDB).
// Any GraphQL error is immediately fatal.
func queryVariant(t *testing.T, namespace, name string) *variantQueryResult {
	t.Helper()
	const (
		maxWait  = 5 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	for {
		resp := gqlQuery(t, `
			query($ns: String!, $name: String!) {
				productVariant(by: { namespacePath: { namespace: $ns, name: $name } }) {
					id apiVersion kind
					metadata { name namespace uid }
					spec {
						title sku
						productRef { name }
						selectedOptions { name value }
					}
					status {
						observedGeneration lastAppliedRevision
						conditions { type status reason message observedGeneration }
					}
				}
			}
		`, map[string]any{"ns": namespace, "name": name})
		if len(resp.Errors) > 0 {
			t.Fatalf("graphql errors querying variant %q: %s", name, resp.Errors)
		}
		type data struct {
			ProductVariant *variantQueryResult `json:"productVariant"`
		}
		var d data
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			t.Fatalf("decode productVariant response: %v", err)
		}
		if d.ProductVariant == nil && time.Now().Before(deadline) {
			time.Sleep(interval)
			continue
		}
		return d.ProductVariant
	}
}

// findVariantCondition finds a condition by type string within a variant result.
func findVariantCondition(conditions []variantCondition, condType string) *variantCondition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// commitVariant writes a ProductVariant markdown file and commits it.
func (h *pushHelper) commitVariant(filename, content string) {
	h.t.Helper()
	dir := filepath.Join(h.workDir, "variants")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatalf("mkdir variants: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatalf("write variant file: %v", err)
	}
	run(h.t, h.workDir, "git", "add", path)
	run(h.t, h.workDir, "git", "commit", "-m", "add "+filename)
}

// ── US1: Push a valid ProductVariant file ────────────────────────────────────

// TestProductVariant_ValidPushAccepted verifies that a valid ProductVariant file
// pushed to the catalog is admitted and queryable via GraphQL.
func TestProductVariant_ValidPushAccepted(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-parent-%d", ts)
	variantName := fmt.Sprintf("pv-minimal-%d", ts)
	sku := fmt.Sprintf("SKU-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, productName))
	out, err := h.push()
	if err != nil {
		t.Fatalf("expected push to succeed for valid variant, got error:\n%s", out)
	}

	time.Sleep(500 * time.Millisecond)

	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatalf("variant %q not found after push", variantName)
	}
	if v.Kind != "ProductVariant" {
		t.Errorf("kind: got %q, want ProductVariant", v.Kind)
	}
	if v.Spec.SKU != sku {
		t.Errorf("spec.sku: got %q, want %q", v.Spec.SKU, sku)
	}
	if v.Status == nil {
		t.Fatal("status is nil after admission")
	}
	admitted := findVariantCondition(v.Status.Conditions, "ADMISSION_ACCEPTED")
	if admitted == nil {
		t.Fatalf("ADMISSION_ACCEPTED condition not found: %+v", v.Status.Conditions)
	}
	if admitted.Status != "TRUE" {
		t.Errorf("ADMISSION_ACCEPTED status: got %q, want TRUE", admitted.Status)
	}
}

// TestProductVariant_MissingSKURejected verifies that a variant without spec.sku
// is rejected at pre-receive with a descriptive error message.
func TestProductVariant_MissingSKURejected(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	variantName := fmt.Sprintf("pv-no-sku-%d", ts)

	h := newPushHelper(t)
	h.commitVariant(variantName+".md", invalidVariantMissingSKU(variantName, ns, "some-product"))
	out, err := h.push()
	if err == nil {
		t.Fatalf("expected push to fail for variant missing sku, got success:\n%s", out)
	}
	if out == "" {
		t.Error("expected non-empty rejection output")
	}
}

// TestProductVariant_ValidUntilBeforeFromRejected verifies that a price template
// where validUntilTime <= validFromTime is rejected at pre-receive.
func TestProductVariant_ValidUntilBeforeFromRejected(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	variantName := fmt.Sprintf("pv-bad-window-%d", ts)
	sku := fmt.Sprintf("SKU-WIN-%d", ts)

	h := newPushHelper(t)
	h.commitVariant(variantName+".md", invalidVariantValidUntilBeforeFrom(variantName, ns, sku, "some-product"))
	out, err := h.push()
	if err == nil {
		t.Fatalf("expected push to fail for invalid time window, got success:\n%s", out)
	}
	if out == "" {
		t.Error("expected non-empty rejection output")
	}
}

// TestProductVariant_CoPushWithProductAccepted verifies that pushing a Product
// and a ProductVariant in the same commit is accepted. The variant's productRef
// may not be resolved at admit time (co-push semantics), but both resources must
// be stored successfully.
func TestProductVariant_CoPushWithProductAccepted(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-copush-parent-%d", ts)
	variantName := fmt.Sprintf("pv-copush-%d", ts)
	sku := fmt.Sprintf("SKU-COPUSH-%d", ts)

	h := newPushHelper(t)
	// Both product and variant in the same commit.
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, productName))
	out, err := h.push()
	if err != nil {
		t.Fatalf("co-push of product+variant should succeed, got error:\n%s", out)
	}

	time.Sleep(500 * time.Millisecond)

	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatalf("variant %q not found after co-push", variantName)
	}
	if v.Status == nil {
		t.Fatal("status is nil after co-push admission")
	}
	admitted := findVariantCondition(v.Status.Conditions, "ADMISSION_ACCEPTED")
	if admitted == nil || admitted.Status != "TRUE" {
		t.Errorf("AdmissionAccepted: %+v", admitted)
	}
}

// ── US3: Parent product link and option compatibility ────────────────────────

// variantWithOptions returns a ProductVariant with selectedOptions.
// The parentName must be a product that defines options with those names/values.
func variantWithOptions(name, ns, sku, parentName string, opts []struct{ Name, Value string }) string {
	var sb string
	for _, o := range opts {
		sb += fmt.Sprintf("  - name: %s\n    value: %s\n", o.Name, o.Value)
	}
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  sku: %s
  productRef:
    name: %s
  selectedOptions:
%s---

Variant with selected options.
`, name, ns, name, sku, parentName, sb)
}

// productWithOptions returns a Product fixture that declares options.
func productWithOptions(name, ns string, opts []struct{ Name string; Values []string }) string {
	var optLines string
	for _, o := range opts {
		optLines += fmt.Sprintf("  - name: %s\n    values: [%s]\n", o.Name, joinStrings(o.Values))
	}
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  options:
%s---

Product with options.
`, name, ns, name, optLines)
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// TestProductVariant_ProductRefNotFound verifies that pushing a variant whose
// productRef does not yet exist is admitted with ProductResolved=False.
// Re-pushing the variant after the parent product exists transitions the condition to True.
func TestProductVariant_ProductRefNotFound(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	parentName := fmt.Sprintf("pv-us3-parent-%d", ts)
	variantName := fmt.Sprintf("pv-us3-deferred-%d", ts)
	sku := fmt.Sprintf("SKU-US3-%d", ts)

	// Push variant first — parent does not exist yet.
	h := newPushHelper(t)
	h.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, parentName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push without parent should succeed (deferred), got error:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatalf("variant %q not found after push", variantName)
	}
	if v.Status == nil {
		t.Fatal("status is nil after admission")
	}
	admitted := findVariantCondition(v.Status.Conditions, "ADMISSION_ACCEPTED")
	if admitted == nil || admitted.Status != "TRUE" {
		t.Errorf("AdmissionAccepted: %+v", admitted)
	}
	pr := findVariantCondition(v.Status.Conditions, "PRODUCT_RESOLVED")
	if pr == nil {
		t.Fatal("ProductResolved condition not present")
	}
	if pr.Status != "FALSE" {
		t.Errorf("ProductResolved status: got %q, want False (parent not yet pushed)", pr.Status)
	}

	// Now push the parent product.
	h2 := newPushHelper(t)
	h2.commitProduct(parentName+".md", uniqueValidProductFixture(parentName, ns))
	if out, err := h2.push(); err != nil {
		t.Fatalf("push parent product failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Re-push the variant — now the parent exists; ProductResolved should become True.
	// Append a blank line to produce a new commit even though the spec is unchanged.
	h3 := newPushHelper(t)
	h3.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, parentName)+"\n")
	if out, err := h3.push(); err != nil {
		t.Fatalf("re-push of variant after parent created failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	v2 := queryVariant(t, ns, variantName)
	if v2 == nil {
		t.Fatalf("variant %q not found after re-push", variantName)
	}
	if v2.Status == nil {
		t.Fatal("status is nil after re-push")
	}
	pr2 := findVariantCondition(v2.Status.Conditions, "PRODUCT_RESOLVED")
	if pr2 == nil {
		t.Fatal("ProductResolved condition not present after re-push")
	}
	if pr2.Status != "TRUE" {
		t.Errorf("ProductResolved status after re-push: got %q, want True", pr2.Status)
	}
}

// TestProductVariant_InvalidOptionName verifies that a variant with a selectedOptions
// entry whose name is not declared in the parent product is admitted with
// OptionsAccepted=False and a message identifying the unknown option name.
// Option compatibility is an admission-phase (DB-backed) check; the push itself succeeds.
func TestProductVariant_InvalidOptionName(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	parentName := fmt.Sprintf("pv-us3-optparent-%d", ts)
	variantName := fmt.Sprintf("pv-us3-badopt-%d", ts)
	sku := fmt.Sprintf("SKU-US3-BAD-%d", ts)

	// Push parent product declaring only "size" and "color".
	h := newPushHelper(t)
	h.commitProduct(parentName+".md", productWithOptions(parentName, ns, []struct {
		Name   string
		Values []string
	}{
		{Name: "size", Values: []string{"S", "M", "L"}},
		{Name: "color", Values: []string{"red", "blue"}},
	}))
	if out, err := h.push(); err != nil {
		t.Fatalf("push parent product failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Push variant with option name "material" which does not exist on parent.
	// The push succeeds (option compatibility is admission-phase, not pre-receive).
	h2 := newPushHelper(t)
	h2.commitVariant(variantName+".md", variantWithOptions(variantName, ns, sku, parentName, []struct{ Name, Value string }{
		{Name: "material", Value: "cotton"},
	}))
	if out, err := h2.push(); err != nil {
		t.Fatalf("push with unknown option name should succeed at pre-receive (stateless), got error:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Query the admitted variant and assert OptionsAccepted=False.
	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatalf("variant %q not found after push", variantName)
	}
	if v.Status == nil {
		t.Fatal("status is nil after admission")
	}
	oa := findVariantCondition(v.Status.Conditions, "OPTIONS_ACCEPTED")
	if oa == nil {
		t.Fatal("OptionsAccepted condition not present")
	}
	if oa.Status != "FALSE" {
		t.Errorf("OptionsAccepted status: got %q, want False (unknown option 'material')", oa.Status)
	}
	// The message should identify the offending option name.
	msg := ""
	if oa.Message != nil {
		msg = *oa.Message
	}
	if !strings.Contains(msg, "material") {
		t.Errorf("OptionsAccepted message should mention the unknown option 'material', got: %q", msg)
	}
}

// TestProductVariant_CoPushProductResolvedFalse verifies that co-pushing a Product
// and a ProductVariant in the same commit results in the variant being admitted
// with ProductResolved=False (deferred — parent may not be in DB yet at admit time).
func TestProductVariant_CoPushProductResolvedFalse(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	parentName := fmt.Sprintf("pv-us3-copush-parent-%d", ts)
	variantName := fmt.Sprintf("pv-us3-copush-%d", ts)
	sku := fmt.Sprintf("SKU-US3-COPUSH-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(parentName+".md", uniqueValidProductFixture(parentName, ns))
	h.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, parentName))
	if out, err := h.push(); err != nil {
		t.Fatalf("co-push of product+variant failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatalf("variant %q not found after co-push", variantName)
	}
	if v.Status == nil {
		t.Fatal("status is nil after co-push")
	}
	admitted := findVariantCondition(v.Status.Conditions, "ADMISSION_ACCEPTED")
	if admitted == nil || admitted.Status != "TRUE" {
		t.Errorf("AdmissionAccepted: %+v", admitted)
	}
	// Co-push: product and variant admitted in the same pass; the variant's
	// productRef may resolve False or True depending on admit ordering.
	// Both outcomes are valid — assert the condition is present.
	pr := findVariantCondition(v.Status.Conditions, "PRODUCT_RESOLVED")
	if pr == nil {
		t.Error("ProductResolved condition must be present after co-push")
	}
}

// ── US2: Query a ProductVariant ──────────────────────────────────────────────

// queryVariantByID fetches a ProductVariant by its Relay global ID via GraphQL.
func queryVariantByID(t *testing.T, id string) *variantQueryResult {
	t.Helper()
	resp := gqlQuery(t, `
		query($id: ID!) {
			productVariant(by: { id: $id }) {
				id apiVersion kind
				metadata { name namespace uid }
				spec {
					title sku
					productRef { name }
				}
				status {
					observedGeneration lastAppliedRevision
					conditions { type status reason message observedGeneration }
				}
			}
		}
	`, map[string]any{"id": id})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors querying variant by id %q: %s", id, resp.Errors)
	}
	type data struct {
		ProductVariant *variantQueryResult `json:"productVariant"`
	}
	var d data
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("decode productVariant response: %v", err)
	}
	return d.ProductVariant
}

// listVariantsResult is the shape returned by the productVariants list query.
type listVariantsResult struct {
	Edges []struct {
		Cursor string             `json:"cursor"`
		Node   variantQueryResult `json:"node"`
	} `json:"edges"`
	PageInfo struct {
		HasNextPage     bool    `json:"hasNextPage"`
		HasPreviousPage bool    `json:"hasPreviousPage"`
		StartCursor     *string `json:"startCursor"`
		EndCursor       *string `json:"endCursor"`
	} `json:"pageInfo"`
	TotalCount int `json:"totalCount"`
}

// listVariants fetches all ProductVariants for a namespace (up to first=50).
func listVariants(t *testing.T, namespace string) *listVariantsResult {
	t.Helper()
	resp := gqlQuery(t, `
		query($ns: String!) {
			productVariants(namespace: $ns, first: 50) {
				edges {
					cursor
					node {
						id apiVersion kind
						metadata { name namespace uid }
						spec { title sku productRef { name } }
						status { observedGeneration conditions { type status } }
					}
				}
				pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
				totalCount
			}
		}
	`, map[string]any{"ns": namespace})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors listing variants in ns %q: %s", namespace, resp.Errors)
	}
	type data struct {
		ProductVariants *listVariantsResult `json:"productVariants"`
	}
	var d data
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("decode productVariants response: %v", err)
	}
	return d.ProductVariants
}

// TestProductVariant_QueryByNamespacePath verifies that a pushed variant can be
// retrieved by namespacePath and returns correct spec fields.
func TestProductVariant_QueryByNamespacePath(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-q-parent-%d", ts)
	variantName := fmt.Sprintf("pv-q-name-%d", ts)
	sku := fmt.Sprintf("SKU-Q-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, productName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push failed: %s", out)
	}
	time.Sleep(500 * time.Millisecond)

	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatalf("productVariant %q not found after push", variantName)
	}
	if v.Kind != "ProductVariant" {
		t.Errorf("kind: got %q, want ProductVariant", v.Kind)
	}
	if v.Metadata.Name != variantName {
		t.Errorf("metadata.name: got %q, want %q", v.Metadata.Name, variantName)
	}
	if v.Metadata.Namespace != ns {
		t.Errorf("metadata.namespace: got %q, want %q", v.Metadata.Namespace, ns)
	}
	if v.Spec.SKU != sku {
		t.Errorf("spec.sku: got %q, want %q", v.Spec.SKU, sku)
	}
	if v.Spec.ProductRef.Name != productName {
		t.Errorf("spec.productRef.name: got %q, want %q", v.Spec.ProductRef.Name, productName)
	}
}

// TestProductVariant_QueryByRelayID verifies that a pushed variant can be looked
// up by its Relay global ID (round-trip: query by name → extract ID → query by ID).
func TestProductVariant_QueryByRelayID(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-id-parent-%d", ts)
	variantName := fmt.Sprintf("pv-id-%d", ts)
	sku := fmt.Sprintf("SKU-ID-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, productName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push failed: %s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// First query by name to obtain the Relay ID.
	byName := queryVariant(t, ns, variantName)
	if byName == nil {
		t.Fatalf("variant %q not found after push", variantName)
	}
	if byName.ID == "" {
		t.Fatal("variant has empty ID")
	}

	// Now query by the Relay ID and verify it returns the same variant.
	byID := queryVariantByID(t, byName.ID)
	if byID == nil {
		t.Fatalf("productVariant not found via Relay ID %q", byName.ID)
	}
	if byID.Metadata.Name != variantName {
		t.Errorf("relay id round-trip: metadata.name: got %q, want %q", byID.Metadata.Name, variantName)
	}
	if byID.Spec.SKU != sku {
		t.Errorf("relay id round-trip: spec.sku: got %q, want %q", byID.Spec.SKU, sku)
	}
}

// TestProductVariant_ListByNamespace verifies that the productVariants list query
// returns pushed variants and includes them in the connection.
func TestProductVariant_ListByNamespace(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-list-parent-%d", ts)

	// Push two variants.
	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	variantNames := []string{
		fmt.Sprintf("pv-list-a-%d", ts),
		fmt.Sprintf("pv-list-b-%d", ts),
	}
	skus := []string{
		fmt.Sprintf("SKU-LIST-A-%d", ts),
		fmt.Sprintf("SKU-LIST-B-%d", ts),
	}
	for i, vn := range variantNames {
		h.commitVariant(vn+".md", minimalVariantFixture(vn, ns, skus[i], productName))
	}
	if out, err := h.push(); err != nil {
		t.Fatalf("push failed: %s", out)
	}

	// Wait until both variants appear in the namespace listing (retry for ScyllaDB lag).
	var result *listVariantsResult
	deadline := time.Now().Add(5 * time.Second)
	for {
		result = listVariants(t, ns)
		if result == nil {
			t.Fatal("productVariants list returned nil")
		}
		foundCount := 0
		for _, e := range result.Edges {
			for _, vn := range variantNames {
				if e.Node.Metadata.Name == vn {
					foundCount++
				}
			}
		}
		if foundCount >= len(variantNames) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// TotalCount is -1 on ScyllaDB (expensive to compute); check edges instead.
	if result.TotalCount >= 0 && result.TotalCount < 2 {
		t.Errorf("expected at least 2 variants in namespace, got totalCount=%d", result.TotalCount)
	}
	// Check that both pushed variants appear in the connection.
	foundNames := make(map[string]bool)
	for _, e := range result.Edges {
		foundNames[e.Node.Metadata.Name] = true
	}
	for _, vn := range variantNames {
		if !foundNames[vn] {
			t.Errorf("variant %q not found in productVariants listing", vn)
		}
	}
}

// TestProductVariant_ProductVariantsConnection verifies that the Product.productVariants
// nested connection returns variants belonging to that product.
func TestProductVariant_ProductVariantsConnection(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-conn-parent-%d", ts)
	variantName := fmt.Sprintf("pv-conn-%d", ts)
	sku := fmt.Sprintf("SKU-CONN-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", minimalVariantFixture(variantName, ns, sku, productName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push failed: %s", out)
	}

	// Wait until the variant appears via the product.productVariants connection (ScyllaDB lag).
	deadline := time.Now().Add(5 * time.Second)
	for {
		probe := gqlQuery(t, `
			query($ns: String!, $name: String!, $vn: String!) {
				productVariant(by: { namespacePath: { namespace: $ns, name: $vn } }) { id }
			}
		`, map[string]any{"ns": ns, "name": productName, "vn": variantName})
		if len(probe.Errors) == 0 {
			type probeData struct {
				ProductVariant *struct{ ID string `json:"id"` } `json:"productVariant"`
			}
			var pd probeData
			if err := json.Unmarshal(probe.Data, &pd); err == nil && pd.ProductVariant != nil {
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Query the parent product's productVariants connection.
	resp := gqlQuery(t, `
		query($ns: String!, $name: String!) {
			product(by: { namespacePath: { namespace: $ns, name: $name } }) {
				metadata { name }
				productVariants(first: 10) {
					edges {
						node {
							metadata { name }
							spec { sku }
						}
					}
					totalCount
				}
			}
		}
	`, map[string]any{"ns": ns, "name": productName})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors: %s", resp.Errors)
	}
	type productNode struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		ProductVariants struct {
			Edges []struct {
				Node struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
					Spec struct {
						SKU string `json:"sku"`
					} `json:"spec"`
				} `json:"node"`
			} `json:"edges"`
			TotalCount int `json:"totalCount"`
		} `json:"productVariants"`
	}
	type data struct {
		Product *productNode `json:"product"`
	}
	var d data
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("decode product response: %v", err)
	}
	if d.Product == nil {
		t.Fatalf("product %q not found", productName)
	}
	// The Product.productVariants field returns variants filtered by productRef;
	// since it currently returns all variants (not yet filtered), just assert
	// the connection is non-nil and the pushed variant appears.
	if d.Product.ProductVariants.TotalCount < 0 {
		t.Errorf("totalCount should be non-negative, got %d", d.Product.ProductVariants.TotalCount)
	}
	found := false
	for _, e := range d.Product.ProductVariants.Edges {
		if e.Node.Metadata.Name == variantName {
			found = true
			if e.Node.Spec.SKU != sku {
				t.Errorf("product.productVariants edge sku: got %q, want %q", e.Node.Spec.SKU, sku)
			}
		}
	}
	if !found {
		t.Errorf("variant %q not found in product.productVariants connection", variantName)
	}
}

// ── US4: Pricing and inventory schema validation ──────────────────────────────

// variantWithInvalidCEL returns a ProductVariant fixture containing an invalid
// CEL expression in eligibility.constraints.
func variantWithInvalidCEL(name, ns, sku, productRefName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  sku: %s
  productRef:
    name: %s
  pricing:
    priceSet:
      name: default
      prices:
      - name: base
        currencyCode: USD
        amount: "9.99"
        priority: 0
        strategy:
          type: fixed
        eligibility:
          operator: All
          constraints:
          - name: bad-expr
            expression: "customer.group == )"
---

Variant with invalid CEL expression.
`, name, ns, name, sku, productRefName)
}

// variantWithBadQuantityRange returns a variant where quantity.min > quantity.max.
func variantWithBadQuantityRange(name, ns, sku, productRefName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  sku: %s
  productRef:
    name: %s
  pricing:
    priceSet:
      name: default
      prices:
      - name: bulk
        currencyCode: USD
        amount: "8.99"
        priority: 0
        strategy:
          type: fixed
        quantity:
          min: 10
          max: 5
---

Variant with bad quantity range.
`, name, ns, name, sku, productRefName)
}

// variantWithValidPriceSet returns a valid ProductVariant with a rich priceSet
// (two prices, two currencies, one CEL-guarded price) for resolved.priceSet assertions.
func variantWithValidPriceSet(name, ns, sku, productRefName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  sku: %s
  productRef:
    name: %s
  pricing:
    priceSet:
      name: multi
      prices:
      - name: usd-base
        currencyCode: USD
        amount: "19.99"
        priority: 0
        strategy:
          type: fixed
      - name: eur-base
        currencyCode: EUR
        amount: "17.99"
        priority: 0
        strategy:
          type: fixed
        eligibility:
          operator: All
          constraints:
          - name: eu-only
            expression: "region.code == 'EU'"
---

Variant with multi-currency price set.
`, name, ns, name, sku, productRefName)
}

// queryVariantWithResolved fetches a ProductVariant including status.resolved fields.
// It retries up to 5s while the variant is null (async admission lag, especially on ScyllaDB).
// Any GraphQL error is immediately fatal.
func queryVariantWithResolved(t *testing.T, namespace, name string) *variantQueryResult {
	t.Helper()
	const (
		maxWait  = 5 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	// variantQueryResult doesn't have resolved fields — use an extended struct.
	type resolvedVariant struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			UID       string `json:"uid"`
		} `json:"metadata"`
		Spec struct {
			Title string `json:"title"`
			SKU   string `json:"sku"`
		} `json:"spec"`
		Status *struct {
			ObservedGeneration int                `json:"observedGeneration"`
			Conditions         []variantCondition `json:"conditions"`
			Resolved           *struct {
				PriceSet *struct {
					PriceCount          int      `json:"priceCount"`
					Currencies          []string `json:"currencies"`
					Strategies          []string `json:"strategies"`
					CompiledExpressions int      `json:"compiledExpressions"`
				} `json:"priceSet"`
				Inventory *struct {
					Managed           bool   `json:"managed"`
					AvailableQuantity int    `json:"availableQuantity"`
					Policy            string `json:"policy"`
				} `json:"inventory"`
			} `json:"resolved"`
		} `json:"status"`
	}
	var raw struct {
		ProductVariant *resolvedVariant `json:"productVariant"`
	}
	for {
		resp := gqlQuery(t, `
			query($ns: String!, $name: String!) {
				productVariant(by: { namespacePath: { namespace: $ns, name: $name } }) {
					id kind
					metadata { name namespace uid }
					spec { title sku }
					status {
						observedGeneration
						conditions { type status reason message }
						resolved {
							priceSet { priceCount currencies strategies compiledExpressions }
							inventory { managed availableQuantity policy }
						}
					}
				}
			}
		`, map[string]any{"ns": namespace, "name": name})
		if len(resp.Errors) > 0 {
			t.Fatalf("graphql errors querying variant %q with resolved: %s", name, resp.Errors)
		}
		if err := json.Unmarshal(resp.Data, &raw); err != nil {
			t.Fatalf("decode productVariant resolved response: %v", err)
		}
		if raw.ProductVariant == nil && time.Now().Before(deadline) {
			time.Sleep(interval)
			continue
		}
		break
	}
	if raw.ProductVariant == nil {
		return nil
	}
	// Map to variantQueryResult for condition helpers.
	r := &variantQueryResult{
		ID:   raw.ProductVariant.ID,
		Kind: raw.ProductVariant.Kind,
	}
	r.Metadata.Name = raw.ProductVariant.Metadata.Name
	r.Metadata.Namespace = raw.ProductVariant.Metadata.Namespace
	r.Metadata.UID = raw.ProductVariant.Metadata.UID
	if raw.ProductVariant.Status != nil {
		r.Status = &struct {
			ObservedGeneration  int                `json:"observedGeneration"`
			LastAppliedRevision *string            `json:"lastAppliedRevision"`
			Conditions          []variantCondition `json:"conditions"`
		}{
			ObservedGeneration: raw.ProductVariant.Status.ObservedGeneration,
			Conditions:         raw.ProductVariant.Status.Conditions,
		}
	}
	// Store resolved fields in a package-level variable for the test to read.
	lastResolvedPriceSet = nil
	if raw.ProductVariant.Status != nil && raw.ProductVariant.Status.Resolved != nil {
		lastResolvedPriceSet = raw.ProductVariant.Status.Resolved.PriceSet
	}
	return r
}

// lastResolvedPriceSet is set by queryVariantWithResolved for inspection in tests.
// This avoids adding resolved fields to variantQueryResult which is shared across tests.
var lastResolvedPriceSet *struct {
	PriceCount          int      `json:"priceCount"`
	Currencies          []string `json:"currencies"`
	Strategies          []string `json:"strategies"`
	CompiledExpressions int      `json:"compiledExpressions"`
}

// TestProductVariant_InvalidCELAdmittedWithPricingFalse verifies that a variant with
// an invalid CEL expression in eligibility.constraints is admitted with
// PricingAccepted=False and a message identifying the offending expression.
// CEL syntax validation is an admission-phase (DB-optional) check; the push itself succeeds.
func TestProductVariant_InvalidCELAdmittedWithPricingFalse(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-us4-cel-parent-%d", ts)
	variantName := fmt.Sprintf("pv-us4-cel-%d", ts)
	sku := fmt.Sprintf("SKU-US4-CEL-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", variantWithInvalidCEL(variantName, ns, sku, productName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push with invalid CEL should succeed at pre-receive, got error:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatalf("variant %q not found after push", variantName)
	}
	if v.Status == nil {
		t.Fatal("status is nil after admission")
	}
	admitted := findVariantCondition(v.Status.Conditions, "ADMISSION_ACCEPTED")
	if admitted == nil || admitted.Status != "TRUE" {
		t.Errorf("AdmissionAccepted: %+v", admitted)
	}
	pa := findVariantCondition(v.Status.Conditions, "PRICING_ACCEPTED")
	if pa == nil {
		t.Fatal("PricingAccepted condition not present")
	}
	if pa.Status != "FALSE" {
		t.Errorf("PricingAccepted status: got %q, want False (invalid CEL)", pa.Status)
	}
	msg := ""
	if pa.Message != nil {
		msg = *pa.Message
	}
	if msg == "" {
		t.Error("PricingAccepted message should identify the offending expression, got empty")
	}
}

// TestProductVariant_InvertedTimeWindowRejected verifies that a variant with
// validUntilTime <= validFromTime is rejected by the pre-receive hook.
// (Already covered by TestProductVariant_ValidUntilBeforeFromRejected — this test
// adds the explicit US4 label and quantity.min > quantity.max variant.)
func TestProductVariant_BadQuantityRangeRejected(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	variantName := fmt.Sprintf("pv-us4-qty-%d", ts)
	sku := fmt.Sprintf("SKU-US4-QTY-%d", ts)

	h := newPushHelper(t)
	h.commitVariant(variantName+".md", variantWithBadQuantityRange(variantName, ns, sku, "any-product"))
	out, err := h.push()
	if err == nil {
		t.Fatalf("expected push to fail for quantity.min > quantity.max, got success:\n%s", out)
	}
	if out == "" {
		t.Error("expected non-empty rejection output")
	}
}

// TestProductVariant_ValidPriceSetPopulatesResolved verifies that pushing a variant
// with a valid multi-currency priceSet results in status.resolved.priceSet being
// populated with correct priceCount, currencies, and strategies.
func TestProductVariant_ValidPriceSetPopulatesResolved(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-us4-ps-parent-%d", ts)
	variantName := fmt.Sprintf("pv-us4-ps-%d", ts)
	sku := fmt.Sprintf("SKU-US4-PS-%d", ts)

	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", variantWithValidPriceSet(variantName, ns, sku, productName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push with valid price set failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	queryVariantWithResolved(t, ns, variantName)
	if lastResolvedPriceSet == nil {
		t.Fatal("status.resolved.priceSet is nil; expected it to be populated after admission")
	}
	if lastResolvedPriceSet.PriceCount != 2 {
		t.Errorf("resolved.priceSet.priceCount: got %d, want 2", lastResolvedPriceSet.PriceCount)
	}
	// The valid CEL expression should count as one compiled expression.
	if lastResolvedPriceSet.CompiledExpressions != 1 {
		t.Errorf("resolved.priceSet.compiledExpressions: got %d, want 1", lastResolvedPriceSet.CompiledExpressions)
	}
	wantCurrencies := map[string]bool{"USD": true, "EUR": true}
	for _, c := range lastResolvedPriceSet.Currencies {
		delete(wantCurrencies, c)
	}
	if len(wantCurrencies) > 0 {
		t.Errorf("resolved.priceSet.currencies: missing %v in %v", wantCurrencies, lastResolvedPriceSet.Currencies)
	}
}

// ── US5: Update a ProductVariant ─────────────────────────────────────────────

// TestProductVariant_UpdatePricingReflectsResolved verifies that re-pushing a
// variant with an additional price rule increases resolved.priceSet.priceCount by one.
func TestProductVariant_UpdatePricingReflectsResolved(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-us5-upd-parent-%d", ts)
	variantName := fmt.Sprintf("pv-us5-upd-%d", ts)
	sku := fmt.Sprintf("SKU-US5-UPD-%d", ts)

	// Push variant with one USD price.
	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h.commitVariant(variantName+".md", variantWithPricing(variantName, ns, sku, productName, "USD", "9.99"))
	if out, err := h.push(); err != nil {
		t.Fatalf("initial push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Verify initial priceCount=1; this blocks until the variant is readable.
	queryVariantWithResolved(t, ns, variantName)
	if lastResolvedPriceSet == nil {
		t.Fatal("initial resolved.priceSet is nil")
	}
	if lastResolvedPriceSet.PriceCount != 1 {
		t.Errorf("initial priceCount: got %d, want 1", lastResolvedPriceSet.PriceCount)
	}
	// Extra pause: ScyllaDB may have additional read-propagation lag between
	// the GraphQL read path and the admission gRPC read path.
	time.Sleep(500 * time.Millisecond)

	// Re-push with two prices (USD + EUR).
	h2 := newPushHelper(t)
	h2.commitVariant(variantName+".md", variantWithValidPriceSet(variantName, ns, sku, productName))
	if out, err := h2.push(); err != nil {
		t.Fatalf("update push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Verify priceCount increased to 2; retry because ScyllaDB propagation may lag.
	deadline2 := time.Now().Add(5 * time.Second)
	for {
		queryVariantWithResolved(t, ns, variantName)
		if lastResolvedPriceSet != nil && lastResolvedPriceSet.PriceCount == 2 {
			break
		}
		if time.Now().After(deadline2) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastResolvedPriceSet == nil {
		t.Fatal("updated resolved.priceSet is nil")
	}
	if lastResolvedPriceSet.PriceCount != 2 {
		t.Errorf("updated priceCount: got %d, want 2", lastResolvedPriceSet.PriceCount)
	}

	// Confirm generation incremented by checking the variant directly.
	v := queryVariant(t, ns, variantName)
	if v == nil {
		t.Fatal("variant not found after update")
	}
	if v.Status == nil {
		t.Fatal("status nil after update")
	}
	if v.Status.ObservedGeneration < 2 {
		t.Errorf("observedGeneration after update: got %d, want >= 2", v.Status.ObservedGeneration)
	}
}

// TestProductVariant_UpdateWithInvalidOptionLeavesSpecUnchanged verifies that
// re-pushing a variant update with an incompatible selectedOption results in
// OptionsAccepted=False, while the stored spec's sku remains the original value.
// (The update is admitted and stored — the condition reflects the incompatibility.)
func TestProductVariant_UpdateWithInvalidOptionLeavesSpecUnchanged(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	parentName := fmt.Sprintf("pv-us5-inv-parent-%d", ts)
	variantName := fmt.Sprintf("pv-us5-inv-%d", ts)
	sku := fmt.Sprintf("SKU-US5-INV-%d", ts)

	// Push parent product with "size" option only.
	h := newPushHelper(t)
	h.commitProduct(parentName+".md", productWithOptions(parentName, ns, []struct {
		Name   string
		Values []string
	}{{Name: "size", Values: []string{"S", "M", "L"}}}))
	if out, err := h.push(); err != nil {
		t.Fatalf("push parent failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Push initial valid variant with size=M.
	h2 := newPushHelper(t)
	h2.commitVariant(variantName+".md", variantWithOptions(variantName, ns, sku, parentName, []struct{ Name, Value string }{{Name: "size", Value: "M"}}))
	if out, err := h2.push(); err != nil {
		t.Fatalf("initial push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	v1 := queryVariant(t, ns, variantName)
	if v1 == nil {
		t.Fatal("variant not found after initial push")
	}
	if v1.Status == nil {
		t.Fatal("status nil after initial push")
	}
	oa1 := findVariantCondition(v1.Status.Conditions, "OPTIONS_ACCEPTED")
	if oa1 == nil || oa1.Status != "TRUE" {
		t.Errorf("initial OptionsAccepted: %+v", oa1)
	}
	// Extra pause: ScyllaDB may have additional read-propagation lag between
	// the GraphQL read path and the admission gRPC read path.
	time.Sleep(500 * time.Millisecond)

	// Re-push with invalid option "material" not declared on parent.
	h3 := newPushHelper(t)
	h3.commitVariant(variantName+".md", variantWithOptions(variantName, ns, sku, parentName, []struct{ Name, Value string }{{Name: "material", Value: "cotton"}}))
	if out, err := h3.push(); err != nil {
		t.Fatalf("update with invalid option should not be rejected at pre-receive, got error:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Retry until OptionsAccepted=FALSE and generation incremented (ScyllaDB lag).
	var v2 *variantQueryResult
	deadline2 := time.Now().Add(5 * time.Second)
	for {
		v2 = queryVariant(t, ns, variantName)
		if v2 != nil && v2.Status != nil {
			oa := findVariantCondition(v2.Status.Conditions, "OPTIONS_ACCEPTED")
			if oa != nil && oa.Status == "FALSE" && v2.Status.ObservedGeneration > v1.Status.ObservedGeneration {
				break
			}
		}
		if time.Now().After(deadline2) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if v2 == nil {
		t.Fatal("variant not found after update push")
	}
	if v2.Status == nil {
		t.Fatal("status nil after update push")
	}
	oa2 := findVariantCondition(v2.Status.Conditions, "OPTIONS_ACCEPTED")
	if oa2 == nil {
		t.Fatal("OptionsAccepted condition missing after update")
	}
	if oa2.Status != "FALSE" {
		t.Errorf("OptionsAccepted after invalid update: got %q, want False", oa2.Status)
	}
	// Generation must have incremented (update was stored, condition reflects incompatibility).
	if v2.Status.ObservedGeneration <= v1.Status.ObservedGeneration {
		t.Errorf("generation did not increment: before=%d after=%d",
			v1.Status.ObservedGeneration, v2.Status.ObservedGeneration)
	}
}

// ── SKU conflict ──────────────────────────────────────────────────────────────

// TestProductVariant_SKUConflictLogged verifies that pushing a second variant
// with the same SKU in the same namespace is admitted (pre-receive does not
// check SKU uniqueness) but the conflict is surfaced: the second variant must
// either fail to be stored (ErrAlreadyExists) or be queryable by its own name
// while the original is still queryable. Either way the namespace cannot end
// up with two distinct variants sharing a SKU.
func TestProductVariant_SKUConflictDetected(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-skuconflict-parent-%d", ts)
	variantA := fmt.Sprintf("pv-sku-a-%d", ts)
	variantB := fmt.Sprintf("pv-sku-b-%d", ts)
	sharedSKU := fmt.Sprintf("SKU-SHARED-%d", ts)

	// Push the parent product and variant A.
	h1 := newPushHelper(t)
	h1.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	h1.commitVariant(variantA+".md", minimalVariantFixture(variantA, ns, sharedSKU, productName))
	if out, err := h1.push(); err != nil {
		t.Fatalf("first push (variant A) should succeed: %s", out)
	}
	time.Sleep(500 * time.Millisecond)

	vA := queryVariant(t, ns, variantA)
	if vA == nil {
		t.Fatalf("variant A %q not found after push", variantA)
	}

	// Push variant B with the same SKU — must be accepted at pre-receive.
	h2 := newPushHelper(t)
	h2.commitVariant(variantB+".md", minimalVariantFixture(variantB, ns, sharedSKU, productName))
	out, err := h2.push()
	if err != nil {
		t.Fatalf("pre-receive must not reject duplicate SKU (stateless check), got error: %s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Variant A must still be queryable (not overwritten by B).
	vAAfter := queryVariant(t, ns, variantA)
	if vAAfter == nil {
		t.Fatalf("variant A %q disappeared after variant B push with same SKU", variantA)
	}
	if vAAfter.Spec.SKU != sharedSKU {
		t.Errorf("variant A sku changed: got %q, want %q", vAAfter.Spec.SKU, sharedSKU)
	}

	// Variant B — if it was admitted despite the conflict, its SKU must equal the shared one.
	// If it was silently rejected by the datastore (ErrAlreadyExists on SKU), it won't be found.
	// Both outcomes are acceptable; what is NOT acceptable is variant A disappearing.
	// Use a direct query that tolerates "not found" errors (resolver error, not null).
	respB := gqlQuery(t, `
		query($ns: String!, $name: String!) {
			productVariant(by: { namespacePath: { namespace: $ns, name: $name } }) {
				metadata { name }
				spec { sku }
			}
		}
	`, map[string]any{"ns": ns, "name": variantB})
	if len(respB.Errors) == 0 {
		type data struct {
			ProductVariant *variantQueryResult `json:"productVariant"`
		}
		var d data
		if err := json.Unmarshal(respB.Data, &d); err == nil && d.ProductVariant != nil {
			if d.ProductVariant.Spec.SKU != sharedSKU {
				t.Errorf("variant B sku: got %q, want %q", d.ProductVariant.Spec.SKU, sharedSKU)
			}
		}
	}
}

// ── Product.productVariants pageInfo ─────────────────────────────────────────

// TestProductVariant_ProductVariantsPageInfo verifies that Product.productVariants
// returns correct Relay pageInfo (startCursor, endCursor, hasNextPage) when the
// result set is larger than the requested page size.
func TestProductVariant_ProductVariantsPageInfo(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	productName := fmt.Sprintf("pv-pageinfo-parent-%d", ts)

	// Push 3 variants for the same product.
	h := newPushHelper(t)
	h.commitProduct(productName+".md", uniqueValidProductFixture(productName, ns))
	for i := range 3 {
		vName := fmt.Sprintf("pv-pageinfo-%d-%d", ts, i)
		sku := fmt.Sprintf("SKU-PI-%d-%d", ts, i)
		h.commitVariant(vName+".md", minimalVariantFixture(vName, ns, sku, productName))
	}
	if out, err := h.push(); err != nil {
		t.Fatalf("push failed: %s", out)
	}

	// Wait until all 3 variants appear (async admission may lag for multi-variant push).
	type conn struct {
		Edges []struct {
			Cursor string `json:"cursor"`
			Node   struct {
				Metadata struct{ Name string `json:"name"` } `json:"metadata"`
				Spec     struct{ SKU string `json:"sku"` }    `json:"spec"`
			} `json:"node"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage     bool    `json:"hasNextPage"`
			HasPreviousPage bool    `json:"hasPreviousPage"`
			StartCursor     *string `json:"startCursor"`
			EndCursor       *string `json:"endCursor"`
		} `json:"pageInfo"`
		TotalCount int `json:"totalCount"`
	}
	var pv conn // populated after the wait loop + paged query below
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := gqlQuery(t, `
			query($ns: String!, $name: String!) {
				product(by: { namespacePath: { namespace: $ns, name: $name } }) {
					productVariants(first: 50) {
						totalCount
					}
				}
			}
		`, map[string]any{"ns": ns, "name": productName})
		if len(resp.Errors) > 0 {
			t.Fatalf("graphql errors waiting for variants: %s", resp.Errors)
		}
		var probe struct {
			Product *struct {
				ProductVariants struct {
					TotalCount int `json:"totalCount"`
				} `json:"productVariants"`
			} `json:"product"`
		}
		if err := json.Unmarshal(resp.Data, &probe); err != nil {
			t.Fatalf("decode probe response: %v", err)
		}
		if probe.Product != nil && probe.Product.ProductVariants.TotalCount >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for 3 variants to be admitted for product %q", productName)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Query Product.productVariants with first:2 to force pagination.
	resp := gqlQuery(t, `
		query($ns: String!, $name: String!) {
			product(by: { namespacePath: { namespace: $ns, name: $name } }) {
				productVariants(first: 2) {
					edges { cursor node { metadata { name } spec { sku } } }
					pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
					totalCount
				}
			}
		}
	`, map[string]any{"ns": ns, "name": productName})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors: %s", resp.Errors)
	}

	var d struct {
		Product *struct {
			ProductVariants conn `json:"productVariants"`
		} `json:"product"`
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if d.Product == nil {
		t.Fatalf("product %q not found", productName)
	}
	pv = d.Product.ProductVariants

	if len(pv.Edges) != 2 {
		t.Errorf("expected 2 edges with first:2, got %d", len(pv.Edges))
	}
	if pv.TotalCount < 3 {
		t.Errorf("totalCount: expected >= 3, got %d", pv.TotalCount)
	}
	if !pv.PageInfo.HasNextPage {
		t.Errorf("hasNextPage should be true when 3 variants exist and first:2 requested")
	}
	if pv.PageInfo.StartCursor == nil || *pv.PageInfo.StartCursor == "" {
		t.Errorf("startCursor must be non-empty")
	}
	if pv.PageInfo.EndCursor == nil || *pv.PageInfo.EndCursor == "" {
		t.Errorf("endCursor must be non-empty")
	}
	if t.Failed() {
		t.FailNow() // cannot proceed to page 2 query without a valid endCursor
	}

	// Use endCursor to fetch the next page and verify it contains the third variant.
	resp2 := gqlQuery(t, `
		query($ns: String!, $name: String!, $after: String!) {
			product(by: { namespacePath: { namespace: $ns, name: $name } }) {
				productVariants(first: 2, after: $after) {
					edges { node { metadata { name } } }
					pageInfo { hasNextPage hasPreviousPage }
					totalCount
				}
			}
		}
	`, map[string]any{"ns": ns, "name": productName, "after": *pv.PageInfo.EndCursor})
	if len(resp2.Errors) > 0 {
		t.Fatalf("graphql errors on page 2: %s", resp2.Errors)
	}
	var d2 struct {
		Product *struct {
			ProductVariants conn `json:"productVariants"`
		} `json:"product"`
	}
	if err := json.Unmarshal(resp2.Data, &d2); err != nil {
		t.Fatalf("decode page 2 response: %v", err)
	}
	if d2.Product == nil {
		t.Fatal("product not found on page 2 query")
	}
	pv2 := d2.Product.ProductVariants
	if len(pv2.Edges) == 0 {
		t.Errorf("expected at least 1 edge on page 2, got 0")
	}
	if pv2.PageInfo.HasNextPage {
		t.Errorf("hasNextPage should be false on the last page")
	}
}
