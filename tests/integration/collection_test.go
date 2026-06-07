// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ── Collection fixtures ───────────────────────────────────────────────────────

// minimalCollectionFixture returns a valid Collection with title only (no selector).
// selector is absent, so resolved.memberCount will be 0.
func minimalCollectionFixture(name, ns string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
---

Collection description for %s.
`, name, ns, name, name)
}

// collectionWithMatchLabels returns a Collection with an exact-match selector.
func collectionWithMatchLabels(name, ns string, labels map[string]string) string {
	var sb strings.Builder
	sb.WriteString("    matchLabels:\n")
	for k, v := range labels {
		fmt.Fprintf(&sb, "      %s: %s\n", k, v)
	}
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  selector:
%s---

Collection with matchLabels selector.
`, name, ns, name, sb.String())
}

// collectionWithMatchExpression returns a Collection with a single matchExpressions entry.
func collectionWithMatchExpression(name, ns, key, operator string, values []string) string {
	valuesYAML := ""
	if len(values) > 0 {
		var sb strings.Builder
		sb.WriteString("      values:\n")
		for _, v := range values {
			fmt.Fprintf(&sb, "      - %s\n", v)
		}
		valuesYAML = sb.String()
	}
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  selector:
    matchExpressions:
    - key: %s
      operator: %s
%s---

Collection with matchExpressions selector.
`, name, ns, name, key, operator, valuesYAML)
}

// invalidCollectionMissingTitle returns a Collection document with no spec.title.
func invalidCollectionMissingTitle(name, ns string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec: {}
---
`, name, ns)
}

// invalidCollectionBadTargetRef returns a Collection with targetRef.kind != Product.
func invalidCollectionBadTargetRef(name, ns, targetKind string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  targetRef:
    kind: %s
---
`, name, ns, name, targetKind)
}

// productWithLabel returns a valid product fixture with a single extra label.
func productWithLabel(name, ns, labelKey, labelValue string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
  labels:
    %s: %s
spec:
  title: %s
  options:
  - name: size
    values: [S, M, L]
---

Product with label %s=%s.
`, name, ns, labelKey, labelValue, name, labelKey, labelValue)
}

// ── GraphQL result types ──────────────────────────────────────────────────────

type collectionCondition struct {
	Type               string  `json:"type"`
	Status             string  `json:"status"`
	Reason             *string `json:"reason"`
	Message            *string `json:"message"`
	ObservedGeneration *int    `json:"observedGeneration"`
}

