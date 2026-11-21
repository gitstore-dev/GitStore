// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"path/filepath"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestUserActionsOnlyClusterAdminByDefault(t *testing.T) {
	actions := []string{"user.create", "user.read", "user.list", "user.update", "user.delete", "user.group.update"}
	for _, path := range []string{"../../../../../config/policy.yaml", "../../../../policy.yaml.example"} {
		policy, err := loadPolicy(path)
		require.NoError(t, err)
		for role := range policy.ClusterRoles {
			t.Run(path+"/"+role, func(t *testing.T) {
				isolated := *policy
				isolated.RoleBindings = nil
				isolated.ClusterRoleBindings = []Binding{{
					RoleRef:  RoleRef{Kind: "ClusterRole", Name: role},
					Subjects: []SubjectRef{{Kind: "User", Name: "operator"}},
				}}
				provider := &RBACLocalProvider{}
				provider.snapshot.Store(compilePolicy(&isolated))
				principal := &auth.Principal{Subject: "operator", AuthMethod: "oidc-jwt",
					Groups: []string{"system:authenticated", "system:staff"}, Username: "operator", ProfileKind: auth.ProfileStaff}
				for _, action := range actions {
					decision, err := provider.Authorize(t.Context(), principal, action, auth.ResourceContext{Kind: "user", Name: "target"})
					require.NoError(t, err)
					require.Equal(t, role == "cluster-admin", decision.Outcome == auth.OutcomeAllow, action)
				}
			})
		}
	}
}

func TestUserActionsProjectOnlyToClusterRoles(t *testing.T) {
	actions := []string{"user.create", "user.read", "user.list", "user.update", "user.delete", "user.group.update"}
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, AddRole(path, "user-manager", RolePolicy{Allow: actions}, RoleOptions{Scope: "cluster"}))
	policy, err := loadPolicy(path)
	require.NoError(t, err)
	for _, rule := range policy.ClusterRoles["user-manager"].Rules {
		expanded, err := ExpandActions(rule.Resources, rule.Verbs)
		require.NoError(t, err)
		for _, action := range expanded {
			require.Equal(t, "cluster", action.Scope)
		}
	}
	namespaced := filepath.Join(t.TempDir(), "namespaced.yaml")
	require.Error(t, AddRole(namespaced, "user-manager", RolePolicy{Allow: actions}, RoleOptions{Scope: "namespace"}))
	require.NoFileExists(t, namespaced, "invalid cluster capabilities must not persist a namespaced role")
}
