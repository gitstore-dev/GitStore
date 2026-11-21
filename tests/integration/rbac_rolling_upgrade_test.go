// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// This is the pre-RBAC production release, not a current binary with altered
// definitions. Its 001-013 bundle and running static provider are the baseline.
const rbacRollingBaseline = "5d71e9b05267f8371e8370f5a4639bd175bef9e2"

func rbacRollingAwaitReady(ctx context.Context, exited <-chan struct{}, timeout time.Duration, probe func() bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-exited:
			return errors.New("owned API exited before readiness")
		default:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		ready := probe()
		select {
		case <-exited:
			return errors.New("owned API exited before readiness")
		default:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-exited:
			return errors.New("owned API exited before readiness")
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func TestRBACRollingReadinessRefusesExitedCandidatesWithoutWaitingForDeadline(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	err := rbacRollingAwaitReady(t.Context(), exited, time.Minute, func() bool {
		t.Fatal("an exited candidate must not reach the readiness probe")
		return false
	})
	require.ErrorContains(t, err, "exited before readiness")
	exitsDuringProbe := make(chan struct{})
	err = rbacRollingAwaitReady(t.Context(), exitsDuringProbe, time.Minute, func() bool {
		close(exitsDuringProbe)
		return true
	})
	require.ErrorContains(t, err, "exited before readiness", "a successful probe cannot mask observed process exit")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	err = rbacRollingAwaitReady(canceled, make(chan struct{}), time.Minute, func() bool {
		t.Fatal("a canceled readiness check must not probe")
		return false
	})
	require.ErrorIs(t, err, context.Canceled)
	err = rbacRollingAwaitReady(t.Context(), make(chan struct{}), 0, func() bool {
		t.Fatal("an expired readiness deadline must not probe")
		return false
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, rbacRollingAwaitReady(t.Context(), make(chan struct{}), time.Minute, func() bool { return true }))
}

func rbacRollingCanonicalTip(output string) (string, error) {
	line := strings.TrimSpace(output)
	fields := strings.Fields(line)
	if strings.ContainsAny(line, "\r\n") || len(fields) != 2 || fields[1] != "refs/heads/main" {
		return "", errors.New("canonical advertisement must contain exactly one refs/heads/main record")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(fields[0]) {
		return "", errors.New("canonical advertisement requires an exact SHA-1 object ID")
	}
	return fields[0], nil
}

func TestRBACRollingCanonicalTipUsesActualGitAdvertisementAndSourceCommands(t *testing.T) {
	git := rbacNativeGit{dir: t.TempDir()}
	_, err := git.command(t, "init", "--initial-branch=main")
	require.NoError(t, err)
	path := "rbac/clusterroles/seeded-role.yaml"
	git.write(t, path, rbacNativeFixture{}.manifest("ClusterRole", "seeded-role"))
	commit := git.commit(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	_, err = git.command(t, "clone", "--bare", git.dir, remote)
	require.NoError(t, err)
	_, err = git.command(t, "remote", "add", "origin", remote)
	require.NoError(t, err)
	advertisement, err := git.command(t, "ls-remote", "origin", "refs/heads/main")
	require.NoError(t, err)
	tip, err := rbacRollingCanonicalTip(string(advertisement))
	require.NoError(t, err)
	require.Equal(t, commit, tip)
	_, err = git.command(t, "fetch", "--quiet", "origin", "refs/heads/main")
	require.NoError(t, err)
	_, err = git.command(t, "merge-base", "--is-ancestor", tip, "FETCH_HEAD")
	require.NoError(t, err)
	paths, err := git.command(t, "ls-tree", "--name-only", "FETCH_HEAD", "--", path)
	require.NoError(t, err)
	require.Equal(t, path, strings.TrimSpace(string(paths)))
	expected, err := os.ReadFile(filepath.Join(git.dir, path))
	require.NoError(t, err)
	for _, revision := range []string{tip, "FETCH_HEAD"} {
		content, err := git.command(t, "show", revision+":"+path)
		require.NoError(t, err)
		require.Equal(t, expected, content)
	}
	for _, malformed := range []string{
		"", commit, commit + "\trefs/tags/main\n", "not-a-hash\trefs/heads/main\n",
		commit + "\trefs/heads/main\n" + commit + "\trefs/heads/other\n",
		commit + "\nrefs/heads/main\n",
	} {
		_, err := rbacRollingCanonicalTip(malformed)
		require.Error(t, err, "never infer a canonical ref/hash from malformed output")
	}
}

func rbacRollingVerifyCanonicalSource(resource rbacNativeResource, manifest map[string]any, data []byte, digest [32]byte) error {
	if resource.Metadata.UID == "" {
		return errors.New("canonical source requires an authoritative resource incarnation")
	}
	if sha256.Sum256(data) != digest {
		return errors.New("seeded canonical blob changed")
	}
	// Git stores author-controlled fields; immutable resource UIDs belong to
	// the authoritative projection, checked separately on each traffic target.
	var authored struct {
		APIVersion string                `yaml:"apiVersion"`
		Kind       string                `yaml:"kind"`
		Metadata   struct{ Name string } `yaml:"metadata"`
		Spec       map[string]any        `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &authored); err != nil {
		return err
	}
	if authored.APIVersion != manifest["apiVersion"] || authored.Kind != resource.Kind ||
		authored.Metadata.Name != resource.Metadata.Name {
		return errors.New("canonical author identity does not match the seeded incarnation")
	}
	actualSpec, err := json.Marshal(authored.Spec)
	if err != nil {
		return err
	}
	expectedSpec, err := json.Marshal(manifest["spec"])
	if err != nil {
		return err
	}
	if !bytes.Equal(actualSpec, expectedSpec) {
		return errors.New("canonical spec does not match the seeded author content")
	}
	return nil
}

func TestRBACRollingCanonicalSourceRequiresAuthoritativeIdentityAndSeededContent(t *testing.T) {
	canonical := []byte("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: ClusterRole\nmetadata:\n  name: seeded-role\nspec:\n  rules:\n    - resources: [namespace]\n      verbs: [list]\n")
	resource := rbacNativeResource{Kind: "ClusterRole"}
	resource.Metadata.Name, resource.Metadata.UID = "seeded-role", "seeded-authoritative-uid"
	fixture := rbacNativeFixture{}
	manifest := fixture.manifest("ClusterRole", resource.Metadata.Name)
	manifest["spec"] = map[string]any{"rules": []any{map[string]any{"resources": []string{"namespace"}, "verbs": []string{"list"}}}}
	digest := sha256.Sum256(canonical)
	require.NoError(t, rbacRollingVerifyCanonicalSource(resource, manifest, canonical, digest),
		"canonical author content intentionally has no server-managed UID")
	missing := resource
	missing.Metadata.UID = ""
	require.ErrorContains(t, rbacRollingVerifyCanonicalSource(missing, manifest, canonical, digest), "authoritative")
	changed := bytes.Replace(canonical, []byte("[list]"), []byte("[delete]"), 1)
	require.ErrorContains(t, rbacRollingVerifyCanonicalSource(resource, manifest, changed, digest), "blob changed")
	wrongIdentity := bytes.Replace(canonical, []byte("seeded-role"), []byte("different-role"), 1)
	require.ErrorContains(t, rbacRollingVerifyCanonicalSource(resource, manifest, wrongIdentity, sha256.Sum256(wrongIdentity)), "author identity")
	wrongSpec := fixture.manifest("ClusterRole", resource.Metadata.Name)
	require.ErrorContains(t, rbacRollingVerifyCanonicalSource(resource, wrongSpec, canonical, digest), "canonical spec")
	binding := rbacNativeResource{Kind: "ClusterRoleBinding"}
	binding.Metadata.Name, binding.Metadata.UID = "seeded-binding", "seeded-binding-uid"
	bindingManifest := fixture.manifest(binding.Kind, binding.Metadata.Name)
	bindingSource := []byte("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: ClusterRoleBinding\nmetadata:\n  name: seeded-binding\nspec:\n  roleRef: {kind: ClusterRole, name: view}\n  subjects: [{kind: User, name: native-rbac-nonexistent-subject}]\n")
	require.NoError(t, rbacRollingVerifyCanonicalSource(binding, bindingManifest, bindingSource, sha256.Sum256(bindingSource)),
		"binding author kinds remain canonical, not GraphQL enum spellings")
}

func TestRBACOwnedFeatureDisabledRollingUpgrade(t *testing.T) {
	if os.Getenv("RBAC_TEST_ROLLING_RUN") != "1" || os.Getenv("USER_DIRECTORY_TEST_OWNED_DEPLOYMENT") != "1" {
		t.Skip("explicit owned physical RBAC rolling matrix not selected")
	}
	for _, tool := range []string{"docker", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("owned rolling fixture prerequisite unavailable: %s", tool)
		}
	}
	require.NotEqual(t, "1", os.Getenv("USER_DIRECTORY_TEST_CAPACITY"),
		"this fresh functional fixture must not use retained capacity assets")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	scratchRoot := filepath.Join(root, ".gitstore")
	require.NoError(t, os.MkdirAll(scratchRoot, 0700))
	scratch, err := os.MkdirTemp(scratchRoot, "rbac-rolling-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("retained private rolling failure artifacts: %s", scratch)
			return
		}
		require.NoError(t, os.RemoveAll(scratch))
	})
	// Existing fixture helpers use testing.TempDir; keep their private files
	// inside this fresh checkout-owned directory as well.
	t.Setenv("TMPDIR", scratch)
	run := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, args[0], args[1:]...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
		output, err := command.CombinedOutput()
		require.NoError(t, err, "owned rollout command failed: %s", output)
		return string(output)
	}
	source := filepath.Join(scratch, "baseline")
	require.NoError(t, os.Mkdir(source, 0700))
	archive := filepath.Join(scratch, "baseline.tar")
	run(root, "git", "archive", "--format=tar", "--output="+archive, rbacRollingBaseline)
	run(source, "tar", "-xf", archive)
	oldBinary := filepath.Join(scratch, "baseline-api")
	run(filepath.Join(source, "gitstore-api"), "go", "build", "-o", oldBinary, "./cmd/server")
	migrator := filepath.Join(scratch, "gitctl")
	run(filepath.Join(root, "gitstore-api"), "go", "build", "-o", migrator, "./cmd/gitctl")

	r := startDirectoryTestRuntime(t)
	r.initialAPIBinary = oldBinary
	r.initialAPIHasUserDirectory = true
	// Functional compatibility only: epoll retains the pinned one-shard 1,024
	// storage IOCBs without allocating linux-aio networking/preemption contexts.
	r.scyllaReactorBackend = "epoll"
	r.apiFixtureDirectory = filepath.Join(scratch, "runtime")
	require.NoError(t, os.Mkdir(r.apiFixtureDirectory, 0700))
	operator := r.createHuman(t, "staff", "rbac-rollout-operator", "rbac-rollout@example.test")
	customer := r.createHuman(t, "customer", "rbac-rollout-customer", "rbac-customer@example.test")
	for _, identity := range []directoryTestIdentity{operator, customer} {
		status, _ := r.request(t, http.MethodPatch, "/admin/identities/"+identity.ID, []map[string]any{
			{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
				{"value": identity.Traits["email"], "verified": true, "via": "email", "status": "completed"},
			}},
			{"op": "add", "path": "/metadata_admin/setup_complete", "value": true},
		})
		require.Equal(t, http.StatusOK, status)
	}
	apis, token := r.startAPIs(t, operator.ID, true)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for i, api := range apis {
			log := r.command(t, nil, "logs", api.name)
			require.NoError(t, os.WriteFile(filepath.Join(scratch, fmt.Sprintf("api-%d.log", i)),
				[]byte(strings.ReplaceAll(log, r.secret, "[redacted]")), 0600))
		}
	})
	operatorToken := token(operator.ID)
	oldBytes, err := os.ReadFile(oldBinary)
	require.NoError(t, err)
	newBytes, err := os.ReadFile(r.currentAPIBinary)
	require.NoError(t, err)
	require.NotEqual(t, sha256.Sum256(oldBytes), sha256.Sum256(newBytes),
		"old/new compatibility must exercise distinct production executables")
	t.Logf("owned fixture=%s baseline_sha256=%x current_sha256=%x reactor=epoll",
		r.owner[:12], sha256.Sum256(oldBytes), sha256.Sum256(newBytes))

	owned := func(name string) {
		t.Helper()
		require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{index .Config.Labels "dev.gitstore.test_owner"}}`, name)))
	}
	started := func(name string) string {
		t.Helper()
		owned(name)
		return strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", name))
	}
	gitName := "gitstore-userdir-git-" + r.owner[:12]
	scyllaName := "gitstore-userdir-scylla-" + r.owner[:12]
	gitEpoch, oldPeerEpoch := started(gitName), started(apis[1].name)
	history := func() string {
		t.Helper()
		owned(scyllaName)
		return r.command(t, nil, "exec", scyllaName, "cqlsh", "-e",
			"SELECT name, checksum, done, start_time, end_time FROM gitstore.gocqlx_migrate;")
	}
	initialHistory := history()
	require.Contains(t, initialHistory, "013_service_account_cdc.cql")
	require.NotContains(t, initialHistory, "014_rbac.cql")
	key, err := newSecretCapacityOwnedKey()
	require.NoError(t, err)
	create := r.graphql(t, apis[0], operatorToken, fmt.Sprintf(
		`mutation{createServiceAccount(input:{metadata:{name:"owned-rbac-rollout"},publicKeys:[{kid:%q,algorithm:"Ed25519",publicKeyPEM:%q}]}){serviceAccount{id metadata{uid name resourceVersion}}}}`,
		key.ID, key.Public))
	require.Empty(t, create.Errors)
	require.Contains(t, string(create.Data["createServiceAccount"]), "owned-rbac-rollout")
	var created struct {
		ServiceAccount struct {
			ID string `json:"id"`
		} `json:"serviceAccount"`
	}
	require.NoError(t, json.Unmarshal(create.Data["createServiceAccount"], &created))
	require.NotEmpty(t, created.ServiceAccount.ID)
	read := fmt.Sprintf(`{node(id:%q){id ... on ServiceAccount{metadata{uid name resourceVersion}}}}`, created.ServiceAccount.ID)
	seed := r.graphql(t, apis[0], operatorToken, read)
	require.Empty(t, seed.Errors)
	require.NotEmpty(t, seed.Data["node"])
	assertSeed := func(api directoryAPI) {
		t.Helper()
		response := r.graphql(t, api, operatorToken, read)
		require.Empty(t, response.Errors)
		require.JSONEq(t, string(seed.Data["node"]), string(response.Data["node"]),
			"seeded UID, resource version and caller authority must survive physical replacement")
		denied := r.graphql(t, api, token(customer.ID), read)
		require.NotEmpty(t, denied.Errors, "an untrusted subject must not inherit operator authority")
	}
	// ServiceAccount admission is authoritative datastore state, not Git
	// publication. Seed an independent real repository in the owned Git volume
	// to check physical history preservation without claiming publication.
	gitSeed := filepath.Join(scratch, "git-seed")
	require.NoError(t, os.Mkdir(gitSeed, 0700))
	run(gitSeed, "git", "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(gitSeed, "serviceaccount.json"), seed.Data["node"], 0600))
	run(gitSeed, "git", "add", "serviceaccount.json")
	run(gitSeed, "git", "-c", "user.name=Owned Rolling Fixture",
		"-c", "user.email=rbac-rollout@example.test", "commit", "-m", "Seed owned rolling source identity")
	seedCommit := strings.TrimSpace(run(gitSeed, "git", "rev-parse", "HEAD"))
	gitRepo := filepath.Join(filepath.Dir(apis[0].binaryFile), "repos", "00", "00",
		"00000000-0000-4000-8000-"+r.owner[:12]+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(gitRepo), 0700))
	run(scratch, "git", "clone", "--bare", gitSeed, gitRepo)
	refs := func() map[string]string {
		t.Helper()
		result := make(map[string]string)
		repos := filepath.Join(filepath.Dir(apis[0].binaryFile), "repos")
		require.NoError(t, filepath.WalkDir(repos, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && strings.HasSuffix(entry.Name(), ".git") {
				result[path] = run(root, "git", "--git-dir="+path, "for-each-ref",
					"--format=%(refname) %(objectname)")
				return filepath.SkipDir
			}
			return nil
		}))
		require.NotEmpty(t, result, "seeded admission must retain real native Git storage")
		require.Contains(t, result[gitRepo], "refs/heads/main "+seedCommit)
		require.JSONEq(t, string(seed.Data["node"]),
			run(root, "git", "--git-dir="+gitRepo, "show", "refs/heads/main:serviceaccount.json"))
		return result
	}
	seedRefs := refs()
	assertSeed(apis[1])
	replace := func(i int) {
		t.Helper()
		owned(apis[i].name)
		epoch := started(apis[i].name)
		r.command(t, nil, "restart", "--time", "5", apis[i].name)
		require.NotEqual(t, epoch, started(apis[i].name), "a replacement must change its physical process epoch")
	}
	waitReady := func(i int) {
		t.Helper()
		require.Eventually(t, func() bool {
			response, err := r.client.Get(apis[i].base + "/ready")
			if err != nil {
				return false
			}
			_ = response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 90*time.Second, 200*time.Millisecond)
	}
	waitRefused := func(i int, reason string) {
		t.Helper()
		require.Eventually(t, func() bool {
			return strings.TrimSpace(r.command(t, nil, "inspect", "--format",
				"{{.State.Status}}", apis[i].name)) == "exited"
		}, 30*time.Second, 200*time.Millisecond, "startup refusal must be a process exit, not a harness timeout")
		require.Contains(t, r.command(t, nil, "logs", "--tail", "30", apis[i].name), reason)
	}
	for _, api := range apis {
		config, err := os.ReadFile(api.configFile)
		require.NoError(t, err)
		require.Contains(t, string(config), `provider = "rbac-local"`)
		require.NoError(t, os.WriteFile(api.configFile, []byte(string(config)+
			"\n[api.auth.opa]\nwrites_enabled = false\n"), 0600))
	}
	// Swap the shared mount atomically; the still-running peer retains the old
	// executable inode until its own restart. No Git replacement is necessary.
	require.NoError(t, os.Rename(r.currentAPIBinary, apis[0].binaryFile))
	for _, api := range apis {
		config, err := os.ReadFile(api.configFile)
		require.NoError(t, err)
		require.Contains(t, string(config), `[api.datastore.scylla]`)
		config = []byte(strings.Replace(string(config), "[api.datastore.scylla]",
			"[api.datastore.scylla]\nauto_migrate = false", 1))
		require.NoError(t, os.WriteFile(api.configFile, config, 0600))
	}
	replace(0)
	waitRefused(0, "incompatible schema")
	require.Equal(t, initialHistory, history(), "read-only failed startup must not change the migration ledger")
	assertSeed(apis[1])
	require.Equal(t, oldPeerEpoch, started(apis[1].name))
	owned(r.receiver)
	r.command(t, nil, "cp", migrator, r.receiver+":/rbac-gitctl")
	r.command(t, nil, "exec", r.receiver, "/rbac-gitctl", "migrate",
		"--hosts", "scylla:9042", "--keyspace", "gitstore", "--timeout", "2m")
	migratedHistory := history()
	require.Contains(t, migratedHistory, "014_rbac.cql")
	replace(0)
	waitReady(0)
	assertSeed(apis[0])
	assertSeed(apis[1])
	require.Equal(t, oldPeerEpoch, started(apis[1].name),
		"the old production peer must serve through migration and the mixed-version window")
	require.Equal(t, migratedHistory, history(), "feature-disabled new startup must validate read-only")
	replace(1)
	waitReady(1)
	assertSeed(apis[0])
	assertSeed(apis[1])
	require.Equal(t, migratedHistory, history())
	require.Equal(t, seedRefs, refs(), "migration and read-only replacements must preserve seeded Git history")
	require.Equal(t, gitEpoch, started(gitName), "exactly one unchanged native Git process serves this fixture")

	// Do not bypass the production gate to manufacture provider-switch proof.
	// Production refusal is separate from the diagnostic cutover matrix below.
	config, err := os.ReadFile(apis[0].configFile)
	require.NoError(t, err)
	staticConfig := append([]byte(nil), config...)
	opaConfig := strings.Replace(string(config), `provider = "rbac-local"`, `provider = "opa"`, 1)
	require.NoError(t, os.WriteFile(apis[0].configFile, []byte(opaConfig), 0600))
	replace(0)
	waitRefused(0, "production acceptance is pending")
	assertSeed(apis[1])
	require.NoError(t, os.WriteFile(apis[0].configFile, staticConfig, 0600))
	replace(0)
	waitReady(0)
	assertSeed(apis[0])
	require.Equal(t, migratedHistory, history())
	require.Equal(t, seedRefs, refs())
	require.Equal(t, gitEpoch, started(gitName))
	t.Logf("seed_commit=%s singleton_git_started=%s migration_ledger_sha256=%x",
		seedCommit, gitEpoch, sha256.Sum256([]byte(migratedHistory)))
	t.Logf("feature-disabled production baseline %s compatibility only; production OPA cutover remains refused", rbacRollingBaseline)
}

// Dynamic phases use the existing opt-in native application harness. They do
// not enable the production cmd/server OPA gate or certify shipped images.
func TestRBACOwnedDiagnosticCutoverRollingAndRollback(t *testing.T) {
	if os.Getenv("RBAC_TEST_ROLLING_DIAGNOSTIC_RUN") != "1" ||
		os.Getenv("RBAC_TEST_RUN") != "1" || os.Getenv("RBAC_TEST_OWNED_DEPLOYMENT") != "1" ||
		os.Getenv("RBAC_TEST_CONTROLLER_REPLACEMENT_RUN") != "1" {
		t.Skip("explicit owned diagnostic provider-switch matrix not selected")
	}
	for _, key := range []string{
		"RBAC_TEST_API_BINARY", "RBAC_TEST_UPGRADE_API_BINARY", "RBAC_TEST_ROLLING_STATIC_API_BINARY",
		"RBAC_TEST_ROLLING_STATIC_POLICY_FILE", "RBAC_TEST_BOOTSTRAP_CONFIG_A", "RBAC_TEST_BOOTSTRAP_CONFIG_B",
		"RBAC_TEST_SUBJECT", "RBAC_TEST_BOOTSTRAP_SUBJECT", "RBAC_TEST_ACTOR_TOKEN_FILE", "RBAC_TEST_TOKEN_FILE",
		"RBAC_TEST_API_A", "RBAC_TEST_API_B", "RBAC_TEST_GIT_URL", "RBAC_TEST_NAMESPACE",
		"RBAC_TEST_CONTROLLER_BINARY", "RBAC_TEST_CONTROLLER_CONFIG_A", "RBAC_TEST_CONTROLLER_CONFIG_B",
		"RBAC_TEST_CONTROLLER_A", "RBAC_TEST_CONTROLLER_B", "RBAC_TEST_UPGRADE_CONTROLLER_BINARY",
	} {
		if os.Getenv(key) == "" {
			t.Skipf("owned diagnostic cutover prerequisite unavailable: %s", key)
		}
	}
	fixture := ownedRBACNativeFixture(t)
	staticNativeProcess := os.Getenv("RBAC_TEST_ROLLING_STATIC_NATIVE_PROCESS")
	require.True(t, staticNativeProcess == "" || staticNativeProcess == "1",
		"static native test-process selection must be explicit")
	native := os.Getenv("RBAC_TEST_API_BINARY")
	upgrade := os.Getenv("RBAC_TEST_UPGRADE_API_BINARY")
	static := os.Getenv("RBAC_TEST_ROLLING_STATIC_API_BINARY")
	for _, binary := range []string{native, upgrade, static} {
		require.True(t, filepath.IsAbs(binary),
			"supply native old/new same-contract harnesses and a static cmd/server or explicitly selected static native test binary")
		info, err := os.Stat(binary)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular())
		require.NotZero(t, info.Mode().Perm()&0111)
	}
	oldBytes, err := os.ReadFile(native)
	require.NoError(t, err)
	newBytes, err := os.ReadFile(upgrade)
	require.NoError(t, err)
	require.NotEqual(t, sha256.Sum256(oldBytes), sha256.Sum256(newBytes),
		"compatible rolling coverage requires distinct same-contract release binaries")
	oldController := os.Getenv("RBAC_TEST_CONTROLLER_BINARY")
	newController := os.Getenv("RBAC_TEST_UPGRADE_CONTROLLER_BINARY")
	for _, binary := range []string{oldController, newController} {
		require.True(t, filepath.IsAbs(binary))
		info, err := os.Stat(binary)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular())
		require.NotZero(t, info.Mode().Perm()&0111)
	}
	oldControllerBytes, err := os.ReadFile(oldController)
	require.NoError(t, err)
	newControllerBytes, err := os.ReadFile(newController)
	require.NoError(t, err)
	require.NotEqual(t, sha256.Sum256(oldControllerBytes), sha256.Sum256(newControllerBytes),
		"owner catch-up must use distinct compatible controller releases")
	t.Setenv("RBAC_TEST_CONTROLLER_REPLACEMENT_BINARY", newController)
	subject := os.Getenv("RBAC_TEST_SUBJECT")
	require.NotEmpty(t, subject)
	require.NotEmpty(t, os.Getenv("RBAC_TEST_BOOTSTRAP_SUBJECT"))
	require.NotEqual(t, subject, os.Getenv("RBAC_TEST_BOOTSTRAP_SUBJECT"))
	actor := fixture
	actor.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	for _, credential := range []struct{ token, subject string }{
		{actor.token, subject}, {fixture.token, os.Getenv("RBAC_TEST_BOOTSTRAP_SUBJECT")},
	} {
		var claims jwt.MapClaims
		_, _, err := jwt.NewParser().ParseUnverified(credential.token, &claims)
		require.NoError(t, err)
		require.Equal(t, credential.subject, claims["sub"], "preserve the declared authenticated caller")
		expiry, err := claims.GetExpirationTime()
		require.NoError(t, err)
		require.NotNil(t, expiry)
		require.True(t, expiry.Time.After(time.Now().Add(15*time.Minute)),
			"credential expiry must not masquerade as cutover/revocation denial")
	}
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".gitstore"), 0700))
	scratch, err := os.MkdirTemp(filepath.Join(root, ".gitstore"), "rbac-cutover-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("retained private cutover failure artifacts: %s", scratch)
			return
		}
		require.NoError(t, os.RemoveAll(scratch))
	})
	t.Setenv("TMPDIR", scratch)
	t.Setenv("RBAC_TEST_PROCESS_LOG_DIR", scratch)
	for i, key := range []string{"RBAC_TEST_CONTROLLER_CHECKPOINT_DIR_A", "RBAC_TEST_CONTROLLER_CHECKPOINT_DIR_B"} {
		directory := filepath.Join(scratch, fmt.Sprintf("controller-%d-checkpoints", i))
		require.NoError(t, os.Mkdir(directory, 0700))
		t.Setenv(key, directory)
	}
	configs := []string{os.Getenv("RBAC_TEST_BOOTSTRAP_CONFIG_A"), os.Getenv("RBAC_TEST_BOOTSTRAP_CONFIG_B")}
	staticConfigs := make([]string, len(configs))
	policy := filepath.Join(scratch, "obsolete-policy.yaml")
	policySource := os.Getenv("RBAC_TEST_ROLLING_STATIC_POLICY_FILE")
	require.True(t, filepath.IsAbs(policySource),
		"supply a private complete static fixture policy: actor namespace.list allowed, clusterRole.list denied")
	file, err := os.Open(policySource)
	require.NoError(t, err)
	info, err := file.Stat()
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.Zero(t, info.Mode().Perm()&0077)
	policyBytes, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.NotEmpty(t, policyBytes)
	require.LessOrEqual(t, len(policyBytes), 1<<20)
	require.NoError(t, os.WriteFile(policy, policyBytes, 0600))
	for i, path := range configs {
		require.True(t, filepath.IsAbs(path))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(data), `provider = "opa"`)
		require.Contains(t, string(data), "auto_migrate = false",
			"diagnostic cutover uses an already prepared owned additive schema")
		nativeConfig := regexp.MustCompile(`(?m)^\s*evidence_directory\s*=.*$`).ReplaceAllString(string(data), `evidence_directory = ""`)
		configs[i] = filepath.Join(scratch, fmt.Sprintf("native-%d.toml", i))
		require.NoError(t, os.WriteFile(configs[i], []byte(nativeConfig), 0600))
		converted := strings.Replace(nativeConfig, `provider = "opa"`, `provider = "rbac-local"`, 1)
		converted = regexp.MustCompile(`(?m)^\s*initial_admin_subject\s*=.*$`).ReplaceAllString(converted, `initial_admin_subject = ""`)
		converted = regexp.MustCompile(`(?m)^\s*writes_enabled\s*=.*$`).ReplaceAllString(converted, `writes_enabled = false`)
		staticConfigs[i] = filepath.Join(scratch, fmt.Sprintf("static-%d.toml", i))
		require.NoError(t, os.WriteFile(staticConfigs[i], []byte(converted), 0600))
	}

	type process struct {
		cmd    *exec.Cmd
		cancel context.CancelFunc
		done   chan struct{}
		log    *os.File
		static bool
	}
	processes := make([]*process, len(fixture.apis))
	checkStopped := func(i int) error {
		endpoint, err := url.Parse(fixture.apis[i])
		if err != nil {
			return err
		}
		if endpoint.Scheme != "http" || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
			return fmt.Errorf("API %d must use an isolated loopback endpoint", i)
		}
		connection, err := net.DialTimeout("tcp", endpoint.Host, time.Second)
		if connection != nil {
			_ = connection.Close()
		}
		if err == nil {
			return fmt.Errorf("API %d listener remains occupied; never adopt or overlap processes", i)
		}
		return nil
	}
	assertStopped := func(i int) {
		t.Helper()
		require.NoError(t, checkStopped(i))
	}
	for i := range processes {
		assertStopped(i)
	}
	stopProcess := func(i int) error {
		p := processes[i]
		if p == nil {
			return nil
		}
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(20 * time.Second):
			return fmt.Errorf("API %d failed bounded shutdown; private log: %s", i, p.log.Name())
		}
		closeErr := p.log.Close()
		var shutdownErr error
		if !p.cmd.ProcessState.Success() {
			shutdownErr = fmt.Errorf("API %d exited unsuccessfully; private log: %s", i, p.log.Name())
		}
		listenerErr := checkStopped(i)
		if listenerErr == nil {
			processes[i] = nil
		}
		return errors.Join(closeErr, shutdownErr, listenerErr)
	}
	stop := func(i int) {
		t.Helper()
		require.NoError(t, stopProcess(i), "replacement must not proceed after a shutdown failure")
	}
	t.Cleanup(func() {
		stops := make([]func() error, len(processes))
		for i := range processes {
			stops[i] = func() error { return stopProcess(i) }
		}
		if err := rbacRollingStopAll(stops); err != nil {
			t.Errorf("owned API cleanup failures: %v", err)
		}
	})
	start := func(i int, binary string, staticProvider bool) {
		t.Helper()
		require.Nil(t, processes[i])
		assertStopped(i)
		ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
		log, err := os.CreateTemp(scratch, "api-*.log")
		require.NoError(t, err)
		args := []string{"-test.run", "^TestRBACNativeProcess$", "-test.timeout", "12m"}
		if staticProvider && staticNativeProcess != "1" {
			args = []string{"--config-file", staticConfigs[i]}
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 15 * time.Second
		environment := make([]string, 0, len(os.Environ()))
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "GITSTORE_API__AUTH__OPA__EVIDENCE_DIRECTORY=") &&
				!strings.HasPrefix(value, "GITSTORE_RBAC_NATIVE_TEST_MODELS_FILE=") &&
				!strings.HasPrefix(value, "GITSTORE_RBAC_NATIVE_TEST_STATIC=") {
				environment = append(environment, value)
			}
		}
		processConfig := configs[i]
		if staticProvider {
			processConfig = staticConfigs[i]
			if staticNativeProcess == "1" {
				environment = append(environment, "GITSTORE_RBAC_NATIVE_TEST_STATIC=1")
			}
		}
		cmd.Env = append(environment, "GITSTORE_RBAC_NATIVE_TEST_OWNED=1",
			"GITSTORE_RBAC_NATIVE_TEST_CONFIG="+processConfig,
			"GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE="+policy)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			cancel()
			_ = log.Close()
			t.Fatalf("start owned API: %v", err)
		}
		p := &process{cmd: cmd, cancel: cancel, log: log, done: make(chan struct{}), static: staticProvider}
		processes[i] = p
		go func() {
			_ = cmd.Wait()
			close(p.done)
		}()
	}
	client := &http.Client{Timeout: time.Second}
	waitReady := func(i int) {
		t.Helper()
		p := processes[i]
		err := rbacRollingAwaitReady(t.Context(), p.done, 90*time.Second, func() bool {
			response, err := client.Get(fixture.apis[i] + "/ready")
			if err != nil {
				return false
			}
			_ = response.Body.Close()
			return response.StatusCode == http.StatusOK
		})
		require.NoError(t, err, "only snapshot-ready replacements may join traffic; private log: %s", p.log.Name())
	}
	for i := range processes {
		start(i, native, false)
		waitReady(i)
	}
	roleName, bindingName := uniqueName("rolling-role"), uniqueName("rolling-binding")
	roleManifest := fixture.manifest("ClusterRole", roleName)
	roleManifest["spec"] = map[string]any{"rules": []any{map[string]any{"resources": []string{"namespace"}, "verbs": []string{"list"}}}}
	bindingManifest := fixture.manifest("ClusterRoleBinding", bindingName)
	bindingManifest["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "ClusterRole", "name": roleName},
		"subjects": []any{map[string]any{"kind": "User", "name": subject}},
	}
	var role, binding rbacNativeResource
	// Common creation waits for controller-observed lifecycle on both APIs.
	// These seed controllers stop before static cutover; later owner recovery
	// keeps its own independent, initially empty checkpoint directories.
	seeded := t.Run("seed-with-owned-controllers", func(t *testing.T) {
		for i, key := range []string{"RBAC_TEST_CONTROLLER_CHECKPOINT_DIR_A", "RBAC_TEST_CONTROLLER_CHECKPOINT_DIR_B"} {
			directory := filepath.Join(scratch, fmt.Sprintf("seed-controller-%d-checkpoints", i))
			require.NoError(t, os.Mkdir(directory, 0700))
			t.Setenv(key, directory)
		}
		_, withControllers := rbacNativeOwnerControllerRecovery(t, fixture, subject, 2)
		withControllers(func() {
			role = fixture.create(t, "ClusterRole", roleManifest)
			binding = fixture.create(t, "ClusterRoleBinding", bindingManifest)
		})
	})
	require.True(t, seeded, "both owned seed controllers must reconcile and shut down before provider cutover")
	bindingUID := binding.Metadata.UID
	defer func() {
		if processes[0] == nil || processes[0].static {
			stop(0)
			start(0, upgrade, false)
			waitReady(0)
		}
		fixture.revokeBinding(t, "ClusterRoleBinding", bindingName, bindingManifest["spec"], &bindingUID)
		current, found := fixture.lookup(t, fixture.apis[0], "ClusterRole", roleName)
		if graphqlErrorHasCode(found.Errors, "NOT_FOUND") {
			return
		}
		require.Empty(t, found.Errors)
		require.Equal(t, role.Metadata.UID, current.Metadata.UID)
		if current.Metadata.DeletionTimestamp == nil {
			response := fixture.query(t, fixture.apis[0],
				`mutation($input:DeleteClusterRoleInput!){deleteClusterRole(input:$input){clusterRole{id}}}`,
				map[string]any{"input": map[string]any{"id": current.ID, "resourceVersion": current.Metadata.ResourceVersion}})
			require.Empty(t, response.Errors)
		}
	}()
	const allowedRead = `query{namespaces(first:1){edges{node{id}}}}`
	const deniedRead = `query{clusterRoles(first:1){edges{node{id}}}}`
	expectedAllowed := true
	assertParity := func(api string, allowed bool) {
		t.Helper()
		response := actor.query(t, api, allowedRead, nil)
		if allowed {
			require.Empty(t, response.Errors)
		} else {
			rbacNativeErrorCode(t, response, "FORBIDDEN")
		}
		rbacNativeErrorCode(t, actor.query(t, api, deniedRead, nil), "FORBIDDEN")
	}
	for _, api := range fixture.apis {
		rbacNativeEventually(t, func() bool {
			return len(actor.query(t, api, allowedRead, nil).Errors) == 0
		}, "seeded common-admission grant must propagate to both diagnostic APIs")
		assertParity(api, true)
	}
	git := fixture.clone(t, "ClusterRoleBinding")
	canonicalTip := func() string {
		t.Helper()
		tip, err := rbacRollingCanonicalTip(git.remoteTip(t))
		require.NoError(t, err, "verify the exact advertised canonical ref and object ID")
		return tip
	}
	seedTip := canonicalTip()
	_, err = git.command(t, "fetch", "--quiet", "origin", "refs/heads/main")
	require.NoError(t, err)
	_, err = git.command(t, "merge-base", "--is-ancestor", seedTip, "FETCH_HEAD")
	require.NoError(t, err, "the exact advertised seed must be available in fetched canonical history")
	sourceIdentity := func(api string) [2]string {
		t.Helper()
		response := fixture.query(t, api, `query{
			namespace(by:{name:"gitstore-system"}){metadata{uid}}
			repository(by:{namespacePath:{namespace:"gitstore-system",name:"gitstore-system"}}){metadata{uid}}
		}`, nil)
		require.Empty(t, response.Errors)
		var identity struct {
			Namespace, Repository struct{ Metadata struct{ UID string } }
		}
		require.NoError(t, json.Unmarshal(response.Data, &identity))
		require.NotEmpty(t, identity.Namespace.Metadata.UID)
		require.NotEmpty(t, identity.Repository.Metadata.UID)
		return [2]string{identity.Namespace.Metadata.UID, identity.Repository.Metadata.UID}
	}
	seedIdentity := sourceIdentity(fixture.apis[0])
	seedManifests := map[string]map[string]any{"ClusterRole": roleManifest, "ClusterRoleBinding": bindingManifest}
	seedDigests := make(map[string][32]byte)
	for kind, resource := range map[string]rbacNativeResource{"ClusterRole": role, "ClusterRoleBinding": binding} {
		path := "rbac/" + strings.ToLower(kind) + "s/" + resource.Metadata.Name + ".yaml"
		data, err := git.command(t, "show", seedTip+":"+path)
		require.NoError(t, err)
		seedDigests[kind] = sha256.Sum256(data)
	}
	assertSource := func(i int) {
		t.Helper()
		require.Equal(t, seedIdentity, sourceIdentity(fixture.apis[i]),
			"traffic target must preserve the canonical Namespace and Repository incarnations")
		currentRole, response := fixture.lookup(t, fixture.apis[i], "ClusterRole", roleName)
		require.Empty(t, response.Errors)
		require.Equal(t, role.Metadata.UID, currentRole.Metadata.UID)
		require.JSONEq(t, string(role.Spec), string(currentRole.Spec))
		currentBinding, response := fixture.lookup(t, fixture.apis[i], "ClusterRoleBinding", bindingName)
		if !expectedAllowed && graphqlErrorHasCode(response.Errors, "NOT_FOUND") {
			require.Empty(t, currentBinding.Metadata.UID)
		} else {
			require.Empty(t, response.Errors)
			require.Equal(t, binding.Metadata.UID, currentBinding.Metadata.UID)
			require.JSONEq(t, string(binding.Spec), string(currentBinding.Spec))
			if expectedAllowed {
				require.Nil(t, currentBinding.Metadata.DeletionTimestamp)
			} else {
				require.NotNil(t, currentBinding.Metadata.DeletionTimestamp)
			}
		}
		_, err := git.command(t, "fetch", "--quiet", "origin", "refs/heads/main")
		require.NoError(t, err)
		_, err = git.command(t, "merge-base", "--is-ancestor", seedTip, "FETCH_HEAD")
		require.NoError(t, err, "canonical history must retain the seeded source revision")
		for kind, resource := range map[string]rbacNativeResource{"ClusterRole": role, "ClusterRoleBinding": binding} {
			path := "rbac/" + strings.ToLower(kind) + "s/" + resource.Metadata.Name + ".yaml"
			paths, err := git.command(t, "ls-tree", "--name-only", "FETCH_HEAD", "--", path)
			require.NoError(t, err)
			if kind == "ClusterRoleBinding" && !expectedAllowed {
				require.Empty(t, strings.TrimSpace(string(paths)), "verified revocation must remain absent from canonical Git")
				continue
			}
			require.Equal(t, path, strings.TrimSpace(string(paths)))
			data, err := git.command(t, "show", "FETCH_HEAD:"+path)
			require.NoError(t, err)
			require.NoError(t, rbacRollingVerifyCanonicalSource(resource, seedManifests[kind], data, seedDigests[kind]),
				"authoritative UID and canonical author content must identify the same seeded source")
			require.NoError(t, os.WriteFile(filepath.Join(scratch, strings.ToLower(kind)+"-source.yaml"), data, 0600))
		}
	}

	// The owned frontend stops admitting all traffic, then joins admitted
	// requests. Direct backend URLs are used only for readiness/source probes.
	frozenNames := make([]string, 0, 2)
	gate := newRBACRollingFrontend(t, fixture.apis)
	front := httptest.NewServer(gate)
	defer front.Close()
	freeze := func() {
		t.Helper()
		held := &rbacRollingDrainBarrier{id: uniqueName("drain"), entered: make(chan struct{}), release: make(chan struct{})}
		requestContext, cancelRequest := context.WithTimeout(t.Context(), 10*time.Second)
		body, err := json.Marshal(map[string]string{"query": allowedRead})
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(requestContext, http.MethodPost,
			front.URL+"/admin/graphql", bytes.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+actor.token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-GitStore-Owned-Drain-Barrier", held.id)
		gate.mu.Lock()
		open := gate.admitting
		gate.barrier = held
		gate.mu.Unlock()
		require.True(t, open, "the drain request must enter an admitting frontend")
		requestResult := make(chan error, 1)
		requestDone := make(chan struct{})
		go func() {
			defer close(requestDone)
			response, err := (&http.Client{}).Do(request)
			if err != nil {
				requestResult <- err
				return
			}
			defer response.Body.Close()
			var payload gqlResponse
			err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
			if err == nil && (response.StatusCode != http.StatusOK || len(payload.Errors) != 0) {
				err = fmt.Errorf("admitted drain request failed ordinary authorization")
			}
			requestResult <- err
		}()
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(held.release) }) }
		var drained chan struct{}
		defer func() {
			release()
			cancelRequest()
			select {
			case <-requestDone:
			case <-time.After(3 * time.Second):
				t.Error("owned drain request did not join during cleanup")
			}
			if drained != nil {
				select {
				case <-drained:
				case <-time.After(3 * time.Second):
					t.Error("owned freeze did not join during cleanup")
				}
			}
		}()
		select {
		case <-held.entered:
		case <-requestDone:
			t.Fatalf("ordinary request failed before entering the owned barrier: %v", <-requestResult)
		case <-time.After(5 * time.Second):
			t.Fatal("ordinary request did not enter the owned frontend barrier")
		}
		closed := make(chan struct{})
		drained = make(chan struct{})
		go gate.closeAndDrain(closed, drained)
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("owned freeze did not close admission")
		}
		// Entered follows active.Add; active.Done cannot run while the actual
		// authenticated HTTP request remains blocked on the unreleased barrier.
		select {
		case <-drained:
			t.Fatal("freeze completed before its admitted ordinary request was released")
		default:
		}
		select {
		case <-requestDone:
			t.Fatal("the admitted ordinary request must still be in flight")
		default:
		}
		release()
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.Fatal("provider switch must wait for admitted traffic to drain")
		}
		select {
		case <-requestDone:
			require.NoError(t, <-requestResult)
		case <-time.After(5 * time.Second):
			t.Fatal("released ordinary request did not complete")
		}
		before := gate.forwarded.Load()
		name := uniqueName("frozen-role")
		frozenNames = append(frozenNames, name)
		body, err = json.Marshal(map[string]any{
			"query":     `mutation($input:CreateClusterRoleInput!){createClusterRole(input:$input){clusterRole{id}}}`,
			"variables": map[string]any{"input": fixture.manifest("ClusterRole", name)},
		})
		require.NoError(t, err)
		request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, front.URL+"/admin/graphql", bytes.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+fixture.token)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		require.NoError(t, err)
		_ = response.Body.Close()
		require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
		require.Equal(t, before, gate.forwarded.Load(), "frozen writes must never reach either provider")
	}
	admit := func(i int) {
		t.Helper()
		waitReady(i)
		assertParity(fixture.apis[i], expectedAllowed)
		assertSource(i)
		response, err := client.Get(fixture.apis[i] + "/ready")
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode, "source-verified target must remain ready before admission")
		gate.mu.Lock()
		gate.target, gate.admitting = i, true
		gate.mu.Unlock()
	}
	admit(0)
	freeze()
	for i := range processes {
		stop(i)
		start(i, static, true)
		waitReady(i)
	}
	for _, api := range fixture.apis {
		assertParity(api, true)
	}
	require.Equal(t, seedTip, canonicalTip(), "static parity verification must not rewrite the seeded Git grant")
	admit(1)
	assertParity(front.URL, true)
	freeze()
	for i := range processes {
		stop(i)
		start(i, native, false)
		waitReady(i)
	}
	// Both static processes are gone before dynamic traffic is admitted.
	admit(1)
	assertParity(front.URL, true)
	for _, name := range frozenNames {
		_, response := fixture.lookup(t, fixture.apis[0], "ClusterRole", name)
		rbacNativeErrorCode(t, response, "NOT_FOUND")
	}
	require.True(t, t.Run("user-owner-catch-up-and-controller-replacement", func(t *testing.T) {
		testRBACNativeOwnerAuthentication(t, false, true, false)
	}), "owner recovery must finish and release its owned controllers before API rolling")
	rollingStarted := time.Now()
	var captureOnce sync.Once
	for i := range processes {
		peer := 1 - i
		admit(peer)
		peerPID := processes[peer].cmd.Process.Pid
		oldPID := processes[i].cmd.Process.Pid
		trafficCtx, cancelTraffic := context.WithCancel(t.Context())
		traffic := make(chan error, 1)
		var requests atomic.Uint64
		var phase atomic.Value
		phase.Store("peer-traffic-before-stop")
		captureFailure := func(selected string, status int, requestStarted time.Time) {
			captureOnce.Do(func() {
				at := time.Now()
				event := rbacRollingPeerFailure{
					UTC: at.UTC(), UnixNanoseconds: at.UnixNano(), RollingStartedUTC: rollingStarted.UTC(),
					ElapsedNanoseconds:        at.Sub(rollingStarted).Nanoseconds(),
					RequestElapsedNanoseconds: at.Sub(requestStarted).Nanoseconds(),
					Phase:                     phase.Load().(string), ReplacingAPI: i,
					ExpectedPeer: peer, SelectedTarget: selected, HTTPStatus: status, PeerPID: peerPID,
				}
				if err := rbacRollingCapturePeerFailure(scratch, fixture.apis, event); err != nil {
					t.Logf("first-peer-failure diagnostic capture error: %v", err)
				}
			})
		}
		go func() {
			for {
				requestStarted := time.Now()
				body := bytes.NewBufferString(`{"query":"query{namespaces(first:1){edges{node{id}}}}"}`)
				request, err := http.NewRequestWithContext(trafficCtx, http.MethodPost, front.URL+"/admin/graphql", body)
				if err != nil {
					captureFailure("", 0, requestStarted)
					traffic <- err
					return
				}
				request.Header.Set("Authorization", "Bearer "+actor.token)
				request.Header.Set("Content-Type", "application/json")
				response, err := client.Do(request)
				if trafficCtx.Err() != nil && (response == nil || response.StatusCode == http.StatusOK) {
					if response != nil {
						_ = response.Body.Close()
					}
					traffic <- nil
					return
				}
				if err != nil {
					captureFailure("", 0, requestStarted)
					traffic <- err
					return
				}
				var payload gqlResponse
				err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
				_ = response.Body.Close()
				selectedPeer := response.Header.Get("X-GitStore-Owned-RBAC-Target")
				if selectedPeer != fmt.Sprint(peer) || err != nil || response.StatusCode != http.StatusOK || len(payload.Errors) != 0 {
					captureFailure(selectedPeer, response.StatusCode, requestStarted)
					var codes []string
					for _, raw := range payload.Errors {
						var failure struct{ Extensions struct{ Code string } }
						if json.Unmarshal(raw, &failure) == nil {
							codes = append(codes, failure.Extensions.Code)
						}
					}
					traffic <- fmt.Errorf("healthy peer failed an ordinary request during physical replacement: expected_peer=%d selected_peer=%q status=%d decode_error=%v graphql_codes=%v",
						peer, selectedPeer, response.StatusCode, err, codes)
					return
				}
				requests.Add(1)
				select {
				case <-trafficCtx.Done():
					traffic <- nil
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
		}()
		// Join the ordinary request and its bounded first-failure capture before cleanup.
		func() {
			defer func() {
				cancelTraffic()
				select {
				case err := <-traffic:
					require.NoError(t, err)
				case <-time.After(5 * time.Second):
					t.Error("ordinary replacement traffic did not drain")
				}
			}()
			require.Eventually(t, func() bool { return requests.Load() > 0 }, 3*time.Second, 50*time.Millisecond)
			before := requests.Load()
			phase.Store("stopping-old-api")
			stop(i)
			phase.Store("starting-compatible-api")
			start(i, upgrade, false)
			phase.Store("awaiting-compatible-api-readiness")
			waitReady(i)
			phase.Store("verifying-compatible-api-parity")
			require.Eventually(t, func() bool { return requests.Load() > before }, 3*time.Second, 50*time.Millisecond)
			require.NotEqual(t, oldPID, processes[i].cmd.Process.Pid)
			require.Equal(t, peerPID, processes[peer].cmd.Process.Pid)
			for _, api := range fixture.apis {
				assertParity(api, true)
				current, found := fixture.lookup(t, api, "ClusterRoleBinding", bindingName)
				require.Empty(t, found.Errors)
				require.Equal(t, binding.Metadata.UID, current.Metadata.UID)
				require.JSONEq(t, string(binding.Spec), string(current.Spec))
			}
		}()
	}
	fixture.revokeBinding(t, "ClusterRoleBinding", bindingName, bindingManifest["spec"], &bindingUID)
	for _, api := range fixture.apis {
		rbacNativeEventually(t, func() bool {
			return graphqlErrorHasCode(actor.query(t, api, allowedRead, nil).Errors, "FORBIDDEN")
		}, "a real dynamic revocation must deny before obsolete static rollback is attempted")
	}
	expectedAllowed = false
	require.NotEqual(t, seedTip, canonicalTip())
	// An obsolete static file is still syntactically valid. Launch its physical
	// candidate off traffic and let the independent parity oracle refuse it.
	// SIGHUP does not reload provider selection from disk; it cannot demonstrate
	// a real provider-change attempt in this harness.
	admit(1)
	peerPID := processes[1].cmd.Process.Pid
	stop(0)
	start(0, static, true)
	waitReady(0)
	assertParity(fixture.apis[0], true)
	candidate := actor.query(t, fixture.apis[0], allowedRead, nil)
	serving := actor.query(t, fixture.apis[1], allowedRead, nil)
	compatible := len(candidate.Errors) == 0 && len(serving.Errors) == 0 ||
		graphqlErrorHasCode(candidate.Errors, "FORBIDDEN") && graphqlErrorHasCode(serving.Errors, "FORBIDDEN")
	require.False(t, compatible, "a static rollback that silently restores the revoked grant must fail parity")
	gate.mu.Lock()
	servingTarget := gate.target
	gate.mu.Unlock()
	require.Equal(t, 1, servingTarget, "failed rollback parity must leave the candidate outside serving traffic")
	assertParity(front.URL, false)
	require.Equal(t, peerPID, processes[1].cmd.Process.Pid)
	stop(0)
	start(0, upgrade, false)
	waitReady(0)
	for _, api := range fixture.apis {
		assertParity(api, false)
	}
	t.Log("bounded diagnostic seeded parity, freeze/drain, readiness admission, owner catch-up, distinct compatible replacements and off-traffic stale-rollback parity refusal; not hot-provider reload, production activation or sustained-load acceptance")
}

type rbacRollingPeerFailure struct {
	UTC                       time.Time `json:"utc"`
	RollingStartedUTC         time.Time `json:"rolling_started_utc"`
	UnixNanoseconds           int64     `json:"unix_nanoseconds"`
	ElapsedNanoseconds        int64     `json:"monotonic_elapsed_nanoseconds"`
	RequestElapsedNanoseconds int64     `json:"request_elapsed_nanoseconds"`
	Phase                     string    `json:"phase"`
	ReplacingAPI              int       `json:"replacing_api"`
	ExpectedPeer              int       `json:"expected_peer"`
	SelectedTarget            string    `json:"selected_target"`
	HTTPStatus                int       `json:"ordinary_http_status"`
	PeerPID                   int       `json:"expected_peer_pid"`
}

type rbacRollingDiagnosticRead struct {
	StartedUTC         time.Time `json:"started_utc"`
	ElapsedNanoseconds int64     `json:"elapsed_nanoseconds"`
	Status             int       `json:"status"`
	Bytes              int       `json:"bytes"`
	ReadLimitReached   bool      `json:"read_limit_reached"`
	Error              string    `json:"error,omitempty"`
}

type rbacRollingAPIDiagnostic struct {
	API          int                       `json:"api"`
	Ready        rbacRollingDiagnosticRead `json:"ready"`
	Metrics      rbacRollingDiagnosticRead `json:"metrics"`
	ProcessStart string                    `json:"process_start_time_seconds,omitempty"`
}

// No ordinary request, credentials, headers, or transport error text enter this capture.
func rbacRollingCapturePeerFailure(scratch string, apis []string, event rbacRollingPeerFailure) error {
	manifest, err := os.OpenFile(filepath.Join(scratch, "first-peer-failure.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	records := make([]rbacRollingAPIDiagnostic, len(apis))
	var workers sync.WaitGroup
	for i, endpoint := range apis {
		workers.Go(func() {
			record := &records[i]
			record.API = i
			base, err := url.Parse(endpoint)
			if err != nil || base.Scheme != "http" || !net.ParseIP(base.Hostname()).IsLoopback() ||
				base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
				record.Ready.Error, record.Metrics.Error = "invalid_owned_health_endpoint", "invalid_owned_health_endpoint"
				return
			}
			// Both routes and both APIs share this finite window, independent of traffic cancellation.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var routes sync.WaitGroup
			for _, route := range []string{"ready", "metrics"} {
				routes.Go(func() {
					result, limit := &record.Ready, int64(4<<10)
					if route == "metrics" {
						result, limit = &record.Metrics, 1<<20
					}
					started := time.Now()
					result.StartedUTC = started.UTC()
					defer func() { result.ElapsedNanoseconds = time.Since(started).Nanoseconds() }()
					target := *base
					target.Path = "/" + route
					target.RawPath = ""
					request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
					if err != nil {
						result.Error = "invalid_health_request"
						return
					}
					response, err := client.Do(request)
					if err != nil {
						result.Error = "health_request_failed"
						return
					}
					defer response.Body.Close()
					result.Status = response.StatusCode
					body, readErr := io.ReadAll(io.LimitReader(response.Body, limit))
					result.Bytes, result.ReadLimitReached = len(body), int64(len(body)) == limit
					if readErr != nil {
						result.Error = "health_body_read_failed"
					}
					path := filepath.Join(scratch, fmt.Sprintf("first-peer-failure-api-%d-%s.txt", i, route))
					file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						result.Error = "private_artifact_create_failed"
						return
					}
					_, writeErr := file.Write(body)
					if errors.Join(writeErr, file.Close()) != nil {
						result.Error = "private_artifact_write_failed"
					}
					if route == "metrics" && response.StatusCode == http.StatusOK {
						match := regexp.MustCompile(`(?m)^process_start_time_seconds(?:\{[^\n]*\})? ([0-9.eE+-]+)(?: [^\n]*)?$`).FindSubmatch(body)
						if len(match) == 2 {
							if epoch, err := strconv.ParseFloat(string(match[1]), 64); err == nil && epoch > 0 {
								record.ProcessStart = string(match[1])
							}
						}
					}
				})
			}
			routes.Wait()
		})
	}
	workers.Wait()
	encodeErr := json.NewEncoder(manifest).Encode(struct {
		Failure rbacRollingPeerFailure     `json:"failure"`
		APIs    []rbacRollingAPIDiagnostic `json:"apis"`
	}{event, records})
	return errors.Join(encodeErr, manifest.Close())
}

func TestRBACRollingPeerFailureCapturePrivacyAndBounds(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		require.Empty(t, r.URL.RawQuery)
		require.Equal(t, http.MethodGet, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Empty(t, body)
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, strings.Repeat("r", 8<<10))
		case "/metrics":
			_, _ = io.WriteString(w, "process_start_time_seconds 1794825809.19\n"+strings.Repeat("# bounded\n", 200000))
		default:
			t.Errorf("unexpected diagnostic route %q", r.URL.Path)
		}
	}))
	defer server.Close()
	scratch := t.TempDir()
	event := rbacRollingPeerFailure{UTC: time.Now().UTC(), Phase: "awaiting-compatible-api-readiness", ExpectedPeer: 1, SelectedTarget: "1", HTTPStatus: 503}
	require.NoError(t, rbacRollingCapturePeerFailure(scratch, []string{server.URL, server.URL}, event))
	var receipt struct {
		Failure rbacRollingPeerFailure     `json:"failure"`
		APIs    []rbacRollingAPIDiagnostic `json:"apis"`
	}
	data, err := os.ReadFile(filepath.Join(scratch, "first-peer-failure.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &receipt))
	require.Equal(t, event, receipt.Failure)
	require.Len(t, receipt.APIs, 2)
	for _, api := range receipt.APIs {
		require.Equal(t, 503, api.Ready.Status)
		require.Equal(t, 4<<10, api.Ready.Bytes)
		require.True(t, api.Ready.ReadLimitReached)
		require.Equal(t, 1<<20, api.Metrics.Bytes)
		require.True(t, api.Metrics.ReadLimitReached)
		require.Equal(t, "1794825809.19", api.ProcessStart)
	}
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	require.Len(t, entries, 5)
	for _, entry := range entries {
		info, err := entry.Info()
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0600), info.Mode().Perm())
	}
	require.Error(t, rbacRollingCapturePeerFailure(scratch, []string{server.URL}, event))
	require.EqualValues(t, 4, calls.Load(), "exclusive receipt must refuse overwrite before issuing diagnostic requests")
}

