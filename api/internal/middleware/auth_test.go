package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAuthMiddleware(t *testing.T) {
	t.Run("should create with default credentials", func(t *testing.T) {
		am, err := NewAuthMiddleware()
		require.NoError(t, err)
		require.NotNil(t, am)
		assert.Equal(t, "admin", am.adminUsername)
		assert.NotEmpty(t, am.adminPasswordHash)
	})

	t.Run("should use environment variables", func(t *testing.T) {
		// Set environment variables
		os.Setenv("ADMIN_USERNAME", "testadmin")
		hash, err := HashPassword("testpass123")
		require.NoError(t, err)
		os.Setenv("ADMIN_PASSWORD_HASH", hash)

		defer func() {
			os.Unsetenv("ADMIN_USERNAME")
			os.Unsetenv("ADMIN_PASSWORD_HASH")
		}()

		am, err := NewAuthMiddleware()
		require.NoError(t, err)
		assert.Equal(t, "testadmin", am.adminUsername)
		assert.Equal(t, hash, am.adminPasswordHash)
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
	am, err := NewAuthMiddleware()
	require.NoError(t, err)

	// Handler that checks for user in context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := GetUserFromContext(r.Context())
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
	am, err := NewAuthMiddleware()
	require.NoError(t, err)

	// Handler that checks for user in context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := GetUserFromContext(r.Context())
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

func TestGetUserFromContext(t *testing.T) {
	t.Run("should return user from context", func(t *testing.T) {
		am, err := NewAuthMiddleware()
		require.NoError(t, err)

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer token123")

		handler := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := GetUserFromContext(r.Context())
			assert.True(t, ok)
			assert.NotNil(t, user)
			assert.Equal(t, "admin", user.Username)
			assert.True(t, user.IsAdmin)
		}))

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	})

	t.Run("should return false for missing user", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		user, ok := GetUserFromContext(req.Context())
		assert.False(t, ok)
		assert.Nil(t, user)
	})
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
