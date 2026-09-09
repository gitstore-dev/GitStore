package bridge

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler reports readiness: 200 once both Hydra's and Kratos's Admin
// APIs answer their /health/ready endpoints, 503 otherwise.
type HealthHandler struct {
	upstreams map[string]string
	client    *http.Client
}

func NewHealthHandler(hydraAdminURI, kratosAdminURI string) *HealthHandler {
	return &HealthHandler{
		upstreams: map[string]string{
			"hydra":  hydraAdminURI + "/health/ready",
			"kratos": kratosAdminURI + "/health/ready",
		},
		client: &http.Client{Timeout: 3 * time.Second},
	}
}

func (h *HealthHandler) Handle(c *gin.Context) {
	for name, url := range h.upstreams {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "upstream": name})
			return
		}
		resp, err := h.client.Do(req)
		cancel()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "upstream": name})
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "upstream": name})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
