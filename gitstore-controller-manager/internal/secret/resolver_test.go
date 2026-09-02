// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secret

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestFileResolverResolve(t *testing.T) {
	resolver, err := NewBootstrapResolver(BootstrapProviderConfig{
		Type:     ProviderFile,
		BasePath: filepath.Join("testdata", "files"),
	}, nil)
	if err != nil {
		t.Fatalf("NewBootstrapResolver() error: %v", err)
	}

	value, err := resolver.Resolve(context.Background(), Ref{
		Kind: "SecretRef",
		Name: "controller-manager",
		Key:  "privateKey",
	})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got, want := string(value), "test-private-key\n"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestFileResolverErrorsAreClassifiedAndFailClosed(t *testing.T) {
	resolver, err := NewBootstrapResolver(BootstrapProviderConfig{
		Type:     ProviderFile,
		BasePath: filepath.Join("testdata", "files"),
	}, nil)
	if err != nil {
		t.Fatalf("NewBootstrapResolver() error: %v", err)
	}

	tests := []struct {
		name string
		ref  Ref
		want error
	}{
		{
			name: "missing secret",
			ref:  Ref{Kind: "SecretRef", Name: "missing", Key: "privateKey"},
			want: ErrNotFound,
		},
		{
			name: "missing key",
			ref:  Ref{Kind: "SecretRef", Name: "controller-manager", Key: "missing"},
			want: ErrMissingKey,
		},
		{
			name: "invalid kind",
			ref:  Ref{Kind: "CredentialsRef", Name: "controller-manager", Key: "privateKey"},
			want: ErrInvalidRef,
		},
		{
			name: "path traversal in name",
			ref:  Ref{Kind: "SecretRef", Name: "../controller-manager", Key: "privateKey"},
			want: ErrInvalidRef,
		},
		{
			name: "path traversal in key",
			ref:  Ref{Kind: "SecretRef", Name: "controller-manager", Key: "../privateKey"},
			want: ErrInvalidRef,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := resolver.Resolve(context.Background(), tt.ref)
			if err == nil {
				t.Fatal("Resolve() error = nil")
			}
			if value != nil {
				t.Errorf("Resolve() returned material on error: %q", value)
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("Resolve() error = %v, want errors.Is(..., %v)", err, tt.want)
			}
			var resolutionErr *ResolutionError
			if !errors.As(err, &resolutionErr) {
				t.Errorf("Resolve() error = %T, want *ResolutionError", err)
			}
		})
	}
}

func TestBootstrapResolverRejectsUnavailableFileProvider(t *testing.T) {
	_, err := NewBootstrapResolver(BootstrapProviderConfig{
		Type:     ProviderFile,
		BasePath: filepath.Join("testdata", "does-not-exist"),
	}, nil)
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("NewBootstrapResolver() error = %v, want ProviderUnavailable", err)
	}
}

