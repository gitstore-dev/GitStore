// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
)

// setenv sets environment variables for a test and clears them on cleanup.
func setenv(t *testing.T, pairs ...string) {
	t.Helper()
	for i := 0; i+1 < len(pairs); i += 2 {
		t.Setenv(pairs[i], pairs[i+1])
	}
}

func TestLoad_Defaults(t *testing.T) {
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
