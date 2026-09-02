// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package serviceaccountassertion implements the "serviceaccount-assertion"
// AuthN provider (spec 061): it verifies short-lived, single-use client
// assertions signed by a controller's enrolled private key, used
// exclusively to authorize the issueServiceAccountToken mutation (Phase 4)
// — never for any other GraphQL operation. Unlike serviceaccountjwt, the
// trust model here is inverted: the target ServiceAccount is looked up by
// UNTRUSTED sub/sa_uid claims purely to select which enrolled public key to
// verify against; the lookup result is never treated as authorization
// until *after* the signature has been verified against that specific key.
// See specs/061-controller-serviceaccount-auth/contracts/
// serviceaccount-provider.md for the full behavioral contract.
package serviceaccountassertion

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
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

const providerName = "serviceaccount-assertion"

// assertionTyp is the required protected-header "typ" value (data-model.md
// §3). A mismatch means "not my token" — fall through to the next
// provider in the chain.
const assertionTyp = "gitstore-sa-assertion+jwt"

const (
	defaultAssertionAudience = "gitstore-api/serviceaccount-token"
	defaultClockSkew         = 2 * time.Minute
	// maxAssertionLifetime is the hard exp-iat<=60s bound from data-model.md
	// §3 — assertions are single-use, proof-of-possession tokens, not
	// general-purpose bearer tokens, so their validity window is
	// intentionally far shorter than serviceaccount-jwt's access tokens.
	maxAssertionLifetime = 60 * time.Second
)

// ServiceAccountLookup is the narrow datastore seam this provider needs.
// Identical shape to serviceaccountjwt.ServiceAccountLookup — kept as a
// separate type in this package (rather than a shared import) so each
// provider package only depends on the one method it actually calls,
// without coupling the two packages to each other.
type ServiceAccountLookup interface {
	GetServiceAccountBySubject(ctx context.Context, namespace, name string) (*datastore.ServiceAccount, error)
}

// ServiceAccountStore is the narrow datastore seam this provider needs. The
// replay consume operation is durable and atomic so a single assertion cannot
// be accepted by more than one API replica.
type ServiceAccountStore interface {
	ServiceAccountLookup
	TryConsumeServiceAccountAssertion(ctx context.Context, jtiDigest string, expiresAt time.Time) (bool, error)
}

// assertionClaims mirrors data-model.md §3's client-assertion claim set.
type assertionClaims struct {
	jwt.RegisteredClaims
	ServiceAccountUID string `json:"sa_uid"`
}

// Provider implements auth.AuthNProvider for client-assertion,
// proof-of-possession authentication.
type Provider struct {
	assertionAudience string
	clockSkew         time.Duration
	store             ServiceAccountStore
	logger            *zap.Logger
}

// New constructs a Provider from the resolved ServiceAccountConfig.
func New(cfg config.ServiceAccountConfig, store ServiceAccountStore, logger *zap.Logger) (*Provider, error) {
	if store == nil {
		return nil, errors.New("serviceaccountassertion: service account store is required")
	}
	audience := strings.TrimSpace(cfg.AssertionAudience)
	if audience == "" {
		audience = defaultAssertionAudience
	}
	skew := defaultClockSkew
	if raw := strings.TrimSpace(cfg.ClockSkew); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("serviceaccountassertion: invalid clock_skew %q: %w", raw, err)
		}
		skew = d
	}

	return &Provider{
		assertionAudience: audience,
		clockSkew:         skew,
		store:             store,
		logger:            logger,
	}, nil
}

func (p *Provider) Name() string { return providerName }

func (p *Provider) Capabilities() auth.Capability {
	return auth.CapAuthenticate
}

