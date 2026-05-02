package middleware

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

const (
	// UserContextKey is the key for storing user info in context
	UserContextKey contextKey = "user"
)

// User represents an authenticated user
type User struct {
	Username string
	IsAdmin  bool
}

// AuthMiddleware provides authentication functionality
type AuthMiddleware struct {
	adminUsername     string
	adminPasswordHash string
	sessionManager    *auth.SessionManager
}

// NewAuthMiddleware creates a new authentication middleware
// Expects ADMIN_USERNAME and ADMIN_PASSWORD_HASH environment variables
func NewAuthMiddleware() (*AuthMiddleware, error) {
	username := os.Getenv("ADMIN_USERNAME")
	if username == "" {
		username = "admin" // Default username
	}

	passwordHash := os.Getenv("ADMIN_PASSWORD_HASH")
	if passwordHash == "" {
		// Generate default hash for "admin123" - ONLY FOR DEVELOPMENT
		// In production, this should be set via environment variable
		hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		passwordHash = string(hash)
	}

	// Create session manager
	sessionManager, err := auth.NewSessionManager()
	if err != nil {
		return nil, err
	}

	return &AuthMiddleware{
		adminUsername:     username,
		adminPasswordHash: passwordHash,
		sessionManager:    sessionManager,
	}, nil
}

// ValidateCredentials checks if the provided username and password are valid
func (am *AuthMiddleware) ValidateCredentials(username, password string) bool {
	// Check username
	if username != am.adminUsername {
		return false
	}

	// Check password using bcrypt
	err := bcrypt.CompareHashAndPassword([]byte(am.adminPasswordHash), []byte(password))
	return err == nil
}

// RequireAuth is a middleware that requires authentication
func (am *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get session token from Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized: missing authorization header", http.StatusUnauthorized)
			return
		}

		// Extract bearer token
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			http.Error(w, "Unauthorized: invalid authorization format", http.StatusUnauthorized)
			return
		}

		// Validate JWT token
		claims, err := am.sessionManager.ValidateToken(token)
		if err != nil {
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}

		// Add user to context
		user := &User{
			Username: claims.Username,
			IsAdmin:  claims.IsAdmin,
		}
		ctx := context.WithValue(r.Context(), UserContextKey, user)

		// Call next handler with user context
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuth is a middleware that adds user to context if authenticated, but doesn't require it
func (am *AuthMiddleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get session token from Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			// Extract bearer token
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token != authHeader {
				// Validate JWT token
				claims, err := am.sessionManager.ValidateToken(token)
				if err == nil {
					// Token valid, add user to context
					user := &User{
						Username: claims.Username,
						IsAdmin:  claims.IsAdmin,
					}
					ctx := context.WithValue(r.Context(), UserContextKey, user)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Invalid token, proceed without user context
			}
		}

		// No auth or invalid format, proceed without user context
		next.ServeHTTP(w, r)
	})
}

// GetUserFromContext retrieves the user from the request context
func GetUserFromContext(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(UserContextKey).(*User)
	return user, ok
}

// GenerateSessionToken generates a JWT token for a user (used after successful login)
func (am *AuthMiddleware) GenerateSessionToken(username string, isAdmin bool) (string, error) {
	return am.sessionManager.GenerateToken(username, isAdmin)
}

// RefreshSessionToken refreshes an existing token
func (am *AuthMiddleware) RefreshSessionToken(token string) (string, error) {
	return am.sessionManager.RefreshToken(token)
}

// HashPassword generates a bcrypt hash from a plain text password
// This is a utility function for generating password hashes
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
