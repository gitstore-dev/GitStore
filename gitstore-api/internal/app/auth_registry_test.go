// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func TestProviderRegistryReloadPreservesUsableBindingInvariant(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	policyPath := filepath.Join(dir, "policy.yaml")
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)
	writeUsers := func(username string) {
		require.NoError(t, os.WriteFile(usersPath, []byte("version: v1\nusers:\n  - username: "+username+"\n    password_hash: "+string(hash)+"\n"), 0600))
	}
	writePolicy := func(username string) {
		require.NoError(t, os.WriteFile(policyPath, []byte("version: v1\ndefault_deny: true\nroles:\n  admin:\n    allow: [\"*\"]\nrole_bindings:\n  "+username+": [admin]\n"), 0600))
	}
	writeUsers("alice")
	writePolicy("alice")

	cfg := &config.Config{Auth: config.AuthConfig{
		StaticUsers: config.StaticUsersConfig{UsersFile: usersPath},
		JWT:         config.JWTConfig{Secret: "secret", Issuer: "gitstore", Duration: "1h"},
		AuthN:       config.AuthNConfig{Chain: []string{"static-users", "anonymous"}},
		AuthZ:       config.AuthZConfig{Provider: "rbac-local"},
		UserDir:     config.UserDirConfig{Provider: "static-users"},
		RBAC:        config.RBACConfig{PolicyFile: policyPath},
	}}
	store, err := memdb.New()
	require.NoError(t, err)
	revocations := store.(staticusers.RevocationStore)
	registry, reloader, shutdowns, err := buildProviderRegistry(cfg, zap.NewNop(), revocations)
	require.NoError(t, err)
	for _, shutdown := range shutdowns {
		t.Cleanup(shutdown.Shutdown)
	}

	writeUsers("bob")
	require.Error(t, reloader.Reload())
	assertLoginDecision(t, registry, "alice", auth.OutcomeAllow)
	assertLoginDecision(t, registry, "bob", auth.OutcomeDeny)

	writePolicy("bob")
	require.NoError(t, reloader.Reload())
	assertLoginDecision(t, registry, "alice", auth.OutcomeDeny)
	assertLoginDecision(t, registry, "bob", auth.OutcomeAllow)
}

func assertLoginDecision(t *testing.T, registry *auth.ProviderRegistry, username string, want auth.Outcome) {
	t.Helper()
	credentials := base64.StdEncoding.EncodeToString([]byte(username + ":secret"))
	_, decision, err := registry.AuthN().Authenticate(context.Background(), auth.AuthRequest{
		Header: http.Header{"Authorization": {"Basic " + credentials}},
	})
	require.NoError(t, err)
	assert.Equal(t, want, decision.Outcome)
}
