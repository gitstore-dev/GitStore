// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/kratos"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/userdirnone"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
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

func TestWebhookListenerRejectsPublicAddresses(t *testing.T) {
	_, err := kratosWebhookServer(config.KratosUserDirConfig{WebhookURI: "http://8.8.8.8:8085"}, nil)
	require.Error(t, err)
	server, err := kratosWebhookServer(config.KratosUserDirConfig{}, nil)
	require.NoError(t, err)
	require.Nil(t, server)
}
