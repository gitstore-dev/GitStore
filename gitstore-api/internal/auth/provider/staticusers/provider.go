// SPDX-License-Identifier: AGPL-3.0-or-later
package staticusers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"strings"
	"sync"
	"time"
)

type refreshClaims struct{ jwt.RegisteredClaims }

func (c refreshClaims) GetExpirationTime() (*jwt.NumericDate, error) {
	if c.ExpiresAt == nil {
		return nil, nil
	}
	return jwt.NewNumericDate(time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)), nil
}

type StaticUsersProvider struct {
	mu                        sync.RWMutex
	users                     map[string]UserEntry
	path                      string
	jwtSecret                 []byte
	jwtIssuer                 string
	jwtDuration, refreshGrace time.Duration
	blacklist                 *sessionBlacklist
	logger                    *zap.Logger
}

func New(cfg config.AuthConfig, logger *zap.Logger) (*StaticUsersProvider, error) {
	path := cfg.StaticUsers.UsersFile
	if path == "" {
		path = "users.yaml"
	}
	users, err := loadUsers(path)
	if err != nil {
		return nil, err
	}
	if cfg.JWT.Secret == "" {
		return nil, errors.New("staticusers: GITSTORE_AUTH__JWT__SECRET is required")
	}
	issuer := cfg.JWT.Issuer
	if issuer == "" {
		issuer = "gitstore"
	}
	duration := 24 * time.Hour
	if cfg.JWT.Duration != "" {
		duration, err = time.ParseDuration(cfg.JWT.Duration)
		if err != nil {
			return nil, fmt.Errorf("staticusers: invalid jwt duration %q: %w", cfg.JWT.Duration, err)
		}
	}
	grace := time.Minute
	if cfg.JWT.RefreshGrace != "" {
		grace, err = time.ParseDuration(cfg.JWT.RefreshGrace)
		if err != nil {
			return nil, fmt.Errorf("staticusers: invalid refresh_grace %q: %w", cfg.JWT.RefreshGrace, err)
		}
	}
	bl := newSessionBlacklist()
	go bl.pruneLoop()
	return &StaticUsersProvider{users: users, path: path, jwtSecret: []byte(cfg.JWT.Secret), jwtIssuer: issuer, jwtDuration: duration, refreshGrace: grace, blacklist: bl, logger: logger}, nil
}
func (p *StaticUsersProvider) Name() string { return "static-users" }
func (p *StaticUsersProvider) Shutdown()    { p.blacklist.shutdown() }
func (p *StaticUsersProvider) Capabilities() auth.Capability {
	return auth.CapAuthenticate | auth.CapIssueSession | auth.CapIntrospect | auth.CapUserLookup
}
func (p *StaticUsersProvider) Reload() error {
	users, err := loadUsers(p.path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.users = users
	p.mu.Unlock()
	if p.logger != nil {
		p.logger.Info("static-users user list reloaded", zap.String("path", p.path))
	}
	return nil
}
func (p *StaticUsersProvider) Usernames() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.users))
	for name := range p.users {
		out = append(out, name)
	}
	return out
}
func (p *StaticUsersProvider) Authenticate(_ context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	h := req.Header.Get("Authorization")
	if h == "" {
		return nil, auth.Challenge(p.Name(), "no authorization header"), nil
	}
	if token, ok := strings.CutPrefix(h, "Bearer "); ok {
		return p.authenticateBearer(token)
	}
	if basic, ok := strings.CutPrefix(h, "Basic "); ok {
		return p.authenticateBasic(basic)
	}
	return nil, auth.Challenge(p.Name(), "unrecognized authorization scheme"), nil
}
func (p *StaticUsersProvider) authenticateBasic(encoded string) (*auth.Principal, auth.Decision, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, auth.Challenge(p.Name(), "invalid basic auth encoding"), nil
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return nil, auth.Challenge(p.Name(), "malformed basic auth credentials"), nil
	}
	p.mu.RLock()
	u, ok := p.users[parts[0]]
	p.mu.RUnlock()
	if !ok {
		return nil, auth.Challenge(p.Name(), "unknown username"), nil
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(parts[1])) != nil {
		return nil, auth.Challenge(p.Name(), "invalid password"), nil
	}
	return &auth.Principal{Subject: u.Username, Issuer: p.jwtIssuer, AuthMethod: p.Name()}, auth.Allow(p.Name(), "valid basic auth"), nil
}
func (p *StaticUsersProvider) authenticateBearer(token string) (*auth.Principal, auth.Decision, error) {
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return p.jwtSecret, nil
	}, jwt.WithLeeway(2*time.Minute), jwt.WithIssuer(p.jwtIssuer))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, auth.Deny(p.Name(), "token has expired"), nil
		}
		return nil, auth.Challenge(p.Name(), "jwt parse failed: "+err.Error()), nil
	}
	if !parsed.Valid {
		return nil, auth.Challenge(p.Name(), "jwt invalid"), nil
	}
	if claims.ID != "" && p.blacklist.isRevoked(claims.ID) {
		return nil, auth.Deny(p.Name(), "token has been revoked"), nil
	}
	pr := &auth.Principal{Subject: claims.Subject, Issuer: claims.Issuer, AuthMethod: p.Name(), TokenID: claims.ID}
	if claims.ExpiresAt != nil {
		pr.ExpiresAt = claims.ExpiresAt.Time
	}
	return pr, auth.Allow(p.Name(), "valid jwt"), nil
}
func (p *StaticUsersProvider) IssueSession(_ context.Context, subject string) (string, time.Time, error) {
	return p.issueToken(subject)
}
func (p *StaticUsersProvider) IssueToken(subject string) (string, time.Time, error) {
	return p.issueToken(subject)
}
func (p *StaticUsersProvider) issueToken(subject string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(p.jwtDuration)
	jti, err := generateJTI()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("staticusers: generate jti: %w", err)
	}
	c := jwt.RegisteredClaims{Subject: subject, Issuer: p.jwtIssuer, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp), ID: jti}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(p.jwtSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("staticusers: sign token: %w", err)
	}
	return signed, exp, nil
}
func (p *StaticUsersProvider) RevokeSession(_ context.Context, jti string, expiresAt time.Time) error {
	until := expiresAt.Add(2 * time.Minute)
	if until.Before(time.Now()) {
		until = time.Now().Add(2 * time.Minute)
	}
	p.blacklist.add(jti, until)
	return nil
}
func (p *StaticUsersProvider) RefreshSession(_ context.Context, old string) (string, time.Time, error) {
	c := &refreshClaims{}
	_, err := jwt.ParseWithClaims(old, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return p.jwtSecret, nil
	}, jwt.WithLeeway(2*time.Minute), jwt.WithIssuer(p.jwtIssuer), jwt.WithExpirationRequired())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("staticusers: refresh: %w", auth.ErrInvalidToken)
	}
	if c.ExpiresAt != nil && time.Now().After(c.ExpiresAt.Time.Add(p.refreshGrace)) {
		return "", time.Time{}, fmt.Errorf("staticusers: refresh: %w", auth.ErrTokenTooOld)
	}
	if c.ID != "" && p.blacklist.isRevoked(c.ID) {
		return "", time.Time{}, fmt.Errorf("staticusers: refresh: %w", auth.ErrTokenRevoked)
	}
	if c.ID != "" {
		p.blacklist.add(c.ID, time.Now().Add(2*time.Minute))
	}
	return p.issueToken(c.Subject)
}
func (p *StaticUsersProvider) GetBySubject(_ context.Context, s string) (*auth.UserProfile, error) {
	p.mu.RLock()
	u, ok := p.users[s]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrUserNotFound
	}
	return &auth.UserProfile{Subject: u.Username, DisplayName: u.DisplayName, Email: u.Email, Active: true}, nil
}
func (p *StaticUsersProvider) ListGroups(_ context.Context, s string) ([]string, error) {
	if _, err := p.GetBySubject(context.Background(), s); err != nil {
		return nil, err
	}
	return []string{}, nil
}
func (p *StaticUsersProvider) SearchUsers(_ context.Context, q string, limit int) ([]*auth.UserProfile, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	q = strings.ToLower(q)
	out := make([]*auth.UserProfile, 0)
	for _, u := range p.users {
		if q == "" || strings.Contains(strings.ToLower(u.Username), q) || strings.Contains(strings.ToLower(u.DisplayName), q) || strings.Contains(strings.ToLower(u.Email), q) {
			out = append(out, &auth.UserProfile{Subject: u.Username, DisplayName: u.DisplayName, Email: u.Email, Active: true})
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
func (p *StaticUsersProvider) UpsertProfile(context.Context, *auth.UserProfile) error {
	return auth.ErrNotSupported
}
func (p *StaticUsersProvider) Deactivate(context.Context, string) error { return auth.ErrNotSupported }

type sessionBlacklist struct {
	mu      sync.RWMutex
	entries map[string]time.Time
	stop    chan struct{}
}

func newSessionBlacklist() *sessionBlacklist {
	return &sessionBlacklist{entries: map[string]time.Time{}, stop: make(chan struct{})}
}
func (b *sessionBlacklist) add(j string, e time.Time) { b.mu.Lock(); b.entries[j] = e; b.mu.Unlock() }
func (b *sessionBlacklist) isRevoked(j string) bool {
	b.mu.RLock()
	_, ok := b.entries[j]
	b.mu.RUnlock()
	return ok
}
func (b *sessionBlacklist) pruneLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.prune()
		case <-b.stop:
			return
		}
	}
}
func (b *sessionBlacklist) prune() {
	now := time.Now()
	b.mu.Lock()
	for j, e := range b.entries {
		if now.After(e) {
			delete(b.entries, j)
		}
	}
	b.mu.Unlock()
}
func (b *sessionBlacklist) shutdown() {
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
}

var _ auth.AuthNProvider = (*StaticUsersProvider)(nil)
var _ auth.UserDirProvider = (*StaticUsersProvider)(nil)
