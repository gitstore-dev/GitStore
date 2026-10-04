// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secret

import (
	"time"

	"github.com/gitstore-dev/gitstore/secretmaterial"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

var (
	resolutionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gitstore_secret_resolution_total", Help: "Bootstrap acquisition outcomes by fixed category.",
	}, []string{"consumer", "purpose", "tier", "provider", "reason"})
	resolutionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "gitstore_secret_resolution_duration_seconds", Help: "Bootstrap acquisition duration.",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2},
	}, []string{"consumer", "purpose", "tier", "provider"})
	resolutionInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gitstore_secret_resolution_inflight", Help: "Active bounded bootstrap provider calls.",
	}, []string{"consumer", "purpose", "tier", "provider"})
)

type resolutionObserver struct{ log *zap.Logger }

func NewObserver(log *zap.Logger) secretmaterial.Observer {
	if log == nil {
		log = zap.NewNop()
	}
	return resolutionObserver{log: log}
}

func observationLabels(observation secretmaterial.Observation) []string {
	provider := "unknown"
	switch observation.Provider {
	case secretmaterial.ProviderFile:
		provider = "file"
	case secretmaterial.ProviderEnv:
		provider = "env"
	}
	return []string{"controller-manager", "identity", "bootstrap", provider}
}

func (o resolutionObserver) Inflight(observation secretmaterial.Observation, delta int) {
	resolutionInflight.WithLabelValues(observationLabels(observation)...).Add(float64(delta))
}

func (o resolutionObserver) Observe(observation secretmaterial.Observation, reason string, duration time.Duration) {
	switch reason {
	case "success", "InvalidRef", "NotFound", "MissingKey", "Forbidden", "ProviderUnavailable",
		"UnsupportedType", "ValueTooLarge", "canceled", "deadline_exceeded":
	default:
		reason = "ProviderUnavailable"
	}
	labels := observationLabels(observation)
	resolutionDuration.WithLabelValues(labels...).Observe(duration.Seconds())
	resolutionTotal.WithLabelValues(append(labels, reason)...).Inc()
	if reason != "success" {
		o.log.Warn("bootstrap secret acquisition failed", zap.String("provider", labels[3]), zap.String("reason", reason))
	}
}
