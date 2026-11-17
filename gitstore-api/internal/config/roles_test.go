// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
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
		Admin:      AdvertisedEndpoint{URL: "https://private.example/tenant/admin/graphql"},
		Storefront: AdvertisedEndpoint{URL: "https://shop.example/proxy/storefront/graphql", Disclose: true},
		GitHTTP:    AdvertisedEndpoint{URL: "https://git.example/prefix/", Disclose: true},
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
		require.Error(t, (AdvertisedEndpoints{Admin: AdvertisedEndpoint{URL: value}}).Validate(), value)
	}
	require.Error(t, (AdvertisedEndpoints{Admin: AdvertisedEndpoint{Disclose: true}}).Validate())
	require.NoError(t, (AdvertisedEndpoints{Admin: AdvertisedEndpoint{URL: "http://[::1]:4000/prefix/admin/graphql"}}).Validate())
}
