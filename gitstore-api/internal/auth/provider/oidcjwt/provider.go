// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package oidcjwt implements the Phase 7 `oidc-jwt` AuthN provider
// (docs/implementation/020-pluggable_auth_architecture.md §7): a generic,
// issuer-agnostic OIDC Relying Party that verifies bearer JWTs via OIDC
// Discovery + JWKS against whatever issuer_uri an operator configures —
// including the optional first-party Hydra+Kratos reference stack
// (specs/059-optional-oidc-provider).
package oidcjwt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
)

// OIDCJWTProvider verifies OIDC-issued bearer JWTs against a discovered JWKS.
type OIDCJWTProvider struct {
	provider  *oidc.Provider
	verifier  *oidc.IDTokenVerifier
	issuerURI string
	clientID  string
	audience  string
	logger    *zap.Logger
}

// idTokenClaims is the claim set the provider maps onto auth.Principal.
// Email and preferred_username follow spec 059's Kratos→claims mapping; any
// additional claims are carried through into Principal.Claims verbatim.
// Scope covers the RFC 6749 "scope" string form; Scp covers Hydra's
// array-valued "scp" claim on JWT access tokens — whichever is present wins.
type idTokenClaims struct {
	Subject           string   `json:"sub"`
	Issuer            string   `json:"iss"`
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
	Roles             []string `json:"roles"`
	Scope             string   `json:"scope"`
	Scp               []string `json:"scp"`
	TokenID           string   `json:"jti"`
	ExpiresAt         int64    `json:"exp"`
}

func (c *idTokenClaims) scopes() []string {
	if len(c.Scp) > 0 {
		return c.Scp
	}
	return strings.Fields(c.Scope)
}

// discover runs OIDC Discovery, tolerating the one interoperability wart we
// know real issuers have: go-oidc requires the configured issuer URL to match
// the discovery document's `issuer` byte-for-byte (e.g. Hydra's trailing
// slash). On exactly that mismatch, retry with the issuer's canonical form.
func discover(ctx context.Context, issuerURI string) (*oidc.Provider, string, error) {
	provider, err := oidc.NewProvider(ctx, issuerURI)
	if err == nil {
		return provider, issuerURI, nil
	}
	msg := err.Error()
	const marker = "did not match the issuer URL returned by provider (\""
	if i := strings.Index(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := strings.Index(rest, "\")"); j > 0 {
			canonical := rest[:j]
			provider, retryErr := oidc.NewProvider(ctx, canonical)
			if retryErr == nil {
				return provider, canonical, nil
			}
		}
	}
	return nil, "", err
}

// New runs OIDC Discovery against cfg.IssuerURI and returns a provider whose
// verifier enforces issuer, audience (cfg.Audience, defaulting to
// cfg.ClientID), and expiry (with cfg.ClockSkew leeway).
func New(ctx context.Context, cfg config.OIDCConfig, logger *zap.Logger) (*OIDCJWTProvider, error) {
	if strings.TrimSpace(cfg.IssuerURI) == "" || strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("oidcjwt: issuer_uri and client_id are required")
	}
	skew := 2 * time.Minute
	if cfg.ClockSkew != "" {
		parsed, err := time.ParseDuration(cfg.ClockSkew)
		if err != nil {
			return nil, fmt.Errorf("oidcjwt: invalid clock_skew %q: %w", cfg.ClockSkew, err)
		}
		skew = parsed
	}
	provider, canonicalIssuer, err := discover(ctx, cfg.IssuerURI)
	if err != nil {
		return nil, fmt.Errorf("oidcjwt: discovery against %q: %w", cfg.IssuerURI, err)
	}
	audience := cfg.Audience
	if audience == "" {
		audience = cfg.ClientID
	}
	verifier := provider.Verifier(&oidc.Config{
		ClientID: audience,
		// go-oidc has no explicit leeway knob beyond its built-in 1 minute;
		// shifting Now back by the configured clock_skew widens the exp/nbf
		// tolerance accordingly.
		Now: func() time.Time { return time.Now().Add(-skew) },
	})
	return &OIDCJWTProvider{
		provider:  provider,
		verifier:  verifier,
		issuerURI: strings.TrimSuffix(canonicalIssuer, "/"),
		clientID:  cfg.ClientID,
		audience:  audience,
		logger:    logger,
	}, nil
}

func (p *OIDCJWTProvider) Name() string { return "oidc-jwt" }
func (p *OIDCJWTProvider) Shutdown()    {}

func (p *OIDCJWTProvider) Capabilities() auth.Capability {
	return auth.CapAuthenticate | auth.CapIntrospect | auth.CapGroupResolution
}

