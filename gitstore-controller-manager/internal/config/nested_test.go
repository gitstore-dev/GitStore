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

func TestLoadCanonicalProviderAndWatchLeaves(t *testing.T) {
	setenv(t)
	for key, value := range map[string]string{
		"SECRET_PROVIDERS__BOOTSTRAP__TYPE":       "env",
		"SECRET_PROVIDERS__BOOTSTRAP__FORMAT":     "raw",
		"SECRET_PROVIDERS__BOOTSTRAP__BASE_PATH":  "/test/controller-only",
		"SECRET_PROVIDERS__BOOTSTRAP__ENV_PREFIX": "TEST_SECRET__",
		"WATCH__MAX_BACKOFF":                      "12s",
		"WATCH__RESYNC_INTERVAL":                  "0s",
	} {
		t.Setenv("GITSTORE_CONTROLLER__"+key, value)
	}
	t.Setenv("GITSTORE_SECRET__IGNORED_PROVIDER_MATERIAL", "MUST-NOT-LEAK")
	t.Setenv("GITSTORE_AUTH__SERVICEACCOUNT__ISSUER", "sibling-service")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.Controller.SecretProviders.Bootstrap
	if provider.Type != "env" || provider.Format != "raw" ||
		provider.BasePath != "/test/controller-only" || provider.EnvPrefix != "TEST_SECRET__" {
		t.Fatal("canonical provider environment leaves were not decoded")
	}
	if cfg.Controller.Watch.MaxBackoff != 12*time.Second || cfg.Controller.Watch.ResyncInterval != 0 {
		t.Fatal("watch duration or zero-resync semantics changed")
	}
}

func TestLoadRejectsObsoleteSourcesEvenWhenOverridden(t *testing.T) {
	for _, key := range []string{
		"SERVICEACCOUNT_NAMESPACE", "SERVICEACCOUNT_NAME", "SERVICEACCOUNT_UID",
		"SERVICEACCOUNT_KEY_ID", "SERVICEACCOUNT_KEY_REF__KIND",
		"SERVICEACCOUNT_ASSERTION_AUDIENCE", "SERVICEACCOUNT_ACCESS_TOKEN_AUDIENCE",
		"SECRET_PROVIDER_BOOTSTRAP__TYPE", "CHECKPOINT_DIR",
		"CHECKPOINT_FLUSH_INTERVAL_EVENTS", "DEFAULT_MAX_ATTEMPTS",
		"DEFAULT_STALL_THRESHOLD", "MAX_WATCH_BACKOFF", "RESYNC_INTERVAL",
		"CONTROLLER__SERVICEACCOUNT__NAME", "SECRET_PROVIDERS__RUNTIME__TYPE",
	} {
		t.Run(key, func(t *testing.T) {
			setenv(t)
			t.Setenv("GITSTORE_CONTROLLER__"+key, "MUST-NOT-LEAK")
			_, err := config.Load()
			if err == nil || strings.Contains(err.Error(), "MUST-NOT-LEAK") {
				t.Fatalf("obsolete environment path did not fail safely: %v", err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error must identify the environment path: %v", err)
			}
		})
	}
}

func TestLoadRejectsObsoleteFilePaths(t *testing.T) {
	for _, content := range []string{
		`[controller]
serviceaccount_namespace = "MUST-NOT-LEAK"`,
		`[controller.controller]
serviceaccount_namespace = "MUST-NOT-LEAK"`,
		`[controller.secret_providers.runtime]`,
	} {
		setenv(t)
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := config.LoadFrom(path)
		if err == nil || strings.Contains(err.Error(), "MUST-NOT-LEAK") {
			t.Fatalf("obsolete file path did not fail safely: %v", err)
		}
	}
}

func TestLoadRedactsMalformedTypedValues(t *testing.T) {
	for _, key := range []string{
		"WATCH__MAX_BACKOFF", "WATCH__RESYNC_INTERVAL",
		"RECONCILE__STALL_THRESHOLD", "RECONCILE__MAX_ATTEMPTS",
		"CHECKPOINT__FLUSH_INTERVAL_EVENTS", "PORT",
	} {
		t.Run(key, func(t *testing.T) {
			setenv(t)
			t.Setenv("GITSTORE_CONTROLLER__"+key, "MUST-NOT-LEAK")
			_, err := config.Load()
			if err == nil || strings.Contains(err.Error(), "MUST-NOT-LEAK") {
				t.Fatalf("typed value did not fail safely: %v", err)
			}
		})
	}
}
