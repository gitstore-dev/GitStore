// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package staticadmin

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func newTestViper(username, passwordHash, secret, issuer string) *viper.Viper {
	v := viper.New()
	v.SetDefault("auth.admin.username", username)
	v.SetDefault("auth.admin.password_hash", passwordHash)
	v.SetDefault("auth.jwt.secret", secret)
	v.SetDefault("auth.jwt.issuer", issuer)
	v.SetDefault("auth.jwt.duration", "1h")
	return v
}

func mustBcrypt(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

func TestStaticAdmin_BearerJWT_Allow(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Bearer " + token}}}
	principal, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, "admin", principal.Subject)
	assert.Contains(t, principal.Roles, "admin")
	assert.Equal(t, "static-admin", principal.AuthMethod)
}

func TestStaticAdmin_ExpiredJWT_Deny(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	// Use a very short duration so the token expires before the leeway window.
	v.SetDefault("auth.jwt.duration", "-10m") // already in the past by 10 minutes
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)
	// No sleep needed; token was issued with exp already past the leeway window.

	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Bearer " + token}}}
	_, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	// Expired token from our issuer → Deny (not Challenge).
	assert.Equal(t, authpkg.OutcomeDeny, decision.Outcome)
}

func TestStaticAdmin_BlacklistedJTI_Deny(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	token, exp, err := p.IssueToken("admin")
	require.NoError(t, err)

	// Parse the token to extract its jti.
	newToken, _, err := p.RefreshSession(context.Background(), token)
	require.NoError(t, err)
	// The old token was revoked during refresh; re-using it must be denied.
	_ = exp
	_ = newToken

	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Bearer " + token}}}
	_, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, decision.Outcome)
}

func TestStaticAdmin_WrongIssuer_Challenge(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	// Issue a token with a different issuer by temporarily using a different viper.
	vOther := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "other-issuer")
	pOther, err := New(vOther, zap.NewNop())
	require.NoError(t, err)

	token, _, err := pOther.IssueToken("admin")
	require.NoError(t, err)

	// Now try to verify it with the "gitstore" issuer provider.
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Bearer " + token}}}
	_, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeChallenge, decision.Outcome)
}

func TestStaticAdmin_BasicAuth_Allow(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	creds := base64.StdEncoding.EncodeToString([]byte("admin:testpass"))
	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Basic " + creds}}}
	principal, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, "admin", principal.Subject)
}

func TestStaticAdmin_BasicAuth_WrongPassword_Challenge(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	creds := base64.StdEncoding.EncodeToString([]byte("admin:wrongpass"))
	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Basic " + creds}}}
	_, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeChallenge, decision.Outcome)
}

func TestStaticAdmin_NoAuthHeader_Challenge(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	req := authpkg.AuthRequest{Header: http.Header{}}
	_, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeChallenge, decision.Outcome)
}

func TestStaticAdmin_IssueSession_ReturnsToken(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	token, exp, err := p.IssueSession(context.Background(), "admin")
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.False(t, exp.IsZero())
}

func TestStaticAdmin_BearerJWT_SetsTokenID(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Bearer " + token}}}
	principal, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.NotEmpty(t, principal.TokenID, "TokenID must be populated from the JWT jti claim")
}

func TestStaticAdmin_BasicAuth_TokenIDEmpty(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	creds := base64.StdEncoding.EncodeToString([]byte("admin:testpass"))
	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Basic " + creds}}}
	principal, decision, err := p.Authenticate(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Empty(t, principal.TokenID, "Basic Auth principal must not carry a TokenID")
}

func TestStaticAdmin_RefreshSession_WithinGrace_Succeeds(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	// Issue a token that expired 30s ago (within default 60s grace).
	v.SetDefault("auth.jwt.duration", "-30s")
	v.SetDefault("auth.jwt.refresh_grace", "60s")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	newToken, exp, err := p.RefreshSession(context.Background(), token)
	require.NoError(t, err)
	assert.NotEmpty(t, newToken)
	assert.False(t, exp.IsZero())
}

func TestStaticAdmin_RefreshSession_BeyondGrace_Fails(t *testing.T) {
	v := newTestViper("admin", mustBcrypt(t, "testpass"), "test-secret-key", "gitstore")
	// Issue a token that expired 5 minutes ago (beyond default 60s grace).
	v.SetDefault("auth.jwt.duration", "-5m")
	v.SetDefault("auth.jwt.refresh_grace", "60s")
	p, err := New(v, zap.NewNop())
	require.NoError(t, err)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	_, _, err = p.RefreshSession(context.Background(), token)
	require.Error(t, err)
	assert.ErrorIs(t, err, authpkg.ErrTokenTooOld)
}
