// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestEndpointCORSIsIndependentAndFailClosed(t *testing.T) {
	for _, item := range []struct {
		name    string
		origins []string
		request string
		want    int
	}{
		{"empty", nil, "https://shop.example", http.StatusForbidden},
		{"allow", []string{"https://shop.example"}, "https://shop.example", http.StatusOK},
		{"deny", []string{"https://admin.example"}, "https://shop.example", http.StatusForbidden},
		{"same origin", nil, "http://example.com", http.StatusOK},
	} {
		t.Run(item.name, func(t *testing.T) {
			r := gin.New()
			r.Use(EndpointCORS(config.CORSConfig{AllowedOrigins: item.origins}))
			r.POST("/", func(c *gin.Context) { c.Status(http.StatusOK) })
			request := httptest.NewRequest(http.MethodPost, "http://example.com/", nil)
			request.Header.Set("Origin", item.request)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, request)
			require.Equal(t, item.want, w.Code)
			if item.want == http.StatusForbidden {
				require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

func TestCORSRejectsCredentialWildcard(t *testing.T) {
	require.Error(t, (config.CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true}).Validate())
}

func TestCORSPreflightAndErrorResponsesKeepConfiguredPolicy(t *testing.T) {
	policy := config.DefaultEndpoint("admin").CORS
	policy.AllowedOrigins = []string{"https://console.example"}
	r := gin.New()
	r.Use(EndpointCORS(policy))
	r.POST("/", func(c *gin.Context) { c.AbortWithStatus(503) })
	for _, method := range []string{http.MethodOptions, http.MethodPost} {
		request := httptest.NewRequest(method, "http://api.example/", nil)
		request.Header.Set("Origin", "https://console.example")
		request.Header.Set("Access-Control-Request-Method", "POST")
		request.Header.Set("Access-Control-Request-Headers", "Authorization,Content-Type")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, request)
		if method == http.MethodOptions {
			require.Equal(t, 204, w.Code)
			require.NotEmpty(t, w.Header().Get("Access-Control-Max-Age"))
		} else {
			require.Equal(t, 503, w.Code)
		}
		require.Equal(t, "https://console.example", w.Header().Get("Access-Control-Allow-Origin"))
		require.Contains(t, w.Header().Values("Vary"), "Origin")
	}
	require.Error(t, (config.CORSConfig{AllowedHeaders: []string{"bad header"}}).Validate())
}