func (p *Provider) Authenticate(ctx context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	authHeader := req.Header.Get("Authorization")
	bearer, ok := strings.CutPrefix(authHeader, "Bearer ")
	if authHeader == "" || !ok {
		return nil, auth.Challenge(providerName, "no bearer token"), nil
	}

	// Step 1: parse without verifying signature, to check the protected
	// typ header. A mismatch means "not my token" — fall through to the
	// next provider in the chain (e.g. serviceaccount-jwt, static-users).
	var peek assertionClaims
	peekToken, _, err := jwt.NewParser().ParseUnverified(bearer, &peek)
	if err != nil {
		return nil, auth.Challenge(providerName, "jwt parse failed: "+err.Error()), nil
	}
	typ, _ := peekToken.Header["typ"].(string)
	if typ != assertionTyp {
		return nil, auth.Challenge(providerName, "typ header mismatch"), nil
	}

	// Step 2: look up the target ServiceAccount by the UNTRUSTED sub/sa_uid
	// claims. This lookup exists purely to select which enrolled public
	// key to verify the signature against — it is never treated as
	// authorization on its own, and every field checked here is re-checked
	// against the now-trusted claims after signature verification below.
	namespace, name, ok := datastore.ParseServiceAccountSubject(peek.Subject)
	if !ok {
		return nil, auth.Deny(providerName, "subject is not a serviceaccount identifier"), nil
	}
	sa, err := p.store.GetServiceAccountBySubject(ctx, namespace, name)
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
	if sa.UID != peek.ServiceAccountUID {
		return nil, auth.Deny(providerName, "sa_uid does not match current service account"), nil
	}

	// Step 3: select the enrolled public key matching the assertion's kid
	// header.
	kid, _ := peekToken.Header["kid"].(string)
	enrolledKey, ok := findEnrolledKey(sa.PublicKeys, kid)
	if !ok {
		return nil, auth.Deny(providerName, "no enrolled key matches kid"), nil
	}
	pubKey, method, err := parseEnrolledPublicKey(enrolledKey)
	if err != nil {
		return nil, auth.Deny(providerName, "enrolled key is unparsable: "+err.Error()), nil
	}

	// Expected subject is the canonical, datastore-derived form — not the
	// caller-supplied peek.Subject — so a token cannot smuggle a
	// differently-formatted-but-equivalent subject string past the checks
	// below.
	expectedSubject := datastore.ServiceAccountSubject(namespace, name)

	// Step 4: verify the signature against the enrolled key, plus iss/sub
	// exactly equal to the subject (data-model.md §3: "iss equal to the
	// ServiceAccount subject", "sub equal to iss"), aud, and exp/nbf/iat
	// with clock-skew leeway.
	claims := &assertionClaims{}
	parsed, err := jwt.ParseWithClaims(bearer, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != method.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pubKey, nil
	}, jwt.WithLeeway(p.clockSkew), jwt.WithIssuedAt(), jwt.WithIssuer(expectedSubject), jwt.WithAudience(p.assertionAudience))
	if err != nil {
		return nil, auth.Deny(providerName, "assertion verification failed: "+err.Error()), nil
	}
	if !parsed.Valid {
		return nil, auth.Deny(providerName, "assertion invalid"), nil
	}
	if claims.Subject != expectedSubject {
		return nil, auth.Deny(providerName, "sub does not match iss"), nil
	}
	if claims.ServiceAccountUID != sa.UID {
		return nil, auth.Deny(providerName, "sa_uid does not match current service account"), nil
	}
	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return nil, auth.Deny(providerName, "assertion missing iat/exp"), nil
	}
	if lifetime := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time); lifetime <= 0 || lifetime > maxAssertionLifetime {
		return nil, auth.Deny(providerName, "assertion lifetime exceeds 60s bound"), nil
	}

	// Step 6: single-use enforcement is a shared datastore LWT/transaction,
	// not a process-local cache, so it remains authoritative across replicas.
	if claims.ID == "" {
		return nil, auth.Deny(providerName, "assertion missing jti"), nil
	}
	consumed, err := p.store.TryConsumeServiceAccountAssertion(
		ctx,
		assertionReplayDigest(claims.ID),
		claims.ExpiresAt.Time.Add(p.clockSkew),
	)
	if err != nil {
		return nil, auth.Deny(providerName, "assertion replay check failed"), err
	}
	if !consumed {
		return nil, auth.Deny(providerName, "assertion jti already used"), nil
	}

	principal := &auth.Principal{
		Subject:           claims.Subject,
		Issuer:            claims.Issuer,
		Roles:             nil, // resolved exclusively by rbac-local — never embedded (FR-011)
		AuthMethod:        providerName,
		TokenID:           claims.ID,
		ExpiresAt:         claims.ExpiresAt.Time,
		ServiceAccountUID: sa.UID,
	}
	return principal, auth.Allow(providerName, "valid single-use client assertion"), nil
}

func findEnrolledKey(keys []datastore.ServiceAccountPublicKey, kid string) (datastore.ServiceAccountPublicKey, bool) {
	if kid == "" {
		return datastore.ServiceAccountPublicKey{}, false
	}
	for _, k := range keys {
		if k.KeyID == kid {
			return k, true
		}
	}
	return datastore.ServiceAccountPublicKey{}, false
}

// parseEnrolledPublicKey decodes an enrolled key's raw (PEM-decoded, i.e.
// DER-encoded SubjectPublicKeyInfo) bytes into a crypto.PublicKey plus the
// jwt.SigningMethod it must be verified with, cross-checking the record's
// declared Algorithm field against the key's actual type so a
// mislabeled/corrupted enrollment record is rejected rather than silently
// accepted under the wrong algorithm.
func parseEnrolledPublicKey(key datastore.ServiceAccountPublicKey) (any, jwt.SigningMethod, error) {
	pub, err := x509.ParsePKIXPublicKey(key.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("parse public key: %w", err)
	}
	switch k := pub.(type) {
	case ed25519.PublicKey:
		if key.Algorithm != "Ed25519" {
			return nil, nil, fmt.Errorf("key is Ed25519 but Algorithm field says %q", key.Algorithm)
		}
		return k, jwt.SigningMethodEdDSA, nil
	case *ecdsa.PublicKey:
		if key.Algorithm != "ECDSA-P256" {
			return nil, nil, fmt.Errorf("key is ECDSA but Algorithm field says %q", key.Algorithm)
		}
		if k.Curve.Params().Name != "P-256" {
			return nil, nil, fmt.Errorf("unsupported ECDSA curve %q (only P-256 is supported)", k.Curve.Params().Name)
		}
		return k, jwt.SigningMethodES256, nil
	default:
		return nil, nil, fmt.Errorf("unsupported public key type %T", pub)
	}
}

// RevokeSession is not supported: assertions are single-use by construction,
// not a revocable session.
func (p *Provider) RevokeSession(_ context.Context, _ string, _ time.Time) error {
	return auth.ErrNotSupported
}

// RefreshSession is not supported: there is no "session" to refresh — a
// new assertion is signed for each proof-of-possession exchange.
func (p *Provider) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

// IssueSession is not supported: this provider never mints tokens — that
// is serviceaccount-jwt's issuer half, invoked only through the
// issueServiceAccountToken resolver, never through ChainedAuthN's generic
// per-provider IssueSession dispatch.
func (p *Provider) IssueSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}
