// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package health_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T014: GET /metrics returns 200 and content-type text/plain (Prometheus exposition format).
func TestMetricsEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := health.NewHandler(health.HandlerDeps{})
	router := gin.New()
	router.GET("/metrics", h.Metrics)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	ct := w.Header().Get("Content-Type")
	assert.True(t, strings.HasPrefix(ct, "text/plain"),
		"expected text/plain content-type, got %q", ct)
}
