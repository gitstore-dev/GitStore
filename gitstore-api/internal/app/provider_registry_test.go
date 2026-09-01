// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// stubServiceAccountLookup is a no-op serviceAccountLookup test double —
// buildProviderRegistry only needs it to satisfy the constructor signature;
// none of these tests exercise actual lookups.
type stubServiceAccountLookup struct{}

func (stubServiceAccountLookup) GetServiceAccountBySubject(_ context.Context, _, _ string) (*datastore.ServiceAccount, error) {
	return nil, datastore.ErrNotFound
}

func TestBuildProviderRegistry_DefaultChain_Unchanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.AuthZ.Provider = "allow-all"
	cfg.Auth.UserDir.Provider = ""
	hash, err := bcrypt.GenerateFromPassword([]byte("irrelevant"), bcrypt.MinCost)
	require.NoError(t, err)
	usersFile := filepath.Join(t.TempDir(), "users.yaml")
	require.NoError(t, os.WriteFile(usersFile, []byte("version: v1\nusers:\n  - username: admin\n    password_hash: "+string(hash)+"\n"), 0600))
	cfg.Auth.StaticUsers.UsersFile = usersFile
	cfg.Auth.JWT.Secret = "test-jwt-secret-at-least-32-bytes-long!!"
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
	cfg.Auth.AuthN.Chain = []string{"serviceaccount-assertion", "serviceaccount-jwt", "anonymous"}
	cfg.Auth.AuthZ.Provider = "allow-all"
	cfg.Auth.ServiceAccount.SigningKey = generateEd25519PEMForServerTest(t)

	registry, _, shutdowns, err := buildProviderRegistry(cfg, stubServiceAccountLookup{}, zap.NewNop(), nil)
	require.NoError(t, err)
	require.NotNil(t, registry)
	// The registry runtime owns and shuts down both serviceaccount providers'
	// background replay/revocation pruners as one server-level shutdown entry.
	assert.Len(t, shutdowns, 1)
	for _, s := range shutdowns {
		s.Shutdown()
	}
}

func TestBuildProviderRegistry_UnknownProvider_Errors(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.AuthN.Chain = []string{"not-a-real-provider"}

	_, _, _, err := buildProviderRegistry(cfg, stubServiceAccountLookup{}, zap.NewNop(), nil)
	require.Error(t, err)
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
