// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package retry wraps cenkalti/backoff to provide per-item exponential backoff
// and quarantine after MaxAttempts exhaustion.
package retry

import (
	"context"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"go.uber.org/zap"
)

// Config holds retry parameters for one invocation.
type Config struct {
	MaxAttempts     int
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
}

// RetryRecord tracks attempt count for a single item across calls.
type RetryRecord struct {
	Key      types.WorkItemKey
	Attempts int
}

// Result from RunWithRetry.
type Result int

const (
	ResultOK         Result = iota
	ResultQuarantine        // exhausted MaxAttempts
)

// RunWithRetry calls fn up to cfg.MaxAttempts times with exponential backoff.
// Returns ResultQuarantine if fn keeps failing; ResultOK on first success.
// The third return value is the last error seen (nil on success).
func RunWithRetry(
	ctx context.Context,
	key types.WorkItemKey,
	cfg Config,
	log *zap.Logger,
	fn func(context.Context) error,
) (Result, int, error) {
	attempts := 0

	b := backoff.NewExponentialBackOff()
	b.InitialInterval = cfg.InitialInterval
	b.MaxInterval = cfg.MaxInterval
	b.Multiplier = cfg.Multiplier

	notify := func(err error, d time.Duration) {
		attempts++
		log.Warn("reconcile retry",
			zap.String("kind", key.Kind),
			zap.String("namespace", key.Namespace),
			zap.String("name", key.Name),
			zap.Int("attempt", attempts),
			zap.Duration("backoff", d),
			zap.Error(err),
		)
	}

	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		return struct{}{}, fn(ctx)
	},
		backoff.WithBackOff(b),
		backoff.WithMaxTries(uint(cfg.MaxAttempts)),
		backoff.WithNotify(notify),
	)
	if err != nil {
		return ResultQuarantine, attempts + 1, err
	}
	return ResultOK, attempts + 1, nil
}
