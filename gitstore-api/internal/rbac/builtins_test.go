// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEveryReleaseBuiltinRejectsGitShadowingDeletionAndStatusMutation(t *testing.T) {
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	resources, err := builtins.Resources()
	require.NoError(t, err)
	protection := rbac.Protection{Builtins: builtins}
	for _, resource := range resources {
		t.Run(string(resource.Kind)+"/"+resource.Metadata.Name, func(t *testing.T) {
			require.ErrorIs(t, protection.AuthorChange(nil, resource), rbac.ErrInvalid)
			require.ErrorIs(t, protection.AuthorChange(resource, nil), rbac.ErrInvalid)
			copy, err := rbac.CloneResource(resource)
			require.NoError(t, err)
			require.ErrorIs(t, protection.StatusChange(resource, copy), rbac.ErrInvalid)
			path, err := rbac.CanonicalPath(resource.Kind, resource.Metadata.Name)
			if err != nil {
				require.Empty(t, path)
				return
			}
			manifest, err := rbac.CanonicalManifest(resource)
			require.NoError(t, err)
			external, err := rbac.ParseManifest(manifest, path, rbac.RepositoryScope{
				Namespace: "gitstore-system", NamespaceUID: uuid.NewString(), Root: true,
			})
			require.NoError(t, err)
			require.ErrorIs(t, protection.AuthorChange(nil, external), rbac.ErrInvalid)
		})
	}
}

func TestReleaseBuiltinsAreDeterministicAndCannotBeChangedThroughReadback(t *testing.T) {
	first, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	second, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	require.Equal(t, first.Digest(), second.Digest())
	role, err := first.Get(rbac.ClusterRole, "namespace-full-control")
	require.NoError(t, err)
	original := role.Metadata.UID
	role.Metadata.Name = "changed"
	again, err := first.Get(rbac.ClusterRole, "namespace-full-control")
	require.NoError(t, err)
	require.Equal(t, original, again.Metadata.UID)
	spec, err := rbac.DecodeRoleSpec(again)
	require.NoError(t, err)
	for _, rule := range spec.Rules {
		actions, err := rbaclocal.ExpandActions(rule.Resources, rule.Verbs)
		require.NoError(t, err)
		for _, action := range actions {
			require.Equal(t, "namespace", action.Scope)
		}
	}
	for _, bindingName := range []string{"namespace-owner", "namespace-creator"} {
		binding, err := first.Get(rbac.ClusterRoleBinding, bindingName)
		require.NoError(t, err)
		spec, err := rbac.DecodeBindingSpec(binding)
		require.NoError(t, err)
		group := "system:authenticated"
		if bindingName == "namespace-creator" {
			group = "system:staff"
		}
		require.Equal(t, []rbac.Subject{{Kind: "Group", Name: group}}, spec.Subjects)
	}
}
