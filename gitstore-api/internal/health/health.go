// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Health check handlers for API service
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
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
}

// NewHandler creates a new health check handler
func NewHandler(store datastore.Datastore, logger *zap.Logger, version string) *Handler {
	return &Handler{
		store:     store,
		logger:    logger,
		version:   version,
		startTime: time.Now(),
	}
}

// Health handles /health endpoint - basic liveness check
// Returns 200 if service is running, regardless of dependencies
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response := HealthResponse{
		Status:    StatusHealthy,
		Version:   h.version,
		Timestamp: time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// Ready handles /ready endpoint - readiness check
// Returns 200 only if service is ready to accept traffic
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
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
		Timestamp: time.Now(),
		Checks:    checks,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(response)
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
	_, err := h.store.ListProducts(ctx, datastore.ProductFilter{First: 1})
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

func (h *Handler) checkUptime() Check {
	if time.Since(h.startTime) < 5*time.Second {
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
