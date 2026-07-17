// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Request ID middleware for tracing requests

package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
)

type contextKey string

const RequestIDKey contextKey = "request_id"

type RequestId struct {
	ids apiruntime.IDGenerator
}

func NewRequestId(ids apiruntime.IDGenerator) RequestId {
	return RequestId{
		ids: ids,
	}
}

// RequestIdInserter adds X-Request-Id response header per request pipeline
func (req *RequestId) RequestIdInserter(c *gin.Context) {
	// Check if request ID already exists in header
	requestID := c.GetHeader("X-Request-ID")
	if requestID == "" {
		requestID = req.ids.NewID()
	}

	c.Header("X-Request-ID", requestID)

	// Add request ID to context
	ctx := context.WithValue(c.Request.Context(), RequestIDKey, requestID)
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}

// GetRequestID retrieves the request ID from context
func GetRequestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(RequestIDKey).(string); ok {
		return requestID
	}
	return ""
}
