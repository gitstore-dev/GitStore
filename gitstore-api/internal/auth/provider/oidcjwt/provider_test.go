// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
package oidcjwt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
)

// mockIssuer is a minimal OIDC issuer: discovery document, JWKS, and userinfo.
type mockIssuer struct {
	server      *httptest.Server
	key         *rsa.PrivateKey
	keyID       string
	userinfo    map[string]any
	userinfoHit bool
}

func newMockIssuer(t *testing.T) *mockIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	m := &mockIssuer{key: key, keyID: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 m.server.URL,
			"jwks_uri":               m.server.URL + "/keys",
			"userinfo_endpoint":      m.server.URL + "/userinfo",
			"token_endpoint":         m.server.URL + "/oauth2/token",
			"authorization_endpoint": m.server.URL + "/oauth2/auth",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{jwkFor(t, m.key, m.keyID)},
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		m.userinfoHit = true
		_ = json.NewEncoder(w).Encode(m.userinfo)
	})
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func jwkFor(t *testing.T, key *rsa.PrivateKey, kid string) map[string]any {
	t.Helper()
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": kid,
		"n":   enc(key.PublicKey.N.Bytes()),
		"e":   enc(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}
}

func (m *mockIssuer) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = m.keyID
	raw, err := token.SignedString(m.key)
	require.NoError(t, err)
	return raw
}

func (m *mockIssuer) validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":                m.server.URL,
		"sub":                "kratos-identity-uuid",
		"aud":                "gitstore",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"iat":                time.Now().Unix(),
		"jti":                "token-id-1",
		"email":              "user@example.com",
		"preferred_username": "exampleuser",
		"scope":              "openid profile email offline_access",
	}
}

func newProvider(t *testing.T, issuer *mockIssuer, cfg config.OIDCConfig) *OIDCJWTProvider {
	t.Helper()
	if cfg.IssuerURI == "" {
		cfg.IssuerURI = issuer.server.URL
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "gitstore"
	}
	p, err := New(context.Background(), cfg, zap.NewNop())
	require.NoError(t, err)
	return p
}

func bearerReq(token string) auth.AuthRequest {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return auth.AuthRequest{Header: h}
}

func TestNameAndCapabilities(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})
	assert.Equal(t, "oidc-jwt", p.Name())
	assert.Equal(t, auth.CapAuthenticate|auth.CapIntrospect|auth.CapGroupResolution, p.Capabilities())
}

func TestNewRequiresIssuerURI(t *testing.T) {
	_, err := New(context.Background(), config.OIDCConfig{ClientID: "gitstore"}, zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "issuer_uri")
}

func TestNewRequiresAudienceOrClientID(t *testing.T) {
	_, err := New(context.Background(), config.OIDCConfig{IssuerURI: "http://localhost:4444/"}, zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "audience or client_id")
}

func TestAuthenticateWithAudienceOnlyNoClientID(t *testing.T) {
	issuer := newMockIssuer(t)
	// Pure resource-server mode: audience set, client_id empty.
	p, err := New(context.Background(), config.OIDCConfig{
		IssuerURI: issuer.server.URL,
		Audience:  "gitstore",
	}, zap.NewNop())
	require.NoError(t, err)
	token := issuer.sign(t, issuer.validClaims())

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, "kratos-identity-uuid", principal.Subject)
}

func TestAuthenticateValidToken(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})
	token := issuer.sign(t, issuer.validClaims())

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, "kratos-identity-uuid", principal.Subject)
	assert.Equal(t, issuer.server.URL, principal.Issuer)
	assert.Equal(t, "oidc-jwt", principal.AuthMethod)
	assert.Equal(t, "token-id-1", principal.TokenID)
	assert.Equal(t, "user@example.com", principal.Claims["email"])
	assert.Equal(t, "exampleuser", principal.Claims["preferred_username"])
	assert.Equal(t, []string{"openid", "profile", "email", "offline_access"}, principal.Scopes)
	assert.False(t, principal.ExpiresAt.IsZero())
	assert.False(t, issuer.userinfoHit, "userinfo must not be called when the token already carries email")
}

func TestAuthenticateUserInfoEnrichmentWhenEmailMissing(t *testing.T) {
	issuer := newMockIssuer(t)
	issuer.userinfo = map[string]any{"email": "enriched@example.com", "preferred_username": "enriched"}
	p := newProvider(t, issuer, config.OIDCConfig{})
	claims := issuer.validClaims()
	delete(claims, "email")
	delete(claims, "preferred_username")
	token := issuer.sign(t, claims)

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.True(t, issuer.userinfoHit)
	assert.Equal(t, "enriched@example.com", principal.Claims["email"])
	assert.Equal(t, "enriched", principal.Claims["preferred_username"])
}

