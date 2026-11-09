// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/crypto/bcrypt"
)

func TestProviderRegistryReloadPreservesUsableBindingInvariant(t *testing.T) {
	previous := otel.GetMeterProvider()
	_, shutdownTelemetry, err := observability.InitTelemetry(t.Context(), observability.OTLPConfig{}, observability.APIService, "test", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdownTelemetry(context.Background()); otel.SetMeterProvider(previous) })
	metrics := observabilitytest.HTTPGatherer(observability.MetricsHandler())
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
	initial, err := registry.AuthZ().Authorize(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"},
		"namespace.read", auth.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.Len(t, initial.PolicyDigest, 64)
	diagnostics := registry.Diagnostics()
	require.NotNil(t, diagnostics.Policy, "decision decorator must preserve snapshot diagnostics")
	require.Equal(t, initial.PolicyDigest, diagnostics.Policy.Digest)
	require.Equal(t, []string{"static-users", "anonymous"}, diagnostics.AuthN)
	require.Equal(t, "static-users", diagnostics.UserDir)
	require.Len(t, diagnostics.CatalogDigest, 64)
	peerStore, err := memdb.New()
	require.NoError(t, err)
	peer, peerReloader, peerShutdowns, err := buildProviderRegistry(cfg, peerStore, zap.NewNop(), peerStore.(staticusers.RevocationStore))
	require.NoError(t, err)
	for _, shutdown := range peerShutdowns {
		t.Cleanup(shutdown.Shutdown)
	}
	require.Equal(t, diagnostics, peer.Diagnostics())
	reloadCount := func(outcome string) float64 {
		families, err := metrics.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() != "gitstore_authz_registry_reloads_total" {
				continue
			}
			for _, metric := range family.Metric {
				require.Len(t, metric.Label, 1)
				require.Equal(t, "outcome", metric.Label[0].GetName())
				require.Contains(t, []string{"accepted", "rejected"}, metric.Label[0].GetValue())
				if metric.Label[0].GetValue() == outcome {
					return metric.GetCounter().GetValue()
				}
			}
		}
		return 0
	}
	acceptedBefore, rejectedBefore := reloadCount("accepted"), reloadCount("rejected")
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

	require.NoError(t, os.WriteFile(policyPath, []byte("apiVersion: secret-candidate-marker\nkind: RbacLocalConfiguration\n"), 0600))
	require.Error(t, reloader.Reload())
	assertLoginDecision(t, registry, "alice", auth.OutcomeAllow)
	writePolicy("bob")
	require.NoError(t, reloader.Reload())
	assertLoginDecision(t, registry, "alice", auth.OutcomeDeny)
	assertLoginDecision(t, registry, "bob", auth.OutcomeAllow)
	require.Equal(t, acceptedBefore+1, reloadCount("accepted"))
	require.Equal(t, rejectedBefore+4, reloadCount("rejected"))
	rejected := logs.FilterMessage("auth provider registry reload rejected").All()
	require.Len(t, rejected, 4)
	for _, entry := range rejected {
		fields := entry.ContextMap()
		require.Equal(t, initial.PolicyDigest, fields["policy_digest"])
		require.Equal(t, diagnostics.CatalogDigest, fields["catalog_digest"])
		require.Equal(t, "rbac-local", fields["authz_provider"])
		require.NotContains(t, fields, "error")
		require.NotContains(t, fmt.Sprint(fields), "secret-candidate-marker")
	}
	accepted := logs.FilterMessage("auth provider registry reload accepted").All()
	require.Len(t, accepted, 1)
	current := registry.Diagnostics()
	require.NotEqual(t, initial.PolicyDigest, current.Policy.Digest)
	require.Equal(t, current.Policy.Digest, accepted[0].ContextMap()["policy_digest"])
	require.Equal(t, diagnostics, peer.Diagnostics(), "a peer retains its independently loaded registry")
	assertLoginDecision(t, peer, "alice", auth.OutcomeAllow)
	assertLoginDecision(t, peer, "bob", auth.OutcomeDeny)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 3 {
				assert.NoError(t, reloader.Reload())
				assert.Equal(t, current, registry.Diagnostics())
			}
		})
	}
	wg.Wait()
	require.Equal(t, acceptedBefore+13, reloadCount("accepted"))
	for _, entry := range logs.FilterMessage("auth provider registry reload accepted").All() {
		require.Equal(t, current.Policy.Digest, entry.ContextMap()["policy_digest"])
		require.Equal(t, current.CatalogDigest, entry.ContextMap()["catalog_digest"])
	}
	reloader.Shutdown()
	require.Error(t, reloader.Reload(), "a closed registry must not publish a candidate")
	require.Equal(t, rejectedBefore+5, reloadCount("rejected"))
	require.Equal(t, current, registry.Diagnostics())
	lastRejected := logs.FilterMessage("auth provider registry reload rejected").All()
	require.Equal(t, current.Policy.Digest, lastRejected[len(lastRejected)-1].ContextMap()["policy_digest"])
	require.NoError(t, peerReloader.Reload())
	require.Equal(t, current, peer.Diagnostics())
	assertLoginDecision(t, peer, "alice", auth.OutcomeDeny)
	assertLoginDecision(t, peer, "bob", auth.OutcomeAllow)
	require.Equal(t, acceptedBefore+14, reloadCount("accepted"))
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
