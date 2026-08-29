// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const testPolicy = `version: v1
default_deny: true
roles:
  admin:
    allow:
      - "*"
  developer:
    allow:
      - "namespace.create.user"
      - "namespace.delete.own"
    deny:
      - "namespace.delete.any"
      - "namespace.create.organization"
  anonymous:
    deny:
      - "*"
role_bindings:
  admin: [admin]
  developer: [developer]
  anon: [anonymous]
`

func newRBACProvider(t *testing.T, policyContent string) *RBACLocalProvider {
	t.Helper()
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	require.NoError(t, os.WriteFile(policyPath, []byte(policyContent), 0600))

	p, err := New(config.RBACConfig{PolicyFile: policyPath}, zap.NewNop())
	require.NoError(t, err)
	return p
}

func adminPrincipal() *authpkg.Principal {
	return &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin"}
}

func developerPrincipal() *authpkg.Principal {
	return &authpkg.Principal{Subject: "dev", Roles: []string{"developer"}, AuthMethod: "static-admin"}
}

func anonPrincipal() *authpkg.Principal {
	return authpkg.Anonymous()
}

func TestRBACLocal_AdminDeleteAny_Allow(t *testing.T) {
	p := newRBACProvider(t, testPolicy)
	d, err := p.Authorize(context.Background(), adminPrincipal(), "namespace.delete.any", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, d.Outcome)
}

func TestRBACLocal_DeveloperDeleteAny_Deny(t *testing.T) {
	p := newRBACProvider(t, testPolicy)
	d, err := p.Authorize(context.Background(), developerPrincipal(), "namespace.delete.any", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}

func TestRBACLocal_AdminCreateOrganization_Allow(t *testing.T) {
	p := newRBACProvider(t, testPolicy)
	d, err := p.Authorize(context.Background(), adminPrincipal(), "namespace.create.organization", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, d.Outcome)
}

func TestRBACLocal_DeveloperCreateOrganization_Deny(t *testing.T) {
	p := newRBACProvider(t, testPolicy)
	d, err := p.Authorize(context.Background(), developerPrincipal(), "namespace.create.organization", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}

func TestRBACLocal_AnonymousCreateOrganization_Deny(t *testing.T) {
	p := newRBACProvider(t, testPolicy)
	// Anonymous principal has no roles matching the policy's named roles; default_deny=true → Deny.
	d, err := p.Authorize(context.Background(), anonPrincipal(), "namespace.create.organization", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}

func TestRBACLocal_DefaultDenyTrue_UnmatchedAction_Deny(t *testing.T) {
	p := newRBACProvider(t, testPolicy)
	d, err := p.Authorize(context.Background(), developerPrincipal(), "some.unknown.action", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}

func TestRBACLocal_ExplicitDenyOverridesAllow(t *testing.T) {
	// Policy where a role both allows and denies the same action — deny must win.
	policy := `version: v1
default_deny: false
roles:
  conflicted:
    allow:
      - "something.do"
    deny:
      - "something.do"
`
	p := newRBACProvider(t, policy)
	principal := &authpkg.Principal{Subject: "u", Roles: []string{"conflicted"}, AuthMethod: "test"}
	d, err := p.Authorize(context.Background(), principal, "something.do", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}

func TestRBACLocal_WildcardAllowMatchesAllActions(t *testing.T) {
	policy := `version: v1
default_deny: true
roles:
  superuser:
    allow:
      - "*"
`
	p := newRBACProvider(t, policy)
	principal := &authpkg.Principal{Subject: "su", Roles: []string{"superuser"}, AuthMethod: "test"}
	for _, action := range []string{"namespace.create.user", "repo.delete", "anything.at.all"} {
		d, err := p.Authorize(context.Background(), principal, action, authpkg.ResourceContext{})
		require.NoError(t, err)
		assert.Equal(t, authpkg.OutcomeAllow, d.Outcome, "action %q should be allowed", action)
	}
}

func TestRBACLocal_DefaultDenyAbsent_DefaultsToTrue(t *testing.T) {
	// Policy with no default_deny key — should secure-default to deny for unmatched actions.
	policy := `version: v1
roles:
  reader:
    allow:
      - "repo.read"
`
	p := newRBACProvider(t, policy)
	principal := &authpkg.Principal{Subject: "u", Roles: []string{"reader"}, AuthMethod: "test"}
	// Matched action → allow.
	d, err := p.Authorize(context.Background(), principal, "repo.read", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, d.Outcome)
	// Unmatched action → deny (default_deny defaults to true).
	d, err = p.Authorize(context.Background(), principal, "namespace.create.organization", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}

func TestRBACLocal_RoleBindings_SubjectWithNoRoles_GetsBindingRoles(t *testing.T) {
	// Principal arrives with no Roles but has a matching role_bindings entry.
	policy := `version: v1
default_deny: true
roles:
  developer:
    allow:
      - "namespace.create.user"
role_bindings:
  alice: [developer]
`
	p := newRBACProvider(t, policy)
	// Alice has no pre-populated Roles; the binding should grant her developer access.
	alice := &authpkg.Principal{Subject: "alice", Roles: nil, AuthMethod: "test"}
	d, err := p.Authorize(context.Background(), alice, "namespace.create.user", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, d.Outcome)

	// Unknown subject has no binding → default deny.
	bob := &authpkg.Principal{Subject: "bob", Roles: nil, AuthMethod: "test"}
	d, err = p.Authorize(context.Background(), bob, "namespace.create.user", authpkg.ResourceContext{})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}
