// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build oidcintegration

package oidcjwt

// Opt-in integration test against a live OIDC issuer — mirroring
// `make test-scylla-integration`'s "requires an external instance" pattern.
// Usage (with the reference stack running per spec 059's quickstart):
//
//	OIDC_INTEGRATION_ISSUER_URI=http://localhost:4444 \
//	OIDC_INTEGRATION_CLIENT_ID=gitstore \
//	OIDC_INTEGRATION_ACCESS_TOKEN=<token from the Authorization Code + PKCE flow> \
//	go test -tags oidcintegration -run TestIntegration -v ./internal/auth/provider/oidcjwt/

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
)

func TestIntegrationLiveIssuerVerifiesAccessToken(t *testing.T) {
	issuerURI := os.Getenv("OIDC_INTEGRATION_ISSUER_URI")
	clientID := os.Getenv("OIDC_INTEGRATION_CLIENT_ID")
	token := os.Getenv("OIDC_INTEGRATION_ACCESS_TOKEN")
	if issuerURI == "" || clientID == "" || token == "" {
		t.Skip("set OIDC_INTEGRATION_ISSUER_URI, OIDC_INTEGRATION_CLIENT_ID, OIDC_INTEGRATION_ACCESS_TOKEN")
	}

	p, err := New(context.Background(), config.OIDCConfig{
		IssuerURI: issuerURI,
		ClientID:  clientID,
	}, zap.NewNop())
	require.NoError(t, err)

	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	principal, decision, err := p.Authenticate(context.Background(), auth.AuthRequest{Header: h})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.NotEmpty(t, principal.Subject)
	assert.NotEmpty(t, principal.Scopes)
	assert.Equal(t, "oidc-jwt", principal.AuthMethod)
	// Hydra access tokens carry no email claim; userinfo enrichment fills it
	// from the Kratos-backed identity (spec 059 claims mapping).
	assert.NotEmpty(t, principal.Claims["email"])
	assert.NotEmpty(t, principal.Claims["preferred_username"])
	t.Logf("principal: sub=%s iss=%s scopes=%v email=%v",
		principal.Subject, principal.Issuer, principal.Scopes, principal.Claims["email"])
}
