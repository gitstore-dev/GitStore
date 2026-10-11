// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestV1Beta1EffectivePermissionUsesSystemGroupAndSatisfiableConditions(t *testing.T) {
	for _, tc := range []struct {
		name, when string
		want       bool
	}{
		{"authenticated-creator", "[{attr: tier, op: eq, value: USER}]", true},
		{"contradiction", "[{attr: tier, op: eq, value: USER}, {attr: tier, op: neq, value: USER}]", false},
		{"owner", "[owner]", true},
		{"impossible-tier", "[{attr: tier, op: eq, value: ALIEN}]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.yaml")
			require.NoError(t, os.WriteFile(path, []byte("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  creator:\n    rules: [{resources: [namespace], verbs: [create], when: "+tc.when+"}]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: creator}\n    subjects: [{kind: Group, name: 'system:authenticated'}]\n"), 0600))
			provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
			require.NoError(t, err)
			assert.Equal(t, tc.want, provider.HasAnyRoleBindingFor([]string{"alice"}))
		})
	}
}

// stubProvider is a test-only AuthNProvider with a fixed Authenticate outcome.
type stubProvider struct {
	name      string
	outcome   authpkg.Outcome
	reason    string
	principal *authpkg.Principal
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Capabilities() authpkg.Capability { return authpkg.CapAuthenticate }

func (s *stubProvider) Authenticate(_ context.Context, _ authpkg.AuthRequest) (*authpkg.Principal, authpkg.Decision, error) {
	switch s.outcome {
	case authpkg.OutcomeAllow:
		if s.principal != nil {
			return s.principal, authpkg.Allow(s.name, s.reason), nil
		}
		return &authpkg.Principal{Subject: s.name, AuthMethod: s.name}, authpkg.Allow(s.name, s.reason), nil
	case authpkg.OutcomeDeny:
		return nil, authpkg.Deny(s.name, s.reason), nil
	default:
		return nil, authpkg.Challenge(s.name, s.reason), nil
	}
}

func TestChain_NormalizesTrustedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		principal      authpkg.Principal
		groups         []string
		deny           bool
	}{
		{"human", "static-users", authpkg.Principal{Subject: "alice", AuthMethod: "basic", Groups: []string{"team", "team", "system:unauthenticated", "system:serviceaccounts"}}, []string{"team", "system:authenticated"}, false},
		{"anonymous", "anonymous", authpkg.Principal{Subject: "anon", AuthMethod: "none", Groups: []string{"team", "system:masters", "system:authenticated"}}, []string{"system:unauthenticated"}, false},
		{"account", "serviceaccount-jwt", authpkg.Principal{Subject: "system:serviceaccount:controller", AuthMethod: "serviceaccount-jwt", ServiceAccountUID: "uid", Groups: []string{"operators"}}, []string{"operators", "system:authenticated", "system:serviceaccounts"}, false},
		{"assertion", "serviceaccount-assertion", authpkg.Principal{Subject: "system:serviceaccount:controller", AuthMethod: "serviceaccount-assertion", ServiceAccountUID: "uid"}, []string{"system:authenticated", "system:serviceaccounts"}, false},
		{"internal-spoof", "oidc-jwt", authpkg.Principal{Subject: "system:api", AuthMethod: "internal"}, nil, true},
		{"account-spoof", "oidc-jwt", authpkg.Principal{Subject: "system:serviceaccount:controller", AuthMethod: "serviceaccount-jwt", ServiceAccountUID: "uid"}, nil, true},
		{"uid-spoof", "static-users", authpkg.Principal{Subject: "alice", AuthMethod: "basic", ServiceAccountUID: "uid"}, nil, true},
		{"missing-uid", "serviceaccount-jwt", authpkg.Principal{Subject: "system:serviceaccount:controller", AuthMethod: "serviceaccount-jwt"}, nil, true},
		{"legacy", "serviceaccount-jwt", authpkg.Principal{Subject: "serviceaccount:namespace:controller", AuthMethod: "serviceaccount-jwt", ServiceAccountUID: "uid"}, nil, true},
		{"reserved", "static-users", authpkg.Principal{Subject: "system:masters", AuthMethod: "basic"}, nil, true},
		{"anonymous-spoof", "oidc-jwt", authpkg.Principal{Subject: "anon", AuthMethod: "none"}, nil, true},
		{"overflow", "static-users", authpkg.Principal{Subject: "alice", AuthMethod: "basic", Groups: make([]string, 257)}, nil, true},
		{"missing-method", "static-users", authpkg.Principal{Subject: "alice"}, nil, true},
		{"control-subject", "static-users", authpkg.Principal{Subject: "alice\n", AuthMethod: "basic"}, nil, true},
		{"control-group", "static-users", authpkg.Principal{Subject: "alice", AuthMethod: "basic", Groups: []string{"team\n"}}, nil, true},
		{"control-uid", "serviceaccount-jwt", authpkg.Principal{Subject: "system:serviceaccount:controller", AuthMethod: "serviceaccount-jwt", ServiceAccountUID: "uid\n"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]string(nil), tc.principal.Groups...)
			chain := authpkg.NewChainedAuthN(&stubProvider{name: tc.provider, outcome: authpkg.OutcomeAllow, principal: &tc.principal})
			got, decision, err := chain.Authenticate(context.Background(), authpkg.AuthRequest{})
			require.NoError(t, err)
			if tc.deny {
				require.Equal(t, authpkg.OutcomeDeny, decision.Outcome)
				require.Nil(t, got)
				return
			}
			require.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
			require.Equal(t, tc.groups, got.Groups)
			require.Equal(t, original, tc.principal.Groups, "normalization must not mutate provider-owned identity")
		})
	}
}

