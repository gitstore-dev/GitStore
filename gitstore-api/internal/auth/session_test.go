// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"testing"
	"time"

	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

func newTestSessionManager(t *testing.T, duration string, now time.Time) *SessionManager {
	t.Helper()
	sm, err := NewSessionManager(SessionManagerDeps{
		Secret:   "dev-secret-change-in-production",
		Duration: duration,
		Issuer:   "gitstore",
		Clock:    apiruntime.NewFixedClock(now),
	})
	require.NoError(t, err)
	return sm
}

func TestNewSessionManager(t *testing.T) {
	t.Run("should create with provided settings", func(t *testing.T) {
		sm, err := NewSessionManager(SessionManagerDeps{
			Secret:   "dev-secret-change-in-production",
			Duration: "24h",
			Issuer:   "gitstore",
		})
		require.NoError(t, err)
		require.NotNil(t, sm)
		assert.Equal(t, 24*time.Hour, sm.tokenDuration)
		assert.Equal(t, "gitstore", sm.issuer)
	})

	t.Run("should use injected duration and issuer", func(t *testing.T) {
		sm, err := NewSessionManager(SessionManagerDeps{
			Secret:   "test-secret-key",
			Duration: "2h",
			Issuer:   "test-issuer",
		})
		require.NoError(t, err)
		assert.Equal(t, 2*time.Hour, sm.tokenDuration)
		assert.Equal(t, "test-issuer", sm.issuer)
	})

	t.Run("should reject missing secret", func(t *testing.T) {
		_, err := NewSessionManager(SessionManagerDeps{Duration: "24h", Issuer: "gitstore"})
		require.ErrorContains(t, err, "secret is required")
	})

	t.Run("should reject missing issuer", func(t *testing.T) {
		_, err := NewSessionManager(SessionManagerDeps{Secret: "secret", Duration: "24h"})
		require.ErrorContains(t, err, "issuer is required")
	})

	t.Run("should reject invalid duration", func(t *testing.T) {
		_, err := NewSessionManager(SessionManagerDeps{Secret: "secret", Duration: "soon", Issuer: "gitstore"})
		require.ErrorContains(t, err, "invalid token duration")
	})
}

func TestGenerateToken(t *testing.T) {
	sm := newTestSessionManager(t, "24h", fixedNow)

	t.Run("should generate valid JWT token", func(t *testing.T) {
		token, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)
		assert.NotEmpty(t, token)

		// Verify token format (header.payload.signature)
		assert.Contains(t, token, ".")
		parts := splitToken(token)
		assert.Len(t, parts, 3)
	})

	t.Run("should include correct claims", func(t *testing.T) {
		token, err := sm.GenerateToken("admin", true)
		require.NoError(t, err)

		claims, err := sm.ValidateToken(token)
		require.NoError(t, err)
		assert.Equal(t, "admin", claims.Username)
		assert.True(t, claims.IsAdmin)
		assert.Equal(t, "gitstore", claims.Issuer)
		assert.Equal(t, "admin", claims.Subject)
	})

	t.Run("should set expiration time", func(t *testing.T) {
		token, err := sm.GenerateToken("testuser", false)
		require.NoError(t, err)

		claims, err := sm.ValidateToken(token)
		require.NoError(t, err)

		assert.Equal(t, fixedNow.Add(24*time.Hour), claims.ExpiresAt.Time.UTC())
	})

	t.Run("should generate different tokens for same user", func(t *testing.T) {
		firstSM := newTestSessionManager(t, "24h", fixedNow)
		token1, err := firstSM.GenerateToken("testuser", true)
		require.NoError(t, err)

		secondSM := newTestSessionManager(t, "24h", fixedNow.Add(2*time.Second))
		token2, err := secondSM.GenerateToken("testuser", true)
		require.NoError(t, err)

		// Different tokens due to different issued-at times
		assert.NotEqual(t, token1, token2)
	})
}

