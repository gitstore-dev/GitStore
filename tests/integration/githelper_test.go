// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	t              *testing.T
	workDir        string
	remoteURL      string
	token          string
	provenancePath string
}

// newPushHelper creates a temp git working directory and clones the catalog repo.
// Skips the test if the git server is unreachable.
func newPushHelper(t *testing.T) *pushHelper {
	t.Helper()

	namespace := getEnv("NAMESPACE", "gitstore-test")
	repository := getEnv("REPOSITORY", "catalog")
	return newPushHelperForRepo(t, namespace, repository)
}

func newPushHelperForRepo(t *testing.T, namespace, repository string) *pushHelper {
	t.Helper()
	return newPushHelperForRepoWithToken(t, namespace, repository, integrationAdminToken(t))
}

func newPushHelperForRepoWithToken(t *testing.T, namespace, repository, token string, provenancePaths ...string) *pushHelper {
	t.Helper()
	remoteURL := fmt.Sprintf("%s/%s/%s.git", gitURL, namespace, repository)
	return newPushHelperForRemoteWithToken(t, remoteURL, token, provenancePaths...)
}

func newPushHelperForRemoteWithToken(t *testing.T, remoteURL, token string, provenancePaths ...string) *pushHelper {
	t.Helper()
	require.LessOrEqual(t, len(provenancePaths), 1)

	workDir := t.TempDir()
	if _, err := secretCapacityGit(t.Context(), "", token, "ls-remote", remoteURL); err != nil {
		t.Fatalf("PREREQUISITE: Git repository unreachable at %s: %v", remoteURL, err)
	}
	cloneArgs := []string{"clone"}
	if len(provenancePaths) != 0 || os.Getenv("REPOSITORY_LIFECYCLE_CAPACITY_RUN") == "1" || os.Getenv("NAMESPACE_WATCH_CAPACITY_RUN") == "1" {
		cloneArgs = append(cloneArgs, "--no-tags", "--depth=1")
	}
	if len(provenancePaths) != 0 {
		cloneArgs = append(cloneArgs, "--no-checkout")
	}
	cloneArgs = append(cloneArgs, "--", remoteURL, workDir)
	if _, err := secretCapacityGit(t.Context(), filepath.Dir(workDir), token, cloneArgs...); err != nil {
		t.Fatalf("PREREQUISITE: Git clone failed: %v", err)
	}

	// Configure git identity and ensure the default branch is "main"
	// regardless of the system-level init.defaultBranch setting.
	run(t, workDir, "git", "config", "user.email", "inttest@gitstore.dev")
	run(t, workDir, "git", "config", "user.name", "Integration Test")
	run(t, workDir, "git", "symbolic-ref", "HEAD", "refs/heads/main")

	h := &pushHelper{t: t, workDir: workDir, remoteURL: remoteURL, token: token}
	if len(provenancePaths) != 0 {
		h.provenancePath = provenancePaths[0]
		_, err := secretCapacityGit(t.Context(), workDir, token, "checkout", "HEAD", "--", h.provenancePath)
		require.NoError(t, err, "check out only the provenance manifest")
	}
	return h
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

// commitCollection writes a Collection markdown file and commits it.
func (h *pushHelper) commitCollection(filename, content string) {
	h.t.Helper()
	dir := filepath.Join(h.workDir, "collections")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatalf("mkdir collections: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatalf("write collection file: %v", err)
	}
	run(h.t, h.workDir, "git", "add", path)
	run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("add %s", filename))
}

func (h *pushHelper) commitNamespace(filename, content string) {
	h.t.Helper()
	dir := filepath.Join(h.workDir, "namespaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatalf("mkdir namespaces: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatalf("write namespace file: %v", err)
	}
	run(h.t, h.workDir, "git", "add", path)
	if h.provenancePath != "" {
		// A path-only clone has no other files in its index. --only takes the
		// remaining tree from HEAD instead of recording unrelated deletions.
		run(h.t, h.workDir, "git", "commit", "--only", "-m", fmt.Sprintf("update %s", filename), "--", path)
	} else {
		run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("update %s", filename))
	}
}

// commitRepository writes a declarative Repository manifest and commits it.
// Repository manifests are admitted from the authoring repository's
// repositories/ directory, just like the post-receive hook supplies them.
func (h *pushHelper) commitRepository(filename, content string) {
	h.t.Helper()
	dir := filepath.Join(h.workDir, "repositories")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatalf("mkdir repositories: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatalf("write repository file: %v", err)
	}
	run(h.t, h.workDir, "git", "add", path)
	run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("add %s", filename))
}

