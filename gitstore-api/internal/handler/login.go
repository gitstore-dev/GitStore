package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"go.uber.org/zap"
)

// LoginHandler handles authentication requests
type LoginHandler struct {
	authMiddleware *middleware.AuthMiddleware
	logger         *zap.Logger
}

// NewLoginHandler creates a new login handler
func NewLoginHandler(authMiddleware *middleware.AuthMiddleware, logger *zap.Logger) *LoginHandler {
	return &LoginHandler{
		authMiddleware: authMiddleware,
		logger:         logger,
	}
}

// LoginResponse represents the login response
type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	Username  string `json:"username"`
	IsAdmin   bool   `json:"isAdmin"`
}

// ServeHTTP handles the login request
func (h *LoginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract Basic Auth credentials
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Basic ") {
		h.logger.Debug("Missing or invalid Authorization header")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse Basic Auth (format: "Basic base64(username:password)")
	// The browser will handle the base64 encoding
	username, password, ok := r.BasicAuth()
	if !ok {
		h.logger.Debug("Failed to parse Basic Auth")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Validate credentials
	if !h.authMiddleware.ValidateCredentials(username, password) {
		h.logger.Debug("Invalid credentials",
			zap.String("username", username),
		)
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}

	// Generate JWT token
	token, err := h.authMiddleware.GenerateSessionToken(username, true)
	if err != nil {
		h.logger.Error("Failed to generate session token",
			zap.Error(err),
		)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Calculate expiry time (24 hours from now)
	expiresAt := time.Now().Add(24 * time.Hour)

	// Return response
	response := LoginResponse{
		Token:     token,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		Username:  username,
		IsAdmin:   true,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error("Failed to encode login response",
			zap.Error(err),
		)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	h.logger.Info("User logged in successfully",
		zap.String("username", username),
	)
}
