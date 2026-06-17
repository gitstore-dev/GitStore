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

// ReconcileResult is the sealed return type of Reconciler.Reconcile.
type ReconcileResult = types.ReconcileResult

// Reconciler is implemented by controller authors.
// It receives only a WorkItemKey; it MUST read current resource state from the
// informer cache at dispatch time (level-triggered).
type Reconciler = types.Reconciler

// syncChecker is satisfied by any cache that reports whether its initial list
// has completed. Used to gate dispatch until the cache is warm.
type syncChecker interface {
	HasSynced() bool
	SyncedCh() <-chan struct{}
}

// ReconcilerRegistration configures a controller for one resource kind.
type ReconcilerRegistration struct {
	Kind       string
	Reconciler Reconciler

	// Cache gates dispatch until HasSynced() returns true (FR-013).
	Cache syncChecker

	MaxAttempts     int
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	StallThreshold  time.Duration
	WorkerCount     int
}
