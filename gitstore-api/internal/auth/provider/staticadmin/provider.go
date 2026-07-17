// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package staticadmin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// StaticAdminProvider authenticates using HS256 JWT bearer tokens and Basic Auth
// credentials checked against a bcrypt-hashed password.
type StaticAdminProvider struct {
	username     string
	passwordHash string
	jwtSecret    []byte
	jwtIssuer    string
	jwtDuration  time.Duration
	refreshGrace time.Duration
	blacklist    *sessionBlacklist
	logger       *zap.Logger
}

func New(cfg config.AuthConfig, logger *zap.Logger) (*StaticAdminProvider, error) {
	username := cfg.Admin.Username
	if username == "" {
		username = "admin"
	}
	hash := cfg.Admin.Password
	if hash == "" {
		return nil, errors.New("staticadmin: GITSTORE_AUTH__ADMIN__PASSWORD_HASH is required")
	}
	secret := cfg.JWT.Secret
	if secret == "" {
		return nil, errors.New("staticadmin: GITSTORE_AUTH__JWT__SECRET is required")
	}
	issuer := cfg.JWT.Issuer
	if issuer == "" {
		issuer = "gitstore"
	}
	duration := 24 * time.Hour
	if d := cfg.JWT.Duration; d != "" {
		parsed, err := time.ParseDuration(d)
		if err != nil {
			return nil, fmt.Errorf("staticadmin: invalid jwt duration %q: %w", d, err)
		}
		duration = parsed
	}
	refreshGrace := 60 * time.Second
	if g := cfg.JWT.RefreshGrace; g != "" {
		parsed, err := time.ParseDuration(g)
		if err != nil {
			return nil, fmt.Errorf("staticadmin: invalid refresh_grace %q: %w", g, err)
		}
		refreshGrace = parsed
	}

	bl := newSessionBlacklist()
	go bl.pruneLoop()

	return &StaticAdminProvider{
		username:     username,
		passwordHash: hash,
		jwtSecret:    []byte(secret),
		jwtIssuer:    issuer,
		jwtDuration:  duration,
		refreshGrace: refreshGrace,
		blacklist:    bl,
		logger:       logger,
	}, nil
}

func (p *StaticAdminProvider) Name() string { return "static-admin" }

// Shutdown stops the background blacklist pruning goroutine.
// It must be called when the provider is no longer needed (e.g. on server shutdown
// or after a SIGHUP-triggered provider replacement).
func (p *StaticAdminProvider) Shutdown() { p.blacklist.shutdown() }

func (p *StaticAdminProvider) Capabilities() auth.Capability {
	return auth.CapAuthenticate | auth.CapIssueSession | auth.CapIntrospect
}

func (p *StaticAdminProvider) Authenticate(_ context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		return nil, auth.Challenge("static-admin", "no authorization header"), nil
	}

	if bearer, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		return p.authenticateBearer(bearer)
	}

	if basic, ok := strings.CutPrefix(authHeader, "Basic "); ok {
		return p.authenticateBasic(basic)
	}

	return nil, auth.Challenge("static-admin", "unrecognized authorization scheme"), nil
}

func (p *StaticAdminProvider) authenticateBearer(token string) (*auth.Principal, auth.Decision, error) {
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return p.jwtSecret, nil
	}, jwt.WithLeeway(2*time.Minute), jwt.WithIssuer(p.jwtIssuer))

	if err != nil {
		// An expired token issued by us (correct key/issuer) is our token — Deny it.
		// A token with wrong key, wrong alg, or wrong issuer is not ours — Challenge.
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, auth.Deny("static-admin", "token has expired"), nil
		}
		return nil, auth.Challenge("static-admin", "jwt parse failed: "+err.Error()), nil
	}
	if !parsed.Valid {
		return nil, auth.Challenge("static-admin", "jwt invalid"), nil
	}

	// Check blacklist by jti.
	if claims.ID != "" && p.blacklist.isRevoked(claims.ID) {
		return nil, auth.Deny("static-admin", "token has been revoked"), nil
	}

	principal := &auth.Principal{
		Subject:    claims.Subject,
		Issuer:     claims.Issuer,
		Roles:      []string{"admin"},
		AuthMethod: "static-admin",
		TokenID:    claims.ID,
	}
	if claims.ExpiresAt != nil {
		principal.ExpiresAt = claims.ExpiresAt.Time
	}
	return principal, auth.Allow("static-admin", "valid jwt"), nil
}

