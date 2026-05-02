package auth

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	// ErrInvalidToken is returned when a token is invalid
	ErrInvalidToken = errors.New("invalid token")
	// ErrExpiredToken is returned when a token has expired
	ErrExpiredToken = errors.New("token has expired")
	// ErrMissingToken is returned when a token is missing
	ErrMissingToken = errors.New("token is missing")
)

// Claims represents the JWT claims for a session
type Claims struct {
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
	jwt.RegisteredClaims
}

// SessionManager handles JWT token generation and validation
type SessionManager struct {
	secretKey     []byte
	tokenDuration time.Duration
	issuer        string
}

// NewSessionManager creates a new session manager
// Expects JWT_SECRET environment variable for production
func NewSessionManager() (*SessionManager, error) {
	// Get JWT secret from environment
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		// Default secret for development only - MUST be set in production
		secret = "dev-secret-change-in-production"
	}

	// Get token duration from environment (default: 24 hours)
	duration := 24 * time.Hour
	if durationStr := os.Getenv("JWT_DURATION"); durationStr != "" {
		if d, err := time.ParseDuration(durationStr); err == nil {
			duration = d
		}
	}

	issuer := os.Getenv("JWT_ISSUER")
	if issuer == "" {
		issuer = "gitstore"
	}

	return &SessionManager{
		secretKey:     []byte(secret),
		tokenDuration: duration,
		issuer:        issuer,
	}, nil
}

// GenerateToken creates a new JWT token for a user
func (sm *SessionManager) GenerateToken(username string, isAdmin bool) (string, error) {
	now := time.Now()
	expiresAt := now.Add(sm.tokenDuration)

	claims := Claims{
		Username: username,
		IsAdmin:  isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    sm.issuer,
			Subject:   username,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(sm.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// ValidateToken validates a JWT token and returns the claims
func (sm *SessionManager) ValidateToken(tokenString string) (*Claims, error) {
	if tokenString == "" {
		return nil, ErrMissingToken
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return sm.secretKey, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// RefreshToken generates a new token from an existing valid token
func (sm *SessionManager) RefreshToken(tokenString string) (string, error) {
	claims, err := sm.ValidateToken(tokenString)
	if err != nil {
		// Allow refreshing expired tokens within a grace period (7 days)
		if errors.Is(err, ErrExpiredToken) {
			// Parse without validation to check expiry time
			token, _ := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
				return sm.secretKey, nil
			})

			if token != nil {
				if claims, ok := token.Claims.(*Claims); ok {
					// Check if token expired within grace period
					gracePeriod := 7 * 24 * time.Hour
					if time.Since(claims.ExpiresAt.Time) > gracePeriod {
						return "", fmt.Errorf("token expired beyond grace period")
					}
					// Generate new token
					return sm.GenerateToken(claims.Username, claims.IsAdmin)
				}
			}
		}
		return "", err
	}

	// Generate new token with same claims
	return sm.GenerateToken(claims.Username, claims.IsAdmin)
}

// GetTokenExpiry returns the expiration time for a token
func (sm *SessionManager) GetTokenExpiry(tokenString string) (time.Time, error) {
	claims, err := sm.ValidateToken(tokenString)
	if err != nil {
		return time.Time{}, err
	}

	return claims.ExpiresAt.Time, nil
}

// RevokeToken invalidates a token (for logout)
// Note: JWT tokens are stateless, so true revocation requires a blacklist
// For now, this is a placeholder that returns success
// A production implementation would store revoked tokens in Redis/database
func (sm *SessionManager) RevokeToken(tokenString string) error {
	// Validate token exists and is valid format
	_, err := sm.ValidateToken(tokenString)
	if err != nil && !errors.Is(err, ErrExpiredToken) {
		return err
	}

	// In production, add token to blacklist:
	// - Store token hash in Redis with expiry
	// - Or store in database revocation table
	// - Check blacklist in ValidateToken()

	return nil
}

// GetTokenDuration returns the configured token duration
func (sm *SessionManager) GetTokenDuration() time.Duration {
	return sm.tokenDuration
}