func TestEnvironmentResolverResolve(t *testing.T) {
	ref := Ref{Kind: "SecretRef", Name: "controller-manager", Key: "privateKey"}
	config := BootstrapProviderConfig{Type: ProviderEnvironment, EnvPrefix: "TEST_SECRET__"}
	t.Setenv(EnvironmentVariableName(config.EnvPrefix, ref), "test-private-key")

	resolver, err := NewBootstrapResolver(config, nil)
	if err != nil {
		t.Fatalf("NewBootstrapResolver() error: %v", err)
	}
	value, err := resolver.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got, want := string(value), "test-private-key"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestEnvironmentResolverErrorsAreClassifiedAndDoNotLeakValue(t *testing.T) {
	config := BootstrapProviderConfig{Type: ProviderEnvironment, EnvPrefix: "TEST_SECRET__"}
	resolver, err := NewBootstrapResolver(config, nil)
	if err != nil {
		t.Fatalf("NewBootstrapResolver() error: %v", err)
	}

	missing := Ref{Kind: "SecretRef", Name: "missing", Key: "privateKey"}
	value, err := resolver.Resolve(context.Background(), missing)
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(missing) error = %v, want NotFound", err)
	}
	if value != nil {
		t.Errorf("Resolve(missing) returned material: %q", value)
	}

	empty := Ref{Kind: "SecretRef", Name: "controller-manager", Key: "emptyKey"}
	t.Setenv(EnvironmentVariableName(config.EnvPrefix, empty), "")
	value, err = resolver.Resolve(context.Background(), empty)
	if err == nil || !errors.Is(err, ErrMissingKey) {
		t.Fatalf("Resolve(empty) error = %v, want MissingKey", err)
	}
	if value != nil {
		t.Errorf("Resolve(empty) returned material: %q", value)
	}

	invalid := Ref{Kind: "SecretRef", Name: "controller-manager", Key: ""}
	value, err = resolver.Resolve(context.Background(), invalid)
	if err == nil || !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("Resolve(invalid) error = %v, want InvalidRef", err)
	}
	if value != nil {
		t.Errorf("Resolve(invalid) returned material: %q", value)
	}
}

func TestBootstrapResolverRejectsInvalidProviderConfiguration(t *testing.T) {
	tests := []BootstrapProviderConfig{
		{},
		{Type: "vault"},
		{Type: ProviderFile},
		{Type: ProviderEnvironment},
		{Type: ProviderEnvironment, EnvPrefix: "invalid-prefix"},
	}

	for _, config := range tests {
		t.Run(config.Type, func(t *testing.T) {
			if _, err := NewBootstrapResolver(config, nil); err == nil {
				t.Fatal("NewBootstrapResolver() error = nil")
			}
		})
	}
}

func TestResolveHonorsCanceledContext(t *testing.T) {
	resolver, err := NewBootstrapResolver(BootstrapProviderConfig{
		Type:     ProviderFile,
		BasePath: filepath.Join("testdata", "files"),
	}, nil)
	if err != nil {
		t.Fatalf("NewBootstrapResolver() error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	value, err := resolver.Resolve(ctx, Ref{
		Kind: "SecretRef",
		Name: "controller-manager",
		Key:  "privateKey",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve() error = %v, want context.Canceled", err)
	}
	if value != nil {
		t.Errorf("Resolve() returned material: %q", value)
	}
}

func TestResolveNeverLogsSecretMaterial(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	resolver, err := NewBootstrapResolver(BootstrapProviderConfig{
		Type:     ProviderFile,
		BasePath: filepath.Join("testdata", "files"),
	}, zap.New(core))
	if err != nil {
		t.Fatalf("NewBootstrapResolver() error: %v", err)
	}

	if _, err := resolver.Resolve(context.Background(), Ref{
		Kind: "SecretRef",
		Name: "controller-manager",
		Key:  "privateKey",
	}); err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, "test-private-key") {
			t.Fatalf("log message leaked secret material: %q", entry.Message)
		}
		for _, field := range entry.Context {
			if strings.Contains(field.String, "test-private-key") {
				t.Fatalf("log field leaked secret material: %+v", field)
			}
		}
	}
}

func TestEnvironmentVariableNameEscapesIdentifierSeparators(t *testing.T) {
	prefix := "TEST_SECRET__"
	withDash := EnvironmentVariableName(prefix, Ref{
		Kind: "SecretRef",
		Name: "a-b",
		Key:  "key",
	})
	withUnderscore := EnvironmentVariableName(prefix, Ref{
		Kind: "SecretRef",
		Name: "a_b",
		Key:  "key",
	})
	withDot := EnvironmentVariableName(prefix, Ref{
		Kind: "SecretRef",
		Name: "a.b",
		Key:  "key",
	})

	if withDash == withUnderscore || withDash == withDot || withUnderscore == withDot {
		t.Fatalf("environment names must not collide: dash=%q underscore=%q dot=%q",
			withDash, withUnderscore, withDot)
	}
}
