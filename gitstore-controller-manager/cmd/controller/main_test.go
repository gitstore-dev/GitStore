// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	repositorycontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/repository"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/secretmaterial"
	"go.uber.org/zap"
)

func TestParseConfigFile(t *testing.T) {
	paths, err := parseConfigFiles([]string{"--config-file", "/config/shared.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/config/shared.toml" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestParseConfigFilesRepeated(t *testing.T) {
	paths, err := parseConfigFiles([]string{
		"--config-file", "/config/shared.toml",
		"--config-file", "/config/overlay.toml",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/config/shared.toml", "/config/overlay.toml"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths = %v", paths)
	}
}

func TestBuildCredentialSourceUsesResolvedServiceAccountKey(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	ref := secret.Ref{Kind: "SecretRef", Name: "controller-manager", Key: "privateKey"}
	provider := secret.BootstrapProviderConfig{Type: secret.ProviderEnvironment, EnvPrefix: "TEST_SECRET__"}
	variable, err := secretmaterial.BootstrapEnvironmentVariable(provider.EnvPrefix, ref.SecretRef())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(variable, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))

	source, err := buildCredentialSource(context.Background(), &config.Config{
		Controller: config.ControllerConfig{
			ApiURI: "http://api.example.test/graphql",
			ServiceAccount: config.ServiceAccountConfig{
				Namespace: "controllers",
				Name:      "gitstore-controller-manager",
				KeyID:     "key-1",
				UID:       "sa-uid-1",
				KeyRef:    ref,
			},
			SecretProviders: config.SecretProvidersConfig{Bootstrap: provider},
		},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("buildCredentialSource() error: %v", err)
	}
	if _, ok := source.(*graphqlclient.ServiceAccountSource); !ok {
		t.Errorf("source = %T, want *graphqlclient.ServiceAccountSource", source)
	}
	if readiness := credentialReadiness(source); readiness == nil || readiness.Ready() {
		t.Error("dynamic source must begin not ready before acquiring a token")
	}
}

func TestBuildCredentialSourceRejectsMissingCredentialConfiguration(t *testing.T) {
	if _, err := buildCredentialSource(context.Background(), &config.Config{}, zap.NewNop()); err == nil {
		t.Fatal("buildCredentialSource() error = nil")
	}
}

func TestBuildCredentialSourceFailsClosedOnInvalidRecords(t *testing.T) {
	record := func(format string, values map[string]string) []byte {
		t.Helper()
		encoded := make(map[string]string, len(values))
		for name, value := range values {
			encoded[name] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		data, err := json.Marshal(map[string]any{"format": format, "values": encoded})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"absent", nil, secretmaterial.ErrNotFound},
		{"missing-id", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": "MUST-NOT-LEAK"}), secretmaterial.ErrMissingKey},
		{"malformed-key", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": "MUST-NOT-LEAK", "keyID": "id"}), secretmaterial.ErrInvalidRef},
		{"oversized-key", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": strings.Repeat("x", secretmaterial.MaxItemBytes+1), "keyID": "id"}), secretmaterial.ErrValueTooLarge},
		{"runtime-record", record("secret-record/v1", map[string]string{"privateKey": "MUST-NOT-LEAK", "keyID": "id"}), secretmaterial.ErrUnsupportedType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.data != nil {
				if err := os.WriteFile(filepath.Join(root, "controller.json"), tc.data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{Controller: config.ControllerConfig{
				ApiURI: "http://must-not-call.invalid",
				ServiceAccount: config.ServiceAccountConfig{
					Namespace: "controllers", Name: "manager", UID: "uid",
					KeyRef: secret.Ref{Kind: "SecretRef", Name: "controller"},
				},
				SecretProviders: config.SecretProvidersConfig{Bootstrap: secret.BootstrapProviderConfig{
					Type: "file", Format: "json-record", BasePath: root,
				}},
			}}
			source, err := buildCredentialSource(context.Background(), cfg, zap.NewNop())
			if source != nil || !errors.Is(err, tc.want) {
				t.Fatalf("invalid bootstrap produced a source or wrong classification: %v", err)
			}
			if strings.Contains(err.Error(), "MUST-NOT-LEAK") {
				t.Fatal("bootstrap diagnostic leaked private material")
			}
		})
	}
}

type repositoryRegistrationWatch struct {
	events chan listwatch.WatchEvent[repositorycontroller.Repository]
	once   sync.Once
}

func (w *repositoryRegistrationWatch) Events() <-chan listwatch.WatchEvent[repositorycontroller.Repository] {
	return w.events
}
func (w *repositoryRegistrationWatch) Err() error { return nil }
func (w *repositoryRegistrationWatch) Stop() {
	w.once.Do(func() { close(w.events) })
}

type repositoryRegistrationListWatcher struct {
	item repositorycontroller.Repository
}

func (w *repositoryRegistrationListWatcher) List(context.Context) (listwatch.ListResponse[repositorycontroller.Repository], error) {
	return listwatch.ListResponse[repositorycontroller.Repository]{
		Items:           []repositorycontroller.Repository{w.item},
		ResourceVersion: "rwv1:test:1",
	}, nil
}

func (w *repositoryRegistrationListWatcher) Watch(ctx context.Context, _ string) (listwatch.Watcher[repositorycontroller.Repository], error) {
	watch := &repositoryRegistrationWatch{events: make(chan listwatch.WatchEvent[repositorycontroller.Repository])}
	go func() {
		<-ctx.Done()
		watch.Stop()
	}()
	return watch, nil
}

// TestRegisterRepositoryAcrossTwoControllerManagers exercises the production
// registration boundary twice: two independent managers, runners, caches,
// queues, and checkpoint stores consume the same Repository snapshot and call
// the shared idempotent API provisioning/status contracts. This is deliberately
// stronger than calling GraphQLStorageClient twice in isolation.
func TestRegisterRepositoryAcrossTwoControllerManagers(t *testing.T) {
	var provisionCalls atomic.Int64
	var statusCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer controller-token" {
			http.Error(w, "missing controller authorization", http.StatusUnauthorized)
			return
		}
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(request.Query, "provisionRepositoryStorage"):
			provisionCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"provisionRepositoryStorage":{"repository":{"metadata":{"namespace":"acme","name":"catalog"}}}}}`))
		case strings.Contains(request.Query, "updateRepositoryStatus"):
			statusCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"updateRepositoryStatus":{"repository":{"metadata":{"resourceVersion":"2"}}}}}`))
		default:
			http.Error(w, "unexpected GraphQL operation", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	item := repositorycontroller.Repository{
		UID: "repository-1", Namespace: "acme", Name: "catalog", StorageClass: "standard",
		Generation: 1, ResourceVersion: "1",
		Status: status.ResourceStatus{Conditions: []*status.Condition{{
			Type: "AdmissionAccepted", Status: "TRUE", ObservedGeneration: 1,
		}}},
	}
	cfg := &config.Config{Controller: config.ControllerConfig{
		Reconcile:  config.ReconcileConfig{MaxAttempts: 1, StallThreshold: time.Minute},
		Checkpoint: config.CheckpointConfig{FlushIntervalEvents: 1},
		Watch:      config.WatchConfig{MaxBackoff: time.Millisecond},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	// runners is waited on before the test returns so each Runner's final
	// checkpoint flush finishes before t.TempDir cleanup removes its directory.
	var runners sync.WaitGroup
	var checkpointRoots []string
	for replica := 0; replica < 2; replica++ {
		mgr := manager.New().WithLogger(zap.NewNop())
		root := t.TempDir()
		checkpointRoots = append(checkpointRoots, root)
		store, err := checkpoint.NewFilesystemStore(root)
		if err != nil {
			t.Fatalf("replica %d checkpoint store: %v", replica, err)
		}
		client := graphqlclient.New(server.URL, graphqlclient.NewStaticToken("controller-token"))
		if _, err := registerRepository(
			ctx, &runners, mgr, store, cfg, zap.NewNop(), client,
			&repositoryRegistrationListWatcher{item: item},
			repositorycontroller.NewGraphQLStorageClient(client),
		); err != nil {
			t.Fatalf("replica %d register Repository: %v", replica, err)
		}
		if stat := mgr.KindStats()["Repository"]; !stat.Registered {
			t.Fatalf("replica %d Repository registration missing", replica)
		}
		go func() { done <- mgr.Start(ctx) }()
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (provisionCalls.Load() < 2 || statusCalls.Load() < 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := provisionCalls.Load(); got != 2 {
		t.Fatalf("provision calls = %d, want one from each registered controller manager", got)
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("status calls = %d, want one from each registered controller manager", got)
	}

	cancel()
	for replica := 0; replica < 2; replica++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("replica %d manager shutdown: %v", replica, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("replica %d manager did not stop", replica)
		}
	}
	runners.Wait()
	for _, root := range checkpointRoots {
		replacement, err := checkpoint.NewFilesystemStore(root)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := replacement.Load(context.Background(), "Repository")
		if err != nil {
			t.Fatalf("final checkpoint not available to replacement: %v", err)
		}
		var restored []repositorycontroller.Repository
		if err := json.Unmarshal(rec.Snapshot, &restored); err != nil {
			t.Fatal(err)
		}
		if rec.ResourceVersion != "rwv1:test:1" || len(restored) != 1 || restored[0].UID != item.UID {
			t.Fatal("shutdown flush did not retain the replacement snapshot and cursor")
		}
	}
}
