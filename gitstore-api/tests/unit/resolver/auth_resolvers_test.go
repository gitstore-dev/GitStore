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
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/spf13/viper"
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

func newTestViper(t *testing.T, duration string) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetDefault("auth.admin.username", "admin")
	v.SetDefault("auth.admin.password_hash", mustBcrypt(t, "testpass"))
	v.SetDefault("auth.jwt.secret", "test-secret")
	v.SetDefault("auth.jwt.issuer", "gitstore")
	v.SetDefault("auth.jwt.duration", duration)
	v.SetDefault("auth.jwt.refresh_grace", "60s")
	return v
}

func newTestRegistry(t *testing.T, v *viper.Viper) (*authpkg.ProviderRegistry, *staticadmin.StaticAdminProvider) {
	t.Helper()
	p, err := staticadmin.New(v, zap.NewNop())
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

func ctxWithPrincipalAndRawToken(principal *authpkg.Principal, rawToken string) context.Context {
	ctx := authpkg.ContextWithPrincipal(context.Background(), principal)
	return authpkg.ContextWithRawToken(ctx, rawToken)
}

// --- US1: Logout ---

func TestLogout_AuthenticatedBearer_ReturnsSuccess(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, p := newTestRegistry(t, v)
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

func TestLogout_UnauthenticatedCaller_ReturnsError(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, _ := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	// Anonymous principal (AuthMethod == "none")
	ctx := ctxWithPrincipal(authpkg.Anonymous())

	_, err := r.Logout(ctx, model.LogoutInput{})
	require.Error(t, err)
}

func TestLogout_NilPrincipal_ReturnsError(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, _ := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	_, err := r.Logout(context.Background(), model.LogoutInput{})
	require.Error(t, err)
}

func TestLogout_EmptyTokenID_NoOp_ReturnsSuccess(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, _ := newTestRegistry(t, v)
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
	v := newTestViper(t, "1h")
	reg, p := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	principal := &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin"}
	ctx := ctxWithPrincipalAndRawToken(principal, token)

	payload, err := r.RefreshToken(ctx, model.RefreshTokenInput{})
	require.NoError(t, err)
	require.NotNil(t, payload)
	require.NotNil(t, payload.Session)
	assert.NotEmpty(t, payload.Session.Token)
	assert.NotEqual(t, token, payload.Session.Token, "refreshed token must differ from original")
	assert.False(t, payload.Session.ExpiresAt.IsZero())
}

func TestRefreshToken_ExpiredWithinGrace_Succeeds(t *testing.T) {
	v := newTestViper(t, "-30s") // token expired 30s ago, grace is 60s
	reg, p := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	principal := &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin"}
	ctx := ctxWithPrincipalAndRawToken(principal, token)

	payload, err := r.RefreshToken(ctx, model.RefreshTokenInput{})
	require.NoError(t, err)
	require.NotNil(t, payload)
	assert.NotEmpty(t, payload.Session.Token)
}

func TestRefreshToken_ExpiredBeyondGrace_ReturnsError(t *testing.T) {
	v := newTestViper(t, "-5m") // token expired 5 minutes ago, grace is 60s
	reg, p := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	token, _, err := p.IssueToken("admin")
	require.NoError(t, err)

	principal := &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin"}
	ctx := ctxWithPrincipalAndRawToken(principal, token)

	_, err = r.RefreshToken(ctx, model.RefreshTokenInput{})
	require.Error(t, err)
}

func TestRefreshToken_RevokedToken_ReturnsError(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, p := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	token, exp, err := p.IssueToken("admin")
	require.NoError(t, err)
	// Revoke by doing a first refresh
	jti := extractJTI(t, p, token)
	err = p.RevokeSession(context.Background(), jti, exp)
	require.NoError(t, err)

	principal := &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin"}
	ctx := ctxWithPrincipalAndRawToken(principal, token)

	_, err = r.RefreshToken(ctx, model.RefreshTokenInput{})
	require.Error(t, err)
}

func TestRefreshToken_NoRawToken_ReturnsError(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, _ := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	// No raw token in context — unauthenticated or Basic Auth session
	principal := &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin"}
	ctx := ctxWithPrincipal(principal)

	_, err := r.RefreshToken(ctx, model.RefreshTokenInput{})
	require.Error(t, err)
}

// --- US3: Login migration ---

func TestLogin_ValidCredentials_ReturnsSession(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, _ := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	input := model.LoginInput{Username: "admin", Password: "testpass"}
	payload, err := r.Login(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, payload)
	require.NotNil(t, payload.Session)
	assert.NotEmpty(t, payload.Session.Token)
	assert.False(t, payload.Session.ExpiresAt.IsZero())
	require.NotNil(t, payload.Session.User)
	assert.Equal(t, "admin", payload.Session.User.Username)
	assert.True(t, payload.Session.User.IsAdmin, "admin user must have isAdmin=true derived from principal roles")
}

func TestLogin_InvalidPassword_ReturnsError(t *testing.T) {
	v := newTestViper(t, "1h")
	reg, _ := newTestRegistry(t, v)
	r := newTestResolver(t, reg)

	_, err := r.Login(context.Background(), model.LoginInput{Username: "admin", Password: "wrongpass"})
	require.Error(t, err)
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

	principal := &authpkg.Principal{Subject: "admin", Roles: []string{"admin"}, AuthMethod: "static-admin"}
	ctx := ctxWithPrincipalAndRawToken(principal, "some.bearer.token")
	_, err = r.RefreshToken(ctx, model.RefreshTokenInput{})
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
	var _ func(context.Context, model.LogoutInput) (*model.LogoutPayload, error) = r.Logout
	var _ func(context.Context, model.RefreshTokenInput) (*model.RefreshTokenPayload, error) = r.RefreshToken
	var _ func(context.Context, model.LoginInput) (*model.LoginPayload, error) = r.Login
	_ = errors.New
	_ = time.Now
	_ = base64.StdEncoding
	return true
}
