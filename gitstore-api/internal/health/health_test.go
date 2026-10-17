// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestReadyIncludesOnlyBoundedAuthSnapshotMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registry := auth.NewProviderRegistry(auth.NewChainedAuthN(), nil, nil)
	h := health.NewHandler(health.HandlerDeps{
		Store: &testutil.StubStore{}, AuthDiagnostics: registry.Diagnostics,
	})
	router := gin.New()
	router.GET("/ready", h.Ready)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var response health.HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.NotNil(t, response.Auth)
	require.Len(t, response.Auth.CatalogDigest, 64)
	require.Empty(t, response.Auth.AuthN)
	require.Nil(t, response.Auth.Policy, "unsupported snapshot metadata must not invent a policy digest")
	require.NotContains(t, w.Body.String(), "policyFile")
}

func TestMetricsNegotiatesOpenMetricsExemplarsWithoutChangingLegacyScrapes(t *testing.T) {
	histogram := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "api_health_exemplar_test_seconds", Help: "test"})
	prometheus.MustRegister(histogram)
	defer prometheus.Unregister(histogram)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	})
	observability.Observe(trace.ContextWithSpanContext(t.Context(), sc), histogram, 0.01)
	for _, instance := range []string{"", "replica-test"} {
		handler := health.NewHandler(health.HandlerDeps{InstanceID: instance})
		router := gin.New()
		router.GET("/metrics", handler.Metrics)
		for _, openMetrics := range []bool{false, true} {
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if openMetrics {
				request.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			if openMetrics {
				require.Contains(t, response.Header().Get("Content-Type"), "application/openmetrics-text")
				require.Contains(t, response.Body.String(), `trace_id="`+sc.TraceID().String()+`"`)
				require.Contains(t, response.Body.String(), `span_id="`+sc.SpanID().String()+`"`)
				require.Contains(t, response.Body.String(), "# EOF")
			} else {
				require.Contains(t, response.Header().Get("Content-Type"), "text/plain")
				require.NotContains(t, response.Body.String(), "trace_id")
			}
			require.Contains(t, response.Body.String(), "api_health_exemplar_test_seconds_count 1")
			if instance != "" {
				require.Contains(t, response.Body.String(), `gitstore_api_process_instance_info{instance_id="replica-test"} 1`)
			}
		}
	}
}

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

func TestMetricsEndpointExposesProcessInstanceIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := health.NewHandler(health.HandlerDeps{InstanceID: "replica-unique-id"})
	router := gin.New()
	router.GET("/metrics", h.Metrics)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `gitstore_api_process_instance_info{instance_id="replica-unique-id"} 1`)
}

func TestReadyFailsClosedWhenNamespaceWatchIsNotReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := health.NewHandler(health.HandlerDeps{
		Store: &testutil.StubStore{},
		ResourceWatchReady: func(context.Context) error {
			return errors.New("materializer lag")
		},
	})
	router := gin.New()
	router.GET("/ready", h.Ready)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"resource_watch"`)
	assert.Contains(t, w.Body.String(), `"unhealthy"`)
}
