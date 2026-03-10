package auth

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSessionManager(t *testing.T) {
	t.Run("should create with default settings", func(t *testing.T) {
		sm, err := NewSessionManager()
		require.NoError(t, err)
		require.NotNil(t, sm)
		assert.Equal(t, 24*time.Hour, sm.tokenDuration)
		assert.Equal(t, "gitstore", sm.issuer)
	})

	t.Run("should use environment variables", func(t *testing.T) {
		os.Setenv("JWT_SECRET", "test-secret-key")
		os.Setenv("JWT_DURATION", "2h")
		os.Setenv("JWT_ISSUER", "test-issuer")

		defer func() {
			os.Unsetenv("JWT_SECRET")
			os.Unsetenv("JWT_DURATION")
			os.Unsetenv("JWT_ISSUER")
		}()

		sm, err := NewSessionManager()
		require.NoError(t, err)
		assert.Equal(t, 2*time.Hour, sm.tokenDuration)
		assert.Equal(t, "test-issuer", sm.issuer)
	})
}

func TestGenerateToken(t *testing.T) {
	sm, err := NewSessionManager()
	require.NoError(t, err)

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

		// Check expiration is approximately 24 hours from now
		expectedExpiry := time.Now().Add(24 * time.Hour)
		diff := claims.ExpiresAt.Time.Sub(expectedExpiry)
		assert.Less(t, diff.Abs(), 5*time.Second)
	})

	t.Run("should generate different tokens for same user", func(t *testing.T) {
		token1, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		time.Sleep(1100 * time.Millisecond) // JWT timestamps are in seconds, need to wait > 1s

		token2, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		// Different tokens due to different issued-at times
		assert.NotEqual(t, token1, token2)
	})
}

func TestValidateToken(t *testing.T) {
	sm, err := NewSessionManager()
	require.NoError(t, err)

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
		}

		token, err := shortSM.GenerateToken("testuser", true)
		require.NoError(t, err)

		// Wait for token to expire
		time.Sleep(10 * time.Millisecond)

		_, err = sm.ValidateToken(token)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrExpiredToken)
	})

	t.Run("should reject token with invalid signing method", func(t *testing.T) {
		// Create token with RS256 (we expect HS256)
		claims := Claims{
			Username: "testuser",
			IsAdmin:  true,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			},
		}

		// This would fail because we don't support RS256
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		// Modify the header to claim RS256
		tokenString, _ := token.SignedString(sm.secretKey)

		// The token is still valid with HS256, so let's create an actual invalid method scenario
		// by parsing a token claiming to use a different method
		invalidToken := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		tokenString, _ = invalidToken.SignedString(jwt.UnsafeAllowNoneSignatureType)

		_, err := sm.ValidateToken(tokenString)
		require.Error(t, err)
	})
}

func TestRefreshToken(t *testing.T) {
	sm, err := NewSessionManager()
	require.NoError(t, err)

	t.Run("should refresh valid token", func(t *testing.T) {
		originalToken, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		time.Sleep(1100 * time.Millisecond) // JWT timestamps are in seconds, need to wait > 1s

		newToken, err := sm.RefreshToken(originalToken)
		require.NoError(t, err)
		assert.NotEmpty(t, newToken)
		assert.NotEqual(t, originalToken, newToken)

		// Validate new token
		claims, err := sm.ValidateToken(newToken)
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
		}

		expiredToken, err := shortSM.GenerateToken("testuser", false)
		require.NoError(t, err)

		time.Sleep(10 * time.Millisecond)

		// Should allow refresh within grace period
		newToken, err := sm.RefreshToken(expiredToken)
		require.NoError(t, err)
		assert.NotEmpty(t, newToken)

		// New token should be valid
		claims, err := sm.ValidateToken(newToken)
		require.NoError(t, err)
		assert.Equal(t, "testuser", claims.Username)
	})

	t.Run("should reject invalid token", func(t *testing.T) {
		_, err := sm.RefreshToken("invalid.token")
		require.Error(t, err)
	})
}

func TestGetTokenExpiry(t *testing.T) {
	sm, err := NewSessionManager()
	require.NoError(t, err)

	t.Run("should return token expiry time", func(t *testing.T) {
		token, err := sm.GenerateToken("testuser", true)
		require.NoError(t, err)

		expiry, err := sm.GetTokenExpiry(token)
		require.NoError(t, err)

		// Should be approximately 24 hours from now
		expectedExpiry := time.Now().Add(24 * time.Hour)
		diff := expiry.Sub(expectedExpiry)
		assert.Less(t, diff.Abs(), 5*time.Second)
	})

	t.Run("should return error for invalid token", func(t *testing.T) {
		_, err := sm.GetTokenExpiry("invalid.token")
		require.Error(t, err)
	})
}

func TestRevokeToken(t *testing.T) {
	sm, err := NewSessionManager()
	require.NoError(t, err)

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
	sm, err := NewSessionManager()
	require.NoError(t, err)

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