// push executes git push and returns (stdout+stderr, error).
func (h *pushHelper) push() (string, error) {
	h.t.Helper()
	if h.token != "" {
		out, err := secretCapacityGit(h.t.Context(), h.workDir, h.token, "push", "origin", "main")
		return string(out), err
	}
	cmd := exec.Command("git", "push", "origin", "main")
	cmd.Dir = h.workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func pushWatchMetadataProvenance(t *testing.T, h *pushHelper) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for attempt := 1; attempt <= 8; attempt++ {
		pushCtx, traceID, traceErr := secretCapacityPushTrace(ctx, rand.Reader)
		require.NoError(t, traceErr, "create bounded provenance push trace identity")
		rejectionSite := "unclassified"
		pushStarted := time.Now()
		_, err := secretCapacityGitInspect(pushCtx, h.workDir, h.token, func(output []byte) {
			rejectionSite = watchMetadataPushRejectionSite(output)
		}, "push", "origin", "main")
		if err == nil {
			rejectionSite = "none"
		}
		t.Logf("provenance push attempt=%d duration=%s succeeded=%t rejection_site=%s trace_id=%s",
			attempt, time.Since(pushStarted), err == nil, rejectionSite, traceID)
		if err == nil {
			revision, revisionErr := secretCapacityGit(ctx, h.workDir, h.token, "rev-parse", "HEAD")
			if revisionErr != nil {
				t.Fatalf("read accepted provenance revision: %v", revisionErr)
			}
			accepted := "main@sha1:" + strings.TrimSpace(string(revision))
			t.Logf("provenance push accepted revision=%s", accepted)
			return accepted
		}
		class := secretCapacityFailureClass(err)
		if class != "non_fast_forward" || attempt == 8 {
			t.Fatalf("provenance push failed after %d attempts (%s): %v", attempt, class, err)
		}
		t.Logf("provenance push retry=%d classification=%s", attempt, class)
		fetchCtx, fetchTraceID, traceErr := secretCapacityPushTrace(ctx, rand.Reader)
		require.NoError(t, traceErr, "create bounded provenance fetch trace identity")
		fetchStarted := time.Now()
		if _, err := secretCapacityGitInspect(fetchCtx, h.workDir, h.token, func(output []byte) {
			for _, line := range strings.Split(string(output), "\n") {
				if !strings.HasPrefix(line, "fatal: ") && !strings.HasPrefix(line, "error: ") {
					continue
				}
				if h.token != "" {
					line = strings.ReplaceAll(line, h.token, "[REDACTED]")
				}
				t.Logf("provenance rebase Git diagnostic: %.512s", line)
			}
		}, "fetch", "--quiet", "--depth=1", "origin", "main"); err != nil {
			t.Fatalf("refresh bounded provenance authoring tree: %v", err)
		}
		t.Logf("provenance refresh attempt=%d fetch_duration=%s trace_id=%s", attempt, time.Since(fetchStarted), fetchTraceID)
		prepareStarted := time.Now()
		if h.provenancePath != "" {
			require.NoError(t, refreshWatchMetadataProvenancePath(ctx, h), "refresh path-only provenance rewrite")
		} else {
			// Replay only our single comment commit, not the deployment's history.
			if _, err := secretCapacityGit(ctx, h.workDir, h.token, "rebase", "--onto", "FETCH_HEAD", "HEAD^"); err != nil {
				t.Fatalf("rebase provenance rewrite onto concurrent authoring: %v", err)
			}
		}
		t.Logf("provenance refresh attempt=%d prepare_duration=%s", attempt, time.Since(prepareStarted))
	}
	t.Fatal("provenance push exhausted bounded retries")
	return ""
}

func watchMetadataPushRejectionSite(output []byte) string {
	client, remote := false, false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "!" {
			continue
		}
		client = client || fields[1] == "[rejected]"
		remote = remote || fields[1] == "[remote" && fields[2] == "rejected]"
	}
	switch {
	case client && remote:
		return "mixed"
	case client:
		return "client_advertised_ref"
	case remote:
		return "remote_ref_update"
	default:
		return "unclassified"
	}
}

func TestWatchMetadataProvenanceRejectionSite(t *testing.T) {
	for _, test := range []struct {
		name, output, want string
	}{
		{"client fetch first", " ! [rejected] main -> main (fetch first)\n", "client_advertised_ref"},
		{"client non fast forward", " ! [rejected] main -> main (non-fast-forward)\n", "client_advertised_ref"},
		{"remote", " ! [remote rejected] main -> main (non-fast-forward)\n", "remote_ref_update"},
		{"mixed", " ! [rejected] a -> a\n ! [remote rejected] b -> b\n", "mixed"},
		{"remote echoed client text", "remote: ! [rejected] private-marker -> main\n", "unclassified"},
		{"unknown", "fatal: private-marker\n", "unclassified"},
		{"empty", "", "unclassified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := watchMetadataPushRejectionSite([]byte(test.output))
			require.Equal(t, test.want, result)
			require.NotContains(t, result, "private-marker")
		})
	}
}

