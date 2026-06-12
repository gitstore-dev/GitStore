// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package health

import (
	"encoding/json"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ManagerStats is the subset of Manager that the health handler queries.
type ManagerStats interface {
	KindStats() map[string]KindStat
}

// KindStat is a snapshot of per-kind operational state.
type KindStat struct {
	ActiveWorkers int64 `json:"activeWorkers"`
	QueueDepth    int   `json:"queueDepth"`
	PoisonItems   int   `json:"poisonItems"`
	Stalled       bool  `json:"stalled"`
}

type healthResponse struct {
	Status string              `json:"status"`
	Kinds  map[string]KindStat `json:"kinds"`
}

// NewHandler returns an http.Handler for GET /health.
func NewHandler(mgr ManagerStats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kinds := mgr.KindStats()

		status := "ok"
		httpStatus := http.StatusOK
		for _, s := range kinds {
			if s.Stalled || s.PoisonItems > 0 {
				status = "degraded"
				httpStatus = http.StatusServiceUnavailable
				break
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: status, Kinds: kinds})
	})
}

// NewMetricsHandler returns an http.Handler for GET /metrics (Prometheus scrape).
func NewMetricsHandler(_ ManagerStats) http.Handler {
	return promhttp.Handler()
}
