// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubProvider is a test-only AuthNProvider with a fixed Authenticate outcome.
type stubProvider struct {
	name    string
	outcome authpkg.Outcome
	reason  string
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Capabilities() authpkg.Capability { return authpkg.CapAuthenticate }

func (s *stubProvider) Authenticate(_ context.Context, _ authpkg.AuthRequest) (*authpkg.Principal, authpkg.Decision, error) {
	switch s.outcome {
	case authpkg.OutcomeAllow:
		return &authpkg.Principal{Subject: s.name, AuthMethod: s.name}, authpkg.Allow(s.name, s.reason), nil
	case authpkg.OutcomeDeny:
		return nil, authpkg.Deny(s.name, s.reason), nil
	default:
		return nil, authpkg.Challenge(s.name, s.reason), nil
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
