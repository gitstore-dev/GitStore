// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func namespaceCreator(t *testing.T) *rbaclocal.RBACLocalProvider {
	t.Helper()
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
	provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
	require.NoError(t, err)
	return provider
}

func TestStaffNamespaceCreationUsesTrustedUsernameAndPolicy(t *testing.T) {
	provider := namespaceCreator(t)
	for _, scenario := range []string{"staff", "customer", "workload", "missing-username", "wrong-name", "organization", "unlinked"} {
		principal := &auth.Principal{Subject: "alice", AuthMethod: "oidc-jwt",
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

type linkedHumanDirectory struct {
	auth.UserDirProvider
}

func (linkedHumanDirectory) GetBySubject(context.Context, string) (*auth.UserProfile, error) {
	return &auth.UserProfile{
		Subject: "directory-uid", Username: "directory-name", Kind: auth.ProfileStaff,
		Active: true, Lifecycle: auth.ActorActive,
	}, nil
}

func (linkedHumanDirectory) ListGroups(context.Context, string) ([]string, error) {
	return nil, nil
}

func TestStaffNamespaceCreationAgreesWithAuthenticatedIdentity(t *testing.T) {
	provider := namespaceCreator(t)
	usersFile := filepath.Join(t.TempDir(), "users.yaml")
	require.NoError(t, os.WriteFile(usersFile, []byte("version: v1\nusers:\n  - username: authenticated-name\n    password_hash: unused\n"), 0600))
	staticDirectory, err := staticusers.NewDirectory(config.StaticUsersConfig{UsersFile: usersFile}, zap.NewNop())
	require.NoError(t, err)

	for _, method := range []string{"oidc-jwt", "static-users"} {
		for _, directory := range []struct {
			name     string
			provider auth.UserDirProvider
			staff    bool
		}{
			{"linked-human", linkedHumanDirectory{}, true},
			{"static-users", staticDirectory, false},
			{"none", nil, false},
		} {
			t.Run(method+"/"+directory.name, func(t *testing.T) {
				authenticated := &auth.Principal{
					Subject: "authenticated-name", Issuer: "selected-authn", AuthMethod: method,
					Groups: []string{"system:authenticated"},
				}
				principal, err := auth.EnrichPrincipal(t.Context(), directory.provider, authenticated)
				require.NoError(t, err)
				require.Equal(t, authenticated.Subject, principal.Subject)
				require.Equal(t, authenticated.Issuer, principal.Issuer)
				require.Equal(t, authenticated.AuthMethod, principal.AuthMethod)
				if directory.staff {
					require.Equal(t, "directory-name", principal.Username)
					require.Contains(t, principal.Groups, "system:staff")
				}
				for _, name := range []string{"authenticated-name", "directory-name", "directory-uid"} {
					resource := auth.ResourceContext{Kind: "namespace", Name: name, Attrs: map[string]any{"tier": "USER"}}
					decision, err := provider.Authorize(t.Context(), principal, "namespace.create", resource)
					require.NoError(t, err)
					identity := namespaceadmission.ValidateIdentity(namespaceadmission.PolicyCheck{
						Name: name, Tier: datastore.NamespaceTierUser, Actor: principal.Subject,
					}, nil)
					if name == authenticated.Subject {
						require.Nil(t, identity)
					} else {
						require.NotNil(t, identity)
					}
					want := auth.OutcomeDeny
					if directory.staff && name == authenticated.Subject {
						want = auth.OutcomeAllow
					}
					require.Equal(t, want, decision.Outcome, name)
				}
			})
		}
	}
}
