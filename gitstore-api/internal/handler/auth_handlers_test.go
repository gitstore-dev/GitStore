// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeAuthService struct {
	duration time.Duration
}

func (fakeAuthService) ValidateCredentials(username, password string) bool {
	return username == "admin" && password == "correct-password"
}

func (fakeAuthService) GenerateSessionToken(string, bool) (string, error) {
	return "login-token", nil
}

func (fakeAuthService) RefreshSessionToken(string) (string, error) {
	return "refresh-token", nil
}

func (f fakeAuthService) GetTokenDuration() time.Duration {
	return f.duration
}

func TestLoginHandlerUsesConfiguredTokenDurationForExpiry(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	h := NewLoginHandler(LoginHandlerDeps{
		Auth:   fakeAuthService{duration: 2 * time.Hour},
		Logger: zap.NewNop(),
		Clock:  apiruntime.NewFixedClock(now),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:correct-password")))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp LoginResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "login-token", resp.Token)
	assert.Equal(t, now.Add(2*time.Hour).Format(time.RFC3339), resp.ExpiresAt)
}

func TestRefreshTokenHandlerUsesConfiguredTokenDurationForExpiry(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	h := NewRefreshTokenHandler(RefreshTokenHandlerDeps{
		Auth:   fakeAuthService{duration: 90 * time.Minute},
		Logger: zap.NewNop(),
		Clock:  apiruntime.NewFixedClock(now),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	req.Header.Set("Authorization", "Bearer old-token")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp RefreshTokenResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "refresh-token", resp.Token)
	assert.Equal(t, now.Add(90*time.Minute).Format(time.RFC3339), resp.ExpiresAt)
}