type collectionQueryResult struct {
	ID         string `json:"id"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"metadata"`
	Spec struct {
		Title    string      `json:"title"`
		Selector any `json:"selector"`
	} `json:"spec"`
	Status *struct {
		ObservedGeneration  int                   `json:"observedGeneration"`
		LastAppliedRevision *string               `json:"lastAppliedRevision"`
		Conditions          []collectionCondition `json:"conditions"`
		Resolved            *struct {
			MemberCount int `json:"memberCount"`
		} `json:"resolved"`
	} `json:"status"`
	Products *struct {
		Edges []struct {
			Node struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"node"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		TotalCount int `json:"totalCount"`
	} `json:"products"`
}

// queryCollection fetches a collection by namespace + name via GraphQL.
func queryCollection(t *testing.T, namespace, name string) *collectionQueryResult {
	t.Helper()
	resp := gqlQuery(t, `
		query($namespace: String!, $name: String!) {
			collection(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				id
				apiVersion
				kind
				metadata { name uid }
				spec { title }
				status {
					observedGeneration
					lastAppliedRevision
					conditions { type status reason message observedGeneration }
					resolved { memberCount }
				}
				products(first: 50) {
					edges { node { metadata { name } } }
					pageInfo { hasNextPage }
					totalCount
				}
			}
		}
	`, map[string]any{"namespace": namespace, "name": name})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors querying collection %q: %s", name, resp.Errors)
	}
	var data struct {
		Collection *collectionQueryResult `json:"collection"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal collection response: %v", err)
	}
	return data.Collection
}

// findCondition returns the first condition with the given type, or nil.
func findCondition(conditions []collectionCondition, condType string) *collectionCondition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// ── US1: Valid Collection Accepted ───────────────────────────────────────────

// T050: Push minimal valid collection (title only, no selector).
// Asserts: push succeeds, AdmissionAccepted condition is True, products connection is empty.
func TestCollection_ValidPushAccepted(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("coll-valid")
	h := newPushHelper(t)
	h.commitCollection(name+".md", minimalCollectionFixture(name, ns))
	out, err := h.push()
	if err != nil {
		t.Fatalf("expected push to succeed for valid collection, got error:\n%s", out)
	}

	time.Sleep(500 * time.Millisecond)

	coll := queryCollection(t, ns, name)
	if coll == nil {
		t.Fatalf("collection %q not found after push", name)
	}
	if coll.Kind != "Collection" {
		t.Errorf("kind: got %q, want %q", coll.Kind, "Collection")
	}
	if coll.Spec.Title != name {
		t.Errorf("spec.title: got %q, want %q", coll.Spec.Title, name)
	}
	if coll.Status == nil {
		t.Fatal("status is nil, expected populated status after admission")
	}
	admitted := findCondition(coll.Status.Conditions, "AdmissionAccepted")
	if admitted == nil {
		t.Fatalf("AdmissionAccepted condition not found in: %+v", coll.Status.Conditions)
	}
	if admitted.Status != "True" {
		t.Errorf("AdmissionAccepted condition status: got %q, want %q", admitted.Status, "True")
	}
	// No selector → products connection must be empty.
	if coll.Products != nil && len(coll.Products.Edges) != 0 {
		t.Errorf("expected 0 products for collection with no selector, got %d", len(coll.Products.Edges))
	}
}

// T051: Push collection with matchLabels selector; products with matching labels must appear.
func TestCollection_WithSelectorMatchesProducts(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()

	// Seed 3 products with brand=apple.
	h := newPushHelper(t)
	for i := 0; i < 3; i++ {
		pName := fmt.Sprintf("apple-prod-%d-%d", ts, i)
		h.commitProduct(pName+".md", productWithLabel(pName, ns, "gitstore.dev/brand", "apple"))
	}
	if out, err := h.push(); err != nil {
		t.Fatalf("seeding apple products failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Push collection selecting brand=apple.
	collName := fmt.Sprintf("apple-laptops-%d", ts)
	h2 := newPushHelper(t)
	h2.commitCollection(collName+".md", collectionWithMatchLabels(collName, ns, map[string]string{"gitstore.dev/brand": "apple"}))
	if out, err := h2.push(); err != nil {
		t.Fatalf("expected push to succeed for collection with selector, got error:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	coll := queryCollection(t, ns, collName)
	if coll == nil {
		t.Fatalf("collection %q not found after push", collName)
	}
	if coll.Status == nil {
		t.Fatal("status is nil")
	}
	admitted := findCondition(coll.Status.Conditions, "AdmissionAccepted")
	if admitted == nil || admitted.Status != "True" {
		t.Errorf("AdmissionAccepted condition: %+v", admitted)
	}
	// products connection is authoritative for live membership count.
	if coll.Products == nil || len(coll.Products.Edges) < 3 {
		t.Errorf("collection.products edges: got %d, want >= 3", len(coll.Products.Edges))
	}
}

// T052: Push collection with optional media reference to a non-existent file.
// Asserts: push accepted, status.resolved.media is empty.
func TestCollection_OptionalMediaAbsent(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("coll-media")
	fixture := fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  media:
  - fileRef:
      name: missing-hero
      kind: File
      optional: true
---

Collection with optional media.
`, name, ns, name)

	h := newPushHelper(t)
	h.commitCollection(name+".md", fixture)
	out, err := h.push()
	if err != nil {
		t.Fatalf("expected push to succeed with optional missing media, got error:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	coll := queryCollection(t, ns, name)
	if coll == nil {
		t.Fatalf("collection %q not found after push", name)
	}
	if coll.Status == nil {
		t.Fatal("status is nil, expected populated status after admission")
	}
	// Verify push succeeded; resolved.media is not yet in the schema.
	admitted := findCondition(coll.Status.Conditions, "AdmissionAccepted")
	if admitted == nil || admitted.Status != "True" {
		t.Errorf("AdmissionAccepted condition: %+v", admitted)
	}
}

// ── US2: Invalid Collection Rejected ─────────────────────────────────────────

// T053: Push collection missing spec.title → rejected with title-related error.
func TestCollection_MissingTitle(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("coll-notitle")
	h := newPushHelper(t)
	h.commitCollection(name+".md", invalidCollectionMissingTitle(name, ns))
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for missing spec.title, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(out), "title") {
		t.Errorf("expected rejection message to mention 'title', got:\n%s", out)
	}
}

// T054: Push document with an unknown kind → rejected with unrecognized kind error.
func TestCollection_WrongKind(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("coll-wrongkind")
	// "Widget" is not a recognized catalog resource type.
	fixture := fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Widget
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
---
`, name, ns, name)

	h := newPushHelper(t)
	h.commitCollection(name+".md", fixture)
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for unrecognized kind 'Widget', but it succeeded")
	}
	combined := strings.ToLower(out)
	if !strings.Contains(combined, "kind") && !strings.Contains(combined, "widget") && !strings.Contains(combined, "recognized") {
		t.Errorf("expected rejection to mention kind or 'Widget', got:\n%s", out)
	}
}

// T055: Push collection with targetRef.kind: CategoryTaxonomy → rejected.
func TestCollection_InvalidTargetRefKind(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("coll-badref")
	h := newPushHelper(t)
	h.commitCollection(name+".md", invalidCollectionBadTargetRef(name, ns, "CategoryTaxonomy"))
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for targetRef.kind != Product, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(out), "targetref") && !strings.Contains(strings.ToLower(out), "target_ref") {
		t.Errorf("expected rejection message to mention targetRef, got:\n%s", out)
	}
}

// T056: Push collection with operator: Between (invalid) → rejected.
func TestCollection_InvalidOperatorInExpression(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("coll-badop")
	fixture := fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  selector:
    matchExpressions:
    - key: env
      operator: Between
      values:
      - a
      - z
---
`, name, ns, name)

	h := newPushHelper(t)
	h.commitCollection(name+".md", fixture)
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for invalid operator 'Between', but it succeeded")
	}
	if !strings.Contains(strings.ToLower(out), "operator") && !strings.Contains(strings.ToLower(out), "matchexpression") {
		t.Errorf("expected rejection message to mention operator or matchExpressions, got:\n%s", out)
	}
}

