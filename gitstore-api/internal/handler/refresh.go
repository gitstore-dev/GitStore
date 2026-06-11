// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"go.uber.org/zap"
)

// RefreshTokenHandler handles token refresh requests
type RefreshTokenHandler struct {
	auth   AuthService
	logger *zap.Logger
	clock  apiruntime.Clock
}

// RefreshTokenHandlerDeps contains dependencies for RefreshTokenHandler.
type RefreshTokenHandlerDeps struct {
	Auth   AuthService
	Logger *zap.Logger
	Clock  apiruntime.Clock
}

// NewRefreshTokenHandler creates a new refresh token handler
func NewRefreshTokenHandler(deps RefreshTokenHandlerDeps) *RefreshTokenHandler {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	clock := deps.Clock
	if clock == nil {
		clock = apiruntime.SystemClock{}
	}
	return &RefreshTokenHandler{
		auth:   deps.Auth,
		logger: logger,
		clock:  clock,
	}
}

// RefreshTokenResponse represents the refresh token response
type RefreshTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// ServeHTTP handles the refresh token request
func (h *RefreshTokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract Bearer token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		h.logger.Debug("Missing or invalid Authorization header")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		h.logger.Debug("Empty token")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Refresh the token
	if h.auth == nil {
		h.logger.Debug("auth service not configured")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	newToken, err := h.auth.RefreshSessionToken(token)
	if err != nil {
		h.logger.Debug("Failed to refresh token",
			zap.Error(err),
		)
		http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
		return
	}

	expiresAt := h.clock.Now().Add(h.auth.GetTokenDuration())

	// Return response
	response := RefreshTokenResponse{
		Token:     newToken,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error("Failed to encode refresh token response",
			zap.Error(err),
		)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	h.logger.Info("Token refreshed successfully")
}
