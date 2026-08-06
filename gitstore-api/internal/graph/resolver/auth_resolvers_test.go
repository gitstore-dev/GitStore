// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticadmin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// --- helpers ---

func mustBcrypt(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

func newTestConfig(t *testing.T, duration string) config.AuthConfig {
	t.Helper()
	return config.AuthConfig{
		Admin: config.UserConfig{
			Username: "admin",
			Password: mustBcrypt(t, "testpass"),
		},
		JWT: config.JWTConfig{
			Secret:       "test-secret",
			Issuer:       "gitstore",
			Duration:     duration,
			RefreshGrace: "60s",
		},
	}
}

func newTestRegistry(t *testing.T, cfg config.AuthConfig) (*authpkg.ProviderRegistry, *staticadmin.StaticAdminProvider) {
	t.Helper()
	p, err := staticadmin.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(p.Shutdown)
	chain := authpkg.NewChainedAuthN(p)
	reg := authpkg.NewProviderRegistry(chain, nil, nil)
	return reg, p
}

func newTestResolver(t *testing.T, reg *authpkg.ProviderRegistry) *resolver.Resolver {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:    store,
		Registry: reg,
		Logger:   zap.NewNop(),
		Clock:    apiruntime.SystemClock{},
	})
	require.NoError(t, err)
	return r
}

func ctxWithPrincipal(principal *authpkg.Principal) context.Context {
	return authpkg.ContextWithPrincipal(context.Background(), principal)
}

// --- US1: Logout ---

func TestLogout_AuthenticatedBearer_ReturnsSuccess(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, p := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	token, exp, err := p.IssueToken("admin")
	require.NoError(t, err)

	principal := &authpkg.Principal{
		Subject:    "admin",
		Roles:      []string{"admin"},
		AuthMethod: "static-admin",
		ExpiresAt:  exp,
		TokenID:    extractJTI(t, p, token),
	}
	ctx := ctxWithPrincipal(principal)

	payload, err := r.Logout(ctx, model.LogoutInput{})
	require.NoError(t, err)
	require.NotNil(t, payload)
	assert.True(t, payload.Success)
}

func TestLogout_AnonymousPrincipal_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	ctx := ctxWithPrincipal(authpkg.Anonymous())

	_, err := r.Logout(ctx, model.LogoutInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
}

func TestLogout_NilPrincipal_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	_, err := r.Logout(context.Background(), model.LogoutInput{})
	require.Error(t, err)
}

func TestLogout_EmptyTokenID_NoOp_ReturnsSuccess(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	// Basic Auth session — no TokenID
	principal := &authpkg.Principal{
		Subject:    "admin",
		Roles:      []string{"admin"},
		AuthMethod: "static-admin",
		TokenID:    "", // empty — no jti
	}
	ctx := ctxWithPrincipal(principal)

	payload, err := r.Logout(ctx, model.LogoutInput{})
	require.NoError(t, err)
	require.NotNil(t, payload)
	assert.True(t, payload.Success)
}

// --- US2: RefreshToken ---

func TestRefreshToken_ValidToken_ReturnsNewSession(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, p := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	payload, err := r.RefreshToken(context.Background(), model.RefreshTokenInput{RefreshToken: token})
	require.NoError(t, err)
	require.NotNil(t, payload)
	assert.NotEmpty(t, payload.Token.AccessToken)
	assert.Equal(t, "Bearer", payload.Token.TokenType)
	assert.Greater(t, payload.Token.ExpiresIn, int32(0))
	require.NotNil(t, payload.Token.RefreshToken)
	assert.NotEqual(t, token, payload.Token.AccessToken, "refreshed token must differ from original")
	assert.Equal(t, payload.Token.AccessToken, *payload.Token.RefreshToken)
}

func TestRefreshToken_ExpiredWithinGrace_Succeeds(t *testing.T) {
	cfg := newTestConfig(t, "-30s") // token expired 30s ago, grace is 60s
	reg, p := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	payload, err := r.RefreshToken(context.Background(), model.RefreshTokenInput{RefreshToken: token})
	require.NoError(t, err)
	require.NotNil(t, payload)
	assert.NotEmpty(t, payload.Token.AccessToken)
}

func TestRefreshToken_ExpiredBeyondGrace_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "-5m") // token expired 5 minutes ago, grace is 60s
	reg, p := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	_, err = r.RefreshToken(context.Background(), model.RefreshTokenInput{RefreshToken: token})
	require.Error(t, err)
}

