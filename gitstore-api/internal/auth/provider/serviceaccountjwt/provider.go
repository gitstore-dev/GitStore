// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package serviceaccountjwt implements the "serviceaccount-jwt" AuthN
// provider (spec 061): it verifies GitStore-issued, short-lived access
// tokens on ordinary requests, and provides the issuer half used
// exclusively by the assertion-gated issueServiceAccountToken resolver
// (spec 061 Phase 4) — never through the generic per-provider IssueSession
// dispatch. See specs/061-controller-serviceaccount-auth/contracts/
// serviceaccount-provider.md for the full behavioral contract.
package serviceaccountjwt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

const providerName = "serviceaccount-jwt"

const (
	defaultIssuer   = "gitstore"
	defaultAudience = "gitstore-api"
	defaultTTL      = 10 * time.Minute
	defaultMaxTTL   = time.Hour
	defaultSkew     = 2 * time.Minute
)

// ServiceAccountLookup is the narrow datastore seam this provider needs —
// deliberately not the full datastore.Datastore interface, so this package
// depends only on the one read path it actually uses.
type ServiceAccountLookup interface {
	GetServiceAccountBySubject(ctx context.Context, namespace, name string) (*datastore.ServiceAccount, error)
}

// accessTokenClaims carries the private sa_uid claim documented in
// data-model.md §2 alongside the standard registered claims. sa_uid is
// never accepted from an untrusted caller at issuance — it is always
// derived from the ServiceAccount record being issued for.
type accessTokenClaims struct {
	jwt.RegisteredClaims
	ServiceAccountUID string `json:"sa_uid"`
}

// Provider implements auth.AuthNProvider, verifying access tokens issued by
// this same provider's IssueAccessToken.
type Provider struct {
	issuer      string
	audience    string
	keys        *keySet
	defaultTTL  time.Duration
	maxTTL      time.Duration
	clockSkew   time.Duration
	lookup      ServiceAccountLookup
	revocations *revocationList
	logger      *zap.Logger
}

// New constructs a Provider from the resolved ServiceAccountConfig. It
// returns an error if signing_key is empty or unparsable — callers only
// invoke New when "serviceaccount-jwt" is present in auth.authn.chain, at
// which point config.validateAuthChainConfig has already required
// signing_key to be non-empty, so an error here indicates the configured
// value itself is malformed, not merely absent.
func New(cfg config.ServiceAccountConfig, lookup ServiceAccountLookup, logger *zap.Logger) (*Provider, error) {
	issuer := strings.TrimSpace(cfg.Issuer)
	if issuer == "" {
		issuer = defaultIssuer
	}
	audience := strings.TrimSpace(cfg.Audience)
	if audience == "" {
		audience = defaultAudience
	}

	ttl, err := parseDurationOrDefault(cfg.DefaultTTL, defaultTTL, "default_ttl")
	if err != nil {
		return nil, err
	}
	maxTTL, err := parseDurationOrDefault(cfg.MaxTTL, defaultMaxTTL, "max_ttl")
	if err != nil {
		return nil, err
	}
	skew, err := parseDurationOrDefault(cfg.ClockSkew, defaultSkew, "clock_skew")
	if err != nil {
		return nil, err
	}

	keys, err := newKeySet(cfg.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("serviceaccountjwt: %w", err)
	}

	revocations := newRevocationList()
	go revocations.pruneLoop()

	return &Provider{
		issuer:      issuer,
		audience:    audience,
		keys:        keys,
		defaultTTL:  ttl,
		maxTTL:      maxTTL,
		clockSkew:   skew,
		lookup:      lookup,
		revocations: revocations,
		logger:      logger,
	}, nil
}

func parseDurationOrDefault(raw string, fallback time.Duration, field string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("serviceaccountjwt: invalid %s %q: %w", field, raw, err)
	}
	return d, nil
}

func (p *Provider) Name() string { return providerName }

// Shutdown stops the background revocation-list pruning goroutine. Must be
// called on server shutdown or SIGHUP-triggered provider replacement,
// mirroring staticadmin.StaticAdminProvider.Shutdown.
func (p *Provider) Shutdown() { p.revocations.shutdown() }

func (p *Provider) Capabilities() auth.Capability {
	return auth.CapAuthenticate
}

