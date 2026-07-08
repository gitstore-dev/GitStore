// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticadmin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func newTestRegistry(t *testing.T) (*auth.ProviderRegistry, *staticadmin.StaticAdminProvider) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	require.NoError(t, err)

	v := viper.New()
	v.SetDefault("auth.admin.username", "admin")
	v.SetDefault("auth.admin.password_hash", string(hash))
	v.SetDefault("auth.jwt.secret", "dev-secret-change-in-production")
	v.SetDefault("auth.jwt.issuer", "gitstore")
	v.SetDefault("auth.jwt.duration", "24h")

	staticAdmin, err := staticadmin.New(v, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(staticAdmin.Shutdown)

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(staticAdmin, anonymous.New()),
		nil,
		nil,
	)
	return registry, staticAdmin
}

func TestChainAuthMiddlewareValidBearerSetsPrincipal(t *testing.T) {
	registry, staticAdmin := newTestRegistry(t)
	token, _, err := staticAdmin.IssueSession(t.Context(), "admin")
	require.NoError(t, err)

	var captured *auth.Principal
	handler := ChainAuthMiddleware(registry, zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = auth.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "admin", captured.Subject)
	assert.Equal(t, "static-admin", captured.AuthMethod)
	assert.True(t, captured.IsAdmin())
	assert.NotEmpty(t, captured.TokenID)
}

func TestChainAuthMiddlewareNoCredentialsSetsAnonymousPrincipal(t *testing.T) {
	registry, _ := newTestRegistry(t)

	var captured *auth.Principal
	handler := ChainAuthMiddleware(registry, zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = auth.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "anon", captured.Subject)
	assert.Equal(t, "none", captured.AuthMethod)
}

func TestChainAuthMiddlewareInvalidCredentialsReturnUnauthorized(t *testing.T) {
	registry, _ := newTestRegistry(t)
	handler := ChainAuthMiddleware(registry, zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRequireAuthRejectsAnonymousPrincipal(t *testing.T) {
	handler := RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), auth.Anonymous()))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
