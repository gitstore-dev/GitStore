// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
package staticusers

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func testProvider(t *testing.T) *StaticUsersProvider {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("version: v1\nusers:\n  - username: alice\n    password_hash: "+string(hash)+"\n    display_name: Alice\n    email: alice@example.com\n  - username: bob\n    password_hash: "+string(hash)+"\n"), 0600))
	p, err := New(config.AuthConfig{StaticUsers: config.StaticUsersConfig{UsersFile: path}, JWT: config.JWTConfig{Secret: "secret", Issuer: "gitstore", Duration: "1h"}}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(p.Shutdown)
	return p
}

func TestStaticUsersBasicAuthAndUserDir(t *testing.T) {
	p := testProvider(t)
	creds := base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	principal, decision, err := p.Authenticate(context.Background(), auth.AuthRequest{Header: http.Header{"Authorization": {"Basic " + creds}}})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, "alice", principal.Subject)
	assert.Empty(t, principal.Roles)
	assert.Equal(t, "static-users", principal.AuthMethod)
	profile, err := p.GetBySubject(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "Alice", profile.DisplayName)
	_, err = p.GetBySubject(context.Background(), "nobody")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestStaticUsersBearerRoundTrip(t *testing.T) {
	p := testProvider(t)
	token, _, err := p.IssueSession(context.Background(), "bob")
	require.NoError(t, err)
	principal, decision, err := p.Authenticate(context.Background(), auth.AuthRequest{Header: http.Header{"Authorization": {"Bearer " + token}}})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, "bob", principal.Subject)
	assert.Empty(t, principal.Roles)
}

func TestStaticUsersTokenDomainRejectsNewTokenOnLegacyVerifier(t *testing.T) {
	p := testProvider(t)
	token, _, err := p.IssueSession(context.Background(), "alice")
	require.NoError(t, err)

	claims := &jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
		return []byte("secret"), nil
	}, jwt.WithIssuer("gitstore"))
	require.Error(t, err)
	assert.Equal(t, "gitstore/static-users", claims.Issuer)

	principal, decision, err := p.Authenticate(context.Background(), auth.AuthRequest{Header: http.Header{"Authorization": {"Bearer " + token}}})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, "alice", principal.Subject)
}

func TestStaticUsersAcceptsLegacyIssuerDuringRollingUpgrade(t *testing.T) {
	p := testProvider(t)
	now := time.Now()
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: "alice", Issuer: "gitstore", ID: "legacy-jti",
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}).SignedString([]byte("secret"))
	require.NoError(t, err)

	principal, decision, err := p.Authenticate(context.Background(), auth.AuthRequest{Header: http.Header{"Authorization": {"Bearer " + legacy}}})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, "alice", principal.Subject)
}

func TestStaticUsersRevocationIsSharedAcrossProviders(t *testing.T) {
	store := newMemoryRevocationStore()
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("version: v1\nusers:\n  - username: alice\n    password_hash: "+string(hash)+"\n"), 0600))
	cfg := config.AuthConfig{StaticUsers: config.StaticUsersConfig{UsersFile: path}, JWT: config.JWTConfig{Secret: "secret", Issuer: "gitstore", Duration: "1h"}}
	issuer, err := NewWithRevocationStore(cfg, zap.NewNop(), store)
	require.NoError(t, err)
	verifier, err := NewWithRevocationStore(cfg, zap.NewNop(), store)
	require.NoError(t, err)

	token, exp, err := issuer.IssueSession(context.Background(), "alice")
	require.NoError(t, err)
	claims := &jwt.RegisteredClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(token, claims)
	require.NoError(t, err)
	require.NoError(t, issuer.RevokeSession(context.Background(), claims.ID, exp))

	_, decision, err := verifier.Authenticate(context.Background(), auth.AuthRequest{Header: http.Header{"Authorization": {"Bearer " + token}}})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestStaticUsersRefreshRevokesOldTokenThroughNaturalExpiry(t *testing.T) {
	p := testProvider(t)
	oldToken, _, err := p.IssueSession(context.Background(), "alice")
	require.NoError(t, err)
	oldClaims := &jwt.RegisteredClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(oldToken, oldClaims)
	require.NoError(t, err)

	_, _, err = p.RefreshSession(context.Background(), oldToken)
	require.NoError(t, err)
	store := p.revocations.(*memoryRevocationStore)
	store.mu.RLock()
	revokedUntil := store.entries[oldClaims.ID]
	store.mu.RUnlock()
	assert.False(t, revokedUntil.Before(oldClaims.ExpiresAt.Time.Add(2*time.Minute)))
}

func TestStaticUsersConcurrentRefreshIsConsumedOnceAcrossProviders(t *testing.T) {
	store := newMemoryRevocationStore()
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("version: v1\nusers:\n  - username: alice\n    password_hash: "+string(hash)+"\n"), 0600))
	cfg := config.AuthConfig{StaticUsers: config.StaticUsersConfig{UsersFile: path}, JWT: config.JWTConfig{Secret: "secret", Issuer: "gitstore", Duration: "1h"}}
	first, err := NewWithRevocationStore(cfg, zap.NewNop(), store)
	require.NoError(t, err)
	second, err := NewWithRevocationStore(cfg, zap.NewNop(), store)
	require.NoError(t, err)
	token, _, err := first.IssueSession(context.Background(), "alice")
	require.NoError(t, err)

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, provider := range []*StaticUsersProvider{first, second} {
		wg.Add(1)
		go func(provider *StaticUsersProvider) {
			defer wg.Done()
			_, _, refreshErr := provider.RefreshSession(context.Background(), token)
			results <- refreshErr
		}(provider)
	}
	wg.Wait()
	close(results)
	var successes, revoked int
	for refreshErr := range results {
		if refreshErr == nil {
			successes++
		} else if assert.ErrorIs(t, refreshErr, auth.ErrTokenRevoked) {
			revoked++
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, revoked)
}

func TestStaticUsersRemovedUserCannotRefresh(t *testing.T) {
	p := testProvider(t)
	token, _, err := p.IssueSession(context.Background(), "alice")
	require.NoError(t, err)
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.path, []byte("version: v1\nusers:\n  - username: bob\n    password_hash: "+string(hash)+"\n"), 0600))
	require.NoError(t, p.Reload())

	_, _, err = p.RefreshSession(context.Background(), token)
	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestLoadUsersValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: v1\nusers:\n  - username: alice\n    password_hash: x\n  - username: alice\n    password_hash: y\n"), 0600))
	_, err := loadUsers(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), path)
	assert.Contains(t, err.Error(), "duplicate")
}
