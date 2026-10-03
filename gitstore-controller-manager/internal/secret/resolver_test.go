// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secret

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/secretmaterial"
)

func testResolver(t *testing.T, cfg BootstrapProviderConfig, ref Ref, keyID string) *BootstrapResolver {
	t.Helper()
	r, err := NewBootstrapResolver(cfg, "controller", ref, keyID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func TestFileResolverResolve(t *testing.T) {
	r := testResolver(t, BootstrapProviderConfig{Type: ProviderFile, BasePath: filepath.Join("testdata", "files")},
		Ref{Kind: "SecretRef", Name: "controller-manager", Key: "privateKey"}, "enrolled-id")
	value, id, err := r.SigningKey(context.Background())
	defer clear(value)
	if err != nil || string(value) != "test-private-key\n" || id != "enrolled-id" {
		t.Fatalf("raw signing material failed: %v", err)
	}
}

func TestBootstrapReadEnforcesSharedSizeLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "controller"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "controller", "privateKey"),
		[]byte(strings.Repeat("x", secretmaterial.MaxItemBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	r := testResolver(t, BootstrapProviderConfig{Type: ProviderFile, BasePath: root},
		Ref{Kind: "SecretRef", Name: "controller", Key: "privateKey"}, "enrolled-id")
	value, _, err := r.SigningKey(context.Background())
	if !errors.Is(err, secretmaterial.ErrValueTooLarge) || value != nil {
		t.Fatal("bootstrap accepted material outside the shared byte limit")
	}
}

func TestBootstrapFailuresUseSharedClasses(t *testing.T) {
	for _, tc := range []struct {
		ref  Ref
		want error
	}{
		{Ref{Kind: "SecretRef", Name: "missing", Key: "privateKey"}, secretmaterial.ErrNotFound},
		{Ref{Kind: "SecretRef", Name: "controller-manager", Key: "missing"}, secretmaterial.ErrMissingKey},
		{Ref{Kind: "CredentialsRef", Name: "controller-manager", Key: "privateKey"}, secretmaterial.ErrInvalidRef},
		{Ref{Kind: "SecretRef", Name: "../controller-manager", Key: "privateKey"}, secretmaterial.ErrInvalidRef},
		{Ref{Kind: "SecretRef", Name: "controller-manager", Key: "../privateKey"}, secretmaterial.ErrInvalidRef},
	} {
		r, err := NewBootstrapResolver(BootstrapProviderConfig{
			Type: ProviderFile, BasePath: filepath.Join("testdata", "files"),
		}, "controller", tc.ref, "enrolled-id", nil)
		if err == nil {
			value, _, resolveErr := r.SigningKey(context.Background())
			if value != nil {
				clear(value)
				t.Fatal("returned partial material on failure")
			}
			err = errors.Join(resolveErr, r.Close())
		}
		if !errors.Is(err, tc.want) {
			t.Fatalf("unexpected classification: %v", err)
		}
	}
}

func TestEnvironmentResolverAndCancellation(t *testing.T) {
	ref := Ref{Kind: "SecretRef", Name: "controller-manager", Key: "privateKey"}
	variable, err := secretmaterial.BootstrapEnvironmentVariable("TEST_SECRET__", ref.SecretRef())
	if err != nil {
		t.Fatal(err)
	}
	if variable != "TEST_SECRET__CONTROLLER_DASH_MANAGER__PRIVATEKEY" {
		t.Fatal("raw bootstrap environment mapping changed")
	}
	r := testResolver(t, BootstrapProviderConfig{Type: ProviderEnvironment, EnvPrefix: "TEST_SECRET__"}, ref, "enrolled-id")
	if _, _, err := r.SigningKey(context.Background()); !errors.Is(err, secretmaterial.ErrNotFound) {
		t.Fatalf("missing variable: %v", err)
	}
	t.Setenv(variable, "")
	if _, _, err := r.SigningKey(context.Background()); !errors.Is(err, secretmaterial.ErrMissingKey) {
		t.Fatalf("empty variable: %v", err)
	}
	t.Setenv(variable, "synthetic-private-material")
	value, _, err := r.SigningKey(context.Background())
	defer clear(value)
	if err != nil || string(value) != "synthetic-private-material" {
		t.Fatalf("environment resolution failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	value, _, err = r.SigningKey(ctx)
	if value != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not fail closed: %v", err)
	}
}

func TestBootstrapRejectsInvalidBindingsBeforeProviderAccess(t *testing.T) {
	valid := Ref{Kind: "SecretRef", Name: "controller", Key: "privateKey"}
	for _, tc := range []struct {
		cfg   BootstrapProviderConfig
		owner string
		ref   Ref
		keyID string
		want  error
	}{
		{BootstrapProviderConfig{Type: ProviderFile}, "", valid, "id", secretmaterial.ErrForbidden},
		{BootstrapProviderConfig{Type: ProviderFile}, "controller", valid, "", secretmaterial.ErrInvalidRef},
		{BootstrapProviderConfig{Type: ProviderFile, Format: "json-record"}, "controller", valid, "id", secretmaterial.ErrInvalidRef},
		{BootstrapProviderConfig{Type: "vault"}, "controller", valid, "id", secretmaterial.ErrUnsupportedType},
		{BootstrapProviderConfig{Type: ProviderEnvironment, EnvPrefix: "invalid-prefix"}, "controller", valid, "id", secretmaterial.ErrInvalidRef},
		{BootstrapProviderConfig{Type: ProviderFile, BasePath: filepath.Join(t.TempDir(), "missing")}, "controller", valid, "id", secretmaterial.ErrProviderUnavailable},
	} {
		if _, err := NewBootstrapResolver(tc.cfg, tc.owner, tc.ref, tc.keyID, nil); !errors.Is(err, tc.want) {
			t.Fatalf("invalid binding classification: %v", err)
		}
	}
}

func writeSigningRecord(t *testing.T, root, privateKey, keyID, format string) {
	t.Helper()
	values := map[string]string{}
	if privateKey != "" {
		values["privateKey"] = base64.StdEncoding.EncodeToString([]byte(privateKey))
	}
	if keyID != "" {
		values["keyID"] = base64.StdEncoding.EncodeToString([]byte(keyID))
	}
	data, err := json.Marshal(map[string]any{"format": format, "values": values})
	if err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(root, "next.json")
	if err := os.WriteFile(temp, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temp, filepath.Join(root, "controller.json")); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicSigningRecordRotationAndFailure(t *testing.T) {
	root := t.TempDir()
	r := testResolver(t, BootstrapProviderConfig{Type: ProviderFile, Format: "json-record", BasePath: root},
		Ref{Kind: "SecretRef", Name: "controller"}, "")
	for _, id := range []string{"arbitrary-enrolled-id", "next-enrolled-id", " opaque / enrolled: ID "} {
		writeSigningRecord(t, root, "private-"+id, id, "serviceaccount-signing-key/v1")
		key, gotID, err := r.SigningKey(context.Background())
		if err != nil || gotID != id || string(key) != "private-"+id {
			t.Fatalf("atomic revision not observed: %v", err)
		}
		clear(key)
	}
	for _, tc := range []struct {
		key, id, format string
		want            error
	}{
		{"private", "", "serviceaccount-signing-key/v1", secretmaterial.ErrMissingKey},
		{"", "id", "serviceaccount-signing-key/v1", secretmaterial.ErrMissingKey},
		{"private", "id", "secret-record/v1", secretmaterial.ErrUnsupportedType},
		{"private", " \t\n", "serviceaccount-signing-key/v1", secretmaterial.ErrInvalidRef},
	} {
		writeSigningRecord(t, root, tc.key, tc.id, tc.format)
		key, id, err := r.SigningKey(context.Background())
		if !errors.Is(err, tc.want) || key != nil || id != "" {
			t.Fatalf("invalid record did not fail closed: %v", err)
		}
	}
	writeSigningRecord(t, root, "private", "record-id", "serviceaccount-signing-key/v1")
	pinned := testResolver(t, BootstrapProviderConfig{Type: ProviderFile, Format: "json-record", BasePath: root},
		Ref{Kind: "SecretRef", Name: "controller"}, "different-id")
	if key, _, err := pinned.SigningKey(context.Background()); key != nil || !errors.Is(err, secretmaterial.ErrInvalidRef) {
		t.Fatal("configured and record key IDs were mixed")
	}
}
