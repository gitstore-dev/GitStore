// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestStaffNamespaceCreationUsesTrustedUsernameAndPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
clusterRoles:
  namespace-creator:
    rules:
      - resources: [namespace]
        verbs: [create]
        when: [subjectUsername, {attr: tier, op: eq, value: USER}]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: namespace-creator}
    subjects: [{kind: Group, name: "system:staff"}]
`), 0600))
	provider, err := New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
	require.NoError(t, err)
	for _, scenario := range []string{"staff", "customer", "workload", "missing-username", "wrong-name", "organization", "unlinked"} {
		principal := &auth.Principal{Subject: "provider-id", AuthMethod: "oidc-jwt",
			Username: "alice", ProfileKind: auth.ProfileStaff, Groups: []string{"system:staff"}}
		resource := auth.ResourceContext{Kind: "namespace", Name: "alice", Attrs: map[string]any{"tier": "USER"}}
		switch scenario {
		case "customer":
			principal.ProfileKind, principal.Groups = auth.ProfileCustomer, []string{"system:customers"}
		case "workload":
			principal.Subject, principal.AuthMethod = "system:serviceaccount:alice", "serviceaccount-jwt"
			principal.ServiceAccountUID, principal.ProfileKind = "workload-uid", auth.ProfileServiceAccount
			principal.Groups = []string{"system:serviceaccounts"}
		case "missing-username":
			principal.Username = ""
		case "wrong-name":
			resource.Name = "bob"
		case "organization":
			resource.Attrs["tier"] = "ORGANIZATION"
		case "unlinked":
			principal.ProfileKind = ""
		}
		decision, err := provider.Authorize(t.Context(), principal, "namespace.create", resource)
		require.NoError(t, err)
		if scenario == "staff" {
			require.Equal(t, auth.OutcomeAllow, decision.Outcome)
		} else {
			require.Equal(t, auth.OutcomeDeny, decision.Outcome, scenario)
		}
	}
}
