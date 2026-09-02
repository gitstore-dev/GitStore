// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
)

func TestLoadFrom_ExplicitSharedFileAndEnvPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.toml")
	content := `[controller]
port = 6111
api_uri = "http://api:4000/graphql"
serviceaccount_namespace = "controllers"
serviceaccount_name = "gitstore-controller-manager"
serviceaccount_key_id = "key-1"
serviceaccount_uid = "sa-uid-1"
serviceaccount_key_ref = { kind = "SecretRef", name = "controller-manager", key = "privateKey" }
serviceaccount_assertion_audience = "controller-token-exchange"
serviceaccount_access_token_audience = "controller-api"
[api]
port = 4000
[grpc]
port = 50051
[log]
level = "info"
format = "json"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITSTORE_CONTROLLER__PORT", "6222")
	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error: %v", err)
	}
	if cfg.Controller.Port != 6222 {
		t.Fatalf("Port = %d, want 6222", cfg.Controller.Port)
	}
	if cfg.Controller.ServiceAccountAssertionAudience != "controller-token-exchange" {
		t.Fatalf("ServiceAccountAssertionAudience = %q", cfg.Controller.ServiceAccountAssertionAudience)
	}
	if cfg.Controller.ServiceAccountAccessTokenAudience != "controller-api" {
		t.Fatalf("ServiceAccountAccessTokenAudience = %q", cfg.Controller.ServiceAccountAccessTokenAudience)
	}
}

func TestLoadFrom_MissingExplicitFileFails(t *testing.T) {
	if _, err := config.LoadFrom(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("expected missing explicit file error")
	}
}

// setenv sets environment variables for a test and clears them on cleanup.
func setenv(t *testing.T, pairs ...string) {
	t.Helper()
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__NAMESPACE", "controllers")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__NAME", "gitstore-controller-manager")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_ID", "key-1")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__UID", "sa-uid-1")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__KIND", "SecretRef")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__NAME", "controller-manager")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__KEY", "privateKey")
	for i := 0; i+1 < len(pairs); i += 2 {
		t.Setenv(pairs[i], pairs[i+1])
	}
}

func TestLoad_Defaults(t *testing.T) {
	setenv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Controller.Port != 5001 {
		t.Errorf("Port = %d, want 5001", cfg.Controller.Port)
	}
	if cfg.Controller.ApiURI != "http://localhost:4000/graphql" {
		t.Errorf("ApiURI = %q, want http://localhost:4000/graphql", cfg.Controller.ApiURI)
	}
	if cfg.Controller.DefaultMaxAttempts != 5 {
		t.Errorf("DefaultMaxAttempts = %d, want 5", cfg.Controller.DefaultMaxAttempts)
	}
	if cfg.Controller.DefaultStallThreshold != 5*time.Minute {
		t.Errorf("DefaultStallThreshold = %v, want 5m", cfg.Controller.DefaultStallThreshold)
	}
	if cfg.Controller.CheckpointDir != ".gitstore/checkpoints" {
		t.Errorf("CheckpointDir = %q, want .gitstore/checkpoints", cfg.Controller.CheckpointDir)
	}
	if cfg.Controller.CheckpointFlushIntervalEvents != 100 {
		t.Errorf("CheckpointFlushIntervalEvents = %d, want 100", cfg.Controller.CheckpointFlushIntervalEvents)
	}
	if cfg.Controller.MaxWatchBackoff != 30*time.Second {
		t.Errorf("MaxWatchBackoff = %v, want 30s", cfg.Controller.MaxWatchBackoff)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want info", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("Log.Format = %q, want json", cfg.Log.Format)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	setenv(t,
		"GITSTORE_CONTROLLER__PORT", "8080",
		"GITSTORE_CONTROLLER__API_URI", "http://api.example.com/graphql",
		"GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS", "10",
		"GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD", "2m",
		"GITSTORE_CONTROLLER__CHECKPOINT_DIR", "/tmp/checkpoints",
		"GITSTORE_CONTROLLER__CHECKPOINT_FLUSH_INTERVAL_EVENTS", "50",
		"GITSTORE_CONTROLLER__MAX_WATCH_BACKOFF", "1m",
		"GITSTORE_CONTROLLER__SERVICEACCOUNT__ASSERTION_AUDIENCE", "controller-token-exchange",
		"GITSTORE_CONTROLLER__SERVICEACCOUNT__ACCESS_TOKEN_AUDIENCE", "controller-api",
		"GITSTORE_LOG__LEVEL", "debug",
		"GITSTORE_LOG__FORMAT", "text",
	)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Controller.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Controller.Port)
	}
	if cfg.Controller.ApiURI != "http://api.example.com/graphql" {
		t.Errorf("ApiURI = %q", cfg.Controller.ApiURI)
	}
	if cfg.Controller.DefaultMaxAttempts != 10 {
		t.Errorf("DefaultMaxAttempts = %d, want 10", cfg.Controller.DefaultMaxAttempts)
	}
	if cfg.Controller.DefaultStallThreshold != 2*time.Minute {
		t.Errorf("DefaultStallThreshold = %v, want 2m", cfg.Controller.DefaultStallThreshold)
	}
	if cfg.Controller.CheckpointDir != "/tmp/checkpoints" {
		t.Errorf("CheckpointDir = %q, want /tmp/checkpoints", cfg.Controller.CheckpointDir)
	}
	if cfg.Controller.CheckpointFlushIntervalEvents != 50 {
		t.Errorf("CheckpointFlushIntervalEvents = %d, want 50", cfg.Controller.CheckpointFlushIntervalEvents)
	}
	if cfg.Controller.MaxWatchBackoff != time.Minute {
		t.Errorf("MaxWatchBackoff = %v, want 1m", cfg.Controller.MaxWatchBackoff)
	}
	if cfg.Controller.ServiceAccountAssertionAudience != "controller-token-exchange" {
		t.Errorf("ServiceAccountAssertionAudience = %q", cfg.Controller.ServiceAccountAssertionAudience)
	}
	if cfg.Controller.ServiceAccountAccessTokenAudience != "controller-api" {
		t.Errorf("ServiceAccountAccessTokenAudience = %q", cfg.Controller.ServiceAccountAccessTokenAudience)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("Log.Format = %q, want text", cfg.Log.Format)
	}
}

func TestLoad_StallThresholdParsed(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"30s", 30 * time.Second},
		{"1h", time.Hour},
		{"10m", 10 * time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			setenv(t, "GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD", tc.input)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.Controller.DefaultStallThreshold != tc.want {
				t.Errorf("DefaultStallThreshold = %v, want %v", cfg.Controller.DefaultStallThreshold, tc.want)
			}
		})
	}
}

func TestLoad_LogFormatNormalized(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"JSON", "json"},
		{"Text", "text"},
		{"json", "json"},
		{"text", "text"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			setenv(t, "GITSTORE_LOG__FORMAT", tc.input)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.Log.Format != tc.want {
				t.Errorf("Log.Format = %q, want %q", cfg.Log.Format, tc.want)
			}
		})
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		envKey  string
		envVal  string
		wantErr string
	}{
		{
			name:    "port zero",
			envKey:  "GITSTORE_CONTROLLER__PORT",
			envVal:  "0",
			wantErr: "controller.port",
		},
		{
			name:    "port too large",
			envKey:  "GITSTORE_CONTROLLER__PORT",
			envVal:  "99999",
			wantErr: "controller.port",
		},
		{
			name:    "max attempts zero",
			envKey:  "GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS",
			envVal:  "0",
			wantErr: "controller.default_max_attempts",
		},
		{
			name:    "invalid stall threshold",
			envKey:  "GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD",
			envVal:  "not-a-duration",
			wantErr: "controller.default_stall_threshold",
		},
		{
			name:    "checkpoint flush interval zero",
			envKey:  "GITSTORE_CONTROLLER__CHECKPOINT_FLUSH_INTERVAL_EVENTS",
			envVal:  "0",
			wantErr: "controller.checkpoint_flush_interval_events",
		},
		{
			name:    "invalid max watch backoff",
			envKey:  "GITSTORE_CONTROLLER__MAX_WATCH_BACKOFF",
			envVal:  "not-a-duration",
			wantErr: "controller.max_watch_backoff",
		},
		{
			name:    "invalid log format",
			envKey:  "GITSTORE_LOG__FORMAT",
			envVal:  "xml",
			wantErr: "invalid log format",
		},
		{
			name:    "invalid log level",
			envKey:  "GITSTORE_LOG__LEVEL",
			envVal:  "verbose",
			wantErr: "invalid log level",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, tc.envKey, tc.envVal)
			_, err := config.Load()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestLoad_ServiceAccountCredentialMode(t *testing.T) {
	setenv(t)
	t.Setenv("GITSTORE_CONTROLLER__SECRET_PROVIDER_BOOTSTRAP__TYPE", "env")
	t.Setenv("GITSTORE_CONTROLLER__SECRET_PROVIDER_BOOTSTRAP__ENV_PREFIX", "TEST_SECRET__")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Controller.ServiceAccountKeyRef.Name != "controller-manager" {
		t.Errorf("ServiceAccountKeyRef.Name = %q, want controller-manager", cfg.Controller.ServiceAccountKeyRef.Name)
	}
	if cfg.Controller.SecretProviderBootstrap.Type != "env" {
		t.Errorf("SecretProviderBootstrap.Type = %q, want env", cfg.Controller.SecretProviderBootstrap.Type)
	}
	if cfg.Controller.ServiceAccountAssertionAudience != "gitstore-api/serviceaccount-token" {
		t.Errorf("ServiceAccountAssertionAudience = %q", cfg.Controller.ServiceAccountAssertionAudience)
	}
	if cfg.Controller.ServiceAccountAccessTokenAudience != "gitstore-api" {
		t.Errorf("ServiceAccountAccessTokenAudience = %q", cfg.Controller.ServiceAccountAccessTokenAudience)
	}
}

func TestLoad_ServiceAccountCredentialModeRequiresCompleteIdentity(t *testing.T) {
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__KIND", "SecretRef")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__NAME", "controller-manager")
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__KEY", "privateKey")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	if !strings.Contains(err.Error(), "controller.serviceaccount_namespace") {
		t.Errorf("Load() error = %q, want missing namespace", err)
	}
}

func TestLoad_RejectsStaticTokenOnlyConfiguration(t *testing.T) {
	t.Setenv("GITSTORE_CONTROLLER__API_TOKEN", "legacy-token")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	if !strings.Contains(err.Error(), "controller.serviceaccount_key_ref") {
		t.Errorf("Load() error = %q, want service account key reference error", err)
	}
}
