package config

import (
	"testing"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URL", "http://hydra:4445/")
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_URL", "http://kratos:4433/")
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__ADMIN_URL", "http://kratos:4434/")
}

func TestLoadRequiresHydraAdminURL(t *testing.T) {
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_URL", "http://kratos:4433")
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__ADMIN_URL", "http://kratos:4434")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URL is unset")
	}
}

func TestLoadRequiresKratosURLs(t *testing.T) {
	t.Setenv("GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URL", "http://hydra:4445")
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
	if cfg.HydraAdminURL != "http://hydra:4445" {
		t.Errorf("HydraAdminURL = %q, want trailing slash trimmed", cfg.HydraAdminURL)
	}
	// Browser URL defaults to the server-side public URL.
	if cfg.KratosPublicBrowserURL != "http://kratos:4433" {
		t.Errorf("KratosPublicBrowserURL = %q, want default to KratosPublicURL", cfg.KratosPublicBrowserURL)
	}
}

func TestLoadExplicitBrowserURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_BROWSER_URL", "http://localhost:4433")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KratosPublicBrowserURL != "http://localhost:4433" {
		t.Errorf("KratosPublicBrowserURL = %q", cfg.KratosPublicBrowserURL)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITSTORE_OIDC_BRIDGE__LISTEN_PORT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for listen_port 0")
	}
}
