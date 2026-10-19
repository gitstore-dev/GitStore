// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

// DecisionLogger wraps an AuthZProvider and emits a structured zap log line for
// every Authorize call, with bounded fields and finite-label latency metrics.
type DecisionLogger struct {
	inner  AuthZProvider
	logger *zap.Logger
}

var authorizationDecisionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "gitstore_authz_decision_duration_seconds",
	Help:    "Provider decision latency and counts by registered action and outcome.",
	Buckets: []float64{.000001, .000005, .00001, .00005, .0001, .0005, .001, .005, .01, .05, .1, .5, 1},
}, []string{"action", "outcome"})

var providerRegistryReloads = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gitstore_authz_registry_reloads_total",
	Help: "Complete provider registry reload outcomes, excluding initial construction.",
}, []string{"outcome"})

func init() {
	// Publish zero baselines so range queries retain the first reload increment.
	providerRegistryReloads.WithLabelValues("accepted")
	providerRegistryReloads.WithLabelValues("rejected")
}

// ObserveProviderRegistryReload records the final registry publication outcome,
// not intermediate provider construction or candidate policy acceptance.
func ObserveProviderRegistryReload(accepted bool) {
	outcome := "rejected"
	if accepted {
		outcome = "accepted"
	}
	providerRegistryReloads.WithLabelValues(outcome).Inc()
}

// NewDecisionLogger wraps inner with structured decision logging.
func NewDecisionLogger(inner AuthZProvider, logger *zap.Logger) *DecisionLogger {
	return &DecisionLogger{inner: inner, logger: logger.With(zap.String("catalog_digest", authorizationCatalogDigest))}
}

// Name delegates to the wrapped provider.
func (d *DecisionLogger) Name() string { return d.inner.Name() }

func (d *DecisionLogger) AuthorizationSnapshot() (AuthorizationSnapshot, bool) {
	if provider, ok := d.inner.(AuthorizationSnapshotProvider); ok {
		return provider.AuthorizationSnapshot()
	}
	return AuthorizationSnapshot{}, false
}

// Authorize calls the wrapped provider and logs the decision.
func (d *DecisionLogger) Authorize(ctx context.Context, p *Principal, action string, res ResourceContext) (Decision, error) {
	start := time.Now()
	decision, err := d.inner.Authorize(ctx, p, action, res)
	latency := time.Since(start)

	if decision.Provider == "" {
		decision.Provider = d.inner.Name()
	}
	if decision.RequestID == "" && ctx != nil {
		if requestID, ok := ctx.Value(middleware.RequestIDKey).(string); ok {
			decision.RequestID = requestID
		}
	}

	subject := "anon"
	if p != nil {
		subject = p.Subject
	}

	outcomeStr := "invalid"
	switch decision.Outcome {
	case OutcomeAllow:
		outcomeStr = "allow"
	case OutcomeDeny:
		outcomeStr = "deny"
	case OutcomeChallenge:
		outcomeStr = "challenge"
	}

	metricAction := "unknown"
	if definition, registered := LookupAction(action); registered {
		metricAction = definition.Action
	}
	metricOutcome := outcomeStr
	if err != nil {
		metricOutcome = "error"
	}
	authorizationDecisionDuration.WithLabelValues(metricAction, metricOutcome).Observe(latency.Seconds())

	level, message := zap.InfoLevel, "authz decision"
	if err != nil {
		level, message = zap.WarnLevel, "authz decision error"
	}
	entry := d.logger.Check(level, message)
	if entry == nil {
		return decision, err
	}
	truncated := false
	logString := func(key, value string, limit int) zap.Field {
		if len(value) > limit {
			value = strings.Clone(value[:limit])
			truncated = true
		}
		return zap.String(key, strings.ToValidUTF8(value, "?"))
	}
	fields := make([]zap.Field, 0, 13)
	fields = append(fields,
		logString("provider", decision.Provider, 256),
		logString("subject", subject, 256),
		logString("action", action, 256),
		logString("resource_kind", res.Kind, 256),
		logString("resource_name", res.Name, 256),
		logString("request_id", decision.RequestID, 256),
		zap.String("outcome", outcomeStr),
		logString("reason", decision.Reason, 2048),
		zap.Int64("latency_ms", latency.Milliseconds()),
		zap.Int64("latency_ns", latency.Nanoseconds()),
	)
	if decision.PolicyDigest != "" {
		valid := len(decision.PolicyDigest) == 64
		for i := 0; valid && i < len(decision.PolicyDigest); i++ {
			b := decision.PolicyDigest[i]
			valid = b >= '0' && b <= '9' || b >= 'a' && b <= 'f'
		}
		if valid {
			fields = append(fields, zap.String("policy_digest", decision.PolicyDigest))
		} else {
			fields = append(fields, zap.Bool("invalid_policy_digest", true))
		}
	}
	if truncated {
		fields = append(fields, zap.Bool("fields_truncated", true))
	}
	if err != nil {
		errorClass := "provider_error"
		switch {
		case errors.Is(err, context.Canceled):
			errorClass = "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			errorClass = "deadline_exceeded"
		}
		fields = append(fields, zap.String("error_class", errorClass))
	}
	entry.Write(fields...)

	return decision, err
}