func TestRBACRollingPeerFailureCaptureTimeoutAndEndpointPrivacy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	scratch := t.TempDir()
	started := time.Now()
	require.NoError(t, rbacRollingCapturePeerFailure(scratch,
		[]string{server.URL, "http://private-user:private-token@127.0.0.1:1/?private-query"}, rbacRollingPeerFailure{}))
	require.Less(t, time.Since(started), 4*time.Second, "both routes and APIs must share bounded concurrent capture windows")
	data, err := os.ReadFile(filepath.Join(scratch, "first-peer-failure.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), "health_request_failed")
	require.Contains(t, string(data), "invalid_owned_health_endpoint")
	require.NotContains(t, string(data), "private-token")
	require.NotContains(t, string(data), "private-user")
	require.NotContains(t, string(data), "private-query")
}

type rbacRollingDrainBarrier struct {
	id               string
	entered, release chan struct{}
}

type rbacRollingFrontend struct {
	mu        sync.Mutex
	admitting bool
	target    int
	active    sync.WaitGroup
	forwarded atomic.Uint64
	barrier   *rbacRollingDrainBarrier
	proxies   []*httputil.ReverseProxy
}

func newRBACRollingFrontend(t *testing.T, apis []string) *rbacRollingFrontend {
	t.Helper()
	gate := &rbacRollingFrontend{proxies: make([]*httputil.ReverseProxy, len(apis))}
	for i, api := range apis {
		parsed, err := url.Parse(api)
		require.NoError(t, err)
		gate.proxies[i] = httputil.NewSingleHostReverseProxy(parsed)
	}
	return gate
}

func (g *rbacRollingFrontend) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	g.mu.Lock()
	if !g.admitting {
		g.mu.Unlock()
		http.Error(w, "owned cutover traffic frozen", http.StatusServiceUnavailable)
		return
	}
	index := g.target
	g.active.Add(1)
	held := g.barrier
	if held != nil && request.Header.Get("X-GitStore-Owned-Drain-Barrier") == held.id {
		g.barrier = nil
	} else {
		held = nil
	}
	g.mu.Unlock()
	defer g.active.Done()
	if held != nil {
		close(held.entered)
		select {
		case <-held.release:
		case <-request.Context().Done():
			return
		}
	}
	g.forwarded.Add(1)
	w.Header().Set("X-GitStore-Owned-RBAC-Target", fmt.Sprint(index))
	g.proxies[index].ServeHTTP(w, request)
}

