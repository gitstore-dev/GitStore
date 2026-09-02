// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

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
	if !strings.Contains(stdout.String(), "role_bindings") {
		t.Fatalf("stdout = %q, want role binding reminder", stdout.String())
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
		"--allow", "repository.read,repository.write", "--deny", "repository.delete.any",
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
	for _, want := range []string{"developer:", "repository.read", "repository.write", "repository.delete.any", "alice:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("policy missing %q:\n%s", want, text)
		}
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
