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
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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
		require.NoError(t, os.WriteFile(policyPath, []byte("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  admin:\n    rules: [{resources: ['*'], verbs: ['*']}]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: admin}\n    subjects: [{kind: User, name: "+username+"}]\n"), 0600))
	}
	writeFullyDeniedPolicy := func(username string) {
		require.NoError(t, os.WriteFile(policyPath, []byte("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  admin: {rules: []}\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: admin}\n    subjects: [{kind: User, name: "+username+"}]\n"), 0600))
	}
	writeUsers("alice")
	writePolicy("alice")

	cfg := &config.Config{Api: config.ApiConfig{Auth: config.AuthConfig{
		StaticUsers: config.StaticUsersConfig{UsersFile: usersPath},
		JWT:         config.JWTConfig{Secret: "secret", Issuer: "gitstore", TTL: 1 * time.Hour},
		AuthN:       config.AuthNConfig{Chain: []string{"static-users", "anonymous"}},
		AuthZ:       config.AuthZConfig{Provider: "rbac-local"},
		UserDir:     config.UserDirConfig{Provider: "static-users"},
		RBACLocal:   config.RBACConfig{PolicyFile: policyPath},
	}}}
	store, err := memdb.New()
	require.NoError(t, err)
	revocations := store.(staticusers.RevocationStore)
	core, logs := observer.New(zapcore.InfoLevel)
	registry, reloader, shutdowns, err := buildProviderRegistry(cfg, store, zap.New(core), revocations)
	require.NoError(t, err)
	initial := registry.Diagnostics()
	reloadCount := func(outcome string) float64 {
		families, gatherErr := prometheus.DefaultGatherer.Gather()
		require.NoError(t, gatherErr)
		for _, family := range families {
			if family.GetName() == "gitstore_authz_registry_reloads_total" {
				for _, metric := range family.Metric {
					if metric.Label[0].GetValue() == outcome {
						return metric.Counter.GetValue()
					}
				}
			}
		}
		return 0
	}
	initialAccepted, initialRejected := reloadCount("accepted"), reloadCount("rejected")
	for _, shutdown := range shutdowns {
		t.Cleanup(shutdown.Shutdown)
	}
	// One bound administrator must not hide another active user with no grant.
	require.NoError(t, os.WriteFile(usersPath, []byte("version: v1\nusers:\n  - username: alice\n    password_hash: "+string(hash)+"\n  - username: bob\n    password_hash: "+string(hash)+"\n"), 0600))
	require.Error(t, reloader.Reload(), "all active local users need an effective grant")
	assertLoginDecision(t, registry, "alice", auth.OutcomeAllow)
	assertLoginDecision(t, registry, "bob", auth.OutcomeDeny)

	writeUsers("bob")
	require.Error(t, reloader.Reload())
	assertLoginDecision(t, registry, "alice", auth.OutcomeAllow)
	assertLoginDecision(t, registry, "bob", auth.OutcomeDeny)

	writeFullyDeniedPolicy("bob")
	require.Error(t, reloader.Reload())
	assertLoginDecision(t, registry, "alice", auth.OutcomeAllow)
	assertLoginDecision(t, registry, "bob", auth.OutcomeDeny)

	writePolicy("bob")
	require.NoError(t, reloader.Reload())
	assertLoginDecision(t, registry, "alice", auth.OutcomeDeny)
	assertLoginDecision(t, registry, "bob", auth.OutcomeAllow)
	require.Equal(t, initialAccepted+1, reloadCount("accepted"))
	require.Equal(t, initialRejected+3, reloadCount("rejected"))
	rejected := logs.FilterMessage("auth provider registry reload rejected").All()
	require.Len(t, rejected, 3)
	for _, entry := range rejected {
		require.Equal(t, initial.Policy.Digest, entry.ContextMap()["policy_digest"])
		require.Equal(t, initial.CatalogDigest, entry.ContextMap()["catalog_digest"])
		require.NotContains(t, entry.ContextMap(), "error", "raw candidate content must not be logged")
	}
	accepted := logs.FilterMessage("auth provider registry reload accepted").All()
	require.Len(t, accepted, 1)
	require.Equal(t, registry.Diagnostics().Policy.Digest, accepted[0].ContextMap()["policy_digest"])
	require.NotEqual(t, initial.Policy.Digest, accepted[0].ContextMap()["policy_digest"])
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