func TestRBACRollingFrontendRoutesOnlyToHealthyPeerDuringPhysicalReplacement(t *testing.T) {
	var received [2]atomic.Uint64
	handler := func(index int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer owned-harness-caller" {
				http.Error(w, "caller identity lost", http.StatusUnauthorized)
				return
			}
			received[index].Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":{"peer":%d}}`, index)
		})
	}
	first, second := httptest.NewServer(handler(0)), httptest.NewServer(handler(1))
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	gate := newRBACRollingFrontend(t, []string{first.URL, second.URL})
	front := httptest.NewServer(gate)
	t.Cleanup(front.Close)
	client := &http.Client{Timeout: time.Second}
	route := func(index int) {
		gate.mu.Lock()
		gate.target, gate.admitting = index, true
		gate.mu.Unlock()
	}
	read := func(index int) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			front.URL+"/admin/graphql", strings.NewReader(`{"query":"query{namespaces(first:1){edges{node{id}}}}"}`))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer owned-harness-caller")
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Equal(t, fmt.Sprint(index), response.Header.Get("X-GitStore-Owned-RBAC-Target"))
		content, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.JSONEq(t, fmt.Sprintf(`{"data":{"peer":%d}}`, index), string(content))
	}
	route(0)
	read(0)
	route(1)
	first.Close()
	for range 3 {
		read(1)
	}
	firstURL, err := url.Parse(first.URL)
	require.NoError(t, err)
	replacement := httptest.NewUnstartedServer(handler(0))
	require.NoError(t, replacement.Listener.Close())
	replacement.Listener, err = net.Listen("tcp", firstURL.Host)
	require.NoError(t, err)
	replacement.Start()
	t.Cleanup(replacement.Close)
	require.Equal(t, first.URL, replacement.URL, "replacement must reuse the original physical endpoint")
	route(0)
	second.Close()
	for range 3 {
		read(0)
	}
	require.Equal(t, uint64(4), received[0].Load())
	require.Equal(t, uint64(3), received[1].Load())
}

