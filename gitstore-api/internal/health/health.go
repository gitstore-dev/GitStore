// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package health check handlers for API service
package health

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// Status represents the health status of the service
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

// HealthResponse represents the health check response
type HealthResponse struct {
	Status    Status           `json:"status"`
	Version   string           `json:"version,omitempty"`
	Timestamp time.Time        `json:"timestamp"`
	Checks    map[string]Check `json:"checks,omitempty"`
}

// Check represents an individual health check
type Check struct {
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

// Handler provides health check endpoints
type Handler struct {
	store     datastore.Datastore
	logger    *zap.Logger
	version   string
	startTime time.Time
	clock     apiruntime.Clock
}

// HandlerDeps contains dependencies for Handler.
type HandlerDeps struct {
	Store   datastore.Datastore
	Logger  *zap.Logger
	Version string
	Clock   apiruntime.Clock
}

// NewHandler creates a new health check handler
func NewHandler(deps HandlerDeps) *Handler {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	clock := deps.Clock
	if clock == nil {
		clock = apiruntime.SystemClock{}
	}
	return &Handler{
		store:     deps.Store,
		logger:    logger,
		version:   deps.Version,
		startTime: clock.Now(),
		clock:     clock,
	}
}

// Health handles /health endpoint - basic liveness check
// Returns 200 if service is running, regardless of dependencies
func (h *Handler) Health(c *gin.Context) {
	response := HealthResponse{
		Status:    StatusHealthy,
		Version:   h.version,
		Timestamp: h.clock.Now(),
	}

	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, response)
}

// Ready handles /ready endpoint - readiness check
// Returns 200 only if service is ready to accept traffic
func (h *Handler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	checks := h.performChecks(ctx)

	overallStatus := StatusHealthy
	httpStatus := http.StatusOK

	for _, check := range checks {
		if check.Status == StatusUnhealthy {
			overallStatus = StatusUnhealthy
			httpStatus = http.StatusServiceUnavailable
			break
		} else if check.Status == StatusDegraded {
			overallStatus = StatusDegraded
		}
	}

	response := HealthResponse{
		Status:    overallStatus,
		Version:   h.version,
		Timestamp: h.clock.Now(),
		Checks:    checks,
	}

	c.Header("Content-Type", "application/json")
	c.JSON(httpStatus, response)
}

func (h *Handler) performChecks(ctx context.Context) map[string]Check {
	checks := make(map[string]Check)
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		check := h.checkDatastore(ctx)
		mu.Lock()
		checks["datastore"] = check
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		check := h.checkUptime()
		mu.Lock()
		checks["uptime"] = check
		mu.Unlock()
	}()

	wg.Wait()
	return checks
}

func (h *Handler) checkDatastore(ctx context.Context) Check {
	if h.store == nil {
		h.logger.Warn("Datastore check failed", zap.String("reason", "datastore not configured"))
		return Check{
			Status:  StatusUnhealthy,
			Message: "datastore unavailable",
		}
	}
	_, err := h.store.ListProducts(ctx, "", datastore.PageParams{First: 1})
	if err != nil {
		h.logger.Warn("Datastore check failed", zap.Error(err))
		return Check{
			Status:  StatusUnhealthy,
			Message: "datastore unavailable",
		}
	}

	return Check{
		Status:  StatusHealthy,
		Message: "datastore operational",
	}
}

// Metrics serves the Prometheus metrics endpoint using the default registry.
// All instrumented components (datastore, gRPC client, auth outcomes) are included.
func (h *Handler) Metrics(c *gin.Context) {
	promhttp.Handler().ServeHTTP(c.Writer, c.Request)
}

func (h *Handler) checkUptime() Check {
	if h.clock.Now().Sub(h.startTime) < 5*time.Second {
		return Check{
			Status:  StatusDegraded,
			Message: "service warming up",
		}
	}

	return Check{
		Status:  StatusHealthy,
		Message: "service operational",
	}
}