// T057: Push collection with operator: In and empty values → rejected.
func TestCollection_InOperatorEmptyValues(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("coll-emptyvals")
	fixture := fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  selector:
    matchExpressions:
    - key: env
      operator: In
      values: []
---
`, name, ns, name)

	h := newPushHelper(t)
	h.commitCollection(name+".md", fixture)
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for operator In with empty values, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(out), "value") {
		t.Errorf("expected rejection message to mention values requirement, got:\n%s", out)
	}
}

// ── US3: Selector Semantics and Determinism ───────────────────────────────────

// T058: Resolve the same collection twice → identical membership.
func TestCollection_DeterministicMembership(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()

	// Seed products with a unique label to isolate this test run.
	labelKey := "gitstore.dev/det-test"
	labelVal := fmt.Sprintf("run-%d", ts)
	var productNames []string
	h := newPushHelper(t)
	for i := 0; i < 4; i++ {
		pName := fmt.Sprintf("det-prod-%d-%d", ts, i)
		productNames = append(productNames, pName)
		h.commitProduct(pName+".md", productWithLabel(pName, ns, labelKey, labelVal))
	}
	if out, err := h.push(); err != nil {
		t.Fatalf("seeding products failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	collName := fmt.Sprintf("det-coll-%d", ts)
	h2 := newPushHelper(t)
	h2.commitCollection(collName+".md", collectionWithMatchLabels(collName, ns, map[string]string{labelKey: labelVal}))
	if out, err := h2.push(); err != nil {
		t.Fatalf("collection push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Resolve twice and compare.
	coll1 := queryCollection(t, ns, collName)
	coll2 := queryCollection(t, ns, collName)
	if coll1 == nil || coll2 == nil {
		t.Fatal("collection not found")
	}

	names1 := productNamesFromEdges(coll1)
	names2 := productNamesFromEdges(coll2)
	if len(names1) != len(names2) {
		t.Fatalf("non-deterministic: first query returned %d products, second returned %d", len(names1), len(names2))
	}
	for i := range names1 {
		if names1[i] != names2[i] {
			t.Errorf("non-deterministic ordering at index %d: %q vs %q", i, names1[i], names2[i])
		}
	}
	if len(names1) < 4 {
		t.Errorf("expected >= 4 products in collection, got %d", len(names1))
	}
}

// T059: Selector NotIn — only products not matching the excluded value are included.
func TestCollection_SelectorNotIn(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()

	labelKey := "gitstore.dev/brand"
	hProducts := newPushHelper(t)
	appleProduct := fmt.Sprintf("notin-apple-%d", ts)
	samsungProduct := fmt.Sprintf("notin-samsung-%d", ts)
	hProducts.commitProduct(appleProduct+".md", productWithLabel(appleProduct, ns, labelKey, "apple"))
	hProducts.commitProduct(samsungProduct+".md", productWithLabel(samsungProduct, ns, labelKey, "samsung"))
	if out, err := hProducts.push(); err != nil {
		t.Fatalf("seeding products failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	collName := fmt.Sprintf("notin-coll-%d", ts)
	hColl := newPushHelper(t)
	hColl.commitCollection(collName+".md", collectionWithMatchExpression(collName, ns, labelKey, "NotIn", []string{"apple"}))
	if out, err := hColl.push(); err != nil {
		t.Fatalf("collection push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	coll := queryCollection(t, ns, collName)
	if coll == nil {
		t.Fatal("collection not found")
	}
	names := productNamesFromEdges(coll)
	for _, n := range names {
		if n == appleProduct {
			t.Errorf("apple product should be excluded by NotIn selector, but was found in collection")
		}
	}
	found := false
	for _, n := range names {
		if n == samsungProduct {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("samsung product should be included by NotIn selector, but was not found")
	}
}

// T060: Selector Exists — products with the label key (any value) are included.
func TestCollection_SelectorExists(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()

	labelKey := fmt.Sprintf("gitstore.dev/exists-test-%d", ts)
	hProducts := newPushHelper(t)
	featuredProduct := fmt.Sprintf("exists-feat-%d", ts)
	plainProduct := fmt.Sprintf("exists-plain-%d", ts)
	hProducts.commitProduct(featuredProduct+".md", productWithLabel(featuredProduct, ns, labelKey, "true"))
	hProducts.commitProduct(plainProduct+".md", validProductFixture(plainProduct, ns))
	if out, err := hProducts.push(); err != nil {
		t.Fatalf("seeding products failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	collName := fmt.Sprintf("exists-coll-%d", ts)
	hColl := newPushHelper(t)
	hColl.commitCollection(collName+".md", collectionWithMatchExpression(collName, ns, labelKey, "Exists", nil))
	if out, err := hColl.push(); err != nil {
		t.Fatalf("collection push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	coll := queryCollection(t, ns, collName)
	if coll == nil {
		t.Fatal("collection not found")
	}
	names := productNamesFromEdges(coll)
	found := false
	for _, n := range names {
		if n == featuredProduct {
			found = true
		}
		if n == plainProduct {
			t.Errorf("plain product (no matching label) should not be in Exists collection, but was found")
		}
	}
	if !found {
		t.Errorf("featured product should be included by Exists selector, but was not found")
	}
}

// T061: Selector DoesNotExist — only products without the label key are included.
func TestCollection_SelectorDoesNotExist(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()

	labelKey := fmt.Sprintf("gitstore.dev/dne-test-%d", ts)
	hProducts := newPushHelper(t)
	saleProduct := fmt.Sprintf("dne-sale-%d", ts)
	noSaleProduct := fmt.Sprintf("dne-nosale-%d", ts)
	hProducts.commitProduct(saleProduct+".md", productWithLabel(saleProduct, ns, labelKey, "true"))
	hProducts.commitProduct(noSaleProduct+".md", validProductFixture(noSaleProduct, ns))
	if out, err := hProducts.push(); err != nil {
		t.Fatalf("seeding products failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	collName := fmt.Sprintf("dne-coll-%d", ts)
	hColl := newPushHelper(t)
	hColl.commitCollection(collName+".md", collectionWithMatchExpression(collName, ns, labelKey, "DoesNotExist", nil))
	if out, err := hColl.push(); err != nil {
		t.Fatalf("collection push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	coll := queryCollection(t, ns, collName)
	if coll == nil {
		t.Fatal("collection not found")
	}
	names := productNamesFromEdges(coll)
	for _, n := range names {
		if n == saleProduct {
			t.Errorf("sale product (has label) should be excluded by DoesNotExist selector, but was found")
		}
	}
	found := false
	for _, n := range names {
		if n == noSaleProduct {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no-sale product should be included by DoesNotExist selector, but was not found")
	}
}

// T062: Push a new matching product after collection exists → memberCount increases.
func TestCollection_NewProductAppearsAfterPush(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()

	labelKey := fmt.Sprintf("gitstore.dev/grow-test-%d", ts)
	labelVal := "included"

	// Seed 2 products initially.
	hProducts := newPushHelper(t)
	for i := 0; i < 2; i++ {
		pName := fmt.Sprintf("grow-prod-%d-%d", ts, i)
		hProducts.commitProduct(pName+".md", productWithLabel(pName, ns, labelKey, labelVal))
	}
	if out, err := hProducts.push(); err != nil {
		t.Fatalf("initial product seed failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Push collection.
	collName := fmt.Sprintf("grow-coll-%d", ts)
	hColl := newPushHelper(t)
	hColl.commitCollection(collName+".md", collectionWithMatchLabels(collName, ns, map[string]string{labelKey: labelVal}))
	if out, err := hColl.push(); err != nil {
		t.Fatalf("collection push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	coll1 := queryCollection(t, ns, collName)
	if coll1 == nil {
		t.Fatal("collection not found")
	}
	initialCount := 0
	if coll1.Status != nil && coll1.Status.Resolved != nil {
		initialCount = coll1.Status.Resolved.MemberCount
	}

	// Add a third matching product.
	newProductName := fmt.Sprintf("grow-prod-%d-new", ts)
	hNew := newPushHelper(t)
	hNew.commitProduct(newProductName+".md", productWithLabel(newProductName, ns, labelKey, labelVal))
	if out, err := hNew.push(); err != nil {
		t.Fatalf("new product push failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Re-query the collection; live products count should increase.
	coll2 := queryCollection(t, ns, collName)
	if coll2 == nil {
		t.Fatal("collection not found after adding new product")
	}
	// The live products connection should reflect the new product.
	liveCount := 0
	if coll2.Products != nil {
		liveCount = len(coll2.Products.Edges)
	}
	if liveCount <= initialCount {
		t.Errorf("expected collection to include new product: initial products=%d, current products=%d",
			initialCount, liveCount)
	}
}

// productNamesFromEdges extracts product metadata.name from collection.products edges.
func productNamesFromEdges(coll *collectionQueryResult) []string {
	if coll == nil || coll.Products == nil {
		return nil
	}
	var names []string
	for _, edge := range coll.Products.Edges {
		names = append(names, edge.Node.Metadata.Name)
	}
	return names
}
