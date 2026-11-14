// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// uniqueValidProduct returns a Kubernetes-style product fixture with a
// timestamp-unique name so repeated test runs never collide on name in the catalog.
func uniqueValidProduct(ts int64) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: inttest-%d
  namespace: %s
spec:
  title: Integration Test Product
  tags: [integration, test]
---

Integration test product for core-stack contract verification.
`, ts, getEnv("NAMESPACE", "gitstore-test"))
}

// pushHelper holds state for a single test push scenario.
type pushHelper struct {
	t         *testing.T
	workDir   string
	remoteURL string
}

// newPushHelper creates a temp git working directory and clones the catalog repo.
// Fails the test if the git server is unreachable.
func newPushHelper(t *testing.T) *pushHelper {
	t.Helper()

	namespace := getEnv("NAMESPACE", "gitstore-test")
	repository := getEnv("REPOSITORY", "catalog")
	return newPushHelperForRepo(t, namespace, repository)
}

func newPushHelperForRepo(t *testing.T, namespace, repository string) *pushHelper {
	t.Helper()

	workDir := t.TempDir()
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
	h.commitFile("products", filename, content, "add")
}

// commitCollection writes a Collection markdown file and commits it.
func (h *pushHelper) commitCollection(filename, content string) {
	h.t.Helper()
	h.commitFile("collections", filename, content, "add")
}

func (h *pushHelper) commitNamespace(filename, content string) {
	h.t.Helper()
	h.commitFile("namespaces", filename, content, "update")
}

// commitRepository writes a declarative Repository manifest and commits it.
// Repository manifests are admitted from the authoring repository's
// repositories/ directory, just like the post-receive hook supplies them.
func (h *pushHelper) commitRepository(filename, content string) {
	h.t.Helper()
	h.commitFile("repositories", filename, content, "add")
}

func (h *pushHelper) commitFile(directory, filename, content, verb string) {
	h.t.Helper()
	dir := filepath.Join(h.workDir, directory)
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatalf("mkdir %s: %v", directory, err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatalf("write %s: %v", path, err)
	}
	run(h.t, h.workDir, "git", "add", path)
	run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("%s %s", verb, filename))
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

func TestPushHelperCommitsResources(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "Integration Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "inttest@gitstore.dev")
	t.Setenv("GIT_COMMITTER_NAME", "Integration Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "inttest@gitstore.dev")

	for _, test := range []struct {
		directory, verb string
		commit          func(*pushHelper, string, string)
	}{
		{"products", "add", (*pushHelper).commitProduct},
		{"collections", "add", (*pushHelper).commitCollection},
		{"namespaces", "update", (*pushHelper).commitNamespace},
		{"repositories", "add", (*pushHelper).commitRepository},
		{"variants", "add", (*pushHelper).commitVariant},
	} {
		t.Run(test.directory, func(t *testing.T) {
			dir := t.TempDir()
			run(t, dir, "git", "init", "--initial-branch=main")
			h := &pushHelper{t: t, workDir: dir}
			filename := "scenario fixture.md"
			path := filepath.ToSlash(filepath.Join(test.directory, filename))
			for _, content := range []string{"original scenario\n", "updated scenario\n"} {
				test.commit(h, filename, content)
				cmd := exec.Command("git", "show", "HEAD:"+path)
				cmd.Dir = dir
				got, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("read committed fixture: %v\n%s", err, got)
				}
				if string(got) != content {
					t.Fatalf("committed %s = %q, want %q", path, got, content)
				}
			}
			cmd := exec.Command("git", "log", "--format=%s")
			cmd.Dir = dir
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("read fixture commits: %v\n%s", err, got)
			}
			want := strings.Repeat(test.verb+" "+filename+"\n", 2)
			if string(got) != want {
				t.Fatalf("commit messages = %q, want %q", got, want)
			}
			cmd = exec.Command("git", "status", "--porcelain")
			cmd.Dir = dir
			got, err = cmd.CombinedOutput()
			if err != nil || len(got) != 0 {
				t.Fatalf("fixture worktree not clean: %v\n%s", err, got)
			}
		})
	}
}
