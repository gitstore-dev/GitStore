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
	// HydraAdminURI is Hydra's Admin API base URL (internal network only).
	HydraAdminURI string
	// OAuth2ClientScope is the space-separated permitted scope set for the
	// registered first-party OAuth2 client; consent grants are intersected with it.
	OAuth2ClientScope []string
	// KratosPublicURI is Kratos's public API base URL, used for server-side
	// /sessions/whoami calls from within the deployment network.
	KratosPublicURI string
	// KratosPublicBrowserURI is the browser-reachable Kratos public base URL used
	// when redirecting the browser to Kratos's self-service login UI. Defaults to
	// KratosPublicURI when unset (single-interface deployments).
	KratosPublicBrowserURI string
	// KratosAdminURI is Kratos's Admin API base URL (internal network only).
	KratosAdminURI string
}

const defaultClientScope = "openid profile email offline_access"

// Load resolves configuration from GITSTORE_OIDC_BRIDGE__* environment variables.
func Load() (*Config, error) {
	v := viper.New()
	// Trailing underscore: Viper joins prefix and key with "_", and our env
	// convention uses "__" between the namespace and the first key segment
	// (GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URI).
	v.SetEnvPrefix("GITSTORE_OIDC_BRIDGE_")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()

	v.SetDefault("listen_port", 4445)
	v.SetDefault("hydra.oauth2_client_scope", defaultClientScope)

	cfg := &Config{
		ListenPort:             v.GetInt("listen_port"),
		HydraAdminURI:          strings.TrimRight(v.GetString("hydra.admin_uri"), "/"),
		OAuth2ClientScope:      strings.Fields(v.GetString("hydra.oauth2_client_scope")),
		KratosPublicURI:        strings.TrimRight(v.GetString("kratos.public_uri"), "/"),
		KratosPublicBrowserURI: strings.TrimRight(v.GetString("kratos.public_browser_uri"), "/"),
		KratosAdminURI:         strings.TrimRight(v.GetString("kratos.admin_uri"), "/"),
	}
	if cfg.KratosPublicBrowserURI == "" {
		cfg.KratosPublicBrowserURI = cfg.KratosPublicURI
	}

	if cfg.ListenPort <= 0 || cfg.ListenPort > 65535 {
		return nil, fmt.Errorf("config: listen_port %d out of range", cfg.ListenPort)
	}
	var missing []string
	if cfg.HydraAdminURI == "" {
		missing = append(missing, "GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URI")
	}
	if cfg.KratosPublicURI == "" {
		missing = append(missing, "GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_URI")
	}
	if cfg.KratosAdminURI == "" {
		missing = append(missing, "GITSTORE_OIDC_BRIDGE__KRATOS__ADMIN_URI")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: missing required settings: %s", strings.Join(missing, ", "))
	}
	if len(cfg.OAuth2ClientScope) == 0 {
		return nil, fmt.Errorf("config: hydra.oauth2_client_scope must not be empty")
	}
	return cfg, nil
}
