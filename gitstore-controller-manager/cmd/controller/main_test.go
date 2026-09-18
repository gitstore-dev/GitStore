// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
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
	"go.uber.org/zap"
)

func TestParseConfigFile(t *testing.T) {
	path, err := parseConfigFile([]string{"--config-file", "/config/shared.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/config/shared.toml" {
		t.Fatalf("path = %q", path)
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
	t.Setenv(secret.EnvironmentVariableName(provider.EnvPrefix, ref), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))

	source, err := buildCredentialSource(context.Background(), &config.Config{
		Controller: config.ControllerConfig{
			ApiURI:                  "http://api.example.test/graphql",
			ServiceAccountNamespace: "controllers",
			ServiceAccountName:      "gitstore-controller-manager",
			ServiceAccountKeyID:     "key-1",
			ServiceAccountUID:       "sa-uid-1",
			ServiceAccountKeyRef:    ref,
			SecretProviderBootstrap: provider,
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
		DefaultMaxAttempts:            1,
		DefaultStallThreshold:         time.Minute,
		CheckpointFlushIntervalEvents: 1,
		MaxWatchBackoff:               time.Millisecond,
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for replica := 0; replica < 2; replica++ {
		mgr := manager.New().WithLogger(zap.NewNop())
		store, err := checkpoint.NewFilesystemStore(t.TempDir())
		if err != nil {
			t.Fatalf("replica %d checkpoint store: %v", replica, err)
		}
		client := graphqlclient.New(server.URL, graphqlclient.NewStaticToken("controller-token"))
		if _, err := registerRepository(
			ctx, mgr, store, cfg, zap.NewNop(), client,
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
}