func TestRefreshToken_RevokedToken_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, p := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	token, exp, err := p.IssueToken("admin")
	require.NoError(t, err)
	// Revoke by doing a first refresh
	jti := extractJTI(t, p, token)
	err = p.RevokeSession(context.Background(), jti, exp)
	require.NoError(t, err)

	_, err = r.RefreshToken(context.Background(), model.RefreshTokenInput{RefreshToken: token})
	require.Error(t, err)
}

func TestRefreshToken_EmptyRefreshToken_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	_, err := r.RefreshToken(context.Background(), model.RefreshTokenInput{RefreshToken: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh token is required")
}

func TestRefreshToken_UnsupportedScope_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, p := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)
	scope := "catalog:read"
	_, err = r.RefreshToken(context.Background(), model.RefreshTokenInput{
		RefreshToken: token,
		Scope:        &scope,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope requests are not supported")
}

func TestRefreshToken_InvalidToken_ReturnsClientAuthError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	_, err := r.RefreshToken(context.Background(), model.RefreshTokenInput{RefreshToken: "not-a-jwt"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired refresh token")
}

// --- US3: Login migration ---

func TestLogin_ValidCredentials_ReturnsTokenPayload(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	input := model.LoginInput{Username: "admin", Password: "testpass"}
	payload, err := r.Login(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, payload)
	assert.NotEmpty(t, payload.Token.AccessToken)
	assert.Equal(t, "Bearer", payload.Token.TokenType)
	assert.Greater(t, payload.Token.ExpiresIn, int32(0))
	require.NotNil(t, payload.Token.RefreshToken)
	assert.Equal(t, payload.Token.AccessToken, *payload.Token.RefreshToken)
	assert.Nil(t, payload.Token.Scope)
	assert.Nil(t, payload.Token.IDToken)
}

func TestLogin_InvalidPassword_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)

	_, err := r.Login(context.Background(), model.LoginInput{Username: "admin", Password: "wrongpass"})
	require.Error(t, err)
}

func TestLogin_UnsupportedScope_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t, "1h")
	reg, _ := newTestRegistry(t, cfg)
	r := newTestResolver(t, reg)
	scope := "catalog:read"

	_, err := r.Login(context.Background(), model.LoginInput{Username: "admin", Password: "testpass", Scope: &scope})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope requests are not supported")
}

func TestLogin_NilRegistry_ReturnsError(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:  store,
		Logger: zap.NewNop(),
		Clock:  apiruntime.SystemClock{},
		// Registry intentionally nil
	})
	require.NoError(t, err)

	_, err = r.Login(context.Background(), model.LoginInput{Username: "admin", Password: "testpass"})
	require.Error(t, err)
}

func TestLogout_NilRegistry_ReturnsError(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:  store,
		Logger: zap.NewNop(),
		Clock:  apiruntime.SystemClock{},
	})
	require.NoError(t, err)

	principal := &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin", TokenID: "some-jti"}
	ctx := ctxWithPrincipal(principal)
	_, err = r.Logout(ctx, model.LogoutInput{})
	require.Error(t, err)
}

func TestRefreshToken_NilRegistry_ReturnsError(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:  store,
		Logger: zap.NewNop(),
		Clock:  apiruntime.SystemClock{},
	})
	require.NoError(t, err)

	_, err = r.RefreshToken(context.Background(), model.RefreshTokenInput{RefreshToken: "some.refresh.token"})
	require.Error(t, err)
}

// --- helpers ---

// extractJTI issues a new token and parses its jti by authenticating.
func extractJTI(t *testing.T, p *staticadmin.StaticAdminProvider, token string) string {
	t.Helper()
	req := authpkg.AuthRequest{Header: http.Header{"Authorization": []string{"Bearer " + token}}}
	principal, _, err := p.Authenticate(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, principal)
	return principal.TokenID
}

// Ensure that the Logout, RefreshToken, and Login methods are exposed on the resolver.
// These compile-time checks will fail if the resolver doesn't have the required methods.
var _ = func() bool {
	var r *resolver.Resolver
	var _ = r.Logout
	var _ = r.RefreshToken
	var _ = r.Login
	_ = errors.New
	_ = time.Now
	_ = base64.StdEncoding
	return true
}
