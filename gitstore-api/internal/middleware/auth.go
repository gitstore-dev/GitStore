// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package middleware

import (
	"net/http"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"go.uber.org/zap"
)

// ChainAuthMiddleware authenticates a request through the active provider chain.
// Anonymous access passes through as a Principal with AuthMethod "none"; denied
// credentials return 401 before the request reaches GraphQL.
func ChainAuthMiddleware(registry *auth.ProviderRegistry, logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := auth.AuthRequest{
				Header:     r.Header,
				RemoteAddr: r.RemoteAddr,
			}
			// Store the raw Bearer token in context so the refreshToken resolver can
			// pass it to RefreshSession without re-reading the HTTP request.
			ctx := r.Context()
			if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
				ctx = auth.ContextWithRawToken(ctx, strings.TrimPrefix(authHeader, "Bearer "))
			}
			principal, decision, err := registry.AuthN().Authenticate(ctx, req)
			if err != nil {
				logger.Warn("auth chain error", zap.Error(err))
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if decision.Outcome == auth.OutcomeDeny {
				http.Error(w, "Unauthorized: "+decision.Reason, http.StatusUnauthorized)
				return
			}
			ctx = auth.ContextWithPrincipal(ctx, principal)
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
