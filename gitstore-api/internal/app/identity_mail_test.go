// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/kratos"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type invitationDirectory struct {
	auth.UserDirProvider
	sender func(context.Context, *auth.UserProfile, string) error
}

func (p *invitationDirectory) SetInvitationSender(sender func(context.Context, *auth.UserProfile, string) error) {
	p.sender = sender
}

func TestIdentityMailRouteIsOptInAuthenticatedAndNotOnStorefront(t *testing.T) {
	const key = "owned-route-mail-key-at-least-32-characters"
	api := config.ApiConfig{Admin: config.DefaultEndpoint("admin"), Notification: config.NotificationConfig{
		Provider: "development", APIKey: key, Timeout: time.Second, MaxConcurrent: 1, MaxBodyBytes: 65536,
		Development: config.DevelopmentNotificationConfig{SMTPURI: "smtp://mail-catcher:1025", Sender: "noreply@example.test"},
	}}
	api.Admin.Limits.BodyBytes = 64
	disabled := gin.New()
	directory := &invitationDirectory{}
	runtime := &providerRegistryRuntime{registry: auth.NewProviderRegistry(nil, nil, directory)}
	require.NoError(t, registerIdentityMailRoute(disabled, config.ApiConfig{}, runtime))
	require.Nil(t, directory.sender)
	storefront, err := NewStorefrontHandler(config.DefaultEndpoint("storefront"), config.RateLimitConfig{}, nil)
	require.NoError(t, err)
	admin := gin.New()
	require.NoError(t, registerIdentityMailRoute(admin, api, runtime))
	require.NotNil(t, directory.sender)
	for _, test := range []struct {
		handler http.Handler
		key     string
		want    int
	}{{disabled, key, http.StatusNotFound}, {storefront, key, http.StatusNotFound},
		{admin, "", http.StatusUnauthorized}, {admin, key, http.StatusRequestEntityTooLarge}} {
		request := httptest.NewRequest(http.MethodPost, "/internal/notifications/mail", strings.NewReader(strings.Repeat("x", 65)))
		request.Header.Set("GitStore-Mail-Key", test.key)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		test.handler.ServeHTTP(response, request)
		require.Equal(t, test.want, response.Code)
	}
}

func TestIdentityMailInvitationsSurviveReload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	delivered := make(chan string, 3)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
				client := textproto.NewConn(connection)
				_ = client.PrintfLine("220 owned SMTP")
				for {
					line, err := client.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"),
						strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						_ = client.PrintfLine("250 accepted")
					case line == "DATA":
						_ = client.PrintfLine("354 data")
						body, err := client.ReadDotBytes()
						if err != nil {
							return
						}
						delivered <- string(body)
						_ = client.PrintfLine("250 delivered")
					case line == "QUIT":
						_ = client.PrintfLine("221 bye")
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	directory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/recovery/link" {
			json.NewEncoder(w).Encode(map[string]string{"recovery_link": "https://identity.example.test/setup?token=private"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "staff-uid", "schema_id": "staff", "state": "active",
			"traits":         map[string]string{"username": "alice", "email": "alice@example.test"},
			"metadata_admin": map[string]any{"lifecycle_state": auth.ActorInvited, "setup_complete": false},
		})
	}))
	defer directory.Close()
	cfg := &config.Config{}
	cfg.Api.Auth.AuthN.Chain = []string{"anonymous"}
	cfg.Api.Auth.AuthZ.Provider = "allow-all"
	cfg.Api.Auth.UserDir.Provider = "kratos"
	cfg.Api.Auth.Kratos = config.KratosUserDirConfig{
		AdminURI: directory.URL, Timeout: time.Second, MaxConcurrent: 2, MaxResponseBytes: 1 << 20,
		CacheEntries: 16, CacheTTL: time.Minute,
		Mappings: []config.SubjectMapping{{Provider: "oidc-jwt", Issuer: "https://issuer.example", Mode: "uid"}},
	}
	cfg.Api.Notification = config.NotificationConfig{
		Provider: "development", APIKey: "owned-route-mail-key-at-least-32-characters",
		Timeout: time.Second, MaxConcurrent: 1, MaxBodyBytes: 65536,
		Development: config.DevelopmentNotificationConfig{SMTPURI: "smtp://" + listener.Addr().String(), Sender: "noreply@example.test"},
	}
	registry, reloader, shutdowns, err := buildProviderRegistry(cfg, stubServiceAccountLookup{}, zap.NewNop(), nil)
	require.NoError(t, err)
	t.Cleanup(shutdowns[0].Shutdown)
	require.NoError(t, registerIdentityMailRoute(gin.New(), cfg.Api, reloader))
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	_, err = registry.UserDir().(*kratos.Provider).ResendInvitation(ctx, "staff-uid")
	require.NoError(t, err)
	require.NoError(t, reloader.Reload())
	provider := registry.UserDir().(*kratos.Provider)
	require.NoError(t, provider.CreateStaff(ctx, &auth.UserProfile{Username: "alice", Email: "alice@example.test", Kind: auth.ProfileStaff}))
	cfg.Api.Auth.UserDir.Provider = "none"
	require.NoError(t, reloader.Reload())
	cfg.Api.Auth.UserDir.Provider = "kratos"
	require.NoError(t, reloader.Reload())
	provider = registry.UserDir().(*kratos.Provider)
	_, err = provider.ResendInvitation(ctx, "staff-uid")
	require.NoError(t, err)
	require.Len(t, delivered, 3)
}
