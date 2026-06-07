// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestGitClone covers: git clone over port 5000 succeeds and produces a working repo.
func TestGitClone(t *testing.T) {
	namespace := getEnv("NAMESPACE", "gitstore-test")
	repository := getEnv("REPOSITORY", "catalog")
	remoteURL := fmt.Sprintf("%s/%s/%s.git", gitURL, namespace, repository)

	// Lightweight reachability check — skip rather than fail if stack is down.
	check := exec.Command("git", "ls-remote", remoteURL)
	if err := check.Run(); err != nil {
		t.Fatalf("PREREQUISITE: git smart HTTP unreachable at %s: %v — is the stack up?", remoteURL, err)
	}

	workDir := t.TempDir()
	cmd := exec.Command("git", "clone", remoteURL, workDir)
	cmd.Dir = os.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, out)
	}

	// Verify HEAD exists in the cloned repo.
	headFile := filepath.Join(workDir, ".git", "HEAD")
	if _, err := os.Stat(headFile); err != nil {
		t.Fatalf("cloned repo missing .git/HEAD: %v", err)
	}

	// Confirm at least one commit is present.
	logCmd := exec.Command("git", "log", "--oneline", "-1")
	logCmd.Dir = workDir
	out, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log failed in cloned repo: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Error("git log returned empty output — expected at least one commit")
	}
}

// TestGitFetch covers: after a push to the remote, git fetch brings the new ref locally.
func TestGitFetch(t *testing.T) {
	h := newPushHelper(t)

	ts := time.Now().UnixMilli()
	h.commitProduct(fmt.Sprintf("inttest-fetch-%d.md", ts), uniqueValidProduct(ts))
	if _, err := h.push(); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	// Clone a second copy and fetch — the pushed commit must appear.
	fetchDir := t.TempDir()
	namespace := getEnv("NAMESPACE", "gitstore-test")
	repository := getEnv("REPOSITORY", "catalog")
	remoteURL := fmt.Sprintf("%s/%s/%s.git", gitURL, namespace, repository)

	cloneCmd := exec.Command("git", "clone", remoteURL, fetchDir)
	cloneCmd.Dir = os.TempDir()
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("second clone failed: %v\n%s", err, out)
	}

	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = fetchDir
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		t.Fatalf("git fetch failed: %v\n%s", err, out)
	}

	// Verify the pushed commit is present in the fetch result.
	logCmd := exec.Command("git", "log", "--oneline", "origin/main")
	logCmd.Dir = fetchDir
	out, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log origin/main failed: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Error("git log origin/main returned empty output")
	}
}

// TestGitPush covers: a local commit can be pushed and the ref tip advances on the remote.
func TestGitPush(t *testing.T) {
	h := newPushHelper(t)

	// Record the current HEAD before push.
	preCmd := exec.Command("git", "rev-parse", "HEAD")
	preCmd.Dir = h.workDir
	preSHA, err := preCmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD before push: %v", err)
	}

	ts := time.Now().UnixMilli()
	h.commitProduct(fmt.Sprintf("inttest-push-%d.md", ts), uniqueValidProduct(ts))
	if out, err := h.push(); err != nil {
		t.Fatalf("git push failed: %v\n%s", err, out)
	}

	// HEAD after push must differ from HEAD before.
	postCmd := exec.Command("git", "rev-parse", "HEAD")
	postCmd.Dir = h.workDir
	postSHA, err := postCmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD after push: %v", err)
	}
	if string(preSHA) == string(postSHA) {
		t.Error("HEAD SHA did not advance after push — commit may not have been recorded")
	}

	// Verify the remote ref tip matches local HEAD.
	namespace := getEnv("NAMESPACE", "gitstore-test")
	repository := getEnv("REPOSITORY", "catalog")
	remoteURL := fmt.Sprintf("%s/%s/%s.git", gitURL, namespace, repository)
	lsCmd := exec.Command("git", "ls-remote", remoteURL, "refs/heads/main")
	lsOut, err := lsCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote failed: %v\n%s", err, lsOut)
	}

	localHead := string(postSHA[:len(postSHA)-1]) // strip newline
	if len(lsOut) == 0 {
		t.Fatal("ls-remote returned empty — refs/heads/main not found on remote")
	}
	remoteSHA := string(lsOut[:40])
	if remoteSHA != localHead {
		t.Errorf("remote refs/heads/main = %s, want %s", remoteSHA, localHead)
	}
}
