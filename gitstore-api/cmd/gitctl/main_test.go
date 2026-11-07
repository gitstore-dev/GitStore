// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

func TestGenerateAuthorizationCatalog(t *testing.T) {
	seed, err := os.ReadFile("../../../specs/064-authz-action-vocabulary/contracts/action-vocabulary.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalog, seed, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"generate", "authz", "--root", root, "--catalog", catalog}
	var stdout, stderr bytes.Buffer
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("generation failed: code=%d stderr=%s", code, stderr.String())
	}
	paths := []string{"gitstore-api/internal/auth/actions_generated.go", "gitstore-git-service/src/auth/actions_generated.rs"}
	var definition struct {
		Actions []struct {
			Action, Kind, Scope, Resource, Verb string
			Reserved                            bool
		}
	}
	if err := json.Unmarshal(seed, &definition); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		first, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range definition.Actions {
			if !bytes.Contains(first, []byte(`"`+entry.Action+`"`)) {
				t.Errorf("%s missing %s", path, entry.Action)
			}
		}
		if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("regenerate: %s", stderr.String())
		}
		second, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("non-deterministic output %s", path)
		}
	}
	if code := run(append(args, "--check"), strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("check: %s", stderr.String())
	}
	if err := os.WriteFile(filepath.Join(root, paths[0]), []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run(append(args, "--check"), strings.NewReader(""), &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "drift") {
		t.Fatalf("drift not detected: %s", stderr.String())
	}
	unchanged, _ := os.ReadFile(filepath.Join(root, paths[0]))
	if string(unchanged) != "drift" {
		t.Fatal("check mutated output")
	}

	var tree map[string]any
	if err := json.Unmarshal(seed, &tree); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"duplicate", "pair", "scope", "partial-wildcard", "kind", "unknown-field"} {
		t.Run(mutation, func(t *testing.T) {
			if err := json.Unmarshal(seed, &tree); err != nil {
				t.Fatal(err)
			}
			actions := tree["actions"].([]any)
			entry := actions[0].(map[string]any)
			switch mutation {
			case "duplicate":
				tree["actions"] = append(actions, entry)
			case "pair":
				entry["resource"] = "repository/contents"
			case "scope":
				entry["scope"] = "arbitrary"
			case "partial-wildcard":
				entry["action"] = "namespace.*"
			case "kind":
				entry["kind"] = "Namespace"
			case "unknown-field":
				entry["unrecognized"] = true
			}
			data, err := json.Marshal(tree)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(catalog, data, 0600); err != nil {
				t.Fatal(err)
			}
			if code := run(args, strings.NewReader(""), &stdout, &stderr); code == 0 {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}

func TestCommittedAuthorizationCatalogParity(t *testing.T) {
	data, err := os.ReadFile("../../../shared/schemas/authorization-actions.json")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../../../specs/064-authz-action-vocabulary/contracts/action-vocabulary.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(seed)) {
		t.Fatal("reviewed catalog snapshot drift")
	}
	var catalog authorizationCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Actions) != 82 {
		t.Fatalf("unexpected catalog size %d", len(catalog.Actions))
	}
	for _, e := range catalog.Actions {
		got, ok := auth.LookupAction(e.Action)
		if !ok || got.Kind != e.Kind || got.Scope != e.Scope || got.Resource != e.Resource || got.Verb != e.Verb || got.Reserved != e.Reserved {
			t.Fatalf("generated metadata drift for %s", e.Action)
		}
	}
	for _, legacy := range []string{"*", "repository.*", "category.read", "repository.read.any", "namespace.create.user", "product.read.management", "product.delete.complete", "workflow.action.foo"} {
		if _, ok := auth.LookupAction(legacy); ok {
			t.Errorf("unregistered action accepted: %s", legacy)
		}
	}
	copy := auth.AuthorizationActions()
	copy[0].Action = "tampered"
	if _, ok := auth.LookupAction("tampered"); ok {
		t.Fatal("caller mutated catalog")
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"generate", "authz", "--root", "../../..", "--check"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("committed catalog drift: %s", stderr.String())
	}
}

func TestMigrateRunsExplicitlyWithDeadlineAndReportsFailure(t *testing.T) {
	original := migrateSchema
	t.Cleanup(func() { migrateSchema = original })
	calls := 0
	var failure error
	migrateSchema = func(ctx context.Context, cfg config.ScyllaConfig, log *zap.Logger) error {
		calls++
		if cfg.Keyspace != "prepared" || len(cfg.Hosts) != 1 || cfg.Hosts[0] != "scylla:9042" {
			t.Fatalf("unexpected migration connection")
		}
		if _, ok := ctx.Deadline(); !ok || log == nil {
			t.Fatal("migration needs a bounded context and logger")
		}
		return failure
	}
	var stdout, stderr bytes.Buffer
	args := []string{"migrate", "--hosts", "scylla:9042", "--keyspace", "prepared", "--timeout", "1m"}
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "complete") {
		t.Fatalf("migration code=%d stderr=%s", code, stderr.String())
	}
	failure = errors.New("schema is incompatible")
	stdout.Reset()
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), failure.Error()) {
		t.Fatalf("migration error was hidden: code=%d stderr=%s", code, stderr.String())
	}
	for _, invalid := range [][]string{{"migrate", "--timeout", "0s"}, {"migrate", "unexpected"}} {
		if code := run(invalid, strings.NewReader(""), &stdout, &stderr); code != 2 {
			t.Fatalf("invalid migration arguments: code=%d", code)
		}
	}
	if calls != 2 {
		t.Fatalf("invalid arguments opened a migration connection: %d calls", calls)
	}
}

func TestUsersAddCreatesFileWithHashedPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"users", "add", "--file", path, "--username", "alice",
		"--email", "alice@example.com", "--display-name", "Alice Doe", "--password-stdin",
	}, strings.NewReader("secret\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list staticusers.UserList
	if err := yaml.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	if list.Version != "v1" || len(list.Users) != 1 {
		t.Fatalf("user list = %#v", list)
	}
	user := list.Users[0]
	if user.Username != "alice" || user.Email != "alice@example.com" || user.DisplayName != "Alice Doe" {
		t.Fatalf("user = %#v", user)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("secret")); err != nil {
		t.Fatalf("stored password hash does not match: %v", err)
	}
	if !strings.Contains(stdout.String(), "typed role binding") {
		t.Fatalf("stdout = %q, want role binding reminder", stdout.String())
	}
}

func TestValidateLocalConfigRejectsMalformedInputs(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(configPath, []byte("[api\nport = 4000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte("version: v1\nroles: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"validate-local-config", "--config-file", configPath, "--policy-file", policyPath}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Invalid configuration") {
		t.Fatalf("malformed config: code = %d, stderr = %q", code, stderr.String())
	}

	validConfig := filepath.Join("..", "..", "..", "config", "config.toml")
	stderr.Reset()
	t.Setenv("GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY", "config-validation-placeholder")
	code = run([]string{"validate-local-config", "--config-file", validConfig, "--policy-file", policyPath}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Invalid RBAC policy") {
		t.Fatalf("malformed policy: code = %d, stderr = %q", code, stderr.String())
	}
}

func TestUsersAddRejectsDuplicateWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	original := []byte("version: v1\nusers:\n  - username: alice\n    password_hash: old\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"users", "add", "--file", path, "--username", "alice", "--password-stdin",
	}, strings.NewReader("new-secret\n"), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("duplicate add changed file:\n%s", got)
	}
}

func TestRBACRoleAddAndBindingAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"rbac", "role", "add", "--file", path, "--name", "developer",
		"--allow", "repository.read,repository.contents.write",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("role add code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run([]string{
		"rbac", "binding", "add", "--file", path, "--subject", "alice", "--role", "developer",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("binding add code = %d, stderr = %q", code, stderr.String())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"developer:", "repository", "contents", "read", "write", "name: alice", "kind: ClusterRole"} {
		if !strings.Contains(text, want) {
			t.Fatalf("policy missing %q:\n%s", want, text)
		}
	}
}

func TestRBACScopedCLIAndInvalidWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	exec := func(args ...string) (int, string) {
		var out, err bytes.Buffer
		code := run(append([]string{"rbac"}, args...), strings.NewReader(""), &out, &err)
		return code, err.String()
	}
	if code, err := exec("role", "add", "--file", path, "--name", "reader", "--allow", "product.read", "--scope", "namespace"); code != 0 {
		t.Fatalf("%d: %s", code, err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"role", "add", "--name", "bad", "--allow", "product.read", "--deny", ""},
		{"role", "add", "--name", "bad", "--allow", "repository.read.any"},
		{"role", "add", "--name", "bad", "--allow", "product.read", "--namespace", "acme"},
		{"binding", "add", "--subject", "alice", "--role", "reader", "--scope", "namespace", "--role-kind", "Role"},
		{"binding", "add", "--subject", "alice", "--role", "reader", "--namespace", "acme"},
		{"binding", "add", "--subject", "alice", "--role", "reader", "--scope", "wrong"},
	} {
		if code, _ := exec(append(args, "--file", path)...); code == 0 {
			t.Fatalf("accepted %v", args)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, after) {
			t.Fatalf("invalid operation changed file: %v", args)
		}
	}
	args := []string{"binding", "add", "--file", path, "--subject", "controller", "--subject-kind", "ServiceAccount", "--role", "reader", "--role-kind", "Role", "--scope", "namespace", "--namespace", "acme"}
	if code, err := exec(args...); code != 0 {
		t.Fatalf("%d: %s", code, err)
	}
	original, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := exec(args...); code != 0 {
		t.Fatalf("%d: %s", code, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("duplicate binding changed file")
	}
}

