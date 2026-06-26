// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T026: GetRequestMetadata injects Bearer token
func TestHmacCreds_GetRequestMetadata(t *testing.T) {
	creds := hmacCreds{token: "mysecret"}
	md, err := creds.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer mysecret", md["authorization"])
}

// T027: RequireTransportSecurity returns false (no TLS in Phase 4)
func TestHmacCreds_RequireTransportSecurity_False(t *testing.T) {
	creds := hmacCreds{}
	assert.False(t, creds.RequireTransportSecurity())
}
