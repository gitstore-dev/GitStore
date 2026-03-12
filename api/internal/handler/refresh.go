package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/commerce-projects/gitstore/api/internal/middleware"
	"go.uber.org/zap"
)

// RefreshTokenHandler handles token refresh requests
type RefreshTokenHandler struct {
	authMiddleware *middleware.AuthMiddleware
	logger         *zap.Logger
}

// NewRefreshTokenHandler creates a new refresh token handler
func NewRefreshTokenHandler(authMiddleware *middleware.AuthMiddleware, logger *zap.Logger) *RefreshTokenHandler {
	return &RefreshTokenHandler{
		authMiddleware: authMiddleware,
		logger:         logger,
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
	newToken, err := h.authMiddleware.RefreshSessionToken(token)
	if err != nil {
		h.logger.Debug("Failed to refresh token",
			zap.Error(err),
		)
		http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
		return
	}

	// Calculate expiry time (24 hours from now)
	expiresAt := time.Now().Add(24 * time.Hour)

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
