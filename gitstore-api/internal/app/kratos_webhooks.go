// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"net/http"
	"net/url"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/kratos"
	"github.com/gitstore-dev/gitstore/api/internal/config"
)

func kratosWebhookServer(cfg config.KratosUserDirConfig, registry *auth.ProviderRegistry) (*http.Server, error) {
	if cfg.WebhookURI == "" {
		return nil, nil
	}
	if err := cfg.ValidateWebhookURI(); err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(cfg.WebhookURI)
	handler := http.NotFoundHandler()
	if registry != nil {
		if provider, ok := registry.UserDir().(*kratos.Provider); ok {
			handler = provider.WebhookHandler()
		}
	}
	return &http.Server{
		Addr: endpoint.Host, Handler: handler,
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
	}, nil
}
