// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/kratos"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// stubServiceAccountLookup is a no-op serviceAccountStore test double —
// buildProviderRegistry only needs it to satisfy the constructor signature;
// none of these tests exercise actual lookups.
type stubServiceAccountLookup struct{}

func (stubServiceAccountLookup) GetServiceAccountBySubject(_ context.Context, _ string) (*datastore.ServiceAccount, error) {
	return nil, datastore.ErrNotFound
}

func (stubServiceAccountLookup) TryConsumeServiceAccountAssertion(_ context.Context, _ string, _ time.Time) (bool, error) {
	return true, nil
}

func TestWorkloadConsumerReloadStartsFreshAndCancelsOldProvider(t *testing.T) {
	created := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var record struct {
			Traits struct {
				SourceUID string `json:"source_uid"`
			} `json:"traits"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&record))
		created <- record.Traits.SourceUID
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	cfg := &config.Config{}
	cfg.Api.Auth.AuthN.Chain = []string{"anonymous"}
	cfg.Api.Auth.AuthZ.Provider = "allow-all"
	cfg.Api.Auth.UserDir.Provider = "kratos"
	cfg.Api.Auth.Kratos = config.KratosUserDirConfig{
		AdminURI: server.URL, Timeout: time.Second, MaxConcurrent: 2, MaxResponseBytes: 1 << 20,
		CacheEntries: 16, CacheTTL: time.Minute,
		Mappings: []config.SubjectMapping{{Provider: "oidc-jwt", Issuer: "https://issuer.example", Mode: "uid"}},
	}
	create := func(name string) string {
		t.Helper()
		uid := uuid.NewString()
		require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: uid, Name: name,
			ResourceVersion: "1", Generation: 1, CreationTimestamp: time.Now().UTC()}))
		return uid
	}
	create("historical")
	registry, reloader, shutdowns, err := buildProviderRegistry(cfg, store, zap.NewNop(), store.(staticusers.RevocationStore), journal)
	require.NoError(t, err)
	for _, shutdown := range shutdowns {
		t.Cleanup(shutdown.Shutdown)
	}
	var invalidations atomic.Int32
	reloader.(*providerRegistryRuntime).setInvalidationHandler(func() { invalidations.Add(1) })
	first := registry.UserDir().(*kratos.Provider)
	receive := func(uid string) {
		t.Helper()
		select {
		case observed := <-created:
			require.Equal(t, uid, observed, "only new live events may be consumed")
		case <-time.After(3 * time.Second):
			t.Fatal("live workload event was not delivered")
		}
	}
	receive(create("first-live"))
	require.Eventually(t, func() bool { return invalidations.Load() == 1 }, time.Second, 10*time.Millisecond)
	cfg.Api.Auth.UserDir.Provider = "none"
	require.NoError(t, reloader.Reload())
	require.Error(t, first.StartServiceAccountConsumer(journal, store), "reload must shut down the previous consumer")
	create("while-disabled")
	cfg.Api.Auth.UserDir.Provider = "kratos"
	require.NoError(t, reloader.Reload())
	second := registry.UserDir().(*kratos.Provider)
	require.NotSame(t, first, second)
	receive(create("after-reload"))
	require.Eventually(t, func() bool { return invalidations.Load() == 2 }, time.Second, 10*time.Millisecond)
	shutdowns[0].Shutdown()
	require.Error(t, second.StartServiceAccountConsumer(journal, store), "server shutdown must stop the replacement consumer")
}

func TestBuildProviderRegistry_DefaultChain_Unchanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.Api.Auth.AuthZ.Provider = "allow-all"
	cfg.Api.Auth.UserDir.Provider = ""
	hash, err := bcrypt.GenerateFromPassword([]byte("irrelevant"), bcrypt.MinCost)
	require.NoError(t, err)
	usersFile := filepath.Join(t.TempDir(), "users.yaml")
	require.NoError(t, os.WriteFile(usersFile, []byte("version: v1\nusers:\n  - username: admin\n    password_hash: "+string(hash)+"\n"), 0600))
	cfg.Api.Auth.StaticUsers.UsersFile = usersFile
	cfg.Api.Auth.JWT.Secret = "test-jwt-secret-at-least-32-bytes-long!!"
	store, err := memdb.New()
	require.NoError(t, err)
	revocations := store.(staticusers.RevocationStore)

	registry, _, shutdowns, err := buildProviderRegistry(cfg, store, zap.NewNop(), revocations)
	require.NoError(t, err)
	require.NotNil(t, registry)
	// Default chain is ["static-users", "anonymous"] — static-users owns a
	// background goroutine (session blacklist pruning) and so appears in
	// shutdowns; anonymous does not.
	assert.Len(t, shutdowns, 1)
}

func TestBuildProviderRegistry_ServiceAccountProvidersChainedIn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Api.Auth.AuthN.Chain = []string{"serviceaccount-assertion", "serviceaccount-jwt", "anonymous"}
	cfg.Api.Auth.AuthZ.Provider = "allow-all"
	cfg.Api.Auth.ServiceAccount.SigningKey = generateEd25519PEMForServerTest(t)
	cfg.Api.Auth.ServiceAccount.DefaultTTL = 10 * time.Minute
	cfg.Api.Auth.ServiceAccount.MaxTTL = time.Hour

	registry, _, shutdowns, err := buildProviderRegistry(cfg, stubServiceAccountLookup{}, zap.NewNop(), nil)
	require.NoError(t, err)
	require.NotNil(t, registry)
	// The registry runtime owns the serviceaccount-jwt revocation-pruner
	// shutdown; assertion replay protection is datastore-owned.
	assert.Len(t, shutdowns, 1)
	for _, s := range shutdowns {
		s.Shutdown()
	}
}

func TestBuildProviderRegistry_UnknownProvider_Errors(t *testing.T) {
	cfg := &config.Config{}
	cfg.Api.Auth.AuthN.Chain = []string{"not-a-real-provider"}

	_, _, _, err := buildProviderRegistry(cfg, stubServiceAccountLookup{}, zap.NewNop(), nil)
	require.Error(t, err)
}

func TestStaticDirectoryDoesNotRequireStaticAuthenticationOrSigningKey(t *testing.T) {
	cfg := &config.Config{}
	cfg.Api.Auth.AuthN.Chain = []string{"anonymous"}
	cfg.Api.Auth.AuthZ.Provider = "allow-all"
	cfg.Api.Auth.UserDir.Provider = "static-users"
	hash, err := bcrypt.GenerateFromPassword([]byte("irrelevant"), bcrypt.MinCost)
	require.NoError(t, err)
	cfg.Api.Auth.StaticUsers.UsersFile = filepath.Join(t.TempDir(), "users.yaml")
	require.NoError(t, os.WriteFile(cfg.Api.Auth.StaticUsers.UsersFile,
		[]byte("version: v1\nusers:\n  - username: alice\n    password_hash: "+string(hash)+"\n"), 0600))
	registry, _, shutdowns, err := buildProviderRegistry(cfg, stubServiceAccountLookup{}, zap.NewNop(), nil)
	require.NoError(t, err)
	for _, provider := range shutdowns {
		t.Cleanup(provider.Shutdown)
	}
	profile, err := registry.UserDir().GetBySubject(t.Context(), "alice")
	require.NoError(t, err)
	require.Equal(t, "alice", profile.Username)
	directory := registry.UserDir().(auth.AuthNProvider)
	_, decision, err := directory.Authenticate(t.Context(), auth.AuthRequest{})
	require.ErrorIs(t, err, auth.ErrNotSupported)
	require.Equal(t, auth.OutcomeDeny, decision.Outcome)
	token, _, err := directory.IssueSession(t.Context(), "alice")
	require.ErrorIs(t, err, auth.ErrNotSupported)
	require.Empty(t, token)
}

// generateEd25519PEMForServerTest returns a fresh Ed25519 private key
// PEM-encoded as PKCS#8, satisfying auth.serviceaccount.signing_key's
// required-when-chained-in validation for this package's tests.
func generateEd25519PEMForServerTest(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}
