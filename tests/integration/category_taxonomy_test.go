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

// ── CategoryTaxonomy fixtures ─────────────────────────────────────────────────

func rootCategoryFixture(name string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: %s
spec:
  title: %s
---

Category description for %s.
`, name, name, name)
}

func childCategoryFixture(name, parentName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: %s
spec:
  title: %s
  parentRef:
    name: %s
    kind: CategoryTaxonomy
---

Category description for %s.
`, name, name, parentName, name)
}

func selfRefCategoryFixture(name string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: %s
spec:
  title: %s
  parentRef:
    name: %s
    kind: CategoryTaxonomy
---
`, name, name, name)
}

func missingTitleCategoryFixture(name string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: %s
spec: {}
---
`, name)
}

func productWithCategoryRefFixture(name, ns, categoryName string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
  categoryRef:
    name: %s
    kind: CategoryTaxonomy
---

Product in category %s.
`, name, ns, name, categoryName, categoryName)
}

func productWithArrayCategoryRefFixture(name, ns string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
spec:
  categoryRef:
    - name: electronics
    - name: computers
---
`, name, ns)
}

// ── Push helpers for categories ───────────────────────────────────────────────

// commitCategory writes a CategoryTaxonomy markdown file and commits it.
func (h *pushHelper) commitCategory(filename, content string) {
	h.t.Helper()
	dir := filepath.Join(h.workDir, "categories")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatalf("mkdir categories: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatalf("write category file: %v", err)
	}
	run(h.t, h.workDir, "git", "add", path)
	run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("add %s", filename))
}

// ── GraphQL query helpers for categories ─────────────────────────────────────

type categoryQueryResult struct {
	ID         string `json:"id"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Title string `json:"title"`
	} `json:"spec"`
	Path  []string `json:"path"`
	Depth int      `json:"depth"`
}

func queryCategory(t *testing.T, name string) *categoryQueryResult {
	t.Helper()
	ns := getEnv("NAMESPACE", "gitstore-test")
	resp := gqlQuery(t, `
		query($namespace: String!, $name: String!) {
			category(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				id
				apiVersion
				kind
				metadata { name }
				spec { title }
				path
				depth
			}
		}
	`, map[string]any{"namespace": ns, "name": name})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors querying category %q: %s", name, resp.Errors)
	}
	var data struct {
		Category *categoryQueryResult `json:"category"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal category response: %v", err)
	}
	return data.Category
}

// ── T041: Push a valid root CategoryTaxonomy and query it ────────────────────

func TestCategoryTaxonomyPublish(t *testing.T) {
	name := fmt.Sprintf("electronics-%d", time.Now().UnixNano())
	h := newPushHelper(t)
	h.commitCategory(name+".md", rootCategoryFixture(name))
	out, err := h.push()
	if err != nil {
		t.Fatalf("expected push to succeed, got error:\n%s", out)
	}

	// Give post-receive admission a moment to complete.
	time.Sleep(500 * time.Millisecond)

	cat := queryCategory(t, name)
	if cat == nil {
		t.Fatalf("expected category %q to be queryable after push, got nil", name)
	}
	if cat.Metadata.Name != name {
		t.Errorf("metadata.name: got %q, want %q", cat.Metadata.Name, name)
	}
	if cat.APIVersion != "catalog.gitstore.dev/v1beta1" {
		t.Errorf("apiVersion: got %q, want %q", cat.APIVersion, "catalog.gitstore.dev/v1beta1")
	}
	if cat.Kind != "CategoryTaxonomy" {
		t.Errorf("kind: got %q, want %q", cat.Kind, "CategoryTaxonomy")
	}
	if cat.Spec.Title == "" {
		t.Error("spec.title: got empty string, want non-empty")
	}
}

// ── T042: Push parent then child — hierarchy path and depth ──────────────────

