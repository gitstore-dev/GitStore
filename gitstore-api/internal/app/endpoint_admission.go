// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gorilla/websocket"
)

type endpointAdmission struct {
	kind               string
	limits             config.GraphQLLimits
	httpSlots, wsSlots chan struct{}
	metrics            endpointMetrics
}

type operationTransportKey struct{}
type operationTransport struct {
	writer    http.ResponseWriter
	websocket bool
}

// gqlgen owns serialization/status emission. Preserve that path while letting
// operation admission select 429 before gqlgen emits its normal 200 envelope.
type operationResponseWriter struct {
	gin.ResponseWriter
	status int
}

func (w *operationResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		status = w.status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *operationResponseWriter) Write(body []byte) (int, error) {
	if !w.Written() {
		w.WriteHeader(w.Status())
	}
	return w.ResponseWriter.Write(body)
}

func (w *operationResponseWriter) WriteString(body string) (int, error) {
	if !w.Written() {
		w.WriteHeader(w.Status())
	}
	return w.ResponseWriter.WriteString(body)
}

func newEndpointAdmission(kind string, limits config.GraphQLLimits) *endpointAdmission {
	return &endpointAdmission{kind: kind, limits: limits, httpSlots: make(chan struct{}, limits.InFlight), wsSlots: make(chan struct{}, limits.WSConnections), metrics: newEndpointMetrics()}
}

func rejectIngress(c *gin.Context, status int, code string) {
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(status, gin.H{"errors": []gin.H{{"message": http.StatusText(status), "extensions": gin.H{"code": code}}}})
}

const endpointAdmissionHandled = "gitstore.endpoint.admission.handled"

// Observe only fixed GraphQL routes. CORS and IP admission may reject a request
// before it reaches execution admission; do not lose or double-count those.
func (a *endpointAdmission) observeIngress() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.FullPath() != "/"+a.kind+"/graphql" {
			c.Next()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
		if _, handled := c.Get(endpointAdmissionHandled); handled {
			return
		}
		switch c.Writer.Status() {
		case http.StatusForbidden:
			a.metrics.outcome(c.Request.Context(), a.kind, "rejected", "origin")
		case http.StatusTooManyRequests:
			a.metrics.outcome(c.Request.Context(), a.kind, "rejected", "ip")
		case http.StatusNoContent:
			a.metrics.outcome(c.Request.Context(), a.kind, "completed", "preflight")
		}
	}
}

func (a *endpointAdmission) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(endpointAdmissionHandled, true)
		ws := websocket.IsWebSocketUpgrade(c.Request)
		slots := a.httpSlots
		if ws && a.kind == "admin" {
			slots = a.wsSlots
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			a.metrics.outcome(c.Request.Context(), a.kind, "rejected", "capacity")
			rejectIngress(c, 429, "THROTTLED")
			return
		}
		transport := "http"
		if ws && a.kind == "admin" {
			transport = "websocket"
		}
		writer := &operationResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		ctx := context.WithValue(c.Request.Context(), operationTransportKey{}, operationTransport{writer, ws})
		if !ws {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, a.limits.Deadline)
			defer cancel()
		}
		c.Request = c.Request.WithContext(ctx)
		finish := a.metrics.start(ctx, a.kind, transport)
		defer func() {
			status := c.Writer.Status()
			failure := recover()
			if failure != nil {
				status = http.StatusInternalServerError
			}
			finish(c.Request.Context(), status)
			if failure != nil {
				panic(failure)
			}
		}()
		if c.Request.ContentLength > a.limits.BodyBytes {
			rejectIngress(c, 413, "REQUEST_TOO_LARGE")
			return
		}
		// The unavailable public endpoint deliberately never consumes the body.
		// Admin buffers at most cap+1 bytes, including chunked requests, before
		// gqlgen/authentication can allocate or execute from the document.
		if a.kind == "admin" && !ws && c.Request.Body != nil {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, a.limits.BodyBytes+1))
			if int64(len(body)) > a.limits.BodyBytes {
				rejectIngress(c, 413, "REQUEST_TOO_LARGE")
				return
			}
			if err != nil {
				rejectIngress(c, 400, "BAD_REQUEST")
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}
		c.Next()
	}
}