func TestAuthenticateHydraStyleScpArrayClaim(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})
	claims := issuer.validClaims()
	delete(claims, "scope")
	claims["scp"] = []string{"openid", "email"} // Hydra JWT access-token form
	token := issuer.sign(t, claims)

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, []string{"openid", "email"}, principal.Scopes)
}

func TestAuthenticateNoHeaderChallenges(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})
	principal, decision, err := p.Authenticate(context.Background(), auth.AuthRequest{Header: http.Header{}})
	require.NoError(t, err)
	assert.Nil(t, principal)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestAuthenticateForeignIssuerChallenges(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})
	claims := issuer.validClaims()
	claims["iss"] = "gitstore/static-users"
	token := issuer.sign(t, claims)

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Nil(t, principal)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome, "foreign-issuer tokens must fall through to the next provider in the chain")
}

func TestAuthenticateExpiredTokenDenies(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{ClockSkew: "0s"})
	claims := issuer.validClaims()
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	token := issuer.sign(t, claims)

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Nil(t, principal)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestAuthenticateClockSkewToleratesRecentExpiry(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{ClockSkew: "5m"})
	claims := issuer.validClaims()
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	token := issuer.sign(t, claims)

	_, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome, "1m-expired token must be tolerated under 5m clock skew")
}

func TestAuthenticateWrongAudienceChallenges(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{Audience: "someone-else"})
	token := issuer.sign(t, issuer.validClaims())

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Nil(t, principal)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestAuthenticateBadSignatureChallenges(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})
	token := issuer.sign(t, issuer.validClaims())
	// Corrupt the signature segment.
	token = token[:len(token)-4] + "AAAA"

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Nil(t, principal)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestAuthenticateKeyRotation(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})

	// Rotate: new key published at the same JWKS URI.
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer.key = newKey
	issuer.keyID = "test-key-2"

	token := issuer.sign(t, issuer.validClaims())
	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome, "unknown kid must trigger a JWKS refresh")
	assert.Equal(t, "kratos-identity-uuid", principal.Subject)
}

func TestAuthenticateUsernameClaimPreferredUsername(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{UsernameClaim: "preferred_username"})
	token := issuer.sign(t, issuer.validClaims())

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, "exampleuser", principal.Subject, "Subject should come from the configured username claim")
	assert.Equal(t, "kratos-identity-uuid", principal.Claims["sub"], "raw sub must survive in Claims")
}

func TestAuthenticateUsernameClaimFallsBackToSub(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{UsernameClaim: "department"})
	token := issuer.sign(t, issuer.validClaims())

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, "kratos-identity-uuid", principal.Subject, "absent username claim must fall back to sub")
}

func TestAuthenticateUsernameClaimResolvedViaUserInfo(t *testing.T) {
	issuer := newMockIssuer(t)
	issuer.userinfo = map[string]any{"email": "u@example.com", "preferred_username": "from-userinfo"}
	p := newProvider(t, issuer, config.OIDCConfig{UsernameClaim: "preferred_username"})
	claims := issuer.validClaims()
	// Access-token shape: no preferred_username in the token, but email IS
	// present — the email-only enrichment gate would not fire here.
	delete(claims, "preferred_username")
	token := issuer.sign(t, claims)

	principal, decision, err := p.Authenticate(context.Background(), bearerReq(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.True(t, issuer.userinfoHit, "userinfo must be consulted when the username claim is missing, even if email is present")
	assert.Equal(t, "from-userinfo", principal.Subject)
	assert.Equal(t, "kratos-identity-uuid", principal.Claims["sub"])
}

func TestRefreshIssueRevokeUnsupported(t *testing.T) {
	issuer := newMockIssuer(t)
	p := newProvider(t, issuer, config.OIDCConfig{})

	_, _, err := p.RefreshSession(context.Background(), "token")
	assert.ErrorIs(t, err, auth.ErrNotSupported)
	_, _, err = p.IssueSession(context.Background(), "subject")
	assert.ErrorIs(t, err, auth.ErrNotSupported)
	assert.ErrorIs(t, p.RevokeSession(context.Background(), "jti", time.Now()), auth.ErrNotSupported)
}
