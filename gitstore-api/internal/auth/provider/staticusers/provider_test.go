// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
package staticusers

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
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

func TestLoadUsersValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: v1\nusers:\n  - username: alice\n    password_hash: x\n  - username: alice\n    password_hash: y\n"), 0600))
	_, err := loadUsers(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), path)
	assert.Contains(t, err.Error(), "duplicate")
}