func (p *StaticAdminProvider) authenticateBasic(encoded string) (*auth.Principal, auth.Decision, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, auth.Challenge("static-admin", "invalid basic auth encoding"), nil
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return nil, auth.Challenge("static-admin", "malformed basic auth credentials"), nil
	}
	username, password := parts[0], parts[1]

	if username != p.username {
		return nil, auth.Challenge("static-admin", "unknown username"), nil
	}
	if err := bcrypt.CompareHashAndPassword([]byte(p.passwordHash), []byte(password)); err != nil {
		return nil, auth.Challenge("static-admin", "invalid password"), nil
	}

	return &auth.Principal{
		Subject:    username,
		Issuer:     p.jwtIssuer,
		Roles:      []string{"admin"},
		AuthMethod: "static-admin",
	}, auth.Allow("static-admin", "valid basic auth"), nil
}

func (p *StaticAdminProvider) RevokeSession(_ context.Context, jti string, expiresAt time.Time) error {
	// Store the revocation until the token's natural expiry plus the authentication leeway
	// so that an expired-within-leeway token cannot bypass the blacklist check.
	revokeUntil := expiresAt.Add(2 * time.Minute)
	if revokeUntil.Before(time.Now()) {
		revokeUntil = time.Now().Add(2 * time.Minute)
	}
	p.blacklist.add(jti, revokeUntil)
	return nil
}

func (p *StaticAdminProvider) RefreshSession(_ context.Context, oldToken string) (string, time.Time, error) {
	// Parse ignoring expiry to allow refreshing recently-expired tokens.
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(oldToken, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return p.jwtSecret, nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("staticadmin: refresh: %w", err)
	}

	// Enforce grace window: reject tokens that expired longer ago than refreshGrace.
	if claims.ExpiresAt != nil && time.Now().After(claims.ExpiresAt.Time.Add(p.refreshGrace)) {
		return "", time.Time{}, fmt.Errorf("staticadmin: refresh: %w", auth.ErrTokenTooOld)
	}

	if claims.ID != "" && p.blacklist.isRevoked(claims.ID) {
		return "", time.Time{}, fmt.Errorf("staticadmin: refresh: %w", auth.ErrTokenRevoked)
	}

	// Revoke old jti before issuing replacement. Use a revocation expiry that covers the
	// authentication leeway window (2 min) beyond the token's natural expiry so that an
	// expired-but-within-leeway token cannot be re-used for authentication after rotation.
	if claims.ID != "" {
		revokeUntil := time.Now().Add(2 * time.Minute)
		if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Add(2*time.Minute).After(revokeUntil) {
			revokeUntil = claims.ExpiresAt.Time.Add(2 * time.Minute)
		}
		p.blacklist.add(claims.ID, revokeUntil)
	}

	newToken, exp, err := p.issueToken(claims.Subject)
	if err != nil {
		return "", time.Time{}, err
	}
	return newToken, exp, nil
}

// IssueSession mints a new HS256 JWT for the given subject.
func (p *StaticAdminProvider) IssueSession(_ context.Context, subject string) (string, time.Time, error) {
	return p.issueToken(subject)
}

// IssueToken generates a new HS256 JWT for the given subject.
func (p *StaticAdminProvider) IssueToken(subject string) (string, time.Time, error) {
	return p.issueToken(subject)
}

func (p *StaticAdminProvider) issueToken(subject string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(p.jwtDuration)
	jti, err := generateJTI()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("staticadmin: generate jti: %w", err)
	}
	claims := jwt.RegisteredClaims{
		Subject:   subject,
		Issuer:    p.jwtIssuer,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
		ID:        jti,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(p.jwtSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("staticadmin: sign token: %w", err)
	}
	return signed, exp, nil
}

// sessionBlacklist is an in-memory store of revoked JTIs keyed by jti → expiresAt.
type sessionBlacklist struct {
	mu      sync.RWMutex
	entries map[string]time.Time
	stop    chan struct{}
}

func newSessionBlacklist() *sessionBlacklist {
	return &sessionBlacklist{
		entries: make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
}

func (b *sessionBlacklist) add(jti string, expiresAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[jti] = expiresAt
}

func (b *sessionBlacklist) isRevoked(jti string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.entries[jti]
	return ok
}

func (b *sessionBlacklist) pruneLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			b.prune()
		case <-b.stop:
			return
		}
	}
}

func (b *sessionBlacklist) shutdown() {
	close(b.stop)
}

func (b *sessionBlacklist) prune() {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for jti, exp := range b.entries {
		if now.After(exp) {
			delete(b.entries, jti)
		}
	}
}