func (p *OIDCJWTProvider) Authenticate(ctx context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	header := req.Header.Get("Authorization")
	if header == "" {
		return nil, auth.Challenge(p.Name(), "no authorization header"), nil
	}
	raw, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return nil, auth.Challenge(p.Name(), "unrecognized authorization scheme"), nil
	}

	// Cheap issuer pre-filter (parse, no verify): tokens minted by another
	// provider in the chain (e.g. static-users' HS256 sessions) must fall
	// through to it instead of failing JWKS verification here.
	var unverified jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(raw, &unverified); err != nil {
		return nil, auth.Challenge(p.Name(), "not a jwt: "+err.Error()), nil
	}
	if !sameIssuer(unverified.Issuer, p.issuerURI) {
		return nil, auth.Challenge(p.Name(), "issuer not handled by this provider"), nil
	}

	token, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		if strings.Contains(err.Error(), "token is expired") {
			return nil, auth.Deny(p.Name(), "token has expired"), nil
		}
		return nil, auth.Challenge(p.Name(), "token verification failed: "+err.Error()), nil
	}

	var claims idTokenClaims
	if err := token.Claims(&claims); err != nil {
		return nil, auth.Deny(p.Name(), "claims extraction failed"), fmt.Errorf("oidcjwt: extract claims: %w", err)
	}

	// UserInfo enrichment: JWT access tokens (e.g. Hydra's) may not carry
	// profile claims the ID token has; when email is absent and the issuer
	// exposes a userinfo endpoint, merge its claims (spec 059 claims mapping).
	if claims.Email == "" {
		p.enrichFromUserInfo(ctx, raw, &claims)
	}

	principal := &auth.Principal{
		Subject:    claims.Subject,
		Issuer:     claims.Issuer,
		Groups:     claims.Groups,
		Roles:      claims.Roles,
		Scopes:     claims.scopes(),
		AuthMethod: p.Name(),
		TokenID:    claims.TokenID,
	}
	principal.Claims = map[string]any{}
	if claims.Email != "" {
		principal.Claims["email"] = claims.Email
	}
	if claims.PreferredUsername != "" {
		principal.Claims["preferred_username"] = claims.PreferredUsername
	}
	if len(principal.Claims) == 0 {
		principal.Claims = nil
	}
	if claims.ExpiresAt != 0 {
		principal.ExpiresAt = time.Unix(claims.ExpiresAt, 0)
	}
	return principal, auth.Allow(p.Name(), "valid oidc jwt"), nil
}

// enrichFromUserInfo merges userinfo-endpoint claims into c on a
// fill-only-missing basis. Failures are logged and non-fatal — the verified
// token's own claims remain authoritative.
func (p *OIDCJWTProvider) enrichFromUserInfo(ctx context.Context, rawToken string, c *idTokenClaims) {
	info, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: rawToken,
		TokenType:   "Bearer",
	}))
	if err != nil {
		p.logger.Debug("oidc userinfo enrichment unavailable",
			zap.String("issuer", p.issuerURI), zap.Error(err))
		return
	}
	var extra struct {
		Email             string   `json:"email"`
		PreferredUsername string   `json:"preferred_username"`
		Groups            []string `json:"groups"`
	}
	if err := info.Claims(&extra); err != nil {
		p.logger.Debug("oidc userinfo claims extraction failed",
			zap.String("issuer", p.issuerURI), zap.Error(err))
		return
	}
	if c.Email == "" {
		c.Email = extra.Email
	}
	if c.PreferredUsername == "" {
		c.PreferredUsername = extra.PreferredUsername
	}
	if len(c.Groups) == 0 {
		c.Groups = extra.Groups
	}
}

// RevokeSession is not supported: OIDC tokens are stateless; revocation
// requires the IdP's own (RFC 7009) endpoint, which this Relying Party does
// not call in Phase 7 (see 020 §2b).
func (p *OIDCJWTProvider) RevokeSession(context.Context, string, time.Time) error {
	return auth.ErrNotSupported
}

// RefreshSession is not supported: gitstore-api never refreshes OIDC sessions
// itself (Phase 3d); refresh is between the client application and the issuer.
func (p *OIDCJWTProvider) RefreshSession(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

// IssueSession is not supported: this provider is a Relying Party only; it
// verifies foreign-issued tokens and never mints its own.
func (p *OIDCJWTProvider) IssueSession(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

func sameIssuer(tokenIssuer, configured string) bool {
	return strings.TrimSuffix(tokenIssuer, "/") == strings.TrimSuffix(configured, "/")
}

var _ auth.AuthNProvider = (*OIDCJWTProvider)(nil)
