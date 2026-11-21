// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/kratos"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/userdirnone"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDisabledKratosWebhookRoutesReturnNotFound(t *testing.T) {
	server, err := kratosWebhookServer(config.KratosUserDirConfig{WebhookURI: "http://127.0.0.1:8085"},
		auth.NewProviderRegistry(nil, nil, userdirnone.New()))
	require.NoError(t, err)
	for _, path := range []string{kratos.RegistrationHookPath, kratos.SettingsHookPath} {
		result := httptest.NewRecorder()
		server.Handler.ServeHTTP(result, httptest.NewRequest(http.MethodPost, path, nil))
		require.Equal(t, http.StatusNotFound, result.Code)
	}
}

func TestKratosWebhookDispatchPreservesConcurrentRequestBound(t *testing.T) {
	cfg := config.KratosUserDirConfig{
		AdminURI: "http://127.0.0.1:1", WebhookURI: "http://127.0.0.1:8085",
		Timeout: time.Second, MaxConcurrent: 1, MaxResponseBytes: 1 << 20, CacheEntries: 16, CacheTTL: time.Minute,
		Mappings: []config.SubjectMapping{{Provider: "oidc-jwt", Issuer: "https://issuer.example", Mode: "uid"}},
	}
	provider, err := kratos.New(cfg)
	require.NoError(t, err)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	provider.SetInvalidationHandler(func() { entered <- struct{}{}; <-release })
	server, err := kratosWebhookServer(cfg, auth.NewProviderRegistry(nil, nil, provider))
	require.NoError(t, err)
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, kratos.RegistrationHookPath,
			strings.NewReader(`{"identity":{"id":"customer-uid","schema_id":"customer","traits":{"username":"alice"}}}`))
	}
	first, firstDone := httptest.NewRecorder(), make(chan struct{})
	second, secondDone := httptest.NewRecorder(), make(chan struct{})
	t.Cleanup(func() { close(release); <-firstDone; <-secondDone })
	go func() { defer close(firstDone); server.Handler.ServeHTTP(first, request()) }()
	go func() {
		defer close(secondDone)
		select {
		case <-entered:
			server.Handler.ServeHTTP(second, request())
		case <-t.Context().Done():
		}
	}()
	select {
	case <-secondDone:
		require.Equal(t, http.StatusServiceUnavailable, second.Code)
	case <-time.After(time.Second):
		t.Fatal("webhook dispatch did not preserve the provider's shared admission bound")
	}
}

func TestWebhookListenerRejectsPublicAddresses(t *testing.T) {
	_, err := kratosWebhookServer(config.KratosUserDirConfig{WebhookURI: "http://8.8.8.8:8085"}, nil)
	require.Error(t, err)
	server, err := kratosWebhookServer(config.KratosUserDirConfig{}, nil)
	require.NoError(t, err)
	require.Nil(t, server)
}

func TestKratosWebhooksFollowReloadedDirectoryAndInvalidateItsCache(t *testing.T) {
	var fullName atomic.Value
	fullName.Store("Before")
	record := func(name string) map[string]any {
		return map[string]any{
			"id": "staff-uid", "schema_id": "staff", "state": "active",
			"traits": map[string]string{"username": "alice", "email": "alice@example.test", "full_name": name},
		}
	}
	directory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(record(fullName.Load().(string)))
	}))
	defer directory.Close()
	cfg := &config.Config{}
	cfg.Api.Auth.AuthN.Chain = []string{"anonymous"}
	cfg.Api.Auth.AuthZ.Provider = "allow-all"
	cfg.Api.Auth.UserDir.Provider = "kratos"
	cfg.Api.Auth.Kratos = config.KratosUserDirConfig{
		AdminURI: directory.URL, WebhookURI: "http://127.0.0.1:8085",
		Timeout: time.Second, MaxConcurrent: 2, MaxResponseBytes: 1 << 20, CacheEntries: 16, CacheTTL: time.Minute,
		Mappings: []config.SubjectMapping{{Provider: "oidc-jwt", Issuer: "https://issuer.example", Mode: "uid"}},
	}
	registry, reloader, shutdowns, err := buildProviderRegistry(cfg, stubServiceAccountLookup{}, zap.NewNop(), nil)
	require.NoError(t, err)
	t.Cleanup(shutdowns[0].Shutdown)
	server, err := kratosWebhookServer(cfg.Api.Auth.Kratos, registry)
	require.NoError(t, err)
	require.NoError(t, reloader.Reload())
	profile, err := registry.UserDir().GetBySubject(t.Context(), "staff-uid")
	require.NoError(t, err)
	require.Equal(t, "Before", profile.DisplayName)
	payload, err := json.Marshal(map[string]any{"identity": record("After")})
	require.NoError(t, err)
	result := httptest.NewRecorder()
	server.Handler.ServeHTTP(result, httptest.NewRequest(http.MethodPost, kratos.SettingsHookPath, strings.NewReader(string(payload))))
	require.Equal(t, http.StatusOK, result.Code)
	fullName.Store("After")
	profile, err = registry.UserDir().GetBySubject(t.Context(), "staff-uid")
	require.NoError(t, err)
	require.Equal(t, "After", profile.DisplayName, "the live provider's cached profile must be invalidated")
	cfg.Api.Auth.UserDir.Provider = "none"
	require.NoError(t, reloader.Reload())
	result = httptest.NewRecorder()
	server.Handler.ServeHTTP(result, httptest.NewRequest(http.MethodPost, kratos.SettingsHookPath, strings.NewReader(string(payload))))
	require.Equal(t, http.StatusNotFound, result.Code)
	cfg.Api.Auth.UserDir.Provider = "kratos"
	require.NoError(t, reloader.Reload())
	result = httptest.NewRecorder()
	server.Handler.ServeHTTP(result, httptest.NewRequest(http.MethodPost, kratos.SettingsHookPath, strings.NewReader(string(payload))))
	require.Equal(t, http.StatusOK, result.Code)
}
