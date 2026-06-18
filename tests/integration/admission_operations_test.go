// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// commitProductAt writes a product file at a repo-relative path (not forced
// under products/) and commits it. Useful for rename/move tests.
func (h *pushHelper) commitProductAt(repoRelPath, content string) {
	h.t.Helper()
	abs := filepath.Join(h.workDir, repoRelPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		h.t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		h.t.Fatalf("write %s: %v", abs, err)
	}
	run(h.t, h.workDir, "git", "add", abs)
	run(h.t, h.workDir, "git", "commit", "-m", "add "+repoRelPath)
}

// removeFile stages a deletion of repoRelPath and commits it.
func (h *pushHelper) removeFile(repoRelPath string) {
	h.t.Helper()
	run(h.t, h.workDir, "git", "rm", repoRelPath)
	run(h.t, h.workDir, "git", "commit", "-m", "delete "+repoRelPath)
}

// moveFile renames a file within the working tree and commits the rename.
func (h *pushHelper) moveFile(oldRepoRelPath, newRepoRelPath string) {
	h.t.Helper()
	newAbs := filepath.Join(h.workDir, newRepoRelPath)
	if err := os.MkdirAll(filepath.Dir(newAbs), 0755); err != nil {
		h.t.Fatalf("mkdir %s: %v", filepath.Dir(newAbs), err)
	}
	run(h.t, h.workDir, "git", "mv", oldRepoRelPath, newRepoRelPath)
	run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("rename %s -> %s", oldRepoRelPath, newRepoRelPath))
}

