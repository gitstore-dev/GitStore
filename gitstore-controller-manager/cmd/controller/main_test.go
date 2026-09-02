// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
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
