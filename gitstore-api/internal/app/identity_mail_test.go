// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, registerIdentityMailRoute(disabled, config.ApiConfig{}, directory))
	require.Nil(t, directory.sender)
	storefront, err := NewStorefrontHandler(config.DefaultEndpoint("storefront"), config.RateLimitConfig{}, nil)
	require.NoError(t, err)
	admin := gin.New()
	require.NoError(t, registerIdentityMailRoute(admin, api, directory))
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
