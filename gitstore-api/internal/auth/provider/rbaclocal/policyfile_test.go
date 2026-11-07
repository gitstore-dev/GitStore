// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"errors"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddRolePreservesPolicyAndRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	original := "# policy comment\napiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  admin:\n    rules: [{resources: [\"*\"], verbs: [\"*\"]}]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: admin}\n    subjects: [{kind: User, name: alice}]\n"
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
	if err := AddRole(path, "developer", RolePolicy{Allow: []string{"repository.contents.write"}}); !errors.Is(err, ErrRoleExists) {
		t.Fatalf("AddRole() duplicate error = %v, want ErrRoleExists", err)
	}
}

func TestAssignRoleIsIdempotentAndRejectsUndefinedRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	policy := "apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  developer:\n    rules: [{resources: [repository], verbs: [read]}]\n"
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
	if _, ok := policy.ClusterRoles["reader"]; !ok {
		t.Fatalf("roles = %#v, want reader", policy.ClusterRoles)
	}
}

func TestRoleEditingExactProjectionAndTypedBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	input := []string{"product.read", "product.management.list", "repository.contents.read", "repository.contents.write"}
	require.NoError(t, AddRole(path, "editor", RolePolicy{Allow: input}, RoleOptions{Scope: "namespace"}))
	policy, err := loadPolicy(path)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, rule := range policy.Roles["editor"].Rules {
		for _, resource := range rule.Resources {
			for _, verb := range rule.Verbs {
				definitions, err := ExpandActions([]string{resource}, []string{verb})
				require.NoError(t, err)
				require.Len(t, definitions, 1)
				got[definitions[0].Action] = true
			}
		}
	}
	require.Len(t, got, len(input))
	for _, action := range input {
		require.True(t, got[action], action)
	}
	for _, kind := range []string{"User", "Group", "ServiceAccount"} {
		options := BindingOptions{Scope: "namespace", Namespace: "acme", RoleKind: "Role", SubjectKind: kind}
		added, err := AssignRole(path, "reader", "editor", options)
		require.NoError(t, err)
		require.True(t, added)
		original, err := os.ReadFile(path)
		require.NoError(t, err)
		added, err = AssignRole(path, "reader", "editor", options)
		require.NoError(t, err)
		require.False(t, added)
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, original, after)
	}
}
func TestRoleEditingRejectsInvalidCandidateWithoutWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, AddRole(path, "reader", RolePolicy{Allow: []string{"repository.read"}}))
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, input := range []RolePolicy{{Allow: []string{"repository.read.any"}}, {Allow: []string{"repository.*"}}, {Deny: []string{"repository.delete"}}, {Allow: []string{"repository.read"}, Deny: []string{"product.delete"}}} {
		require.Error(t, AddRole(path, "bad", input))
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, original, after)
	}
	require.Error(t, AddRole(path, "cluster-action", RolePolicy{Allow: []string{"namespace.create"}}, RoleOptions{Scope: "namespace"}))
	for _, options := range []BindingOptions{
		{Scope: "invalid"}, {Scope: "namespace"}, {Scope: "cluster", Namespace: "acme"}, {Scope: "cluster", RoleKind: "Role"}, {SubjectKind: "Unknown"},
	} {
		_, err := AssignRole(path, "alice", "reader", options)
		require.Error(t, err)
	}
	_, err = AssignRole(path, "system:serviceaccount:controller", "reader", BindingOptions{SubjectKind: "ServiceAccount"})
	require.Error(t, err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, after)
}
