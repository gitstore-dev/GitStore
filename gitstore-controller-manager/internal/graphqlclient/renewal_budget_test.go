// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

type waitingSigner struct{}

func (waitingSigner) SignAssertion(ctx context.Context, _, _ string, _ time.Duration, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestExchangeBudgetIncludesSigning(t *testing.T) {
	source := NewServiceAccountSource("http://unused.invalid", "controllers", "manager", waitingSigner{}, "assertion", "api", time.Minute, time.Hour)
	source.exchangeTimeout = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := source.Current(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline failure: %v", err)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("signing escaped the total exchange budget")
	}
}

func TestBackoffClampsAfterJitter(t *testing.T) {
	source := &ServiceAccountSource{maxBackoff: time.Second, failures: 32}
	for range 256 {
		before := time.Now()
		if delay := source.nextBackoff().Sub(before); delay > time.Second+time.Millisecond || delay < 0 {
			t.Fatalf("jittered backoff exceeded the cap: %s", delay)
		}
	}
}

func TestCallerCancellationDoesNotBackoffOtherCallers(t *testing.T) {
	source := NewServiceAccountSource("http://unused.invalid", "controllers", "manager", waitingSigner{}, "assertion", "api", time.Minute, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := source.Current(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller cancellation lost: %v", err)
	}
	if source.failures != 0 || !source.backoffUntil.IsZero() {
		t.Fatal("canceled leader imposed backoff on independent callers")
	}
}