func TestValidateToken(t *testing.T) {
	sm := newTestSessionManager(t, "24h", fixedNow)

	t.Run("should validate correct token", func(t *testing.T) {
		token, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		claims, err := sm.ValidateToken(token)
		require.NoError(t, err)
		assert.Equal(t, "testuser", claims.Username)
		assert.True(t, claims.IsAdmin)
	})

	t.Run("should reject empty token", func(t *testing.T) {
		_, err := sm.ValidateToken("")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMissingToken)
	})

	t.Run("should reject malformed token", func(t *testing.T) {
		_, err := sm.ValidateToken("invalid.token.format")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidToken)
	})

	t.Run("should reject token with wrong signature", func(t *testing.T) {
		// Create token with different secret
		wrongSM := &SessionManager{
			secretKey:     []byte("wrong-secret"),
			tokenDuration: 24 * time.Hour,
			issuer:        "gitstore",
			clock:         apiruntime.NewFixedClock(fixedNow),
		}
		token, err := wrongSM.GenerateToken("testuser", true)
		require.NoError(t, err)

		_, err = sm.ValidateToken(token)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidToken)
	})

	t.Run("should reject expired token", func(t *testing.T) {
		// Create session manager with very short duration
		shortSM := &SessionManager{
			secretKey:     sm.secretKey,
			tokenDuration: 1 * time.Millisecond,
			issuer:        "gitstore",
			clock:         apiruntime.NewFixedClock(fixedNow),
		}

		token, err := shortSM.GenerateToken("testuser", true)
		require.NoError(t, err)

		expiredValidator := newTestSessionManager(t, "24h", fixedNow.Add(10*time.Millisecond))
		_, err = expiredValidator.ValidateToken(token)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrExpiredToken)
	})

	t.Run("should reject token with invalid signing method", func(t *testing.T) {
		// Create token with RS256 (we expect HS256)
		claims := Claims{
			Username: "testuser",
			IsAdmin:  true,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(fixedNow.Add(1 * time.Hour)),
			},
		}

		// Create a token with an invalid signing method (None)
		// This will fail validation because we only accept HS256
		invalidToken := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		tokenString, _ := invalidToken.SignedString(jwt.UnsafeAllowNoneSignatureType)

		_, err := sm.ValidateToken(tokenString)
		require.Error(t, err)
	})
}

func TestRefreshToken(t *testing.T) {
	sm := newTestSessionManager(t, "24h", fixedNow)

	t.Run("should refresh valid token", func(t *testing.T) {
		originalToken, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		refreshSM := newTestSessionManager(t, "24h", fixedNow.Add(2*time.Second))
		newToken, err := refreshSM.RefreshToken(originalToken)
		require.NoError(t, err)
		assert.NotEmpty(t, newToken)
		assert.NotEqual(t, originalToken, newToken)

		// Validate new token
		claims, err := refreshSM.ValidateToken(newToken)
		require.NoError(t, err)
		assert.Equal(t, "testuser", claims.Username)
		assert.True(t, claims.IsAdmin)
	})

	t.Run("should refresh expired token within grace period", func(t *testing.T) {
		// Create token with very short duration
		shortSM := &SessionManager{
			secretKey:     sm.secretKey,
			tokenDuration: 1 * time.Millisecond,
			issuer:        "gitstore",
			clock:         apiruntime.NewFixedClock(fixedNow),
		}

		expiredToken, err := shortSM.GenerateToken("testuser", false)
		require.NoError(t, err)

		// Should allow refresh within grace period
		refreshSM := newTestSessionManager(t, "24h", fixedNow.Add(10*time.Millisecond))
		newToken, err := refreshSM.RefreshToken(expiredToken)
		require.NoError(t, err)
		assert.NotEmpty(t, newToken)

		// New token should be valid
		claims, err := refreshSM.ValidateToken(newToken)
		require.NoError(t, err)
		assert.Equal(t, "testuser", claims.Username)
	})

	t.Run("should reject invalid token", func(t *testing.T) {
		_, err := sm.RefreshToken("invalid.token")
		require.Error(t, err)
	})
}

func TestGetTokenExpiry(t *testing.T) {
	sm := newTestSessionManager(t, "24h", fixedNow)

	t.Run("should return token expiry time", func(t *testing.T) {
		token, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		expiry, err := sm.GetTokenExpiry(token)
		require.NoError(t, err)

		assert.Equal(t, fixedNow.Add(24*time.Hour), expiry.UTC())
	})

	t.Run("should return error for invalid token", func(t *testing.T) {
		_, err := sm.GetTokenExpiry("invalid.token")
		require.Error(t, err)
	})
}

func TestRevokeToken(t *testing.T) {
	sm := newTestSessionManager(t, "24h", fixedNow)

	t.Run("should revoke valid token", func(t *testing.T) {
		token, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		err = sm.RevokeToken(token)
		require.NoError(t, err)

		// Note: Current implementation doesn't actually block revoked tokens
		// This is a placeholder until blacklist is implemented
	})

	t.Run("should return error for invalid token", func(t *testing.T) {
		err := sm.RevokeToken("invalid.token")
		require.Error(t, err)
	})
}

func TestGetTokenDuration(t *testing.T) {
	sm := newTestSessionManager(t, "24h", fixedNow)

	duration := sm.GetTokenDuration()
	assert.Equal(t, 24*time.Hour, duration)
}

// Helper function to split token
func splitToken(token string) []string {
	parts := []string{}
	current := ""
	for _, c := range token {
		if c == '.' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
