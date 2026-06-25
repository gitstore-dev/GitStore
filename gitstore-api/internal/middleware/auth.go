// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"go.uber.org/zap"
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

// AuthDeps contains authentication middleware dependencies and configuration.
type AuthDeps struct {
	AdminUsername     string
	AdminPasswordHash string
	SessionManager    *auth.SessionManager
	JWTSecret         string
	JWTDuration       string
	JWTIssuer         string
}

// NewAuthMiddleware creates a new authentication middleware from explicit config values.
func NewAuthMiddleware(deps AuthDeps) (*AuthMiddleware, error) {
	if deps.AdminUsername == "" {
		return nil, authConfigError("admin username is required")
	}
	if deps.AdminPasswordHash == "" {
		return nil, authConfigError("admin password hash is required")
	}
	sessionManager := deps.SessionManager
	if sessionManager == nil {
		var err error
		sessionManager, err = auth.NewSessionManager(auth.SessionManagerDeps{
			Secret:   deps.JWTSecret,
			Duration: deps.JWTDuration,
			Issuer:   deps.JWTIssuer,
		})
		if err != nil {
			return nil, err
		}
	}

	return &AuthMiddleware{
		adminUsername:     deps.AdminUsername,
		adminPasswordHash: deps.AdminPasswordHash,
		sessionManager:    sessionManager,
	}, nil
}

type authConfigError string

func (e authConfigError) Error() string {
	return "auth: " + string(e)
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
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token != authHeader {
				claims, err := am.sessionManager.ValidateToken(token)
				if err == nil {
					user := &User{
						Username: claims.Username,
						IsAdmin:  claims.IsAdmin,
					}
					ctx := context.WithValue(r.Context(), UserContextKey, user)
					// Also populate the new Principal context so resolvers migrated
					// to auth.PrincipalFromContext work without ChainAuthMiddleware.
					principal := &auth.Principal{
						Subject:    claims.Username,
						Issuer:     "gitstore",
						AuthMethod: "static-admin",
					}
					if claims.IsAdmin {
						principal.Roles = []string{"admin"}
					}
					ctx = auth.ContextWithPrincipal(ctx, principal)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

// ChainAuthMiddleware is the registry-based replacement for OptionalAuth.
// It calls the ChainedAuthN, stores the resulting Principal in ctx, and
// returns 401 for any OutcomeDeny. Anonymous access passes through;
// the package-level RequireAuth guard must protect mutation routes.
func ChainAuthMiddleware(registry *auth.ProviderRegistry, logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := auth.AuthRequest{
				Header:     r.Header,
				RemoteAddr: r.RemoteAddr,
			}
			principal, decision, err := registry.AuthN().Authenticate(r.Context(), req)
			if err != nil {
				logger.Warn("auth chain error", zap.Error(err))
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if decision.Outcome == auth.OutcomeDeny {
				http.Error(w, "Unauthorized: "+decision.Reason, http.StatusUnauthorized)
				return
			}
			ctx := auth.ContextWithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAuth returns 401 if the Principal in ctx is anonymous (AuthMethod == "none").
// Apply to all mutation routes that require an authenticated caller.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalFromContext(r.Context())
		if p == nil || p.AuthMethod == "none" {
			http.Error(w, "Unauthorized: authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GenerateSessionToken generates a JWT token for a user (used after successful login)
func (am *AuthMiddleware) GenerateSessionToken(username string, isAdmin bool) (string, error) {
	return am.sessionManager.GenerateToken(username, isAdmin)
}

// RefreshSessionToken refreshes an existing token
func (am *AuthMiddleware) RefreshSessionToken(token string) (string, error) {
	return am.sessionManager.RefreshToken(token)
}

// GetTokenDuration returns the configured token lifetime.
func (am *AuthMiddleware) GetTokenDuration() time.Duration {
	return am.sessionManager.GetTokenDuration()
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