func (g *rbacRollingFrontend) closeAndDrain(closed, drained chan struct{}) {
	g.mu.Lock()
	g.admitting = false
	g.mu.Unlock()
	close(closed)
	g.active.Wait()
	close(drained)
}

func rbacRollingStopAll(stops []func() error) error {
	var failures []error
	for i := len(stops) - 1; i >= 0; i-- {
		if err := stops[i](); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func TestRBACRollingCleanupAttemptsEveryOwnedProcess(t *testing.T) {
	var attempted []int
	failure := errors.New("owned shutdown failed")
	err := rbacRollingStopAll([]func() error{
		func() error { attempted = append(attempted, 0); return failure },
		func() error { attempted = append(attempted, 1); return nil },
		func() error { attempted = append(attempted, 2); return failure },
	})
	require.ErrorIs(t, err, failure)
	require.Equal(t, []int{2, 1, 0}, attempted, "one shutdown failure must not prevent cleanup of the remaining owned processes")
}

func TestRBACRollingFrontendDrainsAnAdmittedOrdinaryRequest(t *testing.T) {
	var received atomic.Uint64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer owned-harness-caller" {
			http.Error(w, "caller identity lost", http.StatusUnauthorized)
			return
		}
		received.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"namespaces":{"edges":[]}}}`)
	}))
	defer backend.Close()
	gate := newRBACRollingFrontend(t, []string{backend.URL})
	front := httptest.NewServer(gate)
	defer front.Close()
	held := &rbacRollingDrainBarrier{id: "owned-drain", entered: make(chan struct{}), release: make(chan struct{})}
	gate.mu.Lock()
	gate.admitting, gate.barrier = true, held
	gate.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/admin/graphql",
		bytes.NewBufferString(`{"query":"query{namespaces(first:1){edges{node{id}}}}"}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer owned-harness-caller")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitStore-Owned-Drain-Barrier", held.id)
	result := make(chan error, 1)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := (&http.Client{}).Do(request)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("ordinary request failed: HTTP %d", response.StatusCode)
			}
		}
		result <- err
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	var drained chan struct{}
	defer func() {
		release()
		cancel()
		select {
		case <-requestDone:
		case <-time.After(3 * time.Second):
			t.Error("local ordinary request did not join")
		}
		if drained != nil {
			select {
			case <-drained:
			case <-time.After(3 * time.Second):
				t.Error("local freeze did not join")
			}
		}
	}()
	select {
	case <-held.entered:
	case <-requestDone:
		t.Fatalf("ordinary request never reached the barrier: %v", <-result)
	case <-ctx.Done():
		t.Fatal("ordinary request barrier exceeded its deadline")
	}
	closed := make(chan struct{})
	drained = make(chan struct{})
	go gate.closeAndDrain(closed, drained)
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("freeze did not close admission")
	}
	select {
	case <-drained:
		t.Fatal("freeze completed before releasing its admitted request")
	default:
	}
	select {
	case <-requestDone:
		t.Fatal("ordinary request completed before release")
	default:
	}
	rejected, err := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/admin/graphql",
		bytes.NewBufferString(`{"query":"mutation{createClusterRole(input:{metadata:{name:\"must-not-forward\"}}){clusterRole{id}}}"}`))
	require.NoError(t, err)
	rejected.Header.Set("Authorization", "Bearer owned-harness-caller")
	response, err := (&http.Client{}).Do(rejected)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.Zero(t, gate.forwarded.Load())
	require.Zero(t, received.Load())
	release()
	select {
	case <-drained:
	case <-ctx.Done():
		t.Fatal("released request did not drain")
	}
	select {
	case <-requestDone:
		require.NoError(t, <-result)
	case <-ctx.Done():
		t.Fatal("released ordinary request did not complete")
	}
	require.Equal(t, uint64(1), gate.forwarded.Load())
	require.Equal(t, uint64(1), received.Load())
}