func (s *stubProvider) RevokeSession(_ context.Context, _ string, _ time.Time) error {
	return authpkg.ErrNotSupported
}

func (s *stubProvider) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, authpkg.ErrNotSupported
}

func (s *stubProvider) IssueSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, authpkg.ErrNotSupported
}

func TestChain_FirstAllowWins(t *testing.T) {
	p1 := &stubProvider{name: "p1", outcome: authpkg.OutcomeAllow, reason: "ok"}
	p2 := &stubProvider{name: "p2", outcome: authpkg.OutcomeAllow, reason: "ok too"}
	chain := authpkg.NewChainedAuthN(p1, p2)

	principal, decision, err := chain.Authenticate(context.Background(), authpkg.AuthRequest{Header: http.Header{}})

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, "p1", principal.Subject)
}

func TestChain_DenyShortCircuits(t *testing.T) {
	p1 := &stubProvider{name: "p1", outcome: authpkg.OutcomeDeny, reason: "blocked"}
	p2 := &stubProvider{name: "p2", outcome: authpkg.OutcomeAllow, reason: "ok"}
	chain := authpkg.NewChainedAuthN(p1, p2)

	principal, decision, err := chain.Authenticate(context.Background(), authpkg.AuthRequest{Header: http.Header{}})

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, decision.Outcome)
	assert.Nil(t, principal)
	assert.Equal(t, "p1", decision.Provider)
}

func TestChain_AllChallenge_ReturnsDeny(t *testing.T) {
	p1 := &stubProvider{name: "p1", outcome: authpkg.OutcomeChallenge, reason: "not mine"}
	p2 := &stubProvider{name: "p2", outcome: authpkg.OutcomeChallenge, reason: "not mine either"}
	chain := authpkg.NewChainedAuthN(p1, p2)

	principal, decision, err := chain.Authenticate(context.Background(), authpkg.AuthRequest{Header: http.Header{}})

	require.NoError(t, err)
	// Must be Deny, NOT Allow(Anonymous()) — credentials present but rejected by all.
	assert.Equal(t, authpkg.OutcomeDeny, decision.Outcome)
	assert.Nil(t, principal)
}

func TestChain_AnonymousProvider_NoCredentials_AllowAnonymous(t *testing.T) {
	chain := authpkg.NewChainedAuthN(anonymous.New())

	req := authpkg.AuthRequest{Header: http.Header{}} // no Authorization header
	principal, decision, err := chain.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, "anon", principal.Subject)
	assert.Equal(t, "none", principal.AuthMethod)
}

func TestChain_AnonymousProvider_CredentialsPresent_Challenge(t *testing.T) {
	// Anonymous is the only provider but credentials are present → chain returns Deny.
	chain := authpkg.NewChainedAuthN(anonymous.New())

	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Bearer sometoken"}}}
	principal, decision, err := chain.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, decision.Outcome)
	assert.Nil(t, principal)
}

func TestChain_ChallengeBeforeAllow_SkipsToNext(t *testing.T) {
	p1 := &stubProvider{name: "p1", outcome: authpkg.OutcomeChallenge, reason: "not mine"}
	p2 := &stubProvider{name: "p2", outcome: authpkg.OutcomeAllow, reason: "mine"}
	chain := authpkg.NewChainedAuthN(p1, p2)

	principal, decision, err := chain.Authenticate(context.Background(), authpkg.AuthRequest{Header: http.Header{}})

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, "p2", principal.Subject)
}
