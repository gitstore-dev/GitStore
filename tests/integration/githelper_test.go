// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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
	if out, err := gitFixtureCommand(t, workDir, "ls-remote", remoteURL); err != nil {
		t.Fatalf("PREREQUISITE: gitstore-git-service catalog repo unreachable: %v\n%s — is docker compose up?", err, out)
	}

	if out, err := gitFixtureCommand(t, filepath.Dir(workDir), "clone", remoteURL, workDir); err != nil {
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
	out, err := gitFixtureCommand(h.t, h.workDir, "push", "origin", "main")
	return string(out), err
}

// pushTag pushes an annotated tag to the remote.
func (h *pushHelper) pushTag(tag string) (string, error) {
	h.t.Helper()
	run(h.t, h.workDir, "git", "tag", "-a", tag, "-m", fmt.Sprintf("integration test tag %s", tag))
	out, err := gitFixtureCommand(h.t, h.workDir, "push", "origin", tag)
	return string(out), err
}

// run executes a command and fails the test on error.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := fixtureCommandContext(t, 0)
	defer cancel()
	if out, err := fixtureCommandOutput(ctx, dir, legacyGitFixtureEnv(), nil, args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const fixtureCommandWaitDelay = time.Second

func fixtureCommandContext(t *testing.T, operationTimeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	deadline, ok := t.Deadline()
	if ok {
		// Leave time for cancellation and inherited-pipe cleanup before Go's hard timeout.
		deadline = deadline.Add(-2 * fixtureCommandWaitDelay)
	}
	if operationTimeout > 0 {
		operationDeadline := time.Now().Add(operationTimeout)
		if !ok || operationDeadline.Before(deadline) {
			deadline, ok = operationDeadline, true
		}
	}
	if !ok {
		t.Fatal("Git fixture commands require go test -timeout or an explicit command context")
	}
	return context.WithDeadline(t.Context(), deadline)
}

func legacyGitFixtureEnv() []string {
	// These fixtures deliberately inherit CI's Git HTTP authentication configuration.
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}

func gitFixtureCommand(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := fixtureCommandContext(t, 0)
	defer cancel()
	return fixtureCommandOutput(ctx, dir, legacyGitFixtureEnv(), nil, append([]string{"git"}, args...)...)
}

func gitFixtureStdout(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := fixtureCommandContext(t, 0)
	defer cancel()
	return fixtureCommandRun(ctx, dir, legacyGitFixtureEnv(), nil, true, append([]string{"git"}, args...)...)
}

var (
	fixtureAuthorization  = regexp.MustCompile(`(?i)(authorization:\s*(?:basic|bearer)\s+)([^\s"'\\]+)`)
	fixtureURLCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s]+)@`)
)

func redactFixtureCommand(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	value = fixtureAuthorization.ReplaceAllString(value, "${1}[redacted]")
	return fixtureURLCredentials.ReplaceAllString(value, "${1}[redacted]@")
}

type fixtureCommandError struct {
	cause   error
	message string
}

func (err *fixtureCommandError) Error() string { return err.message }
func (err *fixtureCommandError) Unwrap() error { return err.cause }

func fixtureCommandOutput(ctx context.Context, dir string, env, secrets []string, args ...string) ([]byte, error) {
	return fixtureCommandRun(ctx, dir, env, secrets, false, args...)
}

func fixtureCommandRun(ctx context.Context, dir string, env, secrets []string, stdoutOnly bool, args ...string) ([]byte, error) {
	// Credentials supplied through Git configuration can also appear without a header.
	secrets = append([]string{}, secrets...)
	for _, value := range append(append([]string{}, env...), args...) {
		for _, match := range fixtureAuthorization.FindAllStringSubmatch(value, -1) {
			secrets = append(secrets, match[2])
		}
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Dir = dir
	command.Env = env
	command.WaitDelay = fixtureCommandWaitDelay
	var output []byte
	var err error
	if stdoutOnly {
		output, err = command.Output()
	} else {
		output, err = command.CombinedOutput()
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitError.Stderr = []byte(redactFixtureCommand(string(exitError.Stderr), secrets))
		}
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		err = &fixtureCommandError{cause: err, message: redactFixtureCommand(fmt.Sprintf("command %v failed: %v", args, err), secrets)}
	}
	return []byte(redactFixtureCommand(string(output), secrets)), err
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
				got, err := gitFixtureCommand(t, dir, "show", "HEAD:"+path)
				if err != nil {
					t.Fatalf("read committed fixture: %v\n%s", err, got)
				}
				if string(got) != content {
					t.Fatalf("committed %s = %q, want %q", path, got, content)
				}
			}

			got, err := gitFixtureCommand(t, dir, "log", "--format=%s")
			if err != nil {
				t.Fatalf("read fixture commits: %v\n%s", err, got)
			}
			want := strings.Repeat(test.verb+" "+filename+"\n", 2)
			if string(got) != want {
				t.Fatalf("commit messages = %q, want %q", got, want)
			}
			got, err = gitFixtureCommand(t, dir, "status", "--porcelain")
			if err != nil || len(got) != 0 {
				t.Fatalf("fixture worktree not clean: %v\n%s", err, got)
			}
		})
	}
}

func TestPushHelperLocalGitRoundTrip(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	root := t.TempDir()
	remote := filepath.Join(root, "owned", "catalog.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0700); err != nil {
		t.Fatal(err)
	}
	run(t, root, "git", "init", "--bare", "--initial-branch=main", remote)
	previousURL := gitURL
	gitURL = root
	t.Cleanup(func() { gitURL = previousURL })
	h := newPushHelperForRepo(t, "owned", "catalog")
	h.commitProduct("fixture.md", "first fixture\n")
	if out, err := h.push(); err != nil {
		t.Fatalf("local fixture push failed: %v\n%s", err, out)
	}
	if out, err := h.pushTag("fixture-tag"); err != nil {
		t.Fatalf("local fixture tag push failed: %v\n%s", err, out)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := gitFixtureCommand(t, root, "clone", remote, clone); err != nil {
		t.Fatalf("local fixture clone failed: %v\n%s", err, out)
	}
	h.commitProduct("fixture.md", "updated fixture\n")
	if out, err := h.push(); err != nil {
		t.Fatalf("local fixture update failed: %v\n%s", err, out)
	}
	if out, err := gitFixtureCommand(t, clone, "fetch", "origin"); err != nil {
		t.Fatalf("local fixture fetch failed: %v\n%s", err, out)
	}
	content, err := gitFixtureCommand(t, clone, "show", "origin/main:products/fixture.md")
	if err != nil || string(content) != "updated fixture\n" {
		t.Fatalf("fetched fixture update missing: %v\n%s", err, content)
	}
	content, err = gitFixtureCommand(t, clone, "show", "fixture-tag:products/fixture.md")
	if err != nil || string(content) != "first fixture\n" {
		t.Fatalf("annotated fixture tag did not retain the original commit: %v\n%s", err, content)
	}
}

func TestPushHelperCommandSubprocess(t *testing.T) {
	if os.Getenv("GITSTORE_FIXTURE_COMMAND_CHILD") != "1" {
		return
	}
	args := os.Args
	for args[0] != "--" {
		args = args[1:]
	}
	switch args[1] {
	case "hold":
		time.Sleep(10 * time.Second)
	case "exit-with-child", "cancel-with-child":
		child := exec.Command(os.Args[0], "-test.run=^TestPushHelperCommandSubprocess$", "--", "hold")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
		fmt.Print("child started")
		if err := os.WriteFile(args[2], []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			panic(err)
		}
		if args[1] == "cancel-with-child" {
			time.Sleep(10 * time.Second)
		}
	case "credentials":
		fmt.Println("useful diagnostic:", os.Getenv("FIXTURE_SECRET"), args[2], "https://user:url-password@example.invalid/catalog")
		fmt.Fprintln(os.Stderr, os.Getenv("GIT_CONFIG_VALUE_0"))
		os.Exit(7)
	default:
		panic("unknown fixture subprocess mode")
	}
	os.Exit(0)
}

func TestPushHelperCommandBoundsInheritedPipes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exit-with-child", "cancel-with-child"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			pidPath := filepath.Join(dir, "child.pid")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			type result struct {
				output []byte
				err    error
			}
			done := make(chan result, 1)
			go func() {
				env := append(os.Environ(), "GITSTORE_FIXTURE_COMMAND_CHILD=1", "GORACE=atexit_sleep_ms=0")
				output, err := fixtureCommandOutput(ctx, dir, env, nil, executable, "-test.run=^TestPushHelperCommandSubprocess$", "--", mode, pidPath)
				done <- result{output, err}
			}()
			var pid int
			for pid == 0 {
				if data, err := os.ReadFile(pidPath); err == nil {
					pid, _ = strconv.Atoi(string(data))
				}
				if pid != 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("fixture child did not start before deadline")
				case <-time.After(10 * time.Millisecond):
				}
			}
			t.Cleanup(func() {
				child, err := os.FindProcess(pid)
				if err == nil {
					_ = child.Kill()
					_ = child.Release()
				}
			})
			if mode == "cancel-with-child" {
				cancel()
			}
			select {
			case got := <-done:
				if !strings.Contains(string(got.output), "child started") {
					t.Fatalf("fixture diagnostic lost: %q", got.output)
				}
				if mode == "exit-with-child" && !errors.Is(got.err, exec.ErrWaitDelay) {
					t.Fatalf("inherited pipes must report WaitDelay, got %v", got.err)
				}
				if mode == "cancel-with-child" && !errors.Is(got.err, context.Canceled) {
					t.Fatalf("cancellation must remain identifiable, got %v", got.err)
				}
			case <-time.After(3 * fixtureCommandWaitDelay):
				t.Fatal("fixture command remained blocked by inherited child pipes")
			}
		})
	}
}

func TestPushHelperCommandRedactsCredentials(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GITSTORE_FIXTURE_COMMAND_CHILD=1", "GORACE=atexit_sleep_ms=0",
		"FIXTURE_SECRET=explicit-secret", "GIT_CONFIG_VALUE_0=Authorization: Basic environment-secret")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, err := fixtureCommandOutput(ctx, t.TempDir(), env, []string{"explicit-secret"},
		executable, "-test.run=^TestPushHelperCommandSubprocess$", "--", "credentials", "Authorization: Bearer argument-secret")
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 7 {
		t.Fatalf("nonzero subprocess exit lost: %v", err)
	}
	for _, value := range []string{"explicit-secret", "environment-secret", "argument-secret", "url-password"} {
		if strings.Contains(string(output), value) || strings.Contains(err.Error(), value) {
			t.Fatal("fixture output or error exposed a credential")
		}
	}
	if !strings.Contains(string(output), "useful diagnostic:") || !strings.Contains(string(output), "[redacted]") {
		t.Fatalf("redaction removed the useful diagnostic: %q", output)
	}
	output, err = fixtureCommandRun(ctx, t.TempDir(), env, []string{"explicit-secret"}, true,
		executable, "-test.run=^TestPushHelperCommandSubprocess$", "--", "credentials", "Authorization: Bearer argument-secret")
	if !errors.As(err, &exitError) || strings.Contains(string(output), "Authorization: Basic") ||
		!strings.Contains(string(output), "useful diagnostic:") {
		t.Fatal("stdout-only fixture reads must not mix in subprocess stderr")
	}
	if strings.Contains(string(exitError.Stderr), "environment-secret") || !strings.Contains(string(exitError.Stderr), "[redacted]") {
		t.Fatal("stdout-only fixture failures must retain sanitized stderr diagnostics")
	}
}

func TestPushHelperGitEnvironmentPolicies(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(global, []byte("[http]\n\textraHeader = Authorization: Basic inherited-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	output, err := gitFixtureCommand(t, dir, "config", "--get-all", "http.extraHeader")
	if err != nil || strings.TrimSpace(string(output)) != "Authorization: Basic [redacted]" {
		t.Fatalf("legacy fixture must retain configured CI authentication without exposing it: %v\n%s", err, output)
	}
	t.Setenv("GIT_DIR", filepath.Join(dir, "unrelated-repository"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: Bearer unrelated-secret")
	git := rbacNativeGit{dir: dir, fixture: rbacNativeFixture{token: "owned-fixture-secret"}}
	output, err = git.command(t, "init", "--initial-branch=main")
	if err != nil {
		t.Fatalf("RBAC runner inherited repository selection: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "HEAD")); err != nil {
		t.Fatal("RBAC runner did not initialize its owned repository")
	}
	output, err = git.command(t, "config", "--get-all", "http.extraHeader")
	if err != nil || strings.TrimSpace(string(output)) != "Authorization: Bearer [redacted]" {
		t.Fatalf("RBAC runner must isolate credentials and redact returned output: %v\n%s", err, output)
	}
}
