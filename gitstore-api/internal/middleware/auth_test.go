// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAuthMiddleware creates an AuthMiddleware with a known password hash for tests.
func newTestAuthMiddleware(t *testing.T) *AuthMiddleware {
	t.Helper()
	hash, err := HashPassword("admin123")
	require.NoError(t, err)
	am, err := NewAuthMiddleware(AuthDeps{
		AdminUsername:     "admin",
		AdminPasswordHash: hash,
		JWTSecret:         "dev-secret-change-in-production",
		JWTDuration:       "24h",
		JWTIssuer:         "gitstore",
	})
	require.NoError(t, err)
	return am
}

func TestNewAuthMiddleware(t *testing.T) {
	t.Run("should create with provided credentials", func(t *testing.T) {
		hash, err := HashPassword("testpass123")
		require.NoError(t, err)
		am, err := NewAuthMiddleware(AuthDeps{
			AdminUsername:     "admin",
			AdminPasswordHash: hash,
			JWTSecret:         "dev-secret-change-in-production",
			JWTDuration:       "24h",
			JWTIssuer:         "gitstore",
		})
		require.NoError(t, err)
		require.NotNil(t, am)
		assert.Equal(t, "admin", am.adminUsername)
		assert.Equal(t, hash, am.adminPasswordHash)
	})

	t.Run("should use injected credentials", func(t *testing.T) {
		hash, err := HashPassword("testpass123")
		require.NoError(t, err)
		am, err := NewAuthMiddleware(AuthDeps{
			AdminUsername:     "testadmin",
			AdminPasswordHash: hash,
			JWTSecret:         "dev-secret-change-in-production",
			JWTDuration:       "24h",
			JWTIssuer:         "gitstore",
		})
		require.NoError(t, err)
		assert.Equal(t, "testadmin", am.adminUsername)
		assert.Equal(t, hash, am.adminPasswordHash)
	})

	t.Run("should reject missing admin username", func(t *testing.T) {
		hash, err := HashPassword("testpass123")
		require.NoError(t, err)
		_, err = NewAuthMiddleware(AuthDeps{
			AdminPasswordHash: hash,
			JWTSecret:         "dev-secret-change-in-production",
			JWTDuration:       "24h",
			JWTIssuer:         "gitstore",
		})
		require.ErrorContains(t, err, "admin username is required")
	})

	t.Run("should reject missing admin password hash", func(t *testing.T) {
		_, err := NewAuthMiddleware(AuthDeps{
			AdminUsername: "admin",
			JWTSecret:     "dev-secret-change-in-production",
			JWTDuration:   "24h",
			JWTIssuer:     "gitstore",
		})
		require.ErrorContains(t, err, "admin password hash is required")
	})
}

func TestValidateCredentials(t *testing.T) {
	// Create middleware with known credentials
	hash, err := HashPassword("testpass123")
	require.NoError(t, err)

	am := &AuthMiddleware{
		adminUsername:     "testadmin",
		adminPasswordHash: hash,
	}

	t.Run("should validate correct credentials", func(t *testing.T) {
		valid := am.ValidateCredentials("testadmin", "testpass123")
		assert.True(t, valid)
	})

	t.Run("should reject incorrect username", func(t *testing.T) {
		valid := am.ValidateCredentials("wronguser", "testpass123")
		assert.False(t, valid)
	})

	t.Run("should reject incorrect password", func(t *testing.T) {
		valid := am.ValidateCredentials("testadmin", "wrongpass")
		assert.False(t, valid)
	})

	t.Run("should reject empty credentials", func(t *testing.T) {
		valid := am.ValidateCredentials("", "")
		assert.False(t, valid)
	})
}

func TestRequireAuth(t *testing.T) {
	am := newTestAuthMiddleware(t)

	// Handler that checks for user in context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(UserContextKey).(*User)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("No user in context"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello " + user.Username))
	})

	wrappedHandler := am.RequireAuth(handler)

	t.Run("should reject request without auth header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		wrappedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "missing authorization header")
	})

	t.Run("should reject request with invalid auth format", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "InvalidFormat token123")
		w := httptest.NewRecorder()

		wrappedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "invalid authorization format")
	})

	t.Run("should accept request with valid bearer token", func(t *testing.T) {
		// Generate a valid JWT token
		token, err := am.GenerateSessionToken("admin", true)
		require.NoError(t, err)

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		wrappedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "Hello admin")
	})

	t.Run("should reject request with invalid bearer token", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer invalid-token-123")
		w := httptest.NewRecorder()

		wrappedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "Unauthorized")
	})
}

func TestOptionalAuth(t *testing.T) {
	am := newTestAuthMiddleware(t)

	// Handler that checks for user in context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(UserContextKey).(*User)
		if !ok {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Anonymous"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello " + user.Username))
	})

	wrappedHandler := am.OptionalAuth(handler)

	t.Run("should allow request without auth header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		wrappedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "Anonymous", w.Body.String())
	})

	t.Run("should add user to context with valid bearer token", func(t *testing.T) {
		// Generate a valid JWT token
		token, err := am.GenerateSessionToken("admin", true)
		require.NoError(t, err)

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		wrappedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "Hello admin")
	})

	t.Run("should proceed without user for invalid format", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "InvalidFormat token123")
		w := httptest.NewRecorder()

		wrappedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "Anonymous", w.Body.String())
	})
}

func TestOptionalAuth_setsPrincipalInContext(t *testing.T) {
	am := newTestAuthMiddleware(t)
	token, err := am.GenerateSessionToken("admin", true)
	require.NoError(t, err)

	var captured *auth.Principal
	handler := am.OptionalAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = auth.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.NotNil(t, captured)
	assert.Equal(t, "admin", captured.Subject)
	assert.True(t, captured.IsAdmin())
}

func TestHashPassword(t *testing.T) {
	t.Run("should generate valid bcrypt hash", func(t *testing.T) {
		password := "testpassword123"
		hash, err := HashPassword(password)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)
		assert.NotEqual(t, password, hash)

		// Verify hash can be validated
		am := &AuthMiddleware{
			adminUsername:     "test",
			adminPasswordHash: hash,
		}
		valid := am.ValidateCredentials("test", password)
		assert.True(t, valid)
	})

	t.Run("should generate different hashes for same password", func(t *testing.T) {
		password := "testpassword123"
		hash1, err := HashPassword(password)
		require.NoError(t, err)

		hash2, err := HashPassword(password)
		require.NoError(t, err)

		// Different hashes due to random salt
		assert.NotEqual(t, hash1, hash2)

		// Both should validate correctly
		am1 := &AuthMiddleware{adminUsername: "test", adminPasswordHash: hash1}
		am2 := &AuthMiddleware{adminUsername: "test", adminPasswordHash: hash2}

		assert.True(t, am1.ValidateCredentials("test", password))
		assert.True(t, am2.ValidateCredentials("test", password))
	})
}
