// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
}

func queryCategory(t *testing.T, name string) *categoryQueryResult {
	t.Helper()
	ns := getEnv("NAMESPACE", "gitstore-test")
	const (
		maxWait  = 10 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	for {
		resp := gqlQuery(t, `
			query($namespace: String!, $name: String!) {
				category(by: {namespacePath: {namespace: $namespace, name: $name}}) {
					id
					apiVersion
					kind
					metadata { name }
					spec { title }
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
		if data.Category == nil && time.Now().Before(deadline) {
			time.Sleep(interval)
			continue
		}
		return data.Category
	}
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

	// Admission is fire-and-forget; wait for the parent before validating a
	// child push that references it.
	if parent := queryCategory(t, parentName); parent == nil {
		t.Fatalf("expected parent category %q to be queryable after push, got nil", parentName)
	}

	h2 := newPushHelper(t)
	h2.commitCategory(childName+".md", childCategoryFixture(childName, parentName))
	if out, err := h2.push(); err != nil {
		t.Fatalf("push child failed:\n%s", out)
	}
	if cat := queryCategory(t, childName); cat == nil {
		t.Fatalf("expected child category %q to be queryable, got nil", childName)
	}
	waitForResolvedPath(t, childName, []string{parentName, childName})
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
	if category := queryCategory(t, catName); category == nil {
		t.Fatalf("expected category %q to be queryable after push, got nil", catName)
	}

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
	if child := queryCategory(t, childName); child == nil {
		t.Fatalf("expected child category %q to be queryable, got nil", childName)
	}
	waitForResolvedPath(t, childName, []string{parentName, childName})
}

// waitForResolvedPath polls until name's status.resolved.path equals want.
// The hierarchy is materialized asynchronously by the controller.
func waitForResolvedPath(t *testing.T, name string, want []string) *categoryStatusResult {
	t.Helper()
	return waitForResolved(t, name, 30*time.Second, func(s *categoryStatusResult) bool {
		if s.Resolved == nil || len(s.Resolved.Path) != len(want) || s.Resolved.Depth != len(want)-1 {
			return false
		}
		for i := range want {
			if s.Resolved.Path[i] != want[i] {
				return false
			}
		}
		return true
	})
}

// A re-parent pushed for B moves B and every existing descendant once the
// controller cascade converges; the descendant itself is never re-pushed.
func TestCategoryResolvedPathAfterReparent(t *testing.T) {
	ts := time.Now().UnixNano()
	a := fmt.Sprintf("a-%d", ts)
	b := fmt.Sprintf("b-%d", ts)
	c := fmt.Sprintf("c-%d", ts)
	d := fmt.Sprintf("d-%d", ts)

	h := newPushHelper(t)
	h.commitCategory(a+".md", rootCategoryFixture(a))
	h.commitCategory(c+".md", rootCategoryFixture(c))
	h.commitCategory(b+".md", childCategoryFixture(b, a))
	h.commitCategory(d+".md", childCategoryFixture(d, b))
	if out, err := h.push(); err != nil {
		t.Fatalf("push hierarchy failed:\n%s", out)
	}
	waitForResolvedPath(t, d, []string{a, b, d})

	h2 := newPushHelper(t)
	h2.commitCategory(b+".md", childCategoryFixture(b, c))
	if out, err := h2.push(); err != nil {
		t.Fatalf("push re-parent failed:\n%s", out)
	}
	waitForResolvedPath(t, b, []string{c, b})
	waitForResolvedPath(t, d, []string{c, b, d})
}

// ── Spec 057: updateCategory re-parents in the originating repository ────────

const updateCategoryMutation = `
	mutation($input: UpdateCategoryInput!) {
		updateCategory(input: $input) {
			category { id body metadata { name namespace } spec { parentRef { name } } }
		}
	}
`

func categoryInput(namespace, name, parent string, body *string) map[string]any {
	spec := map[string]any{"title": name}
	if parent != "" {
		spec["parentRef"] = map[string]any{"name": parent}
	}
	input := map[string]any{
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     spec,
	}
	if body != nil {
		input["body"] = *body
	}
	return input
}

// T041: updateCategory writes to the repository and path the category was
// admitted from, not the namespace's system repository, and omitting body
// preserves the stored Markdown.
func TestUpdateCategoryReparentsAcrossRepository(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	oldParent := fmt.Sprintf("old-parent-%d", ts)
	newParent := fmt.Sprintf("new-parent-%d", ts)
	laptops := fmt.Sprintf("laptops-%d", ts)
	gaming := fmt.Sprintf("gaming-laptops-%d", ts)
	repoName := fmt.Sprintf("cat-repo-%d", ts)
	wantBody := fmt.Sprintf("Body that must survive re-parenting %d.", ts)

	token := namespaceContractBootstrapToken(t, apiURL)
	h := &namespaceContractHarness{t: t, apiURL: apiURL, token: token}
	repoID := createRepositoryAsUser(t, h, token, ns, repoName)
	t.Cleanup(func() { repositoryReadContractDelete(t, h, repoID) })
	waitForRepositoryReady(t, token, ns, repoName)

	push := newPushHelperForRepo(t, ns, repoName)
	push.commitCategory(oldParent+".md", rootCategoryFixture(oldParent))
	push.commitCategory(newParent+".md", rootCategoryFixture(newParent))
	push.commitCategory(laptops+".md", fmt.Sprintf(`---
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

%s
`, laptops, laptops, oldParent, wantBody))
	push.commitCategory(gaming+".md", childCategoryFixture(gaming, laptops))
	if out, err := push.push(); err != nil {
		t.Fatalf("push hierarchy to %s/%s failed:\n%s", ns, repoName, out)
	}
	waitForResolvedPath(t, gaming, []string{oldParent, laptops, gaming})

	// body omitted: the current Markdown body must be preserved.
	resp := gqlQueryWithURL(t, apiURL, token, updateCategoryMutation,
		map[string]any{"input": categoryInput(ns, laptops, newParent, nil)})
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		UpdateCategory struct {
			Category struct {
				Body *string `json:"body"`
				Spec struct {
					ParentRef *struct {
						Name string `json:"name"`
					} `json:"parentRef"`
				} `json:"spec"`
			} `json:"category"`
		} `json:"updateCategory"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.NotNil(t, data.UpdateCategory.Category.Spec.ParentRef)
	assert.Equal(t, newParent, data.UpdateCategory.Category.Spec.ParentRef.Name)

	// The commit lands at the originating repository's categories/<name>.md.
	pull := exec.Command("git", "pull", "--ff-only", "origin", "main")
	pull.Dir = push.workDir
	if out, err := pull.CombinedOutput(); err != nil {
		t.Fatalf("git pull %s/%s: %v\n%s", ns, repoName, err, out)
	}
	raw, err := os.ReadFile(filepath.Join(push.workDir, "categories", laptops+".md"))
	require.NoError(t, err, "updateCategory must commit to the originating repository")
	assert.Contains(t, string(raw), "name: "+newParent)
	assert.NotContains(t, string(raw), "name: "+oldParent)
	assert.Contains(t, string(raw), wantBody, "omitted body must be preserved in the committed manifest")

	// ... and not to the namespace's system repository.
	system := newPushHelperForRepo(t, ns, "gitstore-system")
	_, statErr := os.Stat(filepath.Join(system.workDir, "categories", laptops+".md"))
	assert.True(t, os.IsNotExist(statErr), "update must not write %s into gitstore-system", laptops)

	// The stored body is also served by the API.
	stored := gqlQueryWithURL(t, apiURL, token, `
		query($namespace: String!, $name: String!) {
			category(by: {namespacePath: {namespace: $namespace, name: $name}}) { body }
		}`, map[string]any{"namespace": ns, "name": laptops})
	require.Empty(t, stored.Errors, namespaceContractErrors(stored.Errors))
	assert.Contains(t, string(stored.Data), wantBody)

	waitForResolvedPath(t, laptops, []string{newParent, laptops})
	waitForResolvedPath(t, gaming, []string{newParent, laptops, gaming})
}

// ── Spec 057: authorization and no-disclosure ────────────────────────────────

type categoryGraphQLError struct {
	Extensions struct {
		Code        string `json:"code"`
		Phase       string `json:"phase"`
		Diagnostics []struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
			Level   string `json:"level"`
			File    string `json:"file"`
			Field   string `json:"field"`
		} `json:"diagnostics"`
	} `json:"extensions"`
}

func singleCategoryError(t *testing.T, resp gqlResponse) categoryGraphQLError {
	t.Helper()
	require.Len(t, resp.Errors, 1, namespaceContractErrors(resp.Errors))
	var e categoryGraphQLError
	require.NoError(t, json.Unmarshal(resp.Errors[0], &e))
	return e
}

func createCategoryAsUser(t *testing.T, h *namespaceContractHarness, token, namespace, name, parentNamespace string) gqlResponse {
	t.Helper()
	input := categoryInput(namespace, name, "", nil)
	if parentNamespace != "" {
		input["spec"].(map[string]any)["parentRef"] = map[string]any{"name": "target", "namespace": parentNamespace}
	}
	return h.gqlWithToken(token, `
		mutation($input: CreateCategoryInput!) {
			createCategory(input: $input) { category { id metadata { name namespace } } }
		}`, map[string]any{"input": input})
}

// T078: a user cannot create, update, delete or filter-list categories in
// another user's namespace; every denial is FORBIDDEN without diagnostics.
func TestCategoryAuthorization_TwoUserNamespaceIsolation(t *testing.T) {
	h := newNamespaceContractHarness(t)
	aliceToken := namespaceContractLogin(t, h, "alice", "admin123")
	bobToken := namespaceContractLogin(t, h, "bob", "admin123")
	aliceNS := uniqueName("cat-alice")
	bobNS := uniqueName("cat-bob")
	createNamespaceAsUser(t, h, aliceToken, aliceNS)
	createNamespaceAsUser(t, h, bobToken, bobNS)
	t.Cleanup(func() {
		h.cleanupNamespace(aliceNS)
		h.cleanupNamespace(bobNS)
	})

	root := uniqueName("root")
	created := createCategoryAsUser(t, h, aliceToken, aliceNS, root, "")
	require.Empty(t, created.Errors, namespaceContractErrors(created.Errors))
	var createdData struct {
		CreateCategory struct {
			Category struct {
				ID string `json:"id"`
			} `json:"category"`
		} `json:"createCategory"`
	}
	require.NoError(t, json.Unmarshal(created.Data, &createdData))
	aliceCategoryID := createdData.CreateCategory.Category.ID
	require.NotEmpty(t, aliceCategoryID)

	for _, tc := range []struct {
		name string
		resp func() gqlResponse
	}{
		{"create", func() gqlResponse {
			return createCategoryAsUser(t, h, bobToken, aliceNS, uniqueName("intruder"), "")
		}},
		{"update", func() gqlResponse {
			return h.gqlWithToken(bobToken, updateCategoryMutation,
				map[string]any{"input": categoryInput(aliceNS, root, "", nil)})
		}},
		{"delete", func() gqlResponse {
			return h.gqlWithToken(bobToken,
				`mutation($id: ID!) { deleteCategory(input: {id: $id}) { outcome } }`,
				map[string]any{"id": aliceCategoryID})
		}},
		{"filter_list", func() gqlResponse {
			return h.gqlWithToken(bobToken, `
				query($namespace: String!, $name: String!) {
					categories(namespace: $namespace, filter: {descendantOf: $name, includeSelf: true}, first: 10) {
						edges { node { id } }
					}
				}`, map[string]any{"namespace": aliceNS, "name": root})
		}},
	} {
		t.Run("bob_cannot_"+tc.name+"_in_alice_namespace", func(t *testing.T) {
			e := singleCategoryError(t, tc.resp())
			assert.Equal(t, "FORBIDDEN", e.Extensions.Code)
			assert.Empty(t, e.Extensions.Diagnostics, "FORBIDDEN must not carry diagnostics")
			assert.Empty(t, e.Extensions.Phase, "phase is only reported for ADMISSION_REJECTED")
		})
	}

	// Alice's category is untouched by the denied attempts.
	own := h.gqlWithToken(aliceToken, `
		query($namespace: String!, $name: String!) {
			category(by: {namespacePath: {namespace: $namespace, name: $name}}) { id }
		}`, map[string]any{"namespace": aliceNS, "name": root})
	require.Empty(t, own.Errors, namespaceContractErrors(own.Errors))
	assert.Contains(t, string(own.Data), aliceCategoryID)
}

// T078: a cross-namespace spec.parentRef.namespace is rejected without a
// lookup, so the diagnostic is identical whether or not the target exists.
func TestCategoryCrossNamespaceParentRef_NoDisclosureMutation(t *testing.T) {
	h := newNamespaceContractHarness(t)
	aliceToken := namespaceContractLogin(t, h, "alice", "admin123")
	bobToken := namespaceContractLogin(t, h, "bob", "admin123")
	aliceNS := uniqueName("cat-alice")
	bobNS := uniqueName("cat-bob")
	missingNS := uniqueName("cat-missing")
	createNamespaceAsUser(t, h, aliceToken, aliceNS)
	createNamespaceAsUser(t, h, bobToken, bobNS)
	t.Cleanup(func() {
		h.cleanupNamespace(aliceNS)
		h.cleanupNamespace(bobNS)
	})

	// Bob owns a category named "target"; missingNS does not exist at all.
	bobTarget := h.gqlWithToken(bobToken, `
		mutation($input: CreateCategoryInput!) { createCategory(input: $input) { category { id } } }`,
		map[string]any{"input": categoryInput(bobNS, "target", "", nil)})
	require.Empty(t, bobTarget.Errors, namespaceContractErrors(bobTarget.Errors))

	existing := singleCategoryError(t, createCategoryAsUser(t, h, aliceToken, aliceNS, uniqueName("child"), bobNS))
	missing := singleCategoryError(t, createCategoryAsUser(t, h, aliceToken, aliceNS, uniqueName("child"), missingNS))

	assert.Equal(t, "ADMISSION_REJECTED", existing.Extensions.Code)
	assert.Equal(t, existing.Extensions.Code, missing.Extensions.Code)
	require.NotEmpty(t, existing.Extensions.Diagnostics)
	require.Equal(t, len(existing.Extensions.Diagnostics), len(missing.Extensions.Diagnostics))
	for i, d := range existing.Extensions.Diagnostics {
		m := missing.Extensions.Diagnostics[i]
		assert.Equal(t, d.Reason, m.Reason)
		assert.Equal(t, d.Level, m.Level)
		assert.Equal(t, d.Field, m.Field)
		assert.Equal(t, strings.ReplaceAll(d.Message, bobNS, "<ns>"), strings.ReplaceAll(m.Message, missingNS, "<ns>"),
			"diagnostic must not depend on whether the target namespace/category exists")
		for _, msg := range []string{d.Message, m.Message} {
			lower := strings.ToLower(msg)
			assert.NotContains(t, lower, "not found")
			assert.NotContains(t, lower, "does not exist")
		}
	}
}

// T078: the pre-receive hook gives the same answer for a cross-namespace
// parentRef whether or not the referenced category exists.
func TestCategoryCrossNamespaceParentRef_NoDisclosurePush(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	token := namespaceContractBootstrapToken(t, apiURL)
	h := &namespaceContractHarness{t: t, apiURL: apiURL, token: token}
	otherNS := uniqueName("cat-other")
	createNamespaceAsUser(t, h, token, otherNS)
	t.Cleanup(func() { h.cleanupNamespace(otherNS) })
	waitForRepositoryReady(t, token, otherNS, "gitstore-system")

	known := uniqueName("known")
	created := gqlQueryWithURL(t, apiURL, token, `
		mutation($input: CreateCategoryInput!) { createCategory(input: $input) { category { id } } }`,
		map[string]any{"input": categoryInput(otherNS, known, "", nil)})
	require.Empty(t, created.Errors, namespaceContractErrors(created.Errors))

	crossRef := func(name, targetNS, target string) string {
		return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: %s
spec:
  title: %s
  parentRef:
    name: %s
    namespace: %s
    kind: CategoryTaxonomy
---
`, name, name, target, targetNS)
	}
	pushOutput := func(target string) string {
		name := uniqueName("cross")
		p := newPushHelperForRepo(t, ns, getEnv("REPOSITORY", "catalog"))
		p.commitCategory(name+".md", crossRef(name, otherNS, target))
		out, err := p.push()
		require.Error(t, err, "cross-namespace parentRef must be rejected:\n%s", out)
		return out
	}

	const want = "spec.parentRef.namespace must be empty or the category's own namespace"
	existingOut := pushOutput(known)
	missingOut := pushOutput(uniqueName("absent"))
	for _, out := range []string{existingOut, missingOut} {
		assert.Contains(t, out, want)
		lower := strings.ToLower(out)
		assert.NotContains(t, lower, "not found")
		assert.NotContains(t, lower, "does not exist")
	}
}
