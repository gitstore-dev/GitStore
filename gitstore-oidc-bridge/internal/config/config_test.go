// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"testing"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URI", "http://hydra:4445/")
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_URI", "http://kratos:4433/")
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__ADMIN_URI", "http://kratos:4434/")
}

func TestLoadRequiresHydraAdminURI(t *testing.T) {
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_URI", "http://kratos:4433")
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__ADMIN_URI", "http://kratos:4434")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URI is unset")
	}
}

func TestLoadRequiresKratosURLs(t *testing.T) {
	t.Setenv("GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URI", "http://hydra:4445")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when Kratos URLs are unset")
	}
}

func TestLoadDefaults(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenPort != 4445 {
		t.Errorf("ListenPort = %d, want 4445", cfg.ListenPort)
	}
	wantScope := []string{"openid", "profile", "email", "offline_access"}
	if len(cfg.OAuth2ClientScope) != len(wantScope) {
		t.Fatalf("OAuth2ClientScope = %v, want %v", cfg.OAuth2ClientScope, wantScope)
	}
	for i, s := range wantScope {
		if cfg.OAuth2ClientScope[i] != s {
			t.Errorf("OAuth2ClientScope[%d] = %q, want %q", i, cfg.OAuth2ClientScope[i], s)
		}
	}
	// Trailing slashes are trimmed.
	if cfg.HydraAdminURI != "http://hydra:4445" {
		t.Errorf("HydraAdminURI = %q, want trailing slash trimmed", cfg.HydraAdminURI)
	}
	// Browser URL defaults to the server-side public URL.
	if cfg.KratosPublicBrowserURI != "http://kratos:4433" {
		t.Errorf("KratosPublicBrowserURI = %q, want default to KratosPublicURI", cfg.KratosPublicBrowserURI)
	}
}

func TestLoadExplicitBrowserURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_BROWSER_URI", "http://localhost:4433")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KratosPublicBrowserURI != "http://localhost:4433" {
		t.Errorf("KratosPublicBrowserURI = %q", cfg.KratosPublicBrowserURI)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITSTORE_OIDC_BRIDGE__LISTEN_PORT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for listen_port 0")
	}
}