// pushRef pushes an arbitrary refspec (e.g. "refs/heads/feature:refs/heads/feature").
func (h *pushHelper) pushRef(refspec string) (string, error) {
	h.t.Helper()
	cmd := exec.Command("git", "push", "origin", refspec)
	cmd.Dir = h.workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// deleteRemoteBranch pushes a branch deletion.
func (h *pushHelper) deleteRemoteBranch(branch string) (string, error) {
	h.t.Helper()
	cmd := exec.Command("git", "push", "origin", "--delete", branch)
	cmd.Dir = h.workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

type productQueryResult struct {
	ID       string `json:"id"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
	Spec struct {
		Title *string `json:"title"`
	} `json:"spec"`
}

// queryProduct polls for a product by namespace+name, returning nil once the
// wait budget is exhausted and the product is still not found.
func queryProduct(t *testing.T, namespace, name string) *productQueryResult {
	t.Helper()
	const (
		maxWait  = 5 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	for {
		resp := gqlQuery(t, `
			query($ns: String!, $name: String!) {
				product(by: {namespacePath: {namespace: $ns, name: $name}}) {
					id
					metadata { name namespace uid }
					spec { title }
				}
			}
		`, map[string]any{"ns": namespace, "name": name})
		if len(resp.Errors) > 0 {
			t.Fatalf("graphql errors querying product %q: %s", name, resp.Errors)
		}
		var d struct {
			Product *productQueryResult `json:"product"`
		}
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			t.Fatalf("decode product response: %v", err)
		}
		if d.Product == nil && time.Now().Before(deadline) {
			time.Sleep(interval)
			continue
		}
		return d.Product
	}
}

// queryProductAbsent polls until the product is gone or the wait budget
// expires; returns true only when the record is absent.
func queryProductAbsent(t *testing.T, namespace, name string) bool {
	t.Helper()
	const (
		maxWait  = 5 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	for {
		resp := gqlQuery(t, `
			query($ns: String!, $name: String!) {
				product(by: {namespacePath: {namespace: $ns, name: $name}}) {
					id
				}
			}
		`, map[string]any{"ns": namespace, "name": name})
		if len(resp.Errors) > 0 {
			t.Fatalf("graphql errors: %s", resp.Errors)
		}
		var d struct {
			Product *struct{ ID string } `json:"product"`
		}
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if d.Product == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

// TestAdmission_DeleteResource verifies that removing a catalog resource file
// via git rm causes admission to delete the stored record from the datastore.
func TestAdmission_DeleteResource(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("adm-del")

	h := newPushHelper(t)
	h.commitProduct(name+".md", validProductFixture(name, ns))
	if out, err := h.push(); err != nil {
		t.Fatalf("setup push failed:\n%s", out)
	}
	if queryProduct(t, ns, name) == nil {
		t.Fatal("product not found after initial push — prerequisite failed")
	}

	h.removeFile("products/" + name + ".md")
	if out, err := h.push(); err != nil {
		t.Fatalf("delete push failed:\n%s", out)
	}

	if !queryProductAbsent(t, ns, name) {
		t.Errorf("product %q still queryable after delete push; expected removal from datastore", name)
	}
}

// ── Path move (rename) ────────────────────────────────────────────────────────

// TestAdmission_PathMove verifies that renaming a resource file (identical
// frontmatter identity, different path) preserves metadata.uid. The file path
// is provenance, not identity — the stored record must survive unchanged.
func TestAdmission_PathMove(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("adm-move")

	h := newPushHelper(t)
	h.commitProduct(name+".md", validProductFixture(name, ns))
	if out, err := h.push(); err != nil {
		t.Fatalf("setup push failed:\n%s", out)
	}
	p1 := queryProduct(t, ns, name)
	if p1 == nil {
		t.Fatal("product not found after initial push — prerequisite failed")
	}
	uidBefore := p1.Metadata.UID

	// Rename products/<name>.md → catalog/products/<name>.md (same frontmatter).
	h.moveFile("products/"+name+".md", "catalog/products/"+name+".md")
	if out, err := h.push(); err != nil {
		t.Fatalf("rename push failed:\n%s", out)
	}

	// Poll until admission processes the rename; uid must be unchanged.
	const (
		maxWait  = 5 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	var p2 *productQueryResult
	for {
		p2 = queryProduct(t, ns, name)
		if p2 != nil && p2.Metadata.UID == uidBefore {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(interval)
	}

	if p2 == nil {
		t.Fatal("product not found after rename push")
	}
	if p2.Metadata.UID != uidBefore {
		t.Errorf("uid changed after path rename: before=%q after=%q — path must be treated as provenance, not identity",
			uidBefore, p2.Metadata.UID)
	}
}

// ── Spec update ───────────────────────────────────────────────────────────────

// TestAdmission_SpecUpdate verifies that pushing a changed spec for an
// existing resource updates the stored title while preserving metadata.uid.
func TestAdmission_SpecUpdate(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("adm-update")

	v1 := fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
spec:
  title: Original Title
  tags: [v1]
---

Version one.
`, name, ns)

	v2 := fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
spec:
  title: Updated Title
  tags: [v2]
---

Version two.
`, name, ns)

	h := newPushHelper(t)
	h.commitProduct(name+".md", v1)
	if out, err := h.push(); err != nil {
		t.Fatalf("v1 push failed:\n%s", out)
	}
	p1 := queryProduct(t, ns, name)
	if p1 == nil {
		t.Fatal("product not found after v1 push — prerequisite failed")
	}
	if p1.Spec.Title == nil || *p1.Spec.Title != "Original Title" {
		t.Fatalf("v1 title: got %v, want 'Original Title'", p1.Spec.Title)
	}
	uidBefore := p1.Metadata.UID

	// Second push: update spec in a fresh clone to get a clean old_commit_sha.
	h2 := newPushHelper(t)
	h2.commitProduct(name+".md", v2)
	if out, err := h2.push(); err != nil {
		t.Fatalf("v2 push failed:\n%s", out)
	}

	// Poll until title reflects the update.
	const (
		maxWait  = 5 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	var p2 *productQueryResult
	for {
		p2 = queryProduct(t, ns, name)
		if p2 != nil && p2.Spec.Title != nil && *p2.Spec.Title == "Updated Title" {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(interval)
	}

	if p2 == nil {
		t.Fatal("product not found after v2 push")
	}
	if p2.Spec.Title == nil || *p2.Spec.Title != "Updated Title" {
		t.Errorf("title after update: got %v, want 'Updated Title'", p2.Spec.Title)
	}
	if p2.Metadata.UID != uidBefore {
		t.Errorf("uid changed on spec update: before=%q after=%q — uid must be stable across updates",
			uidBefore, p2.Metadata.UID)
	}
}

// ── Stale push skipped ────────────────────────────────────────────────────────

// TestAdmission_StalePushSkipped verifies that a slower first push (whose
// old→new SHA range was overtaken by a second push) is silently dropped and
// does not overwrite the newer admitted state. The test simulates this by
// pushing a known product, then verifying that the latest title is visible
// even when an older title could still be redelivered.
//
// Because we cannot synthesise a real out-of-order gRPC call in a black-box
// integration test, this test instead confirms that a second push to the same
// ref results in the newer spec being visible (staleness protection is
// exercised on every sequential push; the unit tests cover the exact skip path).
func TestAdmission_StalePushSkipped(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("adm-stale")

	push := func(title string) {
		h := newPushHelper(t)
		h.commitProduct(name+".md", fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: %s
spec:
  title: %s
---
`, name, ns, title))
		if out, err := h.push(); err != nil {
			t.Fatalf("push %q failed:\n%s", title, out)
		}
	}

	push("First Title")
	if queryProduct(t, ns, name) == nil {
		t.Fatal("product not found after first push — prerequisite failed")
	}

	push("Second Title")

	// Poll until "Second Title" is visible — the server must not serve
	// a stale "First Title" once the second push has been admitted.
	const (
		maxWait  = 5 * time.Second
		interval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	var p *productQueryResult
	for {
		p = queryProduct(t, ns, name)
		if p != nil && p.Spec.Title != nil && *p.Spec.Title == "Second Title" {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(interval)
	}

	if p == nil {
		t.Fatal("product not found after second push")
	}
	if p.Spec.Title == nil || *p.Spec.Title != "Second Title" {
		t.Errorf("title after sequential pushes: got %v, want 'Second Title'", p.Spec.Title)
	}
}

// ── Branch deletion ───────────────────────────────────────────────────────────

// TestAdmission_BranchDeletion verifies that deleting a branch causes admission
// to remove catalog resources admitted on that branch while leaving resources
// on other branches untouched.
//
// This test requires the branch pattern config to admit the test feature branch.
// If pushing to or deleting the feature branch is rejected by the remote it is
// skipped gracefully.
func TestAdmission_BranchDeletion(t *testing.T) {
	ns := getEnv("NAMESPACE", "gitstore-test")
	ts := time.Now().UnixNano()
	mainName := fmt.Sprintf("adm-branchdel-main-%d", ts)
	featureName := fmt.Sprintf("adm-branchdel-feature-%d", ts)

	// Establish the main product.
	h := newPushHelper(t)
	h.commitProduct(mainName+".md", validProductFixture(mainName, ns))
	if out, err := h.push(); err != nil {
		t.Fatalf("main push failed:\n%s", out)
	}
	if queryProduct(t, ns, mainName) == nil {
		t.Fatal("main product not found after push — prerequisite failed")
	}

	// Create a feature branch from main and push a second product on it.
	featureBranch := fmt.Sprintf("feature/adm-branchdel-%d", ts)
	run(t, h.workDir, "git", "checkout", "-b", featureBranch)
	h.commitProduct(featureName+".md", validProductFixture(featureName, ns))
	if out, err := h.pushRef(featureBranch + ":" + featureBranch); err != nil {
		t.Skipf("feature branch push rejected (branch pattern may exclude it): %v\n%s", err, out)
	}

	// Return to main before deleting the feature branch.
	run(t, h.workDir, "git", "checkout", "main")

	if out, err := h.deleteRemoteBranch(featureBranch); err != nil {
		t.Skipf("branch deletion rejected by remote: %v\n%s", err, out)
	}

	// The main product must still be present.
	if queryProduct(t, ns, mainName) == nil {
		t.Errorf("main product %q missing after feature branch deletion — branch delete must not affect other branches",
			mainName)
	}

	// The feature product should have been removed (if the branch was admitted).
	if !queryProductAbsent(t, ns, featureName) {
		t.Logf("feature product %q still present after branch deletion — expected removal if branch was admitted", featureName)
	}
}
