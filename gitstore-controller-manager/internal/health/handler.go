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

// CredentialReadiness reports whether the controller has a usable credential
// without exposing credential material or the underlying failure.
type CredentialReadiness interface {
	Ready() bool
}

// KindStat is a snapshot of per-kind operational state.
type KindStat struct {
	ActiveWorkers int64 `json:"activeWorkers"`
	QueueDepth    int   `json:"queueDepth"`
	PoisonItems   int   `json:"poisonItems"`
	Stalled       bool  `json:"stalled"`
	Registered    bool  `json:"registered"`
}

type healthResponse struct {
	Status          string              `json:"status"`
	Version         string              `json:"version"`
	Kinds           map[string]KindStat `json:"kinds"`
	CredentialReady *bool               `json:"credentialReady,omitempty"`
}

// NewHandler returns an http.Handler for GET /health.
func NewHandler(mgr ManagerStats, version string, credentialReadiness ...CredentialReadiness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kinds := mgr.KindStats()

		status := "ok"
		httpStatus := http.StatusOK
		var credentialReady *bool
		if len(credentialReadiness) > 0 && credentialReadiness[0] != nil {
			ready := credentialReadiness[0].Ready()
			credentialReady = &ready
			if !ready {
				status = "degraded"
				httpStatus = http.StatusServiceUnavailable
			}
		}
		for _, s := range kinds {
			if s.Stalled || s.PoisonItems > 0 {
				status = "degraded"
				httpStatus = http.StatusServiceUnavailable
				break
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		_ = json.NewEncoder(w).Encode(healthResponse{
			Status:          status,
			Version:         version,
			Kinds:           kinds,
			CredentialReady: credentialReady,
		})
	})
}

// NewMetricsHandler returns an http.Handler for GET /metrics (Prometheus scrape).
func NewMetricsHandler(mgr ManagerStats) http.Handler {
	metrics := promhttp.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mgr.KindStats()
		metrics.ServeHTTP(w, r)
	})
}
