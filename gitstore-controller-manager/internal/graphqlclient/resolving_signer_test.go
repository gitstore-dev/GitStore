// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
	"github.com/gitstore-dev/gitstore/secretmaterial"
)

func TestResolvingSignerRotationOutageAndIndependentSources(t *testing.T) {
	publicKeys := map[string]ed25519.PublicKey{}
	privateKeys := map[string][]byte{}
	for _, id := range []string{"enrolled-old", "enrolled-new"} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		publicKeys[id] = public
		privateKeys[id] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		clear(private)
		clear(der)
	}
	defer func() {
		for _, key := range privateKeys {
			clear(key)
		}
	}()
	var exchanges atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assertion := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(assertion, ".")
		if len(parts) != 3 {
			http.Error(w, "invalid assertion", http.StatusUnauthorized)
			return
		}
		headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
		var header struct {
			KeyID string `json:"kid"`
		}
		if err != nil || json.Unmarshal(headerBytes, &header) != nil {
			http.Error(w, "invalid header", http.StatusUnauthorized)
			return
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		public := publicKeys[header.KeyID]
		if err != nil || len(public) != ed25519.PublicKeySize ||
			!ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), signature) {
			http.Error(w, "key pair mismatch", http.StatusUnauthorized)
			return
		}
		exchanges.Add(1)
		if err := json.NewEncoder(w).Encode(tokenExchangeResponse("token-" + header.KeyID)); err != nil {
			t.Error(err)
		}
	})
	roots := []string{t.TempDir(), t.TempDir()}
	writeRecord := func(root, keyID, materialID string) {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"format": "serviceaccount-signing-key/v1",
			"values": map[string]string{
				"privateKey": base64.StdEncoding.EncodeToString(privateKeys[materialID]),
				"keyID":      base64.StdEncoding.EncodeToString([]byte(keyID)),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		next := filepath.Join(root, "next.json")
		if err := os.WriteFile(next, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, filepath.Join(root, "controller.json")); err != nil {
			t.Fatal(err)
		}
		clear(data)
	}
	newSource := func(root string) *ServiceAccountSource {
		t.Helper()
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		resolver, err := secret.NewBootstrapResolver(secret.BootstrapProviderConfig{
			Type: "file", Format: "json-record", BasePath: root,
		}, "controller", secret.Ref{Kind: "SecretRef", Name: "controller"}, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := NewResolvingTokenSigner(context.Background(), resolver, "sa-uid")
		if err != nil {
			_ = resolver.Close()
			t.Fatal(err)
		}
		source := NewServiceAccountSource(server.URL, "controllers", "manager", signer, "assertion", "api", time.Minute, time.Hour)
		t.Cleanup(func() {
			if err := source.Close(); err != nil {
				t.Error(err)
			}
		})
		return source
	}
	expire := func(source *ServiceAccountSource) {
		source.mu.Lock()
		source.expiresAt = time.Now().Add(-time.Second)
		source.backoffUntil = time.Time{}
		source.mu.Unlock()
	}
	check := func(source *ServiceAccountSource, want string) {
		t.Helper()
		token, err := source.Current(context.Background())
		if err != nil || token != want {
			t.Fatalf("unexpected token outcome: %v", err)
		}
	}
	var sources []*ServiceAccountSource
	for _, root := range roots {
		writeRecord(root, "enrolled-old", "enrolled-old")
		source := newSource(root)
		check(source, "token-enrolled-old")
		sources = append(sources, source)
	}
	for i, root := range roots {
		writeRecord(root, "enrolled-new", "enrolled-new")
		check(sources[i], "token-enrolled-old")
	}
	if exchanges.Load() != 2 {
		t.Fatal("usable access tokens were not cached")
	}
	for _, source := range sources {
		expire(source)
		check(source, "token-enrolled-new")
	}
	if exchanges.Load() != 4 {
		t.Fatal("renewal did not sign with freshly resolved keys")
	}
	writeRecord(roots[0], "enrolled-new", "enrolled-old")
	expire(sources[0])
	if token, err := sources[0].Current(context.Background()); err == nil || token != "" {
		t.Fatal("mismatched enrolled key pair was accepted")
	}
	if err := os.Remove(filepath.Join(roots[0], "controller.json")); err != nil {
		t.Fatal(err)
	}
	expire(sources[0])
	if token, err := sources[0].Current(context.Background()); !errors.Is(err, secretmaterial.ErrNotFound) || token != "" || sources[0].Ready() {
		t.Fatal("outage used stale private material or retained expired readiness")
	}
	check(sources[1], "token-enrolled-new")
	writeRecord(roots[0], "enrolled-new", "enrolled-new")
	expire(sources[0])
	check(sources[0], "token-enrolled-new")
	check(newSource(roots[0]), "token-enrolled-new")
}