func (p *Provider) Authenticate(ctx context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	authHeader := req.Header.Get("Authorization")
	bearer, ok := strings.CutPrefix(authHeader, "Bearer ")
	if authHeader == "" || !ok {
		return nil, auth.Challenge(providerName, "no bearer token"), nil
	}

	// Step 1: parse without verifying signature, to check iss. A mismatch
	// means "not my token" — fall through to the next provider in the
	// chain, exactly like static-admin's issuer check.
	var peek accessTokenClaims
	if _, _, err := jwt.NewParser().ParseUnverified(bearer, &peek); err != nil {
		return nil, auth.Challenge(providerName, "jwt parse failed: "+err.Error()), nil
	}
	if peek.Issuer != p.issuer || !strings.HasPrefix(peek.Subject, "serviceaccount:") {
		return nil, auth.Challenge(providerName, "not a service account token"), nil
	}

	// Step 2-4: verify signature (against the kid-selected trusted key),
	// audience, and exp/nbf/iat with clock-skew leeway, all via the parse
	// call. Any failure here is a real auth failure — iss already matched,
	// so this token claims to be ours.
	claims := &accessTokenClaims{}
	parsed, err := jwt.ParseWithClaims(bearer, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, ok := p.keys.lookup(kid)
		if !ok {
			return nil, fmt.Errorf("unknown or no-longer-trusted kid %q", kid)
		}
		if key.method.Alg() != t.Method.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return key.publicKey, nil
	}, jwt.WithLeeway(p.clockSkew), jwt.WithIssuer(p.issuer), jwt.WithAudience(p.audience))
	if err != nil {
		return nil, auth.Deny(providerName, "token verification failed: "+err.Error()), nil
	}
	if !parsed.Valid {
		return nil, auth.Deny(providerName, "token invalid"), nil
	}

	// Step 5: look up the ServiceAccount by subject; disabled/deleted/UID
	// mismatch are all authoritative, persistent denial conditions.
	namespace, name, ok := datastore.ParseServiceAccountSubject(claims.Subject)
	if !ok {
		return nil, auth.Deny(providerName, "subject is not a serviceaccount identifier"), nil
	}
	sa, err := p.lookup.GetServiceAccountBySubject(ctx, namespace, name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, auth.Deny(providerName, "service account not found"), nil
		}
		return nil, auth.Deny(providerName, "service account lookup failed"), err
	}
	if sa.Disabled {
		return nil, auth.Deny(providerName, "service account is disabled"), nil
	}
	if sa.DeletionTimestamp != nil {
		return nil, auth.Deny(providerName, "service account is deleted"), nil
	}
	if sa.UID != claims.ServiceAccountUID {
		return nil, auth.Deny(providerName, "sa_uid does not match current service account"), nil
	}

	if claims.ID != "" && p.revocations.isRevoked(claims.ID) {
		return nil, auth.Deny(providerName, "token has been revoked"), nil
	}

	principal := &auth.Principal{
		Subject:           claims.Subject,
		Issuer:            claims.Issuer,
		Roles:             nil, // roles are resolved exclusively by rbac-local's role_bindings (FR-011) — never embedded here
		AuthMethod:        providerName,
		TokenID:           claims.ID,
		ServiceAccountUID: sa.UID,
	}
	if claims.ExpiresAt != nil {
		principal.ExpiresAt = claims.ExpiresAt.Time
	}
	return principal, auth.Allow(providerName, "valid service account access token"), nil
}

// RevokeSession records jti in the optional, defense-in-depth in-memory
// revocation list. This is never the authoritative revocation mechanism —
// see revocationList's doc comment.
func (p *Provider) RevokeSession(_ context.Context, jti string, expiresAt time.Time) error {
	revokeUntil := expiresAt.Add(p.clockSkew)
	if revokeUntil.Before(time.Now()) {
		revokeUntil = time.Now().Add(p.clockSkew)
	}
	p.revocations.add(jti, revokeUntil)
	return nil
}

// RefreshSession is not supported: service accounts renew via
// proof-of-possession (serviceaccount-assertion -> issueServiceAccountToken),
// never via a refresh-token flow.
func (p *Provider) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

// IssueSession is not supported through the generic ChainedAuthN dispatch.
// Issuance happens exclusively through the assertion-gated
// issueServiceAccountToken resolver, which calls IssueAccessToken directly.
func (p *Provider) IssueSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

// IssueAccessToken mints a new access token for sa, the issuer half invoked
// directly by the issueServiceAccountToken resolver (spec 061 Phase 4) —
// never through the generic IssueSession dispatch (see contract note on
// avoiding spec 060 research.md Decision 8's "first provider that supports
// IssueSession wins" ambiguity).
//
// requestedTTL <= 0 selects auth.serviceaccount.default_ttl. The returned
// token's lifetime is always clamped to auth.serviceaccount.max_ttl,
// regardless of what was requested (FR-013's TTL edge case) — the server
// never issues a longer-lived token than configured.
func (p *Provider) IssueAccessToken(sa *datastore.ServiceAccount, audience string, requestedTTL time.Duration) (token string, expiresAt time.Time, err error) {
	if p.keys.active == nil {
		return "", time.Time{}, errors.New("serviceaccountjwt: no active signing key configured")
	}
	if strings.TrimSpace(audience) == "" {
		audience = p.audience
	}
	ttl := requestedTTL
	if ttl <= 0 {
		ttl = p.defaultTTL
	}
	if ttl > p.maxTTL {
		ttl = p.maxTTL
	}

	jti, err := generateJTI()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("serviceaccountjwt: generate jti: %w", err)
	}

	now := time.Now()
	exp := now.Add(ttl)
	claims := accessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    p.issuer,
			Subject:   datastore.ServiceAccountSubject(sa.Namespace, sa.Name),
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        jti,
		},
		ServiceAccountUID: sa.UID,
	}

	signed, err := p.signWithClaims(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// signWithClaims signs an arbitrary accessTokenClaims value with the active
// signing key. Factored out of IssueAccessToken so tests can exercise
// Authenticate against hand-crafted claim edge cases (e.g. wrong audience,
// already-expired) without needing to fake the system clock.
func (p *Provider) signWithClaims(claims accessTokenClaims) (string, error) {
	if p.keys.active == nil {
		return "", errors.New("serviceaccountjwt: no active signing key configured")
	}
	tok := jwt.NewWithClaims(p.keys.active.method, claims)
	tok.Header["kid"] = p.keys.active.kid
	signed, err := tok.SignedString(p.keys.active.privateKey)
	if err != nil {
		return "", fmt.Errorf("serviceaccountjwt: sign token: %w", err)
	}
	return signed, nil
}
