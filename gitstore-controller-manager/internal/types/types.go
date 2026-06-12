// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package types contains shared primitives used by both the queue and manager
// packages to avoid import cycles.
package types

import (
	"context"
	"errors"
	"time"
)

// WorkItemKey is the identity of a reconciliation work item.
type WorkItemKey struct {
	Kind      string
	Namespace string
	Name      string
}

// Result is returned by a Reconciler to control re-queue behaviour.
type Result struct {
	Requeue      bool
	RequeueAfter time.Duration
}

// Reconciler is implemented by controller authors.
type Reconciler interface {
	Reconcile(ctx context.Context, req WorkItemKey) (Result, error)
}

var (
	ErrNotFound          = errors.New("not found")
	ErrQueueShutdown     = errors.New("queue is shutting down")
	ErrKindNotRegistered = errors.New("kind not registered")
)
