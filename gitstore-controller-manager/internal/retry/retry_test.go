// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package retry_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"go.uber.org/zap"
)

var nopLog = zap.NewNop()

var testKey = types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "test"}

var fastCfg = retry.Config{
	MaxAttempts:     3,
	InitialInterval: 1 * time.Millisecond,
	MaxInterval:     5 * time.Millisecond,
	Multiplier:      2.0,
}

func TestRunWithRetry_SuccessOnFirstTry(t *testing.T) {
	res, attempts, err := retry.RunWithRetry(context.Background(), testKey, fastCfg, 0, nopLog, func(_ context.Context) error {
		return nil
	})
	if res != retry.ResultOK {
		t.Errorf("expected ResultOK, got %v", res)
	}
	if attempts < 1 {
		t.Errorf("expected at least 1 attempt, got %d", attempts)
	}
	if err != nil {
		t.Errorf("expected nil error on success, got %v", err)
	}
}

func TestRunWithRetry_RetriesAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	res, attempts, err := retry.RunWithRetry(context.Background(), testKey, fastCfg, 0, nopLog, func(_ context.Context) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if res != retry.ResultOK {
		t.Errorf("expected ResultOK, got %v", res)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts)
	}
	if err != nil {
		t.Errorf("expected nil error on success, got %v", err)
	}
}

func TestRunWithRetry_QuarantinesAfterMaxAttempts(t *testing.T) {
	res, attempts, err := retry.RunWithRetry(context.Background(), testKey, fastCfg, 0, nopLog, func(_ context.Context) error {
		return errors.New("always fail")
	})
	if res != retry.ResultQuarantine {
		t.Errorf("expected ResultQuarantine, got %v", res)
	}
	if attempts == 0 {
		t.Errorf("expected non-zero attempts, got %d", attempts)
	}
	if err == nil {
		t.Error("expected non-nil error on quarantine")
	}
}

func TestRunWithRetry_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	res, _, _ := retry.RunWithRetry(ctx, testKey, fastCfg, 0, nopLog, func(_ context.Context) error {
		return errors.New("fail")
	})
	// Should quarantine (context cancelled = exhausted)
	if res != retry.ResultQuarantine {
		t.Errorf("expected ResultQuarantine on cancelled ctx, got %v", res)
	}
}
