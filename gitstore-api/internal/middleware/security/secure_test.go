// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticadmin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func newTestRegistry(t *testing.T) (*auth.ProviderRegistry, *staticadmin.StaticAdminProvider) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	require.NoError(t, err)

	cfg := config.AuthConfig{
		Admin: config.UserConfig{
			Username: "admin",
			Password: string(hash),
		},
		JWT: config.JWTConfig{
			Secret:   "dev-secret-change-in-production",
			Issuer:   "gitstore",
			Duration: "24h",
		},
	}

	staticAdmin, err := staticadmin.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(staticAdmin.Shutdown)

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(staticAdmin, anonymous.New()),
		nil,
		nil,
	)
	return registry, staticAdmin
}

func TestAuthenticatorValidBearerSetsPrincipal(t *testing.T) {
	registry, staticAdmin := newTestRegistry(t)
	token, _, err := staticAdmin.IssueSession(t.Context(), "admin")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	authMiddleware.Authenticator(c)

	var captured = auth.PrincipalFromContext(c.Request.Context())

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "admin", captured.Subject)
	assert.Equal(t, "static-admin", captured.AuthMethod)
	assert.True(t, captured.IsAdmin())
	assert.NotEmpty(t, captured.TokenID)
}

func TestAuthenticatorNoCredentialsSetsAnonymousPrincipal(t *testing.T) {
	registry, _ := newTestRegistry(t)

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	authMiddleware.Authenticator(c)

	var captured = auth.PrincipalFromContext(c.Request.Context())

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "anon", captured.Subject)
	assert.Equal(t, "none", captured.AuthMethod)
}

func TestAuthenticatorInvalidCredentialsReturnUnauthorized(t *testing.T) {
	registry, _ := newTestRegistry(t)

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	authMiddleware.Authenticator(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// stubAuthNProvider is an AuthNProvider that returns a fixed outcome and optional error.
type stubAuthNProvider struct {
	principal *auth.Principal
	decision  auth.Decision
	err       error
}

func (s *stubAuthNProvider) Name() string { return "stub" }
func (s *stubAuthNProvider) Capabilities() auth.Capability {
	return auth.CapAuthenticate
}
func (s *stubAuthNProvider) Authenticate(_ context.Context, _ auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	return s.principal, s.decision, s.err
}
func (s *stubAuthNProvider) RevokeSession(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (s *stubAuthNProvider) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}
func (s *stubAuthNProvider) IssueSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

func newRegistryWithStub(stub *stubAuthNProvider) *auth.ProviderRegistry {
	return auth.NewProviderRegistry(
		auth.NewChainedAuthN(stub),
		nil,
		nil,
	)
}

// T011: transient auth-chain error returns 503 (not 401) with no WWW-Authenticate header.
func TestBasicAuthTransientError(t *testing.T) {
	stub := &stubAuthNProvider{
		principal: auth.Anonymous(),
		decision:  auth.Deny("stub", "internal error"),
		err:       errors.New("db connection refused"),
	}
	registry := newRegistryWithStub(stub)

	req := httptest.NewRequest(http.MethodGet, "/git-receive-pack", nil)
	req.SetBasicAuth("admin", "admin123")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	authMiddleware.BasicAuthenticator(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "transient error must return 503")
	assert.Empty(t, w.Header().Get("WWW-Authenticate"), "503 must not set WWW-Authenticate")
}

// T012: OutcomeDeny with err == nil returns 401 with WWW-Authenticate header.
func TestBasicAuthCredentialRejection(t *testing.T) {
	stub := &stubAuthNProvider{
		principal: auth.Anonymous(),
		decision:  auth.Deny("stub", "bad credentials"),
		err:       nil,
	}
	registry := newRegistryWithStub(stub)

	req := httptest.NewRequest(http.MethodGet, "/git-receive-pack", nil)
	req.SetBasicAuth("admin", "wrong-password")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	authMiddleware.BasicAuthenticator(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "credential rejection must return 401")
	assert.Equal(t, `Basic realm="GitStore"`, w.Header().Get("WWW-Authenticate"))
}

// T035: PushContextInserter stores PushContext in request context with correct fields.
func TestPushContextInserter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const repoID = "01960000-0000-7000-8000-000000000001"
	const nsID = "01960000-0000-7000-8000-000000000010"
	principal := &auth.Principal{Subject: "admin", Issuer: "static-admin", AuthMethod: "basic", Roles: []string{"admin"}}

	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			if id != repoID {
				return nil, datastore.ErrNotFound
			}
			return &datastore.Repository{
				ID:               repoID,
				NamespaceID:      nsID,
				Name:             "catalog",
				MaxPackSizeBytes: 52428800,
				MaxFileSizeBytes: 10485760,
			}, nil
		},
	}

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(&stubAuthNProvider{
			principal: principal,
			decision:  auth.Allow("stub", "ok"),
		}),
		nil, nil,
	)

	var capturedCtx context.Context
	router := gin.New()
	authorizeMiddleware := NewAuthorizeWithStore(registry, store, zap.NewNop())
	router.POST("/:namespace/:repo/git-receive-pack",
		func(c *gin.Context) {
			// Inject principal into request context (normally done by BasicAuthenticator).
			ctx := auth.ContextWithPrincipal(c.Request.Context(), principal)
			c.Request = c.Request.WithContext(ctx)
			c.Set(repoIDKey, repoID)
			c.Next()
		},
		authorizeMiddleware.PushContextInserter,
		func(c *gin.Context) {
			capturedCtx = c.Request.Context()
			c.Status(http.StatusOK)
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, capturedCtx)

	pc := gitclient.PushContextFromContext(capturedCtx)
	require.NotNil(t, pc, "PushContext must be set in request context")
	assert.Equal(t, repoID, pc.RepositoryId)
	assert.Equal(t, "admin", pc.Actor.Subject)
	assert.Equal(t, int64(52428800), pc.Policy.MaxPackSizeBytes)
	assert.Equal(t, int64(10485760), pc.Policy.MaxFileSizeBytes)
}

// T013: OutcomeAllow passes through to the next handler.
func TestBasicAuthAllow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubAuthNProvider{
		principal: &auth.Principal{Subject: "admin", Issuer: "stub", AuthMethod: "basic"},
		decision:  auth.Allow("stub", "valid"),
		err:       nil,
	}
	registry := newRegistryWithStub(stub)

	reached := false
	router := gin.New()
	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	router.GET("/git-upload-pack", authMiddleware.BasicAuthenticator, func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/git-upload-pack", nil)
	req.SetBasicAuth("admin", "admin123")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "request must reach next handler on allow")
	assert.True(t, reached, "next handler must be called on allow")
}
