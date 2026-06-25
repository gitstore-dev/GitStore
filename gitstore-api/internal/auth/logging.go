// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// DecisionLogger wraps an AuthZProvider and emits a structured zap log line for
// every Authorize call with: provider, subject, action, resource_kind,
// resource_name, outcome, reason, and latency_ms.
type DecisionLogger struct {
	inner  AuthZProvider
	logger *zap.Logger
}

// NewDecisionLogger wraps inner with structured decision logging.
func NewDecisionLogger(inner AuthZProvider, logger *zap.Logger) *DecisionLogger {
	return &DecisionLogger{inner: inner, logger: logger}
}

// Name delegates to the wrapped provider.
func (d *DecisionLogger) Name() string { return d.inner.Name() }

// Authorize calls the wrapped provider and logs the decision.
func (d *DecisionLogger) Authorize(ctx context.Context, p *Principal, action string, res ResourceContext) (Decision, error) {
	start := time.Now()
	decision, err := d.inner.Authorize(ctx, p, action, res)
	latencyMs := time.Since(start).Milliseconds()

	subject := "anon"
	if p != nil {
		subject = p.Subject
	}

	outcomeStr := "allow"
	switch decision.Outcome {
	case OutcomeDeny:
		outcomeStr = "deny"
	case OutcomeChallenge:
		outcomeStr = "challenge"
	}

	d.logger.Info("authz decision",
		zap.String("provider", decision.Provider),
		zap.String("subject", subject),
		zap.String("action", action),
		zap.String("resource_kind", res.Kind),
		zap.String("resource_name", res.Name),
		zap.String("outcome", outcomeStr),
		zap.String("reason", decision.Reason),
		zap.Int64("latency_ms", latencyMs),
	)

	return decision, err
}
