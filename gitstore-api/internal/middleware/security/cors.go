// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
)

// EndpointCORS has no wildcard default. Same-origin/non-browser traffic is
// handled by the existing middleware; the configured allow-list controls peers.
func EndpointCORS(policy config.CORSConfig, paths ...string) gin.HandlerFunc {
	headers := policy.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Authorization", "Content-Type", "X-Request-ID"}
	}
	handler := cors.New(cors.Config{
		AllowOriginFunc:  func(origin string) bool { return policy.Allows(origin, "") },
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     headers,
		ExposeHeaders:    policy.ExposedHeaders,
		AllowCredentials: policy.AllowCredentials,
		MaxAge:           policy.MaxAge,
	})
	return func(c *gin.Context) {
		if len(paths) == 0 {
			handler(c)
			return
		}
		for _, path := range paths {
			if c.FullPath() == path {
				handler(c)
				return
			}
		}
	}
}
