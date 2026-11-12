// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNamedEndpointPorts(t *testing.T) {
	defer clearEnv(t)()
	setRequiredAuth(t)
	cfg, err := Load()
	require.NoError(t, err)
	admin := reflect.ValueOf(cfg.Api).FieldByName("AdminPort")
	public := reflect.ValueOf(cfg.Api).FieldByName("StorefrontPort")
	require.True(t, admin.IsValid(), "missing named Admin port")
	require.True(t, public.IsValid(), "missing named Storefront port")
	require.EqualValues(t, 4000, admin.Int())
	require.EqualValues(t, 4001, public.Int())
}

func TestEndpointPortCollisionsFailClosed(t *testing.T) {
	for _, port := range []string{"0", "4000", "9000", "6000"} {
		t.Run(port, func(t *testing.T) {
			defer clearEnv(t)()
			setRequiredAuth(t)
			t.Setenv("GITSTORE_API__STOREFRONT_PORT", port)
			_, err := Load()
			require.Error(t, err, "must reject zero or occupied named port")
		})
	}
}

func TestEndpointPoliciesDecodeEnvironmentAndRejectUnboundedSettings(t *testing.T) {
	defer clearEnv(t)()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__ADMIN_PORT", "4400")
	t.Setenv("GITSTORE_API__STOREFRONT_PORT", "4401")
	t.Setenv("GITSTORE_API__ADMIN__CORS__ALLOWED_ORIGINS", "http://localhost:4401")
	t.Setenv("GITSTORE_API__STOREFRONT__LIMITS__IN_FLIGHT", "7")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 4400, cfg.Api.AdminPort)
	require.Equal(t, 4401, cfg.Api.StorefrontPort)
	require.Equal(t, []string{"http://localhost:4401"}, cfg.Api.Admin.CORS.AllowedOrigins)
	require.Equal(t, 7, cfg.Api.Storefront.Limits.InFlight)
	t.Setenv("GITSTORE_API__ADMIN__LIMITS__DEPTH", "0")
	_, err = Load()
	require.Error(t, err)
}