func refreshWatchMetadataProvenancePath(ctx context.Context, h *pushHelper) error {
	changed, err := secretCapacityGit(ctx, h.workDir, "", "diff", "--name-only", "HEAD^", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(changed)) != h.provenancePath {
		return fmt.Errorf("provenance commit must change only %s", h.provenancePath)
	}
	if _, err := secretCapacityGit(ctx, h.workDir, "", "diff", "--quiet", "HEAD^", "FETCH_HEAD", "--", h.provenancePath); err != nil {
		return fmt.Errorf("provenance manifest changed concurrently; refusing to overwrite it: %w", err)
	}
	revision, err := secretCapacityGit(ctx, h.workDir, "", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	// Move only our local parent, retaining the comment rewrite in the one
	// checked-out file. --only preserves FETCH_HEAD's unrelated tree entries
	// without checking out or rebasing the deployment's full resource tree.
	if _, err := secretCapacityGit(ctx, h.workDir, "", "reset", "--soft", "FETCH_HEAD"); err != nil {
		return err
	}
	_, err = secretCapacityGit(ctx, h.workDir, "", "commit", "--quiet", "--only", "--reuse-message="+strings.TrimSpace(string(revision)), "--", h.provenancePath)
	return err
}

func TestWatchMetadataProvenanceRebasesOnlyConcurrentAuthoring(t *testing.T) {
	remote, author, peer := t.TempDir(), t.TempDir(), t.TempDir()
	git := func(directory string, args ...string) []byte {
		t.Helper()
		output, err := secretCapacityGit(t.Context(), directory, "", args...)
		require.NoError(t, err)
		return output
	}
	git(remote, "init", "--bare", "--initial-branch=main")
	git(author, "clone", "--", remote, ".")
	git(author, "config", "user.email", "inttest@gitstore.dev")
	git(author, "config", "user.name", "Integration Test")
	h := &pushHelper{t: t, workDir: author}
	h.commitNamespace("probe.md", "---\nkind: Namespace\n---\n")
	git(author, "push", "origin", "main")
	git(peer, "clone", "--", remote, ".")
	git(peer, "config", "user.email", "inttest@gitstore.dev")
	git(peer, "config", "user.name", "Integration Test")
	peerHelper := &pushHelper{t: t, workDir: peer}
	peerHelper.commitNamespace("concurrent.md", "---\nkind: Namespace\n---\n")
	git(peer, "push", "origin", "main")
	h.commitNamespace("probe.md", "---\n# provenance-only rewrite\nkind: Namespace\n---\n")
	before := strings.TrimSpace(string(git(author, "rev-parse", "HEAD")))
	revision := pushWatchMetadataProvenance(t, h)
	accepted := strings.TrimSpace(string(git(remote, "rev-parse", "refs/heads/main")))
	require.Equal(t, "main@sha1:"+accepted, revision)
	require.NotEqual(t, before, accepted, "accepted provenance must identify the rebased commit")
	require.Equal(t, "---\n# provenance-only rewrite\nkind: Namespace\n---\n", string(git(remote, "show", "main:namespaces/probe.md")))
	require.Equal(t, "---\nkind: Namespace\n---\n", string(git(remote, "show", "main:namespaces/concurrent.md")))
}

func TestWatchMetadataProvenancePathOnlyPreparation(t *testing.T) {
	t.Setenv("NAMESPACE_WATCH_CAPACITY_RUN", "")
	t.Setenv("REPOSITORY_LIFECYCLE_CAPACITY_RUN", "")
	const manifest = "---\nkind: Namespace\nmetadata:\n  name: probe\nspec:\n  title: Probe\n---\n"
	const rewrite = "---\n# provenance-only rewrite\nkind: Namespace\nmetadata:\n  name: probe\nspec:\n  title: Probe\n---\n"
	for _, scenario := range []string{"unrelated authoring", "manifest edited", "manifest deleted", "extra local change"} {
		t.Run(scenario, func(t *testing.T) {
			remote, peer := t.TempDir(), t.TempDir()
			git := func(directory string, args ...string) []byte {
				t.Helper()
				output, err := secretCapacityGit(t.Context(), directory, "", args...)
				require.NoError(t, err)
				return output
			}
			git(remote, "init", "--bare", "--initial-branch=main")
			git(peer, "clone", "--", remote, ".")
			git(peer, "config", "user.email", "inttest@gitstore.dev")
			git(peer, "config", "user.name", "Integration Test")
			peerHelper := &pushHelper{t: t, workDir: peer}
			peerHelper.commitNamespace("probe.md", manifest)
			peerHelper.commitProduct("untouched.md", "unrelated catalog content\n")
			git(peer, "push", "origin", "main")
			h := newPushHelperForRemoteWithToken(t, "file://"+remote, "", "namespaces/probe.md")
			require.Equal(t, "true\n", string(git(h.workDir, "rev-parse", "--is-shallow-repository")))
			require.NoDirExists(t, filepath.Join(h.workDir, "products"), "initial preparation must not check out the catalog")
			h.commitNamespace("probe.md", rewrite)
			if scenario == "extra local change" {
				require.NoError(t, os.WriteFile(filepath.Join(h.workDir, "extra.md"), []byte("not provenance\n"), 0o600))
				git(h.workDir, "add", "--", "extra.md")
				git(h.workDir, "commit", "--only", "--amend", "--no-edit", "--", "extra.md")
			}
			before := strings.TrimSpace(string(git(h.workDir, "rev-parse", "HEAD")))
			switch scenario {
			case "manifest edited":
				peerHelper.commitNamespace("probe.md", strings.Replace(manifest, "title: Probe", "title: Concurrent", 1))
			case "manifest deleted":
				git(peer, "rm", "--", "namespaces/probe.md")
				git(peer, "commit", "-m", "delete probe")
			default:
				peerHelper.commitNamespace("concurrent.md", "---\nkind: Namespace\n---\n")
			}
			git(peer, "push", "origin", "main")
			remoteBefore := strings.TrimSpace(string(git(remote, "rev-parse", "main")))
			if scenario != "unrelated authoring" {
				git(h.workDir, "fetch", "--quiet", "--depth=1", "origin", "main")
				err := refreshWatchMetadataProvenancePath(t.Context(), h)
				require.Error(t, err)
				if scenario == "extra local change" {
					require.ErrorContains(t, err, "must change only")
				} else {
					require.ErrorContains(t, err, "refusing to overwrite")
				}
				require.Equal(t, before, strings.TrimSpace(string(git(h.workDir, "rev-parse", "HEAD"))))
				require.Equal(t, remoteBefore, strings.TrimSpace(string(git(remote, "rev-parse", "main"))))
				return
			}
			for round := range 2 {
				git(h.workDir, "fetch", "--quiet", "--depth=1", "origin", "main")
				require.NoError(t, refreshWatchMetadataProvenancePath(t.Context(), h))
				require.Equal(t, remoteBefore, strings.TrimSpace(string(git(h.workDir, "rev-parse", "HEAD^"))))
				peerHelper.commitProduct("untouched.md", fmt.Sprintf("concurrent catalog content %d\n", round))
				git(peer, "push", "origin", "main")
				remoteBefore = strings.TrimSpace(string(git(remote, "rev-parse", "main")))
			}
			revision := pushWatchMetadataProvenance(t, h)
			accepted := strings.TrimSpace(string(git(remote, "rev-parse", "main")))
			require.Equal(t, "main@sha1:"+accepted, revision)
			require.NotEqual(t, before, accepted)
			require.Equal(t, remoteBefore, strings.TrimSpace(string(git(h.workDir, "rev-parse", "HEAD^"))))
			require.Equal(t, "namespaces/probe.md\n", string(git(h.workDir, "diff", "--name-only", "HEAD^", "HEAD")))
			require.Equal(t, rewrite, string(git(remote, "show", "main:namespaces/probe.md")))
			require.Equal(t, "---\nkind: Namespace\n---\n", string(git(remote, "show", "main:namespaces/concurrent.md")))
			require.Equal(t, "concurrent catalog content 1\n", string(git(remote, "show", "main:products/untouched.md")))
			require.NoDirExists(t, filepath.Join(h.workDir, "products"), "retry must not materialize unrelated catalog paths")
			require.NoFileExists(t, filepath.Join(h.workDir, "namespaces", "concurrent.md"))
		})
	}
}

// pushTag pushes an annotated tag to the remote.
func (h *pushHelper) pushTag(tag string) (string, error) {
	h.t.Helper()
	run(h.t, h.workDir, "git", "tag", "-a", tag, "-m", fmt.Sprintf("integration test tag %s", tag))
	if h.token != "" {
		out, err := secretCapacityGit(h.t.Context(), h.workDir, h.token, "push", "origin", tag)
		return string(out), err
	}
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