func TestCategoryTaxonomyHierarchy(t *testing.T) {
	ts := time.Now().UnixNano()
	parentName := fmt.Sprintf("electronics-%d", ts)
	childName := fmt.Sprintf("computers-%d", ts)

	h := newPushHelper(t)
	h.commitCategory(parentName+".md", rootCategoryFixture(parentName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push parent failed:\n%s", out)
	}

	// Admission is fire-and-forget; wait for parent to be stored.
	time.Sleep(500 * time.Millisecond)

	h2 := newPushHelper(t)
	h2.commitCategory(childName+".md", childCategoryFixture(childName, parentName))
	if out, err := h2.push(); err != nil {
		t.Fatalf("push child failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	cat := queryCategory(t, childName)
	if cat == nil {
		t.Fatalf("expected child category %q to be queryable, got nil", childName)
	}
	if len(cat.Path) != 2 {
		t.Fatalf("path: got %v (len %d), want [%s %s]", cat.Path, len(cat.Path), parentName, childName)
	}
	if cat.Path[0] != parentName || cat.Path[1] != childName {
		t.Errorf("path: got %v, want [%s %s]", cat.Path, parentName, childName)
	}
	if cat.Depth != 1 {
		t.Errorf("depth: got %d, want 1", cat.Depth)
	}
}

// ── T043: Self-referencing parentRef — push rejected pre-receive ─────────────

func TestCategoryTaxonomySelfRefRejected(t *testing.T) {
	name := fmt.Sprintf("self-ref-%d", time.Now().UnixNano())
	h := newPushHelper(t)
	h.commitCategory(name+".md", selfRefCategoryFixture(name))
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for self-referencing parentRef, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(out), "must not reference") &&
		!strings.Contains(strings.ToLower(out), "self") {
		t.Errorf("expected rejection message about self-reference, got:\n%s", out)
	}
}

// ── T044: Missing spec.title — push rejected pre-receive ─────────────────────

func TestCategoryTaxonomyMissingFields(t *testing.T) {
	name := fmt.Sprintf("missing-title-%d", time.Now().UnixNano())
	h := newPushHelper(t)
	h.commitCategory(name+".md", missingTitleCategoryFixture(name))
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for missing spec.title, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(out), "title") {
		t.Errorf("expected rejection message to name spec.title, got:\n%s", out)
	}
}

// ── T045: Product single-category constraint via push ────────────────────────

func TestCategoryTaxonomyProductSingleRef(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	catName := fmt.Sprintf("electronics-%d", ts)
	productName := fmt.Sprintf("widget-%d", ts)

	// Push the root category first.
	h := newPushHelper(t)
	h.commitCategory(catName+".md", rootCategoryFixture(catName))
	if out, err := h.push(); err != nil {
		t.Fatalf("push category failed:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	// Push a product with a single categoryRef — must be accepted.
	h2 := newPushHelper(t)
	h2.commitProduct(productName+".md", productWithCategoryRefFixture(productName, ns, catName))
	if out, err := h2.push(); err != nil {
		t.Fatalf("expected push with single categoryRef to succeed, got:\n%s", out)
	}

	// Push a product with a YAML-array categoryRef — must be rejected.
	badName := fmt.Sprintf("widget-bad-%d", ts)
	h3 := newPushHelper(t)
	h3.commitProduct(badName+".md", productWithArrayCategoryRefFixture(badName, ns))
	out, err := h3.push()
	if err == nil {
		t.Fatal("expected push with array categoryRef to be rejected, but it succeeded")
	}
	_ = out // error output confirms rejection; field name depends on YAML unmarshal error text
}

// ── T046: Co-creation — parent and child in the same push ────────────────────

func TestCategoryTaxonomyCoCreation(t *testing.T) {
	ts := time.Now().UnixNano()
	parentName := fmt.Sprintf("a-%d", ts)
	childName := fmt.Sprintf("b-%d", ts)

	h := newPushHelper(t)
	// Commit both files in a single push (same commit).
	h.commitCategory(parentName+".md", rootCategoryFixture(parentName))
	h.commitCategory(childName+".md", childCategoryFixture(childName, parentName))
	if out, err := h.push(); err != nil {
		t.Fatalf("expected co-creation push to succeed, got:\n%s", out)
	}
	time.Sleep(500 * time.Millisecond)

	child := queryCategory(t, childName)
	if child == nil {
		t.Fatalf("expected child category %q to be queryable, got nil", childName)
	}
	// AncestorPath for in-push co-creation is parentName + "/" + childName.
	wantPath := []string{parentName, childName}
	if len(child.Path) != 2 || child.Path[0] != wantPath[0] || child.Path[1] != wantPath[1] {
		t.Errorf("path: got %v, want %v", child.Path, wantPath)
	}
}
