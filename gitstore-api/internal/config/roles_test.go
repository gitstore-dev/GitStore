// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeRoleEnvironmentAndDefault(t *testing.T) {
	defer clearEnv(t)()
	setRequiredAuth(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, DefaultRoles(), cfg.Api.Roles)
	require.False(t, cfg.Api.HasRole(RoleStorefront))
	t.Setenv("GITSTORE_API__ROLES", "smart-http,catalog")
	t.Setenv("GITSTORE_API__GRAPHQL_LAYOUT", "shared")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, []string{RoleSmartHTTP, RoleCatalog}, cfg.Api.Roles)
	require.Equal(t, "shared", cfg.Api.GraphQLLayout)
}

func TestStorefrontOnlyDoesNotRequireAuthoringConfiguration(t *testing.T) {
	defer clearEnv(t)()
	t.Setenv("GITSTORE_API__ROLES", "storefront")
	t.Setenv("GITSTORE_API__AUTH__JWT__SECRET", "")
	t.Setenv("GITSTORE_GRPC_AUTH__HMAC_SECRET", "")
	t.Setenv("GITSTORE_API__DATASTORE__BACKEND", "unavailable")
	t.Setenv("GITSTORE_API__GIT_SERVICE__URI", "")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{RoleStorefront}, cfg.Api.Roles)
}

func TestRuntimeRoleAndListenerValidation(t *testing.T) {
	for _, roles := range [][]string{{}, {"admin", "admin"}, {"admin", ""}, {"smart_http"}, {"all"}} {
		require.Error(t, (ApiConfig{Roles: roles}).ValidateRuntime())
	}
	cfg := ApiConfig{Roles: []string{RoleAdmin}, AdminPort: 4000, StorefrontPort: 4000, GitPort: 4000, GrpcPort: 4000}
	require.NoError(t, cfg.ValidatePorts(), "disabled roles do not reserve ports")
	cfg.Roles = []string{RoleAdmin, RoleStorefront}
	require.Error(t, cfg.ValidatePorts())
	cfg.GraphQLLayout = "shared"
	require.NoError(t, cfg.ValidatePorts())
	cfg.Roles = append(cfg.Roles, RoleSmartHTTP)
	require.Error(t, cfg.ValidatePorts(), "shared GraphQL never shares a Git listener")
	cfg.GraphQLLayout = "unknown"
	require.Error(t, cfg.ValidateRuntime())
}

func TestAdvertisedDeploymentEndpointsAreExplicitAndPrivateByDefault(t *testing.T) {
	endpoints := AdvertisedEndpoints{
		Admin:      AdvertisedEndpoint{URI: "https://private.example/tenant/admin/graphql"},
		Storefront: AdvertisedEndpoint{URI: "https://shop.example/proxy/storefront/graphql", Disclose: true},
		GitHTTP:    AdvertisedEndpoint{URI: "https://git.example/prefix/", Disclose: true},
	}
	require.NoError(t, endpoints.Validate())
	require.Equal(t, map[string]string{
		"storefront": "https://shop.example/proxy/storefront/graphql", "git_http": "https://git.example/prefix/",
	}, endpoints.Disclosed())
	// Deployment advertisements are not restricted to the locally selected role.
	cfg := ApiConfig{Roles: []string{RoleCatalog}, Advertised: endpoints}
	require.NoError(t, cfg.ValidateRuntime())
	for _, value := range []string{"localhost:4000/admin/graphql", "grpc://internal:6000/admin/graphql",
		"https://name:password@api.example/admin/graphql", "https://api.example/admin/graphql?q=x",
		"https://api.example/admin/graphql?", "https://api.example/admin/graphql#x",
		"https://api.example/admin/graphql#", "https://api.example:/admin/graphql",
		"https://api.example:0/admin/graphql", "https://api.example:65536/admin/graphql",
		"https://api.example/graphql", "https://api.example/storefront/graphql", "https://api.example/admin/graphql\n"} {
		require.ErrorContains(t, (AdvertisedEndpoints{Admin: AdvertisedEndpoint{URI: value}}).Validate(), "api.advertised.admin.uri", value)
	}
	require.Error(t, (AdvertisedEndpoints{Admin: AdvertisedEndpoint{Disclose: true}}).Validate())
	require.NoError(t, (AdvertisedEndpoints{Admin: AdvertisedEndpoint{URI: "http://[::1]:4000/prefix/admin/graphql"}}).Validate())
}

func TestAdvertisedEndpointURIConfigurationContract(t *testing.T) {
	defer clearEnv(t)()
	setRequiredAuth(t)
	want := AdvertisedEndpoints{
		Admin:      AdvertisedEndpoint{URI: "https://management.example/team/admin/graphql"},
		Storefront: AdvertisedEndpoint{URI: "https://shop.example/team/storefront/graphql"},
		GitHTTP:    AdvertisedEndpoint{URI: "https://git.example/team"},
	}
	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"admin":{"uri":"https://management.example/team/admin/graphql","disclose":false},
		"storefront":{"uri":"https://shop.example/team/storefront/graphql","disclose":false},
		"git_http":{"uri":"https://git.example/team","disclose":false}
	}`, string(encoded))
	for _, file := range []struct {
		extension string
		body      string
	}{
		{"toml", `
[api.advertised.admin]
uri = "https://management.example/team/admin/graphql"
[api.advertised.storefront]
uri = "https://shop.example/team/storefront/graphql"
[api.advertised.git_http]
uri = "https://git.example/team"
`},
		{"json", `{"api":{"advertised":` + string(encoded) + `}}`},
	} {
		t.Run(file.extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config."+file.extension)
			require.NoError(t, os.WriteFile(path, []byte(file.body), 0o600))
			cfg, err := LoadFrom(path)
			require.NoError(t, err)
			require.Equal(t, want, cfg.Api.Advertised)
		})
	}
	t.Setenv("GITSTORE_API__ADVERTISED__ADMIN__URI", want.Admin.URI)
	t.Setenv("GITSTORE_API__ADVERTISED__STOREFRONT__URI", want.Storefront.URI)
	t.Setenv("GITSTORE_API__ADVERTISED__GIT_HTTP__URI", want.GitHTTP.URI)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, want, cfg.Api.Advertised)
}

func TestAdvertisedEndpointDoesNotAliasUnreleasedURLField(t *testing.T) {
	defer clearEnv(t)()
	setRequiredAuth(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[api.advertised.admin]
url = "https://management.example/admin/graphql"
disclose = true
`), 0o600))
	_, err := LoadFrom(path)
	require.ErrorContains(t, err, "api.advertised.admin.disclose requires api.advertised.admin.uri")
}
