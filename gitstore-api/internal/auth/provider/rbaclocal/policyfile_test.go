// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddRolePreservesPolicyAndRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	original := "# policy comment\nversion: v1\nroles:\n  admin:\n    allow: [\"*\"]\n    deny: []\ndefault_deny: true\nrole_bindings:\n  alice: [admin]\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := AddRole(path, "developer", RolePolicy{Allow: []string{"repository.read"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "# policy comment") || !strings.Contains(string(b), "developer:") {
		t.Fatalf("policy content not preserved:\n%s", b)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 640", got)
	}
	if err := AddRole(path, "developer", RolePolicy{Allow: []string{"repository.write"}}); !errors.Is(err, ErrRoleExists) {
		t.Fatalf("AddRole() duplicate error = %v, want ErrRoleExists", err)
	}
}

func TestAssignRoleIsIdempotentAndRejectsUndefinedRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	policy := "version: v1\nroles:\n  developer:\n    allow: [repository.read]\n    deny: []\ndefault_deny: true\nrole_bindings: {}\n"
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := AssignRole(path, "alice", "developer")
	if err != nil || !added {
		t.Fatalf("AssignRole() = (%v, %v), want (true, nil)", added, err)
	}
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err = AssignRole(path, "alice", "developer")
	if err != nil || added {
		t.Fatalf("second AssignRole() = (%v, %v), want (false, nil)", added, err)
	}
	afterSecond, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Fatal("idempotent assignment rewrote the policy")
	}
	if _, err := AssignRole(path, "alice", "missing"); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("undefined role error = %v", err)
	}
}

func TestAddRoleCreatesNewPolicyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := AddRole(path, "reader", RolePolicy{Allow: []string{"repository.read"}}); err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := policy.Roles["reader"]; !ok {
		t.Fatalf("roles = %#v, want reader", policy.Roles)
	}
}
