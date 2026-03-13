// Health check handlers for API service
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/commerce-projects/gitstore/api/internal/cache"
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
	Status    Status            `json:"status"`
	Version   string            `json:"version,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Checks    map[string]Check  `json:"checks,omitempty"`
}

// Check represents an individual health check
type Check struct {
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

// Handler provides health check endpoints
type Handler struct {
	cacheManager *cache.Manager
	logger       *zap.Logger
	version      string
	startTime    time.Time
}

// NewHandler creates a new health check handler
func NewHandler(cacheManager *cache.Manager, logger *zap.Logger, version string) *Handler {
	return &Handler{
		cacheManager: cacheManager,
		logger:       logger,
		version:      version,
		startTime:    time.Now(),
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

	// Determine overall status
	overallStatus := StatusHealthy
	httpStatus := http.StatusOK

	for _, check := range checks {
		if check.Status == StatusUnhealthy {
			overallStatus = StatusUnhealthy
			httpStatus = http.StatusServiceUnavailable
			break
		} else if check.Status == StatusDegraded {
			overallStatus = StatusDegraded
			httpStatus = http.StatusOK // Still ready, but degraded
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

// performChecks runs all health checks in parallel
func (h *Handler) performChecks(ctx context.Context) map[string]Check {
	checks := make(map[string]Check)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Check 1: Catalog cache availability
	wg.Add(1)
	go func() {
		defer wg.Done()
		check := h.checkCatalogCache(ctx)
		mu.Lock()
		checks["catalog_cache"] = check
		mu.Unlock()
	}()

	// Check 2: Service uptime
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

// checkCatalogCache verifies catalog cache is accessible
func (h *Handler) checkCatalogCache(ctx context.Context) Check {
	_, err := h.cacheManager.Get(ctx)
	if err != nil {
		h.logger.Warn("Catalog cache check failed", zap.Error(err))
		return Check{
			Status:  StatusUnhealthy,
			Message: "catalog cache unavailable",
		}
	}

	return Check{
		Status:  StatusHealthy,
		Message: "catalog cache operational",
	}
}

// checkUptime verifies service has been running for reasonable duration
func (h *Handler) checkUptime() Check {
	uptime := time.Since(h.startTime)

	// Consider degraded if service just started (< 5 seconds)
	if uptime < 5*time.Second {
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

// Healthz is a simple health check endpoint for backwards compatibility
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
