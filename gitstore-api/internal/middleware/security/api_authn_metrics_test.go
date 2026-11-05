// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/observability/observabilitytest"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAuthenticator_RecordsAPIAuthNMetric_Allow(t *testing.T) {
	registry, staticAdmin := newTestRegistry(t)
	token, _, err := staticAdmin.IssueSession(t.Context(), "admin")
	require.NoError(t, err)

	promReg := prometheus.NewRegistry()
	authMiddleware := NewAuthenticate(registry, zap.NewNop(), observabilitytest.Meter(t, promReg, "gitstore/authentication"))

	req := httptest.NewRequest(http.MethodGet, "/admin/graphql", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	authMiddleware.Authenticator(c)
	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, float64(1), observabilitytest.Value(t, promReg, "gitstore_api_authn_requests_total", map[string]string{
		"provider": "static-users", "outcome": "allow",
	}))
}

func TestAuthenticator_RecordsAPIAuthNMetric_Deny(t *testing.T) {
	registry, _ := newTestRegistry(t)

	promReg := prometheus.NewRegistry()
	authMiddleware := NewAuthenticate(registry, zap.NewNop(), observabilitytest.Meter(t, promReg, "gitstore/authentication"))

	req := httptest.NewRequest(http.MethodGet, "/admin/graphql", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	authMiddleware.Authenticator(c)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// All chained providers challenged (no provider recognized the bogus
	// token), so ChainedAuthN.Authenticate returns Decision{Provider: "chain"}.
	assert.Equal(t, float64(1), observabilitytest.Value(t, promReg, "gitstore_api_authn_requests_total", map[string]string{
		"provider": "chain", "outcome": "deny",
	}))
}

func TestAuthenticator_NilMetrics_DoesNotPanic(t *testing.T) {
	registry, staticAdmin := newTestRegistry(t)
	token, _, err := staticAdmin.IssueSession(t.Context(), "admin")
	require.NoError(t, err)

	// No SDK meter passed — apiAuthnCounts stays nil.
	authMiddleware := NewAuthenticate(registry, zap.NewNop())
	require.Nil(t, authMiddleware.apiAuthnCounts)

	req := httptest.NewRequest(http.MethodGet, "/admin/graphql", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	assert.NotPanics(t, func() { authMiddleware.Authenticator(c) })
}