func TestProjectionRepairRequiresExplicitConfirmation(t *testing.T) {
	original := openProjectionRepair
	t.Cleanup(func() { openProjectionRepair = original })
	called := false
	openProjectionRepair = func(config.ScyllaConfig) (projectionRepairClient, error) {
		called = true
		return &fakeProjectionRepairClient{}, nil
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"scylla-projection-repair"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if called {
		t.Fatal("repair opened Scylla without confirmation")
	}
	if !strings.Contains(stderr.String(), "--dry-run or explicit --confirm") {
		t.Fatalf("stderr = %q, want confirmation guidance", stderr.String())
	}
}

func TestProjectionRepairDryRunPrintsPlanWithoutApply(t *testing.T) {
	client := &fakeProjectionRepairClient{
		plan: scylla.RepairPlan{Findings: []scylla.ProjectionFinding{{
			Type: scylla.FindingMissing, Kind: "Namespace", UID: "uid",
			Table: "namespaces_by_name", Key: "demo", Repairable: true,
		}}},
	}
	withFakeProjectionClient(t, client)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"scylla-projection-repair", "--dry-run", "--hosts", "scylla-a:9042,scylla-b:9042"},
		strings.NewReader(""), &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if client.applyCalls != 0 {
		t.Fatalf("dry run applied %d repairs", client.applyCalls)
	}
	if !strings.Contains(stdout.String(), `"type": "missing"`) {
		t.Fatalf("stdout = %q, want stable JSON finding", stdout.String())
	}
}

func TestProjectionRepairConfirmedAppliesAndVerifies(t *testing.T) {
	client := &fakeProjectionRepairClient{
		plan: scylla.RepairPlan{Actions: []scylla.RepairAction{{
			Type: scylla.RepairInsert, Kind: "Namespace", UID: "uid", ExpectedResourceVersion: "7",
			After: &scylla.ProjectionRecord{Table: "namespaces_by_name", UID: "uid", Name: "demo"},
		}}},
		result: scylla.RepairResult{PlannedActions: 1, AppliedActions: 1},
	}
	withFakeProjectionClient(t, client)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"scylla-projection-repair", "--confirm", "--keyspace", "gitstore"},
		strings.NewReader(""), &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if client.applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", client.applyCalls)
	}
	if !strings.Contains(stdout.String(), `"appliedActions": 1`) {
		t.Fatalf("stdout = %q, want applied action summary", stdout.String())
	}
}

func TestProjectionAuditPrintsMachineReadableSummary(t *testing.T) {
	client := &fakeProjectionRepairClient{plan: scylla.RepairPlan{}}
	withFakeProjectionClient(t, client)

	var stdout, stderr bytes.Buffer
	code := run([]string{"scylla-projection-audit"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != `{
  "findings": null,
  "actions": null
}` {
		t.Fatalf("stdout = %q, want stable JSON summary", stdout.String())
	}
}

func withFakeProjectionClient(t *testing.T, client *fakeProjectionRepairClient) {
	t.Helper()
	original := openProjectionRepair
	t.Cleanup(func() { openProjectionRepair = original })
	openProjectionRepair = func(config.ScyllaConfig) (projectionRepairClient, error) {
		return client, nil
	}
}

type fakeProjectionRepairClient struct {
	plan       scylla.RepairPlan
	result     scylla.RepairResult
	auditErr   error
	applyErr   error
	applyCalls int
}

func (f *fakeProjectionRepairClient) Audit(context.Context) (scylla.RepairPlan, error) {
	return f.plan, f.auditErr
}

func (f *fakeProjectionRepairClient) Apply(context.Context, scylla.RepairPlan) (scylla.RepairResult, error) {
	f.applyCalls++
	return f.result, f.applyErr
}

func (f *fakeProjectionRepairClient) Close() {}
