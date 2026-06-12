// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package manager

import (
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// WorkItemKey is the identity of a reconciliation work item.
// It is the only thing the queue and reconcilers exchange — no event payload.
type WorkItemKey = types.WorkItemKey

// Result is returned by a Reconciler to control re-queue behaviour.
type Result = types.Result

// Reconciler is implemented by controller authors.
// It receives only a WorkItemKey; it MUST read current resource state from the
// informer cache at dispatch time (level-triggered).
type Reconciler = types.Reconciler

// ReconcilerRegistration configures a controller for one resource kind.
type ReconcilerRegistration struct {
	Kind       string
	Reconciler Reconciler

	MaxAttempts     int
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	StallThreshold  time.Duration
	WorkerCount     int
}
