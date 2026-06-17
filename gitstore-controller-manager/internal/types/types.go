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

// ReconcileResult is a sealed interface returned by Reconciler.Reconcile.
// Only the four concrete variants in this package satisfy it.
type ReconcileResult interface {
	reconcileResult()
}

// Success signals that reconciliation completed without error.
type Success struct{}

func (Success) reconcileResult() { /* sealed-interface discriminator */ }

// RequeueAfter signals that reconciliation succeeded but the item should be
// re-enqueued after the specified duration.
type RequeueAfter struct{ After time.Duration }

func (RequeueAfter) reconcileResult() { /* sealed-interface discriminator */ }

// TransientFailure signals a recoverable error. The retry engine will back off
// and retry. BackoffHint overrides the registration's InitialInterval for this
// attempt when non-zero.
type TransientFailure struct {
	Err         error
	BackoffHint time.Duration
}

func (TransientFailure) reconcileResult() { /* sealed-interface discriminator */ }

// TerminalFailure signals an unrecoverable error. The item is quarantined
// immediately without consuming any retry budget.
type TerminalFailure struct{ Err error }

func (TerminalFailure) reconcileResult() { /* sealed-interface discriminator */ }

// ResultOK returns a Success result.
func ResultOK() ReconcileResult { return Success{} }

// ResultAfter returns a RequeueAfter result.
func ResultAfter(d time.Duration) ReconcileResult { return RequeueAfter{After: d} }

// ResultTransient returns a TransientFailure result. An optional BackoffHint
// duration may be passed as the second argument.
func ResultTransient(err error, hint ...time.Duration) ReconcileResult {
	tf := TransientFailure{Err: err}
	if len(hint) > 0 {
		tf.BackoffHint = hint[0]
	}
	return tf
}

// ResultTerminal returns a TerminalFailure result.
func ResultTerminal(err error) ReconcileResult { return TerminalFailure{Err: err} }

// Reconciler is implemented by controller authors.
type Reconciler interface {
	Reconcile(ctx context.Context, req WorkItemKey) ReconcileResult
}

var (
	ErrNotFound          = errors.New("not found")
	ErrQueueShutdown     = errors.New("queue is shutting down")
	ErrKindNotRegistered = errors.New("kind not registered")
	ErrConflict          = errors.New("optimistic concurrency conflict")
)
