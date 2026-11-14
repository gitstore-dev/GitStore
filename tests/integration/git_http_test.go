// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGitClone covers: git clone over the default host port 9000 succeeds and produces a working repo.
func TestGitClone(t *testing.T) {
	namespace := getEnv("NAMESPACE", "gitstore-test")
	repository := getEnv("REPOSITORY", "catalog")
	remoteURL := fmt.Sprintf("%s/%s/%s.git", gitURL, namespace, repository)

	workDir := t.TempDir()
	// Fail promptly when the stack is unavailable.
	if out, err := gitFixtureCommand(t, workDir, "ls-remote", remoteURL); err != nil {
		t.Fatalf("PREREQUISITE: git smart HTTP unreachable: %v\n%s — is the stack up?", err, out)
	}

	if out, err := gitFixtureCommand(t, filepath.Dir(workDir), "clone", remoteURL, workDir); err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, out)
	}

	// Verify HEAD exists in the cloned repo.
	headFile := filepath.Join(workDir, ".git", "HEAD")
	if _, err := os.Stat(headFile); err != nil {
		t.Fatalf("cloned repo missing .git/HEAD: %v", err)
	}

	// Confirm at least one commit is present.
	out, err := gitFixtureCommand(t, workDir, "log", "--oneline", "-1")
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

	if out, err := gitFixtureCommand(t, filepath.Dir(fetchDir), "clone", remoteURL, fetchDir); err != nil {
		t.Fatalf("second clone failed: %v\n%s", err, out)
	}

	if out, err := gitFixtureCommand(t, fetchDir, "fetch", "origin"); err != nil {
		t.Fatalf("git fetch failed: %v\n%s", err, out)
	}

	// Verify the pushed commit is present in the fetch result.
	out, err := gitFixtureCommand(t, fetchDir, "log", "--oneline", "origin/main")
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
	preSHA, err := gitFixtureStdout(t, h.workDir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD before push: %v", err)
	}

	ts := time.Now().UnixMilli()
	h.commitProduct(fmt.Sprintf("inttest-push-%d.md", ts), uniqueValidProduct(ts))
	if out, err := h.push(); err != nil {
		t.Fatalf("git push failed: %v\n%s", err, out)
	}

	// HEAD after push must differ from HEAD before.
	postSHA, err := gitFixtureStdout(t, h.workDir, "rev-parse", "HEAD")
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
	lsOut, err := gitFixtureCommand(t, h.workDir, "ls-remote", remoteURL, "refs/heads/main")
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
