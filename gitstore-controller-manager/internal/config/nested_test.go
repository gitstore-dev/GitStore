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

func TestLoadAPIClientBudgetDefaultsOverridesAndValidation(t *testing.T) {
	setenv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Controller.APIClient.RateLimit.PerSecond != 40 || cfg.Controller.APIClient.RateLimit.Burst != 10 {
		t.Fatalf("unexpected default API budget: %+v", cfg.Controller.APIClient)
	}
	t.Setenv("GITSTORE_CONTROLLER__API_CLIENT__RATE_LIMIT__PER_SECOND", "20")
	t.Setenv("GITSTORE_CONTROLLER__API_CLIENT__RATE_LIMIT__BURST", "5")
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Controller.APIClient.RateLimit.PerSecond != 20 || cfg.Controller.APIClient.RateLimit.Burst != 5 {
		t.Fatal("API client environment overrides ignored")
	}
	for _, key := range []string{"PER_SECOND", "BURST"} {
		t.Run(key, func(t *testing.T) {
			for _, value := range []string{"0", "-1", "MUST-NOT-LEAK"} {
				t.Setenv("GITSTORE_CONTROLLER__API_CLIENT__RATE_LIMIT__"+key, value)
				_, err := config.Load()
				if err == nil || strings.Contains(err.Error(), "MUST-NOT-LEAK") {
					t.Fatalf("invalid API request budget did not fail safely: %v", err)
				}
			}
		})
	}
}

func TestLoadCanonicalProviderAndWatchLeaves(t *testing.T) {
	setenv(t)
	for key, value := range map[string]string{
		"SECRET_PROVIDERS__BOOTSTRAP__TYPE":         "env",
		"SECRET_PROVIDERS__BOOTSTRAP__BASE_PATH":    "/test/controller-only",
		"SECRET_PROVIDERS__BOOTSTRAP__ENV_VARIABLE": "TEST_CONTROLLER_SIGNING_RECORD",
		"WATCH__MAX_BACKOFF":                        "12s",
		"WATCH__RESYNC_INTERVAL":                    "0s",
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
	if provider.Type != "env" ||
		provider.BasePath != "/test/controller-only" || provider.EnvVariable != "TEST_CONTROLLER_SIGNING_RECORD" {
		t.Fatal("canonical provider environment leaves were not decoded")
	}
	if cfg.Controller.Watch.MaxBackoff != 12*time.Second || cfg.Controller.Watch.ResyncInterval != 0 {
		t.Fatal("watch duration or zero-resync semantics changed")
	}
}

func TestLoadRejectsUnknownEnvironmentSourcesEvenWhenOverridden(t *testing.T) {
	for _, key := range []string{
		"UNSUPPORTED_SETTING", "SERVICEACCOUNT__UNSUPPORTED_SETTING",
	} {
		t.Run(key, func(t *testing.T) {
			setenv(t)
			t.Setenv("GITSTORE_CONTROLLER__"+key, "MUST-NOT-LEAK")
			_, err := config.Load()
			if err == nil || strings.Contains(err.Error(), "MUST-NOT-LEAK") {
				t.Fatalf("unknown environment path did not fail safely: %v", err)
			}
			if !strings.Contains(err.Error(), "unknown configuration path") {
				t.Fatalf("expected an unknown configuration path error: %v", err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error must identify the environment path: %v", err)
			}
		})
	}
}

func TestLoadRejectsUnknownFilePaths(t *testing.T) {
	for _, content := range []string{
		`[controller]
unsupported_setting = "MUST-NOT-LEAK"`,
		`[controller.serviceaccount]
unsupported_setting = "MUST-NOT-LEAK"`,
	} {
		setenv(t)
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := config.LoadFrom(path)
		if err == nil || strings.Contains(err.Error(), "MUST-NOT-LEAK") {
			t.Fatalf("unknown file path did not fail safely: %v", err)
		}
		if !strings.Contains(err.Error(), "unknown configuration path") {
			t.Fatalf("expected an unknown configuration path error: %v", err)
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
