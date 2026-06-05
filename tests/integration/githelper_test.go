// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)


// uniqueValidProduct returns a valid product fixture with a timestamp-unique ID and SKU
// so repeated test runs never collide on filename or SKU in the catalog.
func uniqueValidProduct(ts int64) string {
	return fmt.Sprintf(`---
id: prod_inttest_%d
sku: INTTEST-%d
title: Integration Test Product
price: 99.99
currency: USD
category_id: cat_electronics_001
collection_ids: []
images: []
inventory_status: in_stock
inventory_quantity: 10
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Integration test product for core-stack contract verification.
`, ts, ts)
}

// pushHelper holds state for a single test push scenario.
type pushHelper struct {
	t       *testing.T
	workDir string
	remoteURL string
}

// newPushHelper creates a temp git working directory and clones the catalog repo.
// Skips the test if the git server is unreachable.
func newPushHelper(t *testing.T) *pushHelper {
	t.Helper()

	workDir := t.TempDir()
	namespace := getEnv("NAMESPACE", "gitstore")
	repository := getEnv("REPOSITORY", "catalog")
	remoteURL := fmt.Sprintf("%s/%s/%s.git", gitURL, namespace, repository)

	// Try a lightweight reachability check before cloning.
	checkCmd := exec.Command("git", "ls-remote", remoteURL)
	if err := checkCmd.Run(); err != nil {
		t.Fatalf("PREREQUISITE: gitstore-git-service catalog repo unreachable at %s: %v — is docker compose up?", remoteURL, err)
	}

	cloneCmd := exec.Command("git", "clone", remoteURL, workDir)
	cloneCmd.Dir = os.TempDir()
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("PREREQUISITE: could not clone catalog repo: %v\n%s", err, out)
	}

	// Configure git identity and ensure the default branch is "main"
	// regardless of the system-level init.defaultBranch setting.
	run(t, workDir, "git", "config", "user.email", "inttest@gitstore.dev")
	run(t, workDir, "git", "config", "user.name", "Integration Test")
	run(t, workDir, "git", "symbolic-ref", "HEAD", "refs/heads/main")

	return &pushHelper{t: t, workDir: workDir, remoteURL: remoteURL}
}

// commitProduct writes a product markdown file and commits it.
func (h *pushHelper) commitProduct(filename, content string) {
	h.t.Helper()
	dir := filepath.Join(h.workDir, "products")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatalf("mkdir products: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatalf("write product file: %v", err)
	}
	run(h.t, h.workDir, "git", "add", path)
	run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("add %s", filename))
}

// push executes git push and returns (stdout+stderr, error).
func (h *pushHelper) push() (string, error) {
	h.t.Helper()
	cmd := exec.Command("git", "push", "origin", "main")
	cmd.Dir = h.workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// pushTag pushes an annotated tag to the remote.
func (h *pushHelper) pushTag(tag string) (string, error) {
	h.t.Helper()
	run(h.t, h.workDir, "git", "tag", "-a", tag, "-m", fmt.Sprintf("integration test tag %s", tag))
	cmd := exec.Command("git", "push", "origin", tag)
	cmd.Dir = h.workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// run executes a command and fails the test on error.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}
}
