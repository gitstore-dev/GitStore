// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secret

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gitstore-dev/gitstore/secretmaterial"
	"go.uber.org/zap"
)

var (
	resolutionTotal    = observability.MustInstrument(otel.Meter("gitstore/secret").Float64Counter("gitstore_secret_resolution_total", metric.WithDescription("Bootstrap acquisition outcomes by fixed category.")))
	resolutionDuration = observability.MustInstrument(otel.Meter("gitstore/secret").Float64Histogram("gitstore_secret_resolution_duration_seconds", metric.WithDescription("Bootstrap acquisition duration."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2)))
	resolutionInflight = observability.MustInstrument(otel.Meter("gitstore/secret").Float64UpDownCounter("gitstore_secret_resolution_inflight", metric.WithDescription("Active bounded bootstrap provider calls.")))
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
	resolutionInflight.Add(context.Background(), float64(delta), metric.WithAttributes(attribute.String("consumer", (observationLabels(observation))[0]), attribute.String("purpose", (observationLabels(observation))[1]), attribute.String("tier", (observationLabels(observation))[2]), attribute.String("provider", (observationLabels(observation))[3])))
}

func (o resolutionObserver) Observe(observation secretmaterial.Observation, reason string, duration time.Duration) {
	switch reason {
	case "success", "InvalidRef", "NotFound", "MissingKey", "Forbidden", "ProviderUnavailable",
		"UnsupportedType", "ValueTooLarge", "canceled", "deadline_exceeded":
	default:
		reason = "ProviderUnavailable"
	}
	labels := observationLabels(observation)
	resolutionDuration.Record(context.Background(), duration.Seconds(), metric.WithAttributes(attribute.String("consumer", (labels)[0]), attribute.String("purpose", (labels)[1]), attribute.String("tier", (labels)[2]), attribute.String("provider", (labels)[3])))
	resolutionTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("consumer", (append(labels, reason))[0]), attribute.String("purpose", (append(labels, reason))[1]), attribute.String("tier", (append(labels, reason))[2]), attribute.String("provider", (append(labels, reason))[3]), attribute.String("reason", (append(labels, reason))[4])))
	if reason != "success" {
		o.log.Warn("bootstrap secret acquisition failed", zap.String("provider", labels[3]), zap.String("reason", reason))
	}
}
