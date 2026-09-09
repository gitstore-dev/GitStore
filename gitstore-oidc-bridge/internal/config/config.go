// Package config loads gitstore-oidc-bridge's GITSTORE_OIDC_BRIDGE__* configuration
// per specs/059-optional-oidc-provider/contracts/oidc-bridge-routes.md.
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config is the bridge's resolved configuration.
type Config struct {
	// ListenPort is the bridge's own HTTP listen port (/login, /consent, /health).
	ListenPort int
	// HydraAdminURL is Hydra's Admin API base URL (internal network only).
	HydraAdminURL string
	// OAuth2ClientScope is the space-separated permitted scope set for the
	// registered first-party OAuth2 client; consent grants are intersected with it.
	OAuth2ClientScope []string
	// KratosPublicURL is Kratos's public API base URL, used for server-side
	// /sessions/whoami calls from within the deployment network.
	KratosPublicURL string
	// KratosPublicBrowserURL is the browser-reachable Kratos public base URL used
	// when redirecting the browser to Kratos's self-service login UI. Defaults to
	// KratosPublicURL when unset (single-interface deployments).
	KratosPublicBrowserURL string
	// KratosAdminURL is Kratos's Admin API base URL (internal network only).
	KratosAdminURL string
}

const defaultClientScope = "openid profile email offline_access"

// Load resolves configuration from GITSTORE_OIDC_BRIDGE__* environment variables.
func Load() (*Config, error) {
	v := viper.New()
	// Trailing underscore: Viper joins prefix and key with "_", and our env
	// convention uses "__" between the namespace and the first key segment
	// (GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URL).
	v.SetEnvPrefix("GITSTORE_OIDC_BRIDGE_")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()

	v.SetDefault("listen_port", 4445)
	v.SetDefault("hydra.oauth2_client_scope", defaultClientScope)

	cfg := &Config{
		ListenPort:             v.GetInt("listen_port"),
		HydraAdminURL:          strings.TrimRight(v.GetString("hydra.admin_url"), "/"),
		OAuth2ClientScope:      strings.Fields(v.GetString("hydra.oauth2_client_scope")),
		KratosPublicURL:        strings.TrimRight(v.GetString("kratos.public_url"), "/"),
		KratosPublicBrowserURL: strings.TrimRight(v.GetString("kratos.public_browser_url"), "/"),
		KratosAdminURL:         strings.TrimRight(v.GetString("kratos.admin_url"), "/"),
	}
	if cfg.KratosPublicBrowserURL == "" {
		cfg.KratosPublicBrowserURL = cfg.KratosPublicURL
	}

	if cfg.ListenPort <= 0 || cfg.ListenPort > 65535 {
		return nil, fmt.Errorf("config: listen_port %d out of range", cfg.ListenPort)
	}
	var missing []string
	if cfg.HydraAdminURL == "" {
		missing = append(missing, "GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URL")
	}
	if cfg.KratosPublicURL == "" {
		missing = append(missing, "GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_URL")
	}
	if cfg.KratosAdminURL == "" {
		missing = append(missing, "GITSTORE_OIDC_BRIDGE__KRATOS__ADMIN_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: missing required settings: %s", strings.Join(missing, ", "))
	}
	if len(cfg.OAuth2ClientScope) == 0 {
		return nil, fmt.Errorf("config: hydra.oauth2_client_scope must not be empty")
	}
	return cfg, nil
}
